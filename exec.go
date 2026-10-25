// exec.go —— 子进程执行：查找可执行文件、环境变量、逐行输出 writer、前台/后台执行与进程树终止
package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"envkit/internal/platform"
)

// ---------- 命令执行 ----------
func findExe(names ...string) string {
	for _, c := range cfg.Components {
		d := componentBinDir(c)
		if _, err := os.Stat(d); err == nil {
			for _, n := range names {
				p := filepath.Join(d, n)
				if _, err := os.Stat(p); err == nil {
					return p
				}
			}
		}
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

// ---------- npm 解析（v3.3） ----------
//
// 为什么不能直接用 findExe("npm.cmd")：
//
//	Windows 上的 npm.cmd 只是一层批处理壳，真身在
//	<npm 目录>\node_modules\npm\bin\npm-cli.js。壳本身不会检查真身在不在，
//	所以"npm.cmd 存在"完全不代表 npm 能用。残留的 Node 安装目录
//	（node.exe 还在、node_modules 被清掉）会让这个壳一直存在。
//
//	findExe 取的是 PATH 顺序里的**第一个**，一旦那个是坏的，就会拿到一个
//	每次必失败的候选——而失败发生在 node 里，表现为
//	`Error: Cannot find module '...npm-cli.js'` + exit 1，
//	与"杀毒软件零输出秒退"长得完全一样，于是被误判、被重试、被包装，
//	最后只留下"疑似杀毒软件拦截"这句错得离谱的结论。
//	（v1.0 起就反复遇到，v3.3 才定位到根因。）
//
// 因此 npm 走自己的解析：逐个候选校验真身，跳过坏的；并且优先用
// `node.exe + npm-cli.js` 直接执行——绕过批处理壳，少一层 cmd，
// 报错与退出码都更干净（壳在某些环境下还会丢 stderr）。

// npmCandidate 一个 npm 候选：命令行前缀 + 人类可读的来源说明。
type npmCandidate struct {
	Exe   string   // 要执行的文件（node.exe 或 npm.cmd）
	Args  []string // 固定前缀参数（node 时是 npm-cli.js 的路径）
	Why   string   // 来源说明，进日志
	Shell bool     // true = 批处理壳（cmd 解析），false = 直接执行 exe
}

// npmInvocation 拼出一次完整调用（把用户参数接在前缀之后）。
func npmInvocation(c npmCandidate, args ...string) (string, []string) {
	return c.Exe, append(append([]string{}, c.Args...), args...)
}

// npmSearchDirs 按优先级列出所有"可能放着 npm"的目录。
//
// 顺序即信任度：组件安装目录（EnvKit 自己装的，最可控）→ PATH 上的 npm →
// PATH 上 node.exe 所在目录的兄弟 node_modules → npm 的全局前缀
// （%APPDATA%\npm，以及万一存在的 node 安装目录下的 node_global）。
func npmSearchDirs() []string {
	var dirs []string
	seen := map[string]bool{}
	add := func(d string) {
		if d == "" {
			return
		}
		k := strings.ToLower(filepath.Clean(d))
		if seen[k] {
			return
		}
		seen[k] = true
		dirs = append(dirs, d)
	}
	for _, c := range cfg.Components {
		add(componentBinDir(c))
	}
	// PATH 上的 npm：LookPath 只给第一个，这里自己把所有命中都收进来
	for _, n := range []string{"npm.cmd", "npm"} {
		for _, d := range lookPathAll(n) {
			add(filepath.Dir(d))
		}
	}
	// 相邻的 node 安装目录：npm 与 node 通常同级
	for _, d := range lookPathAll("node.exe") {
		add(filepath.Dir(d))
	}
	if app := os.Getenv("APPDATA"); app != "" {
		add(filepath.Join(app, "npm"))
	}
	return dirs
}

// lookPathAll 返回 PATH 中所有命中 name 的绝对路径（去重，保持顺序）。
//
// exec.LookPath 只返回第一个；而"第一个恰好是坏的"正是本 bug 的触发条件，
// 所以必须自己遍历。Windows 下同时尝试 PATHEXT 里的扩展名。
func lookPathAll(name string) []string {
	var out []string
	seen := map[string]bool{}
	exts := []string{""}
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		exts = []string{".cmd", ".exe", ".bat", ""}
	}
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if d == "" {
			continue
		}
		for _, e := range exts {
			p := filepath.Join(d, name+e)
			k := strings.ToLower(p)
			if seen[k] {
				continue
			}
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				seen[k] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// npmCandidateFrom 在 dir 里找出可用的 npm 调用方式；找不到返回 ok=false。
//
// 关键校验：`<dir>\node_modules\npm\bin\npm-cli.js` 必须存在。
// 这是 npm 批处理壳真正会执行的文件——它不在，壳一定失败。
func npmCandidateFrom(dir string) (npmCandidate, bool) {
	cli := filepath.Join(dir, "node_modules", "npm", "bin", "npm-cli.js")
	if fi, err := os.Stat(cli); err != nil || fi.IsDir() {
		return npmCandidate{}, false
	}
	// 优先 node.exe 直跑，绕开 cmd 层；找不到 node 才退回批处理壳。
	for _, n := range []string{"node.exe", "node"} {
		p := filepath.Join(dir, n)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return npmCandidate{Exe: p, Args: []string{cli}, Why: "node+npm-cli.js", Shell: false}, true
		}
	}
	for _, n := range []string{"npm.cmd", "npm"} {
		p := filepath.Join(dir, n)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return npmCandidate{Exe: p, Why: "npm.cmd 壳", Shell: true}, true
		}
	}
	// npm-cli.js 在，但既没 node 也没 npm 壳：仍可执行（本程序的 node 在别处时
	// 由 PATH 兜底），但没有 node 时绝对不能猜——猜错就是又一次"零输出秒退"。
	if p := findExe("node.exe", "node"); p != "" {
		return npmCandidate{Exe: p, Args: []string{cli}, Why: "PATH node + npm-cli.js", Shell: false}, true
	}
	return npmCandidate{}, false
}

// resolveNpm 挑出第一个**真能跑**的 npm。
//
// report 为 false 时不写日志（版本探测等只读路径用，避免刷屏）。
// 返回的 broken 列表用于告诉用户"你 PATH 上有几个 npm 是坏的"——
// 这类环境问题不该只体现在一句 exit status 1 里。
func resolveNpm(report bool) (npmCandidate, []string) {
	var broken []string
	for _, d := range npmSearchDirs() {
		if c, ok := npmCandidateFrom(d); ok {
			if report {
				info(scConfig, "前端", "使用 npm：%s（%s，来源 %s）", c.Exe, c.Why, d)
				for _, b := range broken {
					warn(scConfig, "前端", "已跳过一个坏掉的 npm：%s（缺少 node_modules\\npm\\bin\\npm-cli.js）", b)
				}
			}
			return c, broken
		}
		// 只有当目录里确实有 npm 的痕迹时才算"坏掉"，否则只是没装 npm。
		if hasNpmShim(d) {
			broken = append(broken, d)
		}
	}
	if report {
		for _, b := range broken {
			warn(scConfig, "前端", "坏掉的 npm：%s（缺少 node_modules\\npm\\bin\\npm-cli.js）", b)
		}
	}
	return npmCandidate{}, broken
}

// hasNpmShim 目录里是否有 npm 的可执行入口（有 ⇒ 用户以为它能用）。
func hasNpmShim(dir string) bool {
	for _, n := range []string{"npm.cmd", "npm", "npx.cmd"} {
		if fi, err := os.Stat(filepath.Join(dir, n)); err == nil && !fi.IsDir() {
			return true
		}
	}
	return false
}

// npmBrokenHint 把"npm 坏了"翻译成用户能照做的一句话。
func npmBrokenHint(broken []string) string {
	if len(broken) == 0 {
		return ""
	}
	return "检测到损坏的 npm 安装（" + strings.Join(broken, "、") +
		"）：这些目录里 npm 的启动脚本还在，但 node_modules\\npm 已经被删掉了，" +
		"所以每次调用都必定失败。请在「环境安装」里重新安装 Node.js，" +
		"或把上面这些目录从 PATH 中移除后重试"
}

func toolEnv(extra map[string]string) []string {
	var bins []string
	for _, c := range cfg.Components {
		d := componentBinDir(c)
		if _, err := os.Stat(d); err == nil {
			bins = append(bins, d)
		}
	}
	env := os.Environ()
	if len(bins) > 0 {
		newPath := strings.Join(bins, ";") + ";" + os.Getenv("PATH")
		found := false
		for i, e := range env {
			if len(e) >= 5 && strings.EqualFold(e[:5], "PATH=") {
				env[i] = "PATH=" + newPath
				found = true
				break
			}
		}
		if !found {
			env = append(env, "PATH="+newPath)
		}
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func goEnvExtra() map[string]string {
	// 只设 GOPROXY；不要关 GOSUMDB，否则下载/校验工具链模块会报
	// "checksum database disabled by GOSUMDB=off"。goproxy.cn 会代理 sumdb 校验。
	m := map[string]string{"GOPROXY": cfg.GoProxy}
	if cfg.GoSumDB != "" {
		m["GOSUMDB"] = cfg.GoSumDB
	}
	// cgo：未显式配置时，若无 C 编译器(gcc)则退回纯 Go 构建，避免
	// "runtime/cgo: cgo.exe: exit status 1" 这类报错。有 gcc 则保持 cgo。
	if cfg.GoCGO != "" {
		m["CGO_ENABLED"] = cfg.GoCGO
	} else if findExe("gcc.exe", "gcc") == "" {
		m["CGO_ENABLED"] = "0"
	}
	return m
}
func npmEnvExtra() map[string]string {
	return map[string]string{"npm_config_registry": cfg.NpmRegistry}
}

var urlRe = regexp.MustCompile(`https?://(?:localhost|127\.0\.0\.1|0\.0\.0\.0)(?::\d+)?`)
var portRe = regexp.MustCompile(`(?i)(?:serving http on|listening on|running on)\s*:\s*(\d{2,5})`)

// 去掉 ANSI 转义序列（颜色、光标控制 [2K/[1A、OSC 标题等）
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]`)

func stripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	return ansiRe.ReplaceAllString(s, "")
}

// 逐行 writer：子进程输出直连它（避免管道竞态丢输出），并做 ANSI 清理 / 回车重绘 / 相邻重复折叠
type lineWriter struct {
	scope  string
	tag    string
	svcKey string
	mu     sync.Mutex
	buf    []byte
	last   string
	n      int      // 已产出行数（用于判断子进程是否"零输出秒退"）
	tail   []string // 最近若干行（给失败摘要用：真实报错必须能原样引用）
}

// 保留多少行用于失败摘要。npm/node 的报错通常是一段栈，前几行是
// "node:internal/..."，最有价值的那句 `Error: Cannot find module '...'`
// 往往在第 4~8 行，所以只留末尾 3 行会漏掉它。
const lineTailKeep = 16

// hasOutput 是否捕获到任何输出行。
func (w *lineWriter) hasOutput() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n > 0 || len(w.buf) > 0
}

// errSummary 从已捕获的输出里挑出最能说明问题的一行。
//
// 为什么不直接把最后一行丢给用户：node 崩溃时最后一行通常是
// `Node.js v24.21.0` 或一个空行，真正的原因在中间。这里优先找
// `Error:` / `npm ERR!` 这类签名行，找不到才退回最后一行。
func (w *lineWriter) errSummary() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.tail) == 0 {
		return ""
	}
	for _, l := range w.tail {
		for _, sig := range []string{"Error:", "npm ERR!", "error ", "错误", "No such file"} {
			if strings.Contains(l, sig) {
				return clip280(l)
			}
		}
	}
	return clip280(w.tail[len(w.tail)-1])
}

// clip280 截到 280 字符（日志行不该被一段栈占满，原因放前面）。
func clip280(s string) string {
	const max = 280
	if len(s) <= max {
		return s
	}
	// 按 rune 截，避免切断 UTF-8
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, b := range p {
		switch b {
		case '\n':
			w.emitLine()
		case '\r':
			w.buf = w.buf[:0] // 终端语义：回车回到行首，丢弃当前未完成行
		default:
			w.buf = append(w.buf, b)
		}
	}
	return len(p), nil
}

func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.emitLine()
}

func (w *lineWriter) emitLine() {
	line := strings.TrimRight(stripANSI(string(w.buf)), " \t")
	w.buf = w.buf[:0]
	if strings.TrimSpace(line) == "" || line == w.last {
		return
	}
	w.n++
	w.last = line
	w.tail = append(w.tail, line)
	if len(w.tail) > lineTailKeep {
		w.tail = w.tail[len(w.tail)-lineTailKeep:]
	}
	logfSrc(w.scope, w.svcKey, "INFO", w.tag, "%s", line)
	if w.svcKey == "" {
		return
	}
	u := urlRe.FindString(line)
	if u != "" {
		u = strings.Replace(u, "0.0.0.0", "localhost", 1)
	} else if mm := portRe.FindStringSubmatch(line); mm != nil {
		u = "http://localhost:" + mm[1]
	}
	if u == "" {
		return
	}
	svcMu.Lock()
	if s := svcState[w.svcKey]; s != nil && s.URL == "" {
		s.URL = u
	}
	svcMu.Unlock()
}

func execStreamed(scope, src, step, dir string, extra map[string]string, name string, args ...string) error {
	_, err := execStreamedDetail(scope, src, step, dir, extra, name, args...)
	return err
}

// execStreamedDetail 与 execStreamed 同一条路径，额外回报输出摘要，
// 让调用方能把**真实报错**写进失败结论（而不是只有一句 exit status 1）。
func execStreamedDetail(scope, src, step, dir string, extra map[string]string, name string, args ...string) (streamResult, error) {
	// v3.0 T1.4：前台命令注册到"可按 key 杀"的注册表，任务看门狗超时时
	// 能真正终止它——旧实现只放任务锁不杀子进程，go build 挂死后
	// 锁放了、编译还在跑，之后任何人都拿不到 backend 组锁。
	fgKey := scope + "/" + src
	// 策略：零输出秒退 → 2s 后重试一次 → 仍零输出则改用 cmd /c 包装（见 retry.go）
	var last streamResult
	_, err := runRetry(retryPolicy{
		Attempts: 3,
		Backoff:  2 * time.Second,
		WrapLast: true,
		Scope:    scope,
		Tag:      step,
		// 被看门狗杀掉之后绝不再重试：任务锁已释放，再跑一次等于无锁执行。
		Abort: func() bool { return fgAborted(fgKey) },
	}, func(wrap bool) (bool, error) {
		ctx, cancel := cmdContext(15 * time.Minute)
		defer cancel()
		cmd := buildCmd(ctx, wrap, name, args...)
		cmd.Dir = dir
		cmd.Env = toolEnv(extra)
		w := &lineWriter{scope: scope, tag: step, svcKey: src}
		cmd.Stdout = w
		cmd.Stderr = w
		fgRegister(fgKey, cmd, cancel)
		defer fgUnregister(fgKey, cmd)
		err := cmd.Run()
		w.flush()
		// 留最后一次尝试的摘要：重试把前几次覆盖掉是对的（最后一次才是真相）
		last = streamResult{Summary: w.errSummary(), Lines: w.n}
		return w.hasOutput(), err
	})
	return last, err
}

// streamResult 一次前台执行的可引用结果。
type streamResult struct {
	Summary string // 最能说明问题的一行输出（空 = 确实没输出）
	Lines   int    // 捕获到的行数
}

// explainExit 把"命令失败"翻译成人话：有真实报错就引用它，
// 只有确实零输出时才提环境拦截——措辞不含"一定"，因为无输出也可能
// 是进程被策略直接终止，本程序看不到更多证据。
func explainExit(r streamResult, err error) string {
	if r.Summary != "" {
		return r.Summary
	}
	if err == nil {
		return ""
	}
	return err.Error() + "（子进程没有任何输出；若反复如此，可能是安全软件拦截了该程序）"
}

func execBackground(scope, key, progKey, step, dir string, extra map[string]string, name string, args ...string) error {
	const (
		maxRestarts    = 2               // 秒退后最多自动重启 2 次（第 2 次改用 cmd /c 包装）
		fastExitWindow = 5 * time.Second // 存活时间超过它就不算"秒退"
	)
	var cmd *exec.Cmd
	var w *lineWriter
	var started time.Time
	spawn := func(wrap bool) error {
		// 长驻服务不绑 context：不能被超时杀掉，由 Job Object 在 EnvKit 退出时连带终止
		cmd = buildCmdDetached(wrap, name, args...)
		cmd.Dir = dir
		cmd.Env = toolEnv(extra)
		w = &lineWriter{scope: scope, tag: step, svcKey: key}
		cmd.Stdout = w
		cmd.Stderr = w
		started = time.Now()
		return cmd.Start()
	}
	if err := spawn(false); err != nil {
		return err
	}
	// 绑定到 Job Object：EnvKit 任何方式退出都会连带终止整个子进程树，防止孤儿占端口
	// v3.3：不再静默——宿主把它塞进父 Job 时这里会失败，用户有权知道
	// "关掉 EnvKit 后服务可能还活着"。
	reportJobBind(scope, key, step, cmd.Process.Pid)
	procMu.Lock()
	activeProcs[key] = cmd
	delete(stoppingProcs, key) // 新进程与上一次停止无关，别让残留标记吞掉真实崩溃上报
	procMu.Unlock()
	// v3.0 T1.3：原地更新而不是整体替换 SvcInfo。旧写法
	// `svcState[key] = &SvcInfo{...}` 会把 URL（端口线索，复验候选端口
	// 的第一来源）连同 Phase 一起抹掉；且别的 goroutine 可能正持有
	// 指向旧结构的指针，替换等于让它们读到快照之外的空气。
	svcMu.Lock()
	s := svcState[key]
	if s == nil {
		s = &SvcInfo{}
		svcState[key] = s
	}
	s.Running = true
	s.PID = cmd.Process.Pid
	s.Since = time.Now().Format("15:04:05")
	svcMu.Unlock()
	go func() {
		for attempt := 0; ; attempt++ {
			err := cmd.Wait()
			w.flush()
			// 用户主动停止：不自动重启、不报"已退出"，保持"已停止"语义（竞态：Wait 返回晚于 stopByKey 的 setProg）
			procMu.Lock()
			manualStop := stoppingProcs[key]
			delete(stoppingProcs, key)
			procMu.Unlock()
			if manualStop {
				// v3.0 T1.3：带身份守卫的清理。旧实现无条件
				// delete(activeProcs, key) + Running=false——这期间
				// 若已起了新进程（restart 的停+起就在一个组锁内完成），
				// 旧监控 goroutine 会把新进程的条目删掉、状态写成停止，
				// 之后 stopByKey 找不到进程——"停不掉"。
				procDropIfCurrent(key, cmd)
				svcMarkStoppedIfCurrent(key, cmd)
				setProg(progKey, false, false, "已停止")
				infoS(scope, key, step, "进程已停止")
				break
			}
			// 静默秒退：零输出 + 秒退，才走自动重启。可能是安全软件拦截，
			// 也可能是可执行文件本身坏了——所以这里不断言原因，只记录事实。
			if attempt < maxRestarts && fastExit(err, w.hasOutput(), started, fastExitWindow) {
				// v3.0 T1.9：秒退重试也是无人值守的自动重启，过同一道断路器。
				// 漏了这条，"拒绝一切自动重启"就只掐了 AI 和守护——
				// 秒退重试这条路还开着，熔断就是个半聋的装置。
				// 走完整 gate 而非 svcBreakerTripped：冷静期过后这次重试
				// 就是 half-open 探针，结果由复验结论分流（再崩即重新熔断）。
				if berr := svcBreakerGate(key, "exec_fast_exit_retry"); berr != nil {
					warnS(scope, key, step, "秒退自动重启被断路器拒绝：%v", berr)
					procDropIfCurrent(key, cmd)
					svcMarkStoppedIfCurrent(key, cmd)
					setProg(progKey, false, false, "已退出（断路器熔断，未自动重启）")
					break
				}
				wrap := attempt == maxRestarts-1
				if wrap {
					warnS(scope, key, step, "子进程仍未产生输出就退出，1 秒后改用 cmd /c 包装重启...")
					time.Sleep(1 * time.Second)
				} else {
					warnS(scope, key, step, "子进程未产生任何输出就退出（退出码 %s），2 秒后自动重启...",
						exitCodeText(err))
					time.Sleep(2 * time.Second)
				}
				prev := cmd // spawn 失败时 cmd 已被换成没起来的那个：清理要按旧进程来
				if serr := spawn(wrap); serr != nil {
					failS(scope, key, step, "自动重启失败：%v", serr)
					// 不清理就会留下死进程条目 + Running=true 的假状态
					procDropIfCurrent(key, prev)
					svcMarkStoppedIfCurrent(key, prev)
					break
				}
				platform.AssignToJob(cmd.Process.Pid)
				procMu.Lock()
				activeProcs[key] = cmd
				delete(stoppingProcs, key)
				procMu.Unlock()
				svcMu.Lock()
				if s := svcState[key]; s != nil {
					s.PID = cmd.Process.Pid
				}
				svcMu.Unlock()
				infoS(scope, key, step, "已自动重启（PID %d）", cmd.Process.Pid)
				continue
			}
			procDropIfCurrent(key, cmd)
			svcMarkStoppedIfCurrent(key, cmd)
			if err != nil {
				// v3.3：把孩子们最后说的话带上。旧实现只说 exit status 1，
				// 而真正的原因（`Cannot find module … ` / `端口被占用`）就在日志
				// 正文里某一行——用户不该自己去 2000 行日志里找。
				if sum := w.errSummary(); sum != "" {
					setProg(progKey, false, false, "已退出（"+sum+"）")
					warnS(scope, key, step, "进程已退出：%v —— %s", err, sum)
				} else {
					setProg(progKey, false, false, "已退出（"+err.Error()+"）")
					warnS(scope, key, step, "进程已退出：%v（无任何输出）", err)
				}
			} else {
				setProg(progKey, false, false, "已退出")
				infoS(scope, key, step, "进程已退出")
			}
			break
		}
	}()
	return nil
}

// reportJobBind 把 Job Object 绑定结果落进日志（每个 key 只报一次"受限"，
// 免得秒退/重启循环把同一句话刷满）。
//
// 为什么要报：绑定失败意味着"EnvKit 退出时连带杀掉服务"这条承诺不成立。
// 静默失败等于让用户以为兜底还在，关掉 EnvKit 之后发现 node 还占着端口。
var jobWarnedMu sync.Mutex
var jobWarned = map[string]bool{}

func reportJobBind(scope, key, step string, pid int) {
	err := platform.AssignToJob(pid)
	if err == nil {
		return
	}
	jobWarnedMu.Lock()
	already := jobWarned[key]
	jobWarned[key] = true
	jobWarnedMu.Unlock()
	if already {
		return
	}
	if platform.IsAlreadyInJob(err) {
		warnS(scope, key, step,
			"本进程已在某个父 Job 内（常见于从 IDE/终端启动），无法再嵌套绑定：关掉 EnvKit 时该服务可能不会被连带终止")
		return
	}
	warnS(scope, key, step, "子进程未纳入 Job 管理：%v（关掉 EnvKit 时可能留孤儿进程）", err)
}

// ---------- 进程条目的身份守卫（v3.0 T1.3，修 G3 的另一半） ----------
// 背景：监控 goroutine 在进程退出时无条件 delete(activeProcs, key) 并写
// Running=false。但 Wait 返回是异步的——这期间完全可能已经起了新进程
// （restart = 停+起，两段同组锁；自动重启也在循环里换进程）。
// 旧监控 goroutine 醒来时按 key 删掉的其实是**新进程**的条目：
// 之后 stopByKey 找不到进程，"停不掉"。
//
// 修法：删除/改写前先确认条目还是自己那一个。cmd 指针身份 + PID 双保险。

// procDropIfCurrent 仅当 activeProcs[key] 仍是本 cmd 条目时才删除。
func procDropIfCurrent(key string, cmd *exec.Cmd) {
	procMu.Lock()
	if cur, ok := activeProcs[key]; ok && cur == cmd {
		delete(activeProcs, key)
	}
	procMu.Unlock()
}

// svcMarkStoppedIfCurrent 仅当 svcState 记的还是本进程（PID 相符）时才写"已停止"。
// 旧进程善后不能把新进程的状态写成停止。
func svcMarkStoppedIfCurrent(key string, cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	svcMu.Lock()
	if s := svcState[key]; s != nil && s.PID == cmd.Process.Pid {
		s.Running = false
		s.PID = 0
	}
	svcMu.Unlock()
}

// ---------- 前台命令注册表（v3.0 T1.4，修 G4） ----------
//
// 任务看门狗超时时要能真正终止正在跑的前台命令（go build / 链启动脚本），
// 而不是只把任务锁放掉。execStreamed 每次 attempt 注册当前命令；
// 看门狗经 fgKill 杀它并打上 aborted 标记——重试循环看到标记立即收手，
// 否则"杀了还重试"等于无锁继续执行（任务锁已经释放了）。

type fgRec struct {
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	aborted bool
}

var (
	fgMu  sync.Mutex
	fgReg = map[string]*fgRec{}
)

func fgRegister(key string, cmd *exec.Cmd, cancel context.CancelFunc) {
	fgMu.Lock()
	fgReg[key] = &fgRec{cmd: cmd, cancel: cancel}
	fgMu.Unlock()
}

func fgUnregister(key string, cmd *exec.Cmd) {
	fgMu.Lock()
	if r := fgReg[key]; r != nil && r.cmd == cmd {
		delete(fgReg, key)
	}
	fgMu.Unlock()
}

// fgKill 终止 key 对应的前台命令；没有注册中的命令返回 false。
// 先打 aborted 标记再杀：杀与重试检查之间存在竞态，标记保证
// "杀了之后不会有新 attempt 起来"。
func fgKill(key string) bool {
	fgMu.Lock()
	r := fgReg[key]
	if r == nil {
		fgMu.Unlock()
		return false
	}
	r.aborted = true
	cmd, cancel := r.cmd, r.cancel
	fgMu.Unlock()
	if cancel != nil {
		cancel() // context 一取消，cmd.Run() 立即返回
	}
	killTree(cmd)
	return true
}

func fgAborted(key string) bool {
	fgMu.Lock()
	defer fgMu.Unlock()
	r := fgReg[key]
	return r != nil && r.aborted
}

// 杀整棵进程树：go run 会派生 main.exe、npm 会派生 node，只杀直接子进程会留孤儿占端口
func killTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
		return
	}
	_ = cmd.Process.Kill()
}

// shutdownChildren 停掉本实例启动的前端/后端子进程（与「全部停止」按钮同一套机制，
// 保证 stoppingProcs 标记与监控 goroutine 状态一致，不留孤儿进程）。
// 调用方：main 的控制台关闭钩子（internal/platform.InstallConsoleHook 的 onClose）
// 与网页「退出程序」的"连带停服务"分支。
func shutdownChildren() {
	stopByKey(scStart, "web", "web-start")
	stopByKey(scStart, "backend", "backend-start")
}

func stopByKey(scope, key, progKey string) {
	procMu.Lock()
	cmd, ok2 := activeProcs[key]
	if ok2 {
		// 先立标记再杀：cmd.Wait() 返回晚于下面的 setProg("已停止")，
		// 监控 goroutine 看到标记就保持"已停止"，不会覆盖成"已退出（exit status 1）"
		stoppingProcs[key] = true
	}
	procMu.Unlock()
	if !ok2 {
		return
	}
	killTree(cmd)
	procMu.Lock()
	delete(activeProcs, key)
	procMu.Unlock()
	svcMu.Lock()
	if s := svcState[key]; s != nil {
		s.Running = false
		s.PID = 0
	}
	svcMu.Unlock()
	setProg(progKey, false, false, "已停止")
	infoS(scope, key, "停止", "已发送停止信号")
}
