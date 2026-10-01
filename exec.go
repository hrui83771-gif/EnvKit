// exec.go —— 子进程执行：查找可执行文件、环境变量、逐行输出 writer、前台/后台执行与进程树终止
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
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
	n      int // 已产出行数（用于判断子进程是否"零输出秒退"）
}

// hasOutput 是否捕获到任何输出行。
func (w *lineWriter) hasOutput() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n > 0 || len(w.buf) > 0
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
	// 策略：零输出秒退 → 2s 后重试一次 → 仍零输出则改用 cmd /c 包装（见 retry.go）
	_, err := runRetry(retryPolicy{
		Attempts: 3,
		Backoff:  2 * time.Second,
		WrapLast: true,
		Scope:    scope,
		Tag:      step,
	}, func(wrap bool) (bool, error) {
		ctx, cancel := cmdContext(15 * time.Minute)
		defer cancel()
		cmd := buildCmd(ctx, wrap, name, args...)
		cmd.Dir = dir
		cmd.Env = toolEnv(extra)
		w := &lineWriter{scope: scope, tag: step, svcKey: src}
		cmd.Stdout = w
		cmd.Stderr = w
		err := cmd.Run()
		w.flush()
		return w.hasOutput(), err
	})
	return err
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
	assignToJob(cmd.Process.Pid)
	procMu.Lock()
	activeProcs[key] = cmd
	delete(stoppingProcs, key) // 新进程与上一次停止无关，别让残留标记吞掉真实崩溃上报
	procMu.Unlock()
	svcMu.Lock()
	svcState[key] = &SvcInfo{Running: true, PID: cmd.Process.Pid, Since: time.Now().Format("15:04:05")}
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
				infoS(scope, key, step, "进程已停止")
				break
			}
			// 静默秒退：疑似被杀毒软件拦截（真实错误一定有输出），自动重启
			if attempt < maxRestarts && fastExit(err, w.hasOutput(), started, fastExitWindow) {
				wrap := attempt == maxRestarts-1
				if wrap {
					warnS(scope, key, step, "子进程仍无输出秒退，1 秒后改用 cmd /c 包装重启...")
					time.Sleep(1 * time.Second)
				} else {
					warnS(scope, key, step, "子进程无任何输出即秒退（疑似杀毒软件拦截），2 秒后自动重启...")
					time.Sleep(2 * time.Second)
				}
				if serr := spawn(wrap); serr != nil {
					failS(scope, key, step, "自动重启失败：%v", serr)
					break
				}
				assignToJob(cmd.Process.Pid)
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
			procMu.Lock()
			delete(activeProcs, key)
			procMu.Unlock()
			svcMu.Lock()
			if s := svcState[key]; s != nil {
				s.Running = false
				s.PID = 0
			}
			svcMu.Unlock()
			if err != nil {
				setProg(progKey, false, false, "已退出（"+err.Error()+"）")
				warnS(scope, key, step, "进程已退出：%v", err)
			} else {
				setProg(progKey, false, false, "已退出")
				infoS(scope, key, step, "进程已退出")
			}
			break
		}
	}()
	return nil
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
