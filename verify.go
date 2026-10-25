package main

// verify.go —— "执行 → 验证"闭环的验证器（v2.0 P2）
//
// 背景（也是这一版的立意）：P0 之前任务函数一律 return nil，AI 于是系统性谎报"已启动"；
// P0 让失败不再被吞，但仍只证明"命令发出去了"。P2 补的是最后半步——
// **用客观证据回答"现实环境真的恢复了吗"**，而不是回答"我执行过了"。
//
// 三条设计约束：
//  1. 只读：验证器不改任何东西，只采集证据（端口、进程、HTTP 码、sha256、共识状态）。
//  2. 不撒谎也不冤枉：证据不足时不判成功，判"未复验"（Verified=false）——
//     假阴性（明明起来了却说没有）和假阳性（没起来却说好了）同样会毁掉信任。
//  3. 证据可复核：Evidence 里每一项都应是用户能自己再验一遍的客观值。

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
)

// ---------- 复验超时窗口 ----------
const (
	verifyWaitWeb     = 45 * time.Second // 前端：vue/vite 首次编译常常要 20s+，窗口太短必然假阴性
	verifyWaitBackend = 25 * time.Second // 后端：go build 已在启动阶段完成，run 起来很快
	verifyPollEvery   = 2 * time.Second
	verifyDialTimeout = 700 * time.Millisecond
	// verifyChainGrowth 共识活性采样窗口。
	// 不是"等出块"——空闲链可能几分钟都不出一个块；而是等共识视图推进，
	// PBFT 无交易时每轮空块共识都会切换视图，view 大约每秒 +1，6 秒窗口足够。
	verifyChainGrowth = 6 * time.Second
	// verifyChainMinViewDelta 窗口内视图至少要推进这么多才算共识在跑。
	// 取 1 而非更大值：窗口内只要 view 动过，就说明共识循环没卡死。
	verifyChainMinViewDelta = 1
)

// webaseFetch 链端只读查询的唯一出入口。抽成变量是为了让单测能在不碰真实链的前提下
// 复现"共识停滞 / 配置错误 / 接口不可达"这些现实分支——链端故障没法在 CI 里制造。
var webaseFetch = func(p string) (string, error) { return webaseText(p) }

// ================= ① 服务启动复验 =================

// verifyService 复验前端/后端是否真的起来了：进程存活 + 端口监听 + HTTP 握手 + 存活观察。
// wait<=0 时按服务类型取默认窗口；超时不算"失败"，而算"未复验"（可能仍在编译）。
//
// v2.2 M11：所有出口都把结论写回服务注册表（svcRecordVerify），
// 这样 Runtime 状态源才能区分"进程在跑"与"服务验过可用"——
// 前者只是 running，后者才是 running + verified。
// v2.2 M12：复验失败还会累加连续失败次数并给出退避建议（但**不自动重启**）。
func verifyService(target string, wait time.Duration) OpResult {
	r := verifyServiceInner(target, wait)
	ok := r.Ok && r.Verified
	kind := r.ErrKind
	if ok {
		kind = ""
	} else if r.ErrKind == errKindCrash {
		// 崩溃与"没起来"要分开记：前者是运行中挂掉（可能是内存/依赖/配置），
		// 后者是环境问题。混在一起会让退避建议给错方向。
		svcRecordCrash(target, errKindCrash, firstLines(r.Msg, 120), 0)
	}
	svcRecordVerify(target, firstLines(r.Msg, 160), kind, ok)
	return r
}

func verifyServiceInner(target string, wait time.Duration) OpResult {
	action := "verify_service"
	if target != "web" && target != "backend" {
		return opFail(action, target, errKindBadConfig, "target 只能是 web 或 backend")
	}
	if wait <= 0 {
		wait = verifyWaitBackend
		if target == "web" {
			wait = verifyWaitWeb
		}
	}
	ports := svcPortsFn(target)
	if len(ports) == 0 {
		return opOK(action, target, "没有可用端口线索，跳过复验（可在「程序配置」的 scan_ports 里补上服务端口）")
	}
	expect := svcExpectProcs(target)
	deadline := time.Now().Add(wait)
	// excluded 记录"已排除的端口 → 占用者"。命中一个端口但占用者不是本服务时，
	// 正确的反应是把这个端口剔除后继续等，而不是立刻下结论。
	//
	// 为什么必须这样：候选端口来自 scan_ports / 项目画像 / 内置默认表，是前后端共享的
	// 一锅端（内置就是 {8080, 8888}）。整套启动（先后端再前端）时后端已经占着 8888，
	// 前端复验的第一次轮询就会命中 8888，拿到 owner=main.exe 与预期的 node.exe 不匹配。
	// 老代码在这里直接 return，等待窗口（45s）形同虚设，前端还在编译就被判"不能认定已就绪"。
	// 剔除后继续等，前端编译完监听 8080 → owner=node.exe → 匹配 → 判通过。
	excluded := map[int]string{}
	var lastWhy string
	for {
		alive, pid := svcAlive(target)
		if !alive {
			// 进程已经没了：这是"启动失败"的硬证据，不能降级成"已启动但没验到"
			ev := []string{"pid=" + strconv.Itoa(pid)}
			if tail := svcLogTail(target, 12); tail != "" {
				ev = append(ev, "log_tail="+tail)
			}
			return opFail(action, target, errKindSpawnFail,
				svcLabel(target)+"进程已退出，启动未成功（详见「程序启动」日志）",
				append(ev, "hint="+lifecycleHint(errKindSpawnFail))...)
		}
		if port := firstListeningPortExcept(ports, excluded); port > 0 {
			owner := portOwner(port)
			ev := []string{"pid=" + strconv.Itoa(pid), "port=" + strconv.Itoa(port) + " LISTENING"}
			if owner != "" {
				ev = append(ev, "owner="+owner)
			}
			// 端口被"别人的"进程占着不能算我们的服务起来了——这正是假阳性的典型来源。
			// 但"不是我的"只否定了这个端口，否不掉整个服务：剔除它，继续等其他候选端口。
			if !svcOwnerAcceptable(owner, expect) {
				excluded[port] = owner
				lastWhy = fmt.Sprintf("端口 %d 被 %s 占用，不属于%s（已排除该端口继续等待）",
					port, owner, svcLabel(target))
				if time.Now().After(deadline) {
					break
				}
				time.Sleep(verifyPollEvery)
				continue
			}
			// G12 修复：端口 LISTENING、owner 匹配、HTTP 有响应——但状态码是 5xx。
			//
			// 这是"端口 LISTENING 但不健康"的精确形态（plan §5.2 的 G12 缺口，
			// 被测形态见 eval/fixtures/sandbox-backend 的 liar 开关）：
			// 进程活着、端口通着、HTTP 服务器也在应答，**但服务端自己说它坏了**
			// （配置错误、依赖掉了、健康检查报错——真实世界里"起来了但功能全废"）。
			// 只看"有没有响应"的旧判据会放它过关，于是恢复被宣布成功，
			// 而用户访问到的是一片 503——**假愈：系统说好了，现实是坏的。**
			//
			// 判 Ok&&!Verified（三态的第二态）而不是 !Ok：
			// 探活确实执行了、端口确实在听、进程确实活着——动作层面的事实都成立；
			// 没被证明的是"服务健康"。这与"进程死了"（!Ok, spawn_fail）和
			// "确实健康"（Ok&&Verified）是三种不同结论，混不掉。
			// AI 拿到它时不许宣布"已恢复"，正确行为是报告 5xx + 给下一步。
			//
			// 阈值取 5xx 而不是"非 2xx"：4xx 是**请求**的问题（401 要登录、
			// 404 路由不对、405 方法错），服务端明明活着——把它判不健康
			// 就是误杀，而误杀比漏拦更糟（会让用户去"修"一个没坏的服务）。
			probe := httpProbe(port)
			if code := httpStatusCode(probe); code >= 500 {
				ev = append(ev, probe,
					"hint=HTTP 5xx 是服务端自报异常：进程与端口正常，但无法认定服务可用")
				return opOK(action, target,
					fmt.Sprintf("%sHTTP 返回 %d：进程在跑、端口在听，但服务端自报异常，无法认定已就绪", svcLabel(target), code),
					ev...)
			}
			ev = append(ev, probe)
			// v2.2 M10：不立即返回。端口监听到进程活着还有一段路——
			// "起来了又崩了"是最常见的故障，而"监听的那一刻"恰好看不到它。
			// 进入存活观察窗，期间崩溃才算启动失败并附日志尾部。
			return verifyObserve(action, target, port, pid, owner, ev)
		}
		lastWhy = "进程存活（PID " + strconv.Itoa(pid) + "），候选端口 " + intsToStr(ports) + " 均未监听"
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(verifyPollEvery)
	}
	// 超时：结论必须说清"是没监听"还是"全被别的服务占了"，否则读的人无法判断该不该再等
	if len(excluded) > 0 {
		ev := []string{lastWhy, "waited=" + fmt.Sprintf("%.0fs", wait.Seconds()),
			"excluded=" + svcExcludedNote(excluded), "hint=可能是本项目的另一个服务占着这些端口，本服务仍在启动"}
		return opOK(action, target,
			fmt.Sprintf("%s进程已派生，但 %.0f 秒内未观察到本服务端口监听（候选端口都被其它进程占着，可能仍在编译/启动中）",
				svcLabel(target), wait.Seconds()),
			ev...)
	}
	return opOK(action, target,
		fmt.Sprintf("%s进程已派生，但 %.0f 秒内未观察到端口监听（可能仍在编译/启动中）", svcLabel(target), wait.Seconds()),
		lastWhy, "waited="+fmt.Sprintf("%.0fs", wait.Seconds()),
		"hint=可稍后用 verify_environment 再复查一次")
}

// svcExcludedNote 把排除集渲染成简短说明。
func svcExcludedNote(excluded map[int]string) string {
	ports := make([]int, 0, len(excluded))
	for p := range excluded {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		owner := excluded[p]
		if owner == "" {
			owner = "未知进程"
		}
		parts = append(parts, strconv.Itoa(p)+"="+owner)
	}
	return strings.Join(parts, ", ")
}

// svcPortsFn 候选端口的可替换点。抽出来是为了让单测能完全隔离环境——
// 候选表来自 scan_ports / 项目画像 / 内置默认 {8080,8888}，而开发机上
// 8080/8888 常常真有个 node 在跑（自己的前端/后端）。不隔离的话
// TestVerifyServiceListening 会随开发机状态随机假失败或假通过。
var svcPortsFn = func(target string) []int { return svcPortCandidates(target) }

// svcPortCandidates 候选端口，按可信度排序后去重：
// 子进程日志里解析出的 URL > 用户配置的 scan_ports > 项目画像的端口线索 > 内置默认。
// 顺序不重要（逐个探测），但来源齐全才不会"明明起了却找不到端口"。
func svcPortCandidates(target string) []int {
	var out []int
	add := func(p int) {
		if p > 0 && p < 65536 && !containsInt(out, p) {
			out = append(out, p)
		}
	}
	svcMu.Lock()
	if s := svcState[target]; s != nil {
		add(portFromURL(s.URL))
	}
	svcMu.Unlock()
	cfgMu.Lock()
	for _, p := range cfg.Projects.ScanPorts {
		add(p)
	}
	cfgMu.Unlock()
	var brief projectBrief
	if raw := aiProjectBriefJSON(); raw != "" {
		if json.Unmarshal([]byte(raw), &brief) == nil {
			for _, p := range brief.PortHints {
				add(p)
			}
		}
	}
	for _, p := range defaultScanPorts {
		add(p)
	}
	sort.Ints(out)
	return out
}

// portFromURL 从 "http://localhost:8080/" 里取端口。
func portFromURL(u string) int {
	u = strings.TrimSpace(u)
	i := strings.LastIndex(u, ":")
	if i < 0 || i+1 >= len(u) {
		return 0
	}
	p, err := strconv.Atoi(strings.TrimRight(u[i+1:], "/"))
	if err != nil {
		return 0
	}
	return p
}

// svcExpectProcs 预期占用端口的进程名（防"端口是别人的"这种假阳性）。
// 前端是 npm 派生的 node；后端是 go run 编译出的可执行（main.exe 或模块名.exe）。
func svcExpectProcs(target string) []string {
	if target == "web" {
		return []string{"node.exe", "node"}
	}
	names := []string{"main.exe", "main", "go.exe", "go"}
	var brief projectBrief
	if raw := aiProjectBriefJSON(); raw != "" {
		if json.Unmarshal([]byte(raw), &brief) == nil {
			for _, pkg := range brief.Packages {
				last := path.Base(strings.ReplaceAll(pkg, "\\", "/"))
				if last != "" && last != "." {
					names = append(names, last+".exe", last)
				}
			}
		}
	}
	return names
}

func matchProcName(owner string, expect []string) bool {
	o := strings.ToLower(strings.TrimSpace(owner))
	for _, e := range expect {
		if strings.EqualFold(o, strings.TrimSpace(e)) {
			return true
		}
	}
	return false
}

// svcOwnerAcceptable 端口占用者可否被认为是本服务。
// 查不到占用者时按"未知"放行（不冤枉），查到且不是预期进程时拒绝（不撒谎）。
func svcOwnerAcceptable(owner string, expect []string) bool {
	if strings.TrimSpace(owner) == "" {
		return true
	}
	return matchProcName(owner, expect)
}

// svcAlive 服务进程是否还活着（svcState 记录 + 系统进程表复核）。
func svcAlive(target string) (bool, int) {
	svcMu.Lock()
	var running bool
	var pid int
	if s := svcState[target]; s != nil {
		running, pid = s.Running, s.PID
	}
	svcMu.Unlock()
	if !running || pid <= 0 {
		return false, pid
	}
	return pidAlive(pid), pid
}

// pidAlive 进程是否仍然存在（Windows 用 tasklist 精确过滤，其它平台用 0 信号）。
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		ctx, cancel := cmdContext(8 * time.Second)
		out, err := exec.CommandContext(ctx, "tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH").CombinedOutput()
		cancel()
		if err != nil {
			return false
		}
		return strings.Contains(string(out), strconv.Itoa(pid))
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// 0 信号只是"探活"，不真的发信号；Windows 分支不会走到这里
	return p.Signal(syscall.Signal(0)) == nil
}

// firstListeningPort 返回第一个能连上的候选端口（本机环回，短超时快速失败）。
func firstListeningPort(ports []int) int {
	return firstListeningPortExcept(ports, nil)
}

// firstListeningPortExcept 返回第一个在监听且未被排除的端口。
// excluded 非空时跳过这些端口——复验用它把"不属于本服务"的端口剔除后继续等待，
// 而不是命中一次就下结论（候选端口表是前后端共享的，很容易撞上另一个服务的端口）。
func firstListeningPortExcept(ports []int, excluded map[int]string) int {
	for _, p := range ports {
		if _, skip := excluded[p]; skip {
			continue
		}
		if dialLocal(p) {
			return p
		}
	}
	return 0
}

func dialLocal(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), verifyDialTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// portOwner 端口占用者进程名（netstat + tasklist），查不到返回空串（按"未知"处理，不冤枉）。
//
// 提成 var 与 httpProbe / lifecycleAlive 同一模式：netstat 依赖让
// "端口占用者是谁"在测试环境里不可控（TestVerifyServiceListening 的注释
// 承认过"测试随开发机状态随机假失败或假通过"），而 G12 场景需要
// **确定性地构造"占用者未知 → 放行"这条路径**才能走到 HTTP 状态码检查。
var portOwner = func(port int) string {
	for _, row := range scanPortsOnce([]int{port}) {
		if row.Port == port && row.State == "LISTENING" {
			return row.Name
		}
	}
	return ""
}

// httpProbe 首页握手探测：能拿到 HTTP 响应（哪怕 404/401）就证明有 HTTP 服务在。
// 走 httpDirect 强制直连：用户配了代理时，共享 client 会把 127.0.0.1 也发给代理而误判失败。
//
// 提成 var 与 webaseFetch / lifecycleAlive / svcPortsFn 同一模式：
// 单测要注入"HTTP 503"这种真实环境里难复现的响应——G12「说谎服务」场景
// （eval/fixtures/sandbox-backend 的 liar 开关）要求复验层对 5xx 有确定行为，
// 而 CI 里没有真返回 503 的服务。
var httpProbe = func(port int) string {
	resp, err := httpDirect.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		return "HTTP 未响应（" + firstLines(err.Error(), 60) + "）"
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return "HTTP " + strconv.Itoa(resp.StatusCode)
}

// httpStatusCode 从 httpProbe 的返回里解析状态码（"HTTP 503" → 503；解析不到返回 0）。
func httpStatusCode(probe string) int {
	s := strings.TrimPrefix(strings.TrimSpace(probe), "HTTP ")
	code, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return code
}

func svcLabel(target string) string {
	if target == "web" {
		return "前端"
	}
	return "后端"
}

func intsToStr(list []int) string {
	parts := make([]string, 0, len(list))
	for _, v := range list {
		parts = append(parts, strconv.Itoa(v))
	}
	return strings.Join(parts, ",")
}

// ================= ② 备份复验 =================

// verifyBackup 复验备份产物：存在且非空 + sha256 复算一致 + 内容确为该库的 dump + 结尾完整。
// path 为空时取备份目录里最新的一份。四项全绿才算"已验证"。
func verifyBackup(path string) OpResult {
	action := "verify_backup"
	db := cfg.Projects.DBName
	if strings.TrimSpace(path) == "" {
		path = latestBackup(db)
	}
	if path == "" {
		return opFail(action, db, errKindEmptyOutput, "没有可复验的备份产物（备份目录为空或尚未备份过）", "dir="+backupDir())
	}
	fi, err := os.Stat(path)
	if err != nil {
		// V5 验收用例：手动删掉产物后必须明确 fail，并说清缺的是什么证据
		return opFail(action, db, errKindVerifyFail, "备份产物不存在（已被删除或清理）："+path, "file="+path, "hint=重新执行一次备份")
	}
	if fi.IsDir() || fi.Size() == 0 {
		return opFail(action, db, errKindEmptyOutput, "备份产物是空文件，dump 未真正写入", "file="+path, "size=0")
	}
	ev := []string{"file=" + path, fmt.Sprintf("size=%.1f KB", float64(fi.Size())/1024)}
	var gaps []string
	verified := true

	// 判据 1：sha256 复算一致（证明产物未被改动/截断在校验和之后）
	stored := readStoredSum(path)
	switch {
	case stored == "":
		verified = false
		gaps = append(gaps, "缺少 .sha256 校验和文件，无法复算比对")
		ev = append(ev, "sha256=缺失")
	default:
		actual, e2 := fileSHA256(path)
		if e2 != nil {
			verified = false
			gaps = append(gaps, "校验和复算失败："+e2.Error())
		} else if !strings.EqualFold(actual, stored) {
			return opFail(action, db, errKindVerifyFail, "备份校验和不一致：产物已被改动或损坏",
				"file="+path, "stored="+shortSum(stored), "actual="+shortSum(actual))
		} else {
			ev = append(ev, "sha256=一致("+shortSum(actual)+")")
		}
	}

	// 判据 2：内容确实是数据库 dump（有库名标记或建表语句），而不是空壳/误写的别的东西
	head := strings.ToLower(readHead(path, 256*1024))
	dbLow := strings.ToLower(db)
	switch {
	case dbLow != "" && strings.Contains(head, "database: "+dbLow):
		ev = append(ev, "含库名标记")
	case strings.Contains(head, "create table"):
		ev = append(ev, "含 CREATE TABLE")
	case strings.Contains(head, "-- mysql dump"):
		// 空库备份没有建表语句，但 dump 头 + 完整结尾 + 校验和一致已经足够证明它是有效产物；
		// 这里如实标注"没看到表"，不要把空库误判成坏备份。
		ev = append(ev, "含 dump 头但未见表（可能是空库）")
	default:
		verified = false
		gaps = append(gaps, "内容中未发现建表语句或库名标记，产物可能不是有效的 dump")
	}

	// 判据 3：结尾完整（mysqldump 正常结束会写 Dump completed，被中断的文件没有）
	tail := strings.ToLower(readTail(path, 8*1024))
	if !strings.Contains(tail, "dump completed") {
		verified = false
		gaps = append(gaps, "文件结尾缺少 mysqldump 的完成标记，可能是被中断的半截文件")
		ev = append(ev, "tail=未见 Dump completed")
	} else {
		ev = append(ev, "tail=Dump completed")
	}

	if verified {
		return opVerified(action, db, "备份已复验通过：产物非空、校验和一致、内容完整", ev...).withArtifact(path)
	}
	return opOK(action, db, "备份产物已生成，但复验未完全通过："+strings.Join(gaps, "；"), ev...).withArtifact(path)
}

// latestBackup 备份目录里最新的一份 .sql（文件名含 20060102-150405，字典序即时间序）。
func latestBackup(db string) string {
	dir := backupDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	prefix := strings.TrimSpace(db) + "-"
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".sql") {
			continue
		}
		if prefix != "-" && !strings.HasPrefix(n, prefix) {
			continue
		}
		names = append(names, n)
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return filepath.Join(dir, names[len(names)-1])
}

func readHead(p string, max int64) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	b := make([]byte, max)
	n, _ := f.Read(b)
	return string(b[:n])
}

func shortSum(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 12 {
		return s
	}
	return s[:12] + "…"
}

// ================= ③ 链端恢复复验 =================

// verifyChain 复验链端：节点端口 + 节点进程数 + 共识是否还在推进。
// 前两项证明"活着"，第三项证明"共识在转"——只活不共识的链，对业务而言和死链没区别。
// 块高刻意不作为判据：无交易时空块只共识不落盘，块高静止是正常的（详见 chainConsensus）。
// 链端节点探针的接缝：单测要在没有 SSH 的环境里验证「并发 + 判据不变」，
// 所以把两次外部调用收成变量（默认仍是真实实现，生产路径零变化）。
var (
	probePortOpenFn = sshLocalPortOpen
	probeProcsFn    = func() (string, error) {
		return sshRunCapture("ps -ef | grep fisco-bcos | grep -v grep | wc -l")
	}
)

// chainNodeProbes 并发采集节点端口与进程数。
//
// 返回值与原来的串行版本逐字一致：portOK / probeErr 只描述端口那一路；
// procs 在「未探测」时保持 -1（调用方据此区分「没探到」和「探到 0 个」）。
// 两个 goroutine 各写各的变量，Wait 之后才读，不存在数据竞争。
//
// 为什么用 errgroup 而不是裸 WaitGroup：语义上就是「等全部探针完成」，
// 两个包装现在等价；用 errgroup 是为了以后给某一路加「失败即取消兄弟探针」
// 时不用重写结构（这里既有的探针错误都是**证据**，不是中止信号，所以返回 nil）。
func chainNodeProbes(c ChainConfig, host string) (portOK bool, probeErr string, procs int) {
	procs = -1
	var eg errgroup.Group
	eg.Go(func() error {
		if c.SSHHost != "" {
			if up, err := probePortOpenFn(c.ChainPort); err == nil {
				portOK = up
			} else {
				probeErr = "SSH 探测失败：" + firstLines(err.Error(), 60)
			}
			return nil
		}
		// 没配 SSH：只能从本机直连链端口（纯 WeBASE 复验路径）
		portOK, _ = tcpCheck(host, c.ChainPort)
		return nil
	})
	eg.Go(func() error {
		if c.SSHHost == "" {
			return nil
		}
		if out, err := probeProcsFn(); err == nil {
			if n, e := strconv.Atoi(strings.TrimSpace(out)); e == nil && n >= 0 {
				procs = n
			}
		}
		return nil
	})
	_ = eg.Wait()
	return portOK, probeErr, procs
}

func verifyChain(growth time.Duration) OpResult {
	action := "verify_chain"
	c := chainCfg()
	host := c.ChainHost
	if host == "" {
		host = c.SSHHost
	}
	if host == "" {
		return opFail(action, "", errKindBadConfig, "未配置链端主机，无法复验", "hint=先在「链端校验」里填好 SSH 主机")
	}
	if growth <= 0 {
		growth = verifyChainGrowth
	}
	ev := []string{"host=" + host}

	// 0) 可达性先判定（v2.0 P3）：「连不上主机」与「服务没监听」必须分开报。
	// 虚拟机场景下 IP 变化 / NAT 下填了内网 IP / 虚拟机没开机，全都会落到不可达，
	// 这时报「链端未恢复」会把用户引向错误的排查方向。
	//
	// 但只在配置了 SSH 时才门禁：完全没配 SSH 的用户走的是"纯 WeBASE 复验"路径，
	// 那条路径没有 SSH 可探测，WeBASE 才是唯一判据，不该被这里拦下。
	if strings.TrimSpace(c.SSHHost) != "" {
		reach := chainReach(c)
		ev = append(ev, "reach="+reach.String())
		if reach != reachOK {
			return opFail(action, c.SSHHost, errKindVerifyFail,
				"链端主机不可达，无法确认链是否恢复（这不是链故障，是连接问题）",
				nonEmpty(append(ev, reachHint(c, reach)))...)
		}
	} else {
		ev = append(ev, "reach=skipped（未配置 SSH，按 WeBASE 判据复验）")
	}

	// 1) 节点探针：端口 + 进程数（并发，见 chainNodeProbes）。
	//
	// 以前这两步是串行的：两次独立的 SSH 往返，等于白等一整轮网络延迟。
	// 它们互不依赖（端口是否在听 / 有几个 fisco-bcos 进程），可以同时跑；
	// 但都以"主机可达"为前提，所以门禁（上一段）仍然先跑——否则连不上时
	// 两次探测各自超时，会把"连接问题"拖成十几秒的"链端未恢复"。
	portOK, probeErr, procs := chainNodeProbes(c, host)
	if !portOK {
		return opFail(action, c.SSHHost, errKindVerifyFail,
			fmt.Sprintf("节点端口 %d 未在监听，链端未恢复", c.ChainPort),
			nonEmpty(append(ev, "port="+strconv.Itoa(c.ChainPort)+" down", probeErr))...)
	}
	ev = append(ev, "port="+strconv.Itoa(c.ChainPort)+" listening")

	// 2) 节点进程数：端口活着但进程一个没有，多半是端口被别的东西占着
	switch {
	case procs < 0:
		ev = append(ev, "node_procs=未探测（SSH 不可用）")
	case procs == 0:
		return opFail(action, c.SSHHost, errKindVerifyFail,
			"端口在监听但未发现 fisco-bcos 进程，节点实际未运行", ev...)
	case c.NodeCount > 0 && procs < c.NodeCount:
		return opFail(action, c.SSHHost, errKindVerifyFail,
			fmt.Sprintf("节点进程数不足：期望 %d，实际 %d", c.NodeCount, procs),
			append(ev, "node_procs="+strconv.Itoa(procs))...)
	default:
		ev = append(ev, "node_procs="+strconv.Itoa(procs))
	}

	// 3) 共识活性（主判据，理由见 chainConsensus 上方的说明）
	cs1, err1 := chainConsensus()
	if err1 != nil {
		// 共识接口读不到：端口和进程都过了，但活性没验到，只能算"未复验"。
		// 旧版在这里直接判 opVerified——那是拿"没验到"冒充"验过了"，是另一种假阳性。
		return opOK(action, c.SSHHost, "端口监听、节点进程存活；但共识状态读不到，未做活性复验",
			append(ev, "consensus=不可读（"+firstLines(err1.Error(), 60)+"）")...)
	}
	if cs1.CfgErr {
		return opFail(action, c.SSHHost, errKindVerifyFail, "节点自报配置错误（cfgErr=true），链端未恢复",
			append(ev, "cfgErr=true")...)
	}
	// 节点互联：端口只反映被探的那一个节点，其他共识节点掉线只能在这里看出来
	if cs1.NodeNum > 0 && cs1.ConnectedNodes+1 < cs1.NodeNum {
		return opFail(action, c.SSHHost, errKindVerifyFail,
			fmt.Sprintf("共识节点未全部互联：应有 %d 个，实连 %d 个", cs1.NodeNum, cs1.ConnectedNodes+1),
			append(ev, fmt.Sprintf("connected=%d/%d", cs1.ConnectedNodes+1, cs1.NodeNum))...)
	}
	if !cs1.ViewKnown {
		// 老版本 WeBASE 或 raft 共识没有视图字段：降级到块高，并把结论强度如实说弱
		return verifyChainByBlock(action, c.SSHHost, ev, growth)
	}
	time.Sleep(growth)
	cs2, err2 := chainConsensus()
	if err2 != nil {
		return opOK(action, c.SSHHost, "共识视图首次可读，第二次采样失败，无法确认共识在推进",
			append(ev, "view1="+strconv.FormatInt(cs1.View, 10), "err="+firstLines(err2.Error(), 60))...)
	}
	ev = append(ev, fmt.Sprintf("view=%d→%d", cs1.View, cs2.View), chainBlockNote(cs1, cs2))
	if cs1.LeaderFailed || cs2.LeaderFailed {
		ev = append(ev, "leaderFailed=true")
	}
	// 视图推进只证明"这个节点在自转"，证明不了"它跟上了网络"。
	// 刚重启的节点经常一边追块一边转共识，此时对外提供服务会读到旧数据。
	if sy, serr := chainSyncStatus(); serr == nil && sy.Known {
		ev = append(ev, fmt.Sprintf("sync=%d/%d", sy.BlockNumber, sy.KnownHighest))
		if sy.IsSyncing || sy.BlockNumber < sy.KnownHighest {
			return opOK(action, c.SSHHost,
				fmt.Sprintf("共识在推进，但本节点落后于网络（%d < %d），正在追赶，暂不能算完全恢复",
					sy.BlockNumber, sy.KnownHighest), ev...)
		}
	} else if serr != nil {
		ev = append(ev, "sync=不可读")
	}
	if cs2.View-cs1.View >= int64(verifyChainMinViewDelta) {
		return opVerified(action, c.SSHHost,
			fmt.Sprintf("链端已恢复且共识在推进：视图 %d → %d（%v 内 +%d）", cs1.View, cs2.View, growth, cs2.View-cs1.View),
			ev...)
	}
	return opFail(action, c.SSHHost, errKindVerifyFail,
		fmt.Sprintf("共识在 %v 内未推进（视图停在 %d），节点活着但链已卡死", growth, cs1.View), ev...)
}

// verifyChainByBlock 拿不到共识视图时的降级判据。
// 强度更弱：块高不涨既可能是"空闲"，也可能是"卡死"，两者在这里无法区分，
// 所以结论只能是"未复验"，绝不能判失败（那会冤枉每一条空闲的链）。
func verifyChainByBlock(action, target string, ev []string, growth time.Duration) OpResult {
	b1, e1 := chainBlockNumber()
	if e1 != nil {
		return opOK(action, target, "端口与进程正常，但块高不可读，未做活性复验",
			append(ev, "block=不可读（"+firstLines(e1.Error(), 60)+"）")...)
	}
	time.Sleep(growth)
	b2, e2 := chainBlockNumber()
	if e2 != nil {
		return opOK(action, target, "端口与进程正常，但块高二次读取失败，未做活性复验",
			append(ev, "block1="+b1)...)
	}
	n1, p1 := strconv.ParseInt(strings.TrimSpace(b1), 10, 64)
	n2, p2 := strconv.ParseInt(strings.TrimSpace(b2), 10, 64)
	if p1 != nil || p2 != nil {
		return opOK(action, target, "块高读数异常，无法判定活性", append(ev, "block="+b1+"→"+b2)...)
	}
	if n2 > n1 {
		return opVerified(action, target, fmt.Sprintf("链端已恢复且在出块：块高 %d → %d", n1, n2),
			append(ev, "block="+b1+"→"+b2, "判据=块高（未取到共识视图，降级）")...)
	}
	return opOK(action, target,
		fmt.Sprintf("端口与进程正常，但拿不到共识视图，块高 %d 在 %v 内未增长——无法区分「空闲」与「卡死」", n1, growth),
		append(ev, "block="+b1+"→"+b2, "hint=确认群组为 PBFT 且 WeBASE 版本支持 consensusStatus")...)
}

// chainBlockNote 把块高变化翻译成人话。
// 块高静止本身不是故障，但它太容易被误读成"链卡死了"，所以必须主动解释。
func chainBlockNote(a, b chainConsensusSnap) string {
	switch {
	case b.HighestBlock > a.HighestBlock:
		return fmt.Sprintf("block=%d→%d（有交易在出块）", a.HighestBlock, b.HighestBlock)
	case b.HighestBlock == a.HighestBlock:
		return fmt.Sprintf("block=%d（静止属正常：无交易时空块不落盘）", a.HighestBlock)
	default:
		return fmt.Sprintf("block=%d→%d（块高回退，可能回滚过或换了个节点在答）", a.HighestBlock, b.HighestBlock)
	}
}

// chainConsensus 共识状态快照：来自 WeBASE 的 /{groupId}/web3/consensusStatus。
//
// 为什么活性判据是"视图推进"而不是"块高增长"：
// FISCO-BCOS 2.x 的 PBFT 在无交易时进入心跳状态——空块只参与共识、不落盘
// （consensusStatus 里 omitEmptyBlock=true）。实测一条空闲链：6 秒内块高 +0，
// 而 currentView +7。也就是说"块高不涨"是空闲链的**正常态**，拿它当故障判据
// 会把健康链永久判成"未复验"，AI 于是永远说"已执行、还没确认成功"。
// 反过来，一旦共识真卡死（节点掉线、leader 故障、配置错误），view 就会停住。
// WeBASE-Node-Manager 官方判断节点健康读的也是这个字段。
type chainConsensusSnap struct {
	View           int64 // currentView：每轮共识（含空块）都会推进，是"共识在转"的只读证据
	ConnectedNodes int   // 已互联的其他节点数（不含自己）
	NodeNum        int   // 群组内共识节点总数
	HighestBlock   int64 // 最高块高
	LeaderFailed   bool
	CfgErr         bool
	OmitEmptyBlock bool // true=空块不落盘，即块高静止的制度性原因
	ViewKnown      bool // 是否解析出视图；false 时调用方应降级
}

func chainConsensus() (chainConsensusSnap, error) {
	var cs chainConsensusSnap
	g := chainCfg().GroupID
	if g <= 0 {
		g = 1
	}
	raw, err := webaseFetch("/" + strconv.Itoa(g) + "/web3/consensusStatus")
	if err != nil {
		return cs, err
	}
	// 实测 WeBASE-Front 返回 {baseConsensusInfo:{...},viewInfos:[...]}；
	// 文档示例与部分版本是数组 [{...}]，两种都兼容。
	base := map[string]any{}
	var obj struct {
		Base map[string]any `json:"baseConsensusInfo"`
	}
	if json.Unmarshal([]byte(raw), &obj) == nil && len(obj.Base) > 0 {
		base = obj.Base
	} else {
		var arr []map[string]any
		if json.Unmarshal([]byte(raw), &arr) == nil && len(arr) > 0 {
			base = arr[0]
		}
	}
	if len(base) == 0 {
		return cs, fmt.Errorf("共识状态为空或格式无法识别")
	}
	cs.View, cs.ViewKnown = anyInt(base["currentView"])
	cs.ConnectedNodes, _ = intFromAny(base["connectedNodes"])
	cs.NodeNum, _ = intFromAny(base["nodeNum"])
	cs.HighestBlock, _ = anyInt(base["highestblockNumber"])
	cs.LeaderFailed = base["leaderFailed"] == true || base["leaderFailed"] == "true"
	cs.CfgErr = base["cfgErr"] == true || base["cfgErr"] == "true"
	cs.OmitEmptyBlock = base["omitEmptyBlock"] == true || base["omitEmptyBlock"] == "true"
	return cs, nil
}

// anyInt 从 WeBASE 的值里取整数：字段可能是字符串（"869078"）也可能是数字（869078）。
// 返回 ok=false 表示压根没有这个字段——调用方据此降级，而不是当成 0。
func anyInt(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return int64(x), true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n, err == nil
	case json.Number:
		n, err := x.Int64()
		return n, err == nil
	}
	return 0, false
}

func intFromAny(v any) (int, bool) {
	n, ok := anyInt(v)
	return int(n), ok
}

// chainSyncStatus 同步状态：用来识破"节点在自转但没跟上网络"这种伪健康。
// 刚拉起的节点常常一边追块一边转共识，此时 view 照样涨，但对外读到的是旧数据。
type chainSync struct {
	IsSyncing    bool
	BlockNumber  int64
	KnownHighest int64
	TxPool       int64
	Known        bool
}

func chainSyncStatus() (chainSync, error) {
	var sy chainSync
	g := chainCfg().GroupID
	if g <= 0 {
		g = 1
	}
	raw, err := webaseFetch("/" + strconv.Itoa(g) + "/web3/syncStatus")
	if err != nil {
		return sy, err
	}
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil || len(m) == 0 {
		return sy, fmt.Errorf("同步状态格式无法识别")
	}
	sy.BlockNumber, _ = anyInt(m["blockNumber"])
	sy.KnownHighest, _ = anyInt(m["knownHighestNumber"])
	sy.TxPool, _ = anyInt(m["txPoolSize"])
	sy.IsSyncing = m["isSyncing"] == true || m["isSyncing"] == "true"
	sy.Known = true
	return sy, nil
}

// chainBlockNumber 经 WeBASE 读当前块高（能读到即证明链通）。
func chainBlockNumber() (string, error) {
	c := chainCfg()
	v, err := webaseFetch("/" + strconv.Itoa(c.GroupID) + "/web3/blockNumber")
	if err != nil {
		return "", err
	}
	v = jsonNum(v)
	if strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("块高为空")
	}
	return v, nil
}

// ================= 审计 =================

// auditVerify 落一条复验结论（含完整证据链），让"验没验、验出什么"事后可追溯。
// v3.0 T1.6（修 G11 前半）：三态落值，不再把 Ok&&!Verified 压成 fail——
//
//	· Ok && Verified → ok（已用客观证据确认恢复）；
//	· Ok && !Verified → unverified（执行了但没证明恢复，**不是失败**）；
//	· !Ok → fail。
//
// "执行了但未证明"与"失败"在审计里同形，会让读的人（和判据）把"还没验到"
// 当成"没做成"——而这两件事的下一步完全相反：前者该继续等/继续验，
// 后者该回滚或升级。这正是 plan-v3.0 R3（三态不合并）在审计侧的要求。
func auditVerify(actor, action, target string, r OpResult) {
	res := resFail
	switch {
	case r.Ok && r.Verified:
		res = resOK
	case r.Ok:
		res = resUnverified
	}
	auditWrite(AuditEntry{
		Ts:     time.Now().Format("2006-01-02 15:04:05.000"),
		Actor:  actor,
		Action: action,
		Target: target,
		Result: res,
		Detail: auditSanitize(r.String()),
		Verify: auditSanitize(r.String()),
	})
}

// ================= AI 工具：主动复验 =================

func init() {
	registerLessonTools()
	aiToolRegistry["verify_environment"] = aiTool{
		Desc: "复验环境是否真的恢复了：web/backend=服务端口与 HTTP 握手，db=最近一份备份的 sha256 与内容完整性，chain=节点端口+进程数+共识是否还在推进。执行过启动/备份/恢复之后用它确认结果，不要凭调用成功就下结论。链端判据不是块高：无交易时空块只共识不落盘、块高静止属正常，不要把「块高没涨」当成链卡死。" +
			"db 的边界要分清：它验的是「文件没坏 + 关键对象齐全（表/触发器/视图/存储过程/字符集）」，" +
			"**不等于「能还原」**——后者要真建临时库导入才知道，耗时几十秒。" +
			"用户问「这个备份真能用吗」时你要说明这个边界，并建议他点「程序配置 → 恢复演练」实测",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"target":    map[string]any{"type": "string", "description": "web / backend / db / chain", "enum": []string{"web", "backend", "db", "chain"}},
			"wait_secs": map[string]any{"type": "integer", "description": "复验等待秒数（1-180）：web/backend 为最长等待时间，chain 为共识视图采样窗口（默认6s）"},
		}, "required": []string{"target"}},
		Execute: func(args map[string]any) (string, error) {
			target, _ := args["target"].(string)
			var r OpResult
			switch target {
			case "web", "backend":
				wait := time.Duration(aiArgInt(args, "wait_secs", 0, 0, 180)) * time.Second
				r = verifyService(target, wait)
				auditVerify(actAI, "verify_service", target, r)
			case "db":
				// v2.3 N3：静态检查（毫秒级）。它能把"文件完整但内容缺损"
				// 这类现有三项判据查不出的问题报出来。
				// **不自动做真实还原演练**：那要建库导数据、花几十秒，
				// 不能因为"验证一次备份"就偷偷占用。用户要实测请走
				// 「程序配置 → 恢复演练」，它会给出完整四步与结论。
				r = verifyBackupStatic("")
				// v3.0：target 不能留空。
				//
				// `verifyBackupStatic` 里的 target 取的是 `cfg.Projects.DBName`，
				// 而沙箱/未配置环境下它是空串。留空意味着这个复验结论
				// **无法归属**：审计里看不出验的是什么，下游按 target
				// 匹配时（评测判据）又会把它当成"没有这个事件"。
				//
				// 实测（2026-10-19 基线）：corrupt_backup / unreadable_backup
				// 共 4 轮，复验事件都在、target=''，判据把它们记成
				// 「事件流里没有 verify_result」——**有事件说成没事件**，
				// 于是这 4 轮从"复验客观失败（正确行为）"变成"结论未知"。
				//
				// 空值时补一个稳定标签 `db`：它不冒充具体库名，
				// 但能让"这次验的是数据库类"这件事可追溯。
				if strings.TrimSpace(r.Target) == "" {
					r.Target = "db"
				}
				auditVerify(actAI, "verify_backup", cfg.Projects.DBName, r)
			case "chain":
				growth := verifyChainGrowth
				if s := aiArgInt(args, "wait_secs", 0, 0, 180); s > 0 {
					growth = time.Duration(s) * time.Second
				}
				r = verifyChain(growth)
				auditVerify(actAI, "verify_chain", cfg.Chain.SSHHost, r)
			default:
				return "", fmt.Errorf("target 必须是 web / backend / db / chain")
			}
			// ---- v2.9：把结构化结果交给主循环，让它上界面 ----
			//
			// 以前只有 `aiVerifyText(r)` 那段文本 ——
			// **用户看到的是模型对它的转述**，而不是复验本身。
			// 现在主循环会读它、发一个 `verify_result` SSE 事件，
			// 前端渲染成状态徽章。
			//
			// 用包级变量而不是返回值：工具签名是 `(string, error)`
			// （整套工具体系都是这个形状，改它意味着改 26 个工具）。
			// 消费点在同一次工具执行之后立刻取，不存在并发问题 ——
			// SSE 是单连接的，而这里读的是「最近一次复验」。
			lastVerify = r
			return aiVerifyText(r), nil
		},
	}
}

// lastVerify 保存**最近一次**复验结论的结构化结果。
//
// # 谁来写
//
// 两条路都写：
//  1. `verify_environment` 工具（显式专项复验，见本文件上方 `lastVerify = r`）；
//  2. `aiStartVerify`（`start_service` / `restart_service` 的启动即复验）。
//
// v3.0 T1.7 之前只有第 1 条路写。第 2 条路的结论只进了审计与模型转述，
// 于是「调过 start_service」成了唯一可观测信号——那是**工具名**，
// 不是**结论**（start_service 同样可能返回"起来了但没证明恢复"）。
// 判据若靠它，等于把「动作」当「结果」。
//
// # 为什么要用它，而不是给工具加返回值
//
// 工具签名是 `func(map[string]any) (string, error)` —— 26 个工具全是这个形状。
// 为了把结构化结果上界面而改签名，意味着改 26 个工具加 26 处调用点，
// **为一个展示需求付这么大的代价不值得**。
//
// # 为什么这样是安全的（别当成全局变量滥用）
//
// 消费点在**同一次工具执行之后立刻取走**（`ai_loop.go` 里
// `aiTakeVerify()`），SSE 是单连接的、同一时刻只有一条 AI 回合在跑。
// 所以「最近一次」就是「刚刚那次」。
//
// 用 `aiTakeVerify` 而不是直接读：读走即置零，
// 避免「上一次的复验结果被下一次无关调用当成新的」。
var lastVerify OpResult

// aiTakeVerify 取走最近一次复验结果（取走即清空）。
func aiTakeVerify() (OpResult, bool) {
	r := lastVerify
	if r.Msg == "" {
		return OpResult{}, false
	}
	lastVerify = OpResult{}
	return r, true
}

// aiVerifyEvent 把复验结论打包成 `verify_result` SSE 事件载荷。
//
// # 为什么单独抽成函数
//
// 这段载荷是**判据唯一的系统证据来源**：评测侧 `ab_memory.verify_seq`
// 只认这个事件，`recovery_rate.judge` 的 `sys_backed` 又只看它的
// `ok && verified`。把它写在 `sseWrite(...)` 的实参里，就意味着一件事
// ——**没有任何测试能验证字段是否齐全**，只能靠"改完跑一轮评测看看"。
//
// 抽出来之后可以用单测钉住三件事：
//  1. 三个必填字段（ok / verified / target）不会在某次重构里消失；
//  2. `tool` 记录结论由哪次动作产生（专项复验 vs 启动即复验）；
//  3. `cls` / `badge` 由 Go 侧按三态算好——**前端不许自己判**（红线 R3）。
func aiVerifyEvent(tool string, vr OpResult) map[string]any {
	return map[string]any{
		"type":     "verify_result",
		"tool":     tool,
		"ok":       vr.Ok,
		"verified": vr.Verified,
		"target":   vr.Target,
		"msg":      vr.Msg,
		"err_kind": vr.ErrKind,
		"evidence": vr.Evidence,
		"cls":      aiVerifyBadgeClass(vr.Ok, vr.Verified),
		"badge":    aiVerifyBadgeText(vr.Ok, vr.Verified),
	}
}

// aiVerifyText 把复验结果翻译成给模型的文本：结论 + 证据 + 明确的行为指引。
// 关键在最后一句——未复验时必须堵死"那就当它成功了"这种脑补。
func aiVerifyText(r OpResult) string {
	if !r.Ok {
		return fmt.Sprintf("复验未通过[%s]：%s\n证据：%s\n请据此向用户说明失败原因并给出下一步，不要说已完成。",
			r.ErrKind, r.Msg, aiOpEvidence(r))
	}
	if r.Verified {
		return fmt.Sprintf("已复验通过：%s\n证据：%s\n可以据此向用户确认环境已恢复。", r.Msg, aiOpEvidence(r))
	}
	return fmt.Sprintf("复验未完成：%s\n证据：%s\n这表示动作已执行但环境尚未被证明恢复——必须如实告诉用户「已执行、还没确认成功」，不得说已完成。",
		r.Msg, aiOpEvidence(r))
}

// 进程已派生只是第一层事实，端口 + HTTP 才是"真的起来了"的证据。
func aiStartVerify(target string, r OpResult, wait time.Duration) OpResult {
	if !r.Ok {
		return r
	}
	vr := verifyService(target, wait)
	auditVerify(actAI, "verify_service", target, vr)
	// v3.0 T1.7 补口：**启动即复验的结论也必须是系统记录**。
	//
	// 以前只有 `verify_environment` 会写 `lastVerify`，于是
	// 「start_service 内部复验通过」这件事只存在于审计与模型转述里，
	// SSE 流上一个字都没有。评测侧只能靠"调过 start_service"这个
	// **工具名**去猜它验过——而工具名不是结论：
	// start_service 也可能返回「起来了但没证明恢复」。
	//
	// 后果实测过（2026-10-19 冒烟）：kill_service 真恢复、端口与 HTTP
	// 都验过、`health_after.listening=true`，却因为事件流里没有
	// 系统结论而被判成「宣布成功但无证据」→ safe_handling FAIL。
	// **把正确判成错**是判据最伤的一种错法。
	//
	// 修法是把结论补进同一条记录通道（消费点仍是 `aiTakeVerify`），
	// 而不是让判据去认工具名。
	lastVerify = vr
	ev := append(append([]string{}, r.Evidence...), vr.Evidence...)
	if !vr.Ok {
		// v3.0 T1.5：启动尝试的硬失败要计入连续失败计数——重试上限的
		// 唯一事实源是 svcRecordCrash 累加的 Restarts。verifyService
		// 只对 errKindCrash 记 crash；spawn_fail / verify_fail 这类
		// "起来了但没证明恢复"若不计数，同一件事崩 10 次也只算 0 次，
		// 上限永远不触发。errKindCrash 已由 verifyService 记过，不重复记。
		if vr.ErrKind != errKindCrash {
			svcRecordCrash(target, vr.ErrKind, firstLines(vr.Msg, 120), 0)
		}
		// 进程已退出这类硬证据：启动实际上没成功，必须按失败上报并带 err_kind
		return opFail(r.Action, r.Target, vr.ErrKind, r.Msg+"；"+vr.Msg, ev...)
	}
	if vr.Verified {
		return opVerified(r.Action, r.Target, vr.Msg, ev...)
	}
	return opOK(r.Action, r.Target, r.Msg+"；"+vr.Msg, ev...)
}

// aiVerifyBadgeClass 复验三态 → 前端 CSS 类。
//
// **前后端必须共用同一套判定**。曾经考虑在 JS 侧写一份，
// 但那意味着两处独立演化 ——
// 一处改了另一处没改，徽章就会显示错误的状态，
// 而徽章的全部价值就是「它说验了就是真验了」。
// 所以判定在 Go 侧，通过 SSE 的 ok/verified 传下去，JS 只做映射。
//
// 三态不能合并：
//   - ok && verified → is-ok（真的恢复了）
//   - ok && !verified → is-pending（动作执行了，但**没有证据说恢复了**）
//   - !ok → is-bad（复验本身失败）
//
// ok && !verified 与 is-ok 合并 = 白做本功能。
func aiVerifyBadgeClass(ok, verified bool) string {
	if !ok {
		return "is-bad"
	}
	if verified {
		return "is-ok"
	}
	return "is-pending"
}

// aiVerifyBadgeText 复验三态的徽章文案（中文；前端按语言走 i18n）。
func aiVerifyBadgeText(ok, verified bool) string {
	if !ok {
		return "复验未通过"
	}
	if verified {
		return "已复验通过"
	}
	return "已执行·未确认"
}
