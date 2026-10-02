package main

// verify.go —— "执行 → 验证"闭环的验证器（v2.0 P2）
//
// 背景（也是这一版的立意）：P0 之前任务函数一律 return nil，AI 于是系统性谎报"已启动"；
// P0 让失败不再被吞，但仍只证明"命令发出去了"。P2 补的是最后半步——
// **用客观证据回答"现实环境真的恢复了吗"**，而不是回答"我执行过了"。
//
// 三条设计约束：
//  1. 只读：验证器不改任何东西，只采集证据（端口、进程、HTTP 码、sha256、块高）。
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
)

// ---------- 复验超时窗口 ----------
const (
	verifyWaitWeb     = 45 * time.Second // 前端：vue/vite 首次编译常常要 20s+，窗口太短必然假阴性
	verifyWaitBackend = 25 * time.Second // 后端：go build 已在启动阶段完成，run 起来很快
	verifyPollEvery   = 2 * time.Second
	verifyDialTimeout = 700 * time.Millisecond
	verifyChainGrowth = 6 * time.Second // 块高采样窗口：FISCO-BCOS 正常会持续出块
)

// ================= ① 服务启动复验 =================

// verifyService 复验前端/后端是否真的起来了：进程存活 + 端口监听 + HTTP 握手。
// wait<=0 时按服务类型取默认窗口；超时不算"失败"，而算"未复验"（可能仍在编译）。
func verifyService(target string, wait time.Duration) OpResult {
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
	ports := svcPortCandidates(target)
	if len(ports) == 0 {
		return opOK(action, target, "没有可用端口线索，跳过复验（可在「程序配置」的 scan_ports 里补上服务端口）")
	}
	expect := svcExpectProcs(target)
	deadline := time.Now().Add(wait)
	var lastWhy string
	for {
		alive, pid := svcAlive(target)
		if !alive {
			// 进程已经没了：这是"启动失败"的硬证据，不能降级成"已启动但没验到"
			return opFail(action, target, errKindSpawnFail,
				svcLabel(target)+"进程已退出，启动未成功（详见「程序启动」日志）",
				"pid="+strconv.Itoa(pid), "hint=常见原因是端口被占用、依赖缺失或编译产物崩溃")
		}
		if port := firstListeningPort(ports); port > 0 {
			owner := portOwner(port)
			ev := []string{"pid=" + strconv.Itoa(pid), "port=" + strconv.Itoa(port) + " LISTENING"}
			if owner != "" {
				ev = append(ev, "owner="+owner)
			}
			// 端口被"别人的"进程占着时不能算我们的服务起来了——这正是假阳性的典型来源
			if !svcOwnerAcceptable(owner, expect) {
				return opOK(action, target,
					fmt.Sprintf("端口 %d 被 %s 占用，与预期进程（%s）不符，不能认定%s已就绪",
						port, owner, strings.Join(expect, "/"), svcLabel(target)),
					ev...)
			}
			ev = append(ev, httpProbe(port))
			return opVerified(action, target,
				fmt.Sprintf("%s已就绪：进程存活且端口 %d 在监听", svcLabel(target), port), ev...)
		}
		lastWhy = "进程存活（PID " + strconv.Itoa(pid) + "），候选端口 " + intsToStr(ports) + " 均未监听"
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(verifyPollEvery)
	}
	return opOK(action, target,
		fmt.Sprintf("%s进程已派生，但 %.0f 秒内未观察到端口监听（可能仍在编译/启动中）", svcLabel(target), wait.Seconds()),
		lastWhy, "waited="+fmt.Sprintf("%.0fs", wait.Seconds()),
		"hint=可稍后用 verify_environment 再复查一次")
}

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
	for _, p := range ports {
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
func portOwner(port int) string {
	for _, row := range scanPortsOnce([]int{port}) {
		if row.Port == port && row.State == "LISTENING" {
			return row.Name
		}
	}
	return ""
}

// httpProbe 首页握手探测：能拿到 HTTP 响应（哪怕 404/401）就证明有 HTTP 服务在。
// 走 httpDirect 强制直连：用户配了代理时，共享 client 会把 127.0.0.1 也发给代理而误判失败。
func httpProbe(port int) string {
	resp, err := httpDirect.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		return "HTTP 未响应（" + firstLines(err.Error(), 60) + "）"
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return "HTTP " + strconv.Itoa(resp.StatusCode)
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

// verifyChain 复验链端：节点端口 + 节点进程数 + 块高在窗口内递增。
// 前两项证明"活着"，第三项证明"在出块"——只活不出块同样是不可用的链。
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

	// 1) 节点端口：优先经 SSH 到远程本机探（节点可能已加固为仅本地监听）
	portOK := false
	var probeErr string
	if c.SSHHost != "" {
		if up, err := sshLocalPortOpen(c.ChainPort); err == nil {
			portOK = up
		} else {
			probeErr = "SSH 探测失败：" + firstLines(err.Error(), 60)
		}
	} else {
		portOK, _ = tcpCheck(host, c.ChainPort)
	}
	if !portOK {
		return opFail(action, c.SSHHost, errKindVerifyFail,
			fmt.Sprintf("节点端口 %d 未在监听，链端未恢复", c.ChainPort),
			nonEmpty(append(ev, "port="+strconv.Itoa(c.ChainPort)+" down", probeErr))...)
	}
	ev = append(ev, "port="+strconv.Itoa(c.ChainPort)+" listening")

	// 2) 节点进程数：端口活着但进程一个没有，多半是端口被别的东西占着
	procs := -1
	if c.SSHHost != "" {
		if out, err := sshRunCapture("ps -ef | grep fisco-bcos | grep -v grep | wc -l"); err == nil {
			if n, e := strconv.Atoi(strings.TrimSpace(out)); e == nil && n >= 0 {
				procs = n
			}
		}
	}
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

	// 3) 块高递增
	b1, err1 := chainBlockNumber()
	if err1 != nil {
		// WeBASE 不可达时读不到块高：降级——端口+进程已通过即认定恢复，但要说清没验到什么
		return opVerified(action, c.SSHHost, "链端已恢复（端口监听、节点进程存活）；WeBASE 不可达，未做块高复验",
			append(ev, "block=不可读（"+firstLines(err1.Error(), 60)+"）")...)
	}
	time.Sleep(growth)
	b2, err2 := chainBlockNumber()
	if err2 != nil {
		return opOK(action, c.SSHHost, "端口与进程正常，但第二次读取块高失败，无法确认链在出块",
			append(ev, "block1="+b1, "err="+firstLines(err2.Error(), 60))...)
	}
	n1, e1 := strconv.ParseInt(strings.TrimSpace(b1), 10, 64)
	n2, e2 := strconv.ParseInt(strings.TrimSpace(b2), 10, 64)
	if e1 != nil || e2 != nil {
		return opOK(action, c.SSHHost, "块高读数异常，无法判定是否在出块", append(ev, "block="+b1+"→"+b2)...)
	}
	if n2 > n1 {
		return opVerified(action, c.SSHHost,
			fmt.Sprintf("链端已恢复且在出块：块高 %d → %d（%v 内 +%d）", n1, n2, growth, n2-n1),
			append(ev, "block="+b1+"→"+b2)...)
	}
	return opOK(action, c.SSHHost,
		fmt.Sprintf("端口与进程正常，但块高 %d 在 %v 内未增长（链可能卡死或未出块）", n1, growth),
		append(ev, "block="+b1+"→"+b2)...)
}

// chainBlockNumber 经 WeBASE 读当前块高（能读到即证明链通）。
func chainBlockNumber() (string, error) {
	c := chainCfg()
	v, err := webaseText("/" + strconv.Itoa(c.GroupID) + "/web3/blockNumber")
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
// 复验通过才算 ok；未复验/复验失败一律 fail，避免审计里出现"恢复成功"的假象。
func auditVerify(actor, action, target string, r OpResult) {
	res := resFail
	if r.Ok && r.Verified {
		res = resOK
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
	aiToolRegistry["verify_environment"] = aiTool{
		Desc: "复验环境是否真的恢复了：web/backend=服务端口与 HTTP 握手，db=最近一份备份的 sha256 与内容完整性，chain=节点端口+进程数+块高递增。执行过启动/备份/恢复之后用它确认结果，不要凭调用成功就下结论",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"target":    map[string]any{"type": "string", "description": "web / backend / db / chain", "enum": []string{"web", "backend", "db", "chain"}},
			"wait_secs": map[string]any{"type": "integer", "description": "服务复验最长等待秒数（1-180，默认前端45s/后端25s）"},
		}, "required": []string{"target"}},
		Execute: func(args map[string]any) (string, error) {
			target, _ := args["target"].(string)
			switch target {
			case "web", "backend":
				wait := time.Duration(aiArgInt(args, "wait_secs", 0, 0, 180)) * time.Second
				r := verifyService(target, wait)
				auditVerify(actAI, "verify_service", target, r)
				return aiVerifyText(r), nil
			case "db":
				r := verifyBackup("")
				auditVerify(actAI, "verify_backup", cfg.Projects.DBName, r)
				return aiVerifyText(r), nil
			case "chain":
				r := verifyChain(verifyChainGrowth)
				auditVerify(actAI, "verify_chain", cfg.Chain.SSHHost, r)
				return aiVerifyText(r), nil
			}
			return "", fmt.Errorf("target 必须是 web / backend / db / chain")
		},
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
