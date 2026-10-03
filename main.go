package main

import (
	_ "embed"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

// 内嵌的是「干净模板」而非 config.json —— config.json 是本机实配（含服务器 IP、
// AI Key 等），一旦被 embed 就会随 exe 分发给他人。运行时优先读 exe 同目录的
// config.json，读不到才回落到这份模板。改默认值请改 config.dist.json 后重新构建。
//
//go:embed config.dist.json
var embeddedConfig []byte

// 版本信息（build.bat 用 -ldflags 注入）
var (
	appVersion = "2.0.0"
	buildDate  = "2026-10-03"
)

//go:embed web/index.html
var indexHTML string

// xterm.js 内嵌终端静态资源
var (
	//go:embed web/xterm/xterm.js
	xtermJS []byte
	//go:embed web/xterm/addon-fit.js
	xtermFitJS []byte
	//go:embed web/xterm/xterm.css
	xtermCSS []byte
)

func main() {
	if !acquireSingleInstance() {
		// 已有实例在跑：找到它并直接打开控制台页面（双击场景必须给可见反馈）
		for p := 18765; p < 18815; p++ {
			u := fmt.Sprintf("http://127.0.0.1:%d/", p)
			resp, err := http.Get(u)
			if err != nil {
				continue
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if strings.Contains(string(body), "EnvKit") {
				info(scSys, "服务", "EnvKit 已在运行：%s，已为你打开控制台页面", u)
				go openBrowser(u)
				notify("EnvKit 已在运行", "已为你打开正在运行的控制台页面")
				os.Exit(0)
			}
		}
		notify("EnvKit 已在运行", "请使用已打开的 EnvKit 窗口；若找不到，请在任务管理器结束 EnvKit.exe 后重试")
		fmt.Println("EnvKit 已在运行（单实例保护）。")
		os.Exit(1)
	}
	// 令牌带签发时间：超过 apiTokenTTL 后自动轮换（见 state.go 的 currentToken）
	apiToken = newToken()
	tokenIssued = time.Now()
	loadConfig()
	os.MkdirAll(cfg.InstallDir, 0755)
	isAdminFlag = isAdmin()
	if !isAdminFlag {
		for _, c := range cfg.Components {
			if c.Special == "mysql" {
				warn(scSys, "权限", "未以管理员身份运行，MySQL 服务注册可能失败")
			}
		}
	}

	go runSelfUpdateCheck() // 自版本检查（未配置 update_url 时静默跳过）

	// 控制台关闭钩子：点窗口 X 先弹确认，退出前停掉启动的子进程（console_windows.go）
	installConsoleHook()

	http.HandleFunc("/", serveIndex)
	http.HandleFunc("/api/config", handleConfig)
	http.HandleFunc("/api/check", handleCheck)
	http.HandleFunc("/api/install", handleInstall)
	http.HandleFunc("/api/state", handleState)
	http.HandleFunc("/api/events", handleEvents)
	http.HandleFunc("/api/pick-dir", handlePickDir)
	http.HandleFunc("/api/pick-file", handlePickFile)
	http.HandleFunc("/api/ls", handleLS)
	http.HandleFunc("/api/health", handleHealth)
	http.HandleFunc("/api/elevate", handleElevate)
	http.HandleFunc("/api/chain/check", handleChainCheck)
	http.HandleFunc("/api/chain/ssh-test", handleChainSSHTest)
	http.HandleFunc("/api/chain/exec", handleChainExec)
	http.HandleFunc("/api/chain/terminal", handleChainTerminal)
	http.HandleFunc("/api/chain/term", handleChainTermWS)
	http.HandleFunc("/api/chain/ls", handleChainLS)
	http.HandleFunc("/api/chain/start", handleChainStart)
	http.HandleFunc("/xterm/xterm.js", staticBytes("application/javascript; charset=utf-8", xtermJS))
	http.HandleFunc("/xterm/addon-fit.js", staticBytes("application/javascript; charset=utf-8", xtermFitJS))
	http.HandleFunc("/xterm/xterm.css", staticBytes("text/css; charset=utf-8", xtermCSS))
	http.HandleFunc("/api/config/export", handleConfigExport)
	http.HandleFunc("/api/config/import", handleConfigImport)
	http.HandleFunc("/api/program/db-test", handleDBTest)
	http.HandleFunc("/api/program/init-db", handleInitDB)
	http.HandleFunc("/api/program/mysql-create", handleMySQLCreate)
	http.HandleFunc("/api/program/apply-sql", handleApplySQL)
	http.HandleFunc("/api/program/apply-upload", handleApplyUpload)
	http.HandleFunc("/api/db/list", handleDBList)
	http.HandleFunc("/api/db/tables", handleDBTables)
	http.HandleFunc("/api/db/rows", handleDBRows)
	http.HandleFunc("/api/db/query", handleDBQuery)
	http.HandleFunc("/api/program/web-install", handleWebInstall)
	http.HandleFunc("/api/program/backend-tidy", handleBackendTidy)
	http.HandleFunc("/api/program/web-start", handleWebStart)
	http.HandleFunc("/api/program/backend-start", handleBackendStart)
	http.HandleFunc("/api/program/stop", handleStop)
	http.HandleFunc("/api/program/exit", handleProgramExit)
	http.HandleFunc("/api/ports/scan", handlePortScan)
	http.HandleFunc("/api/ports/kill", handlePortKill)
	http.HandleFunc("/api/program/cleanup", handleCleanupProcs)
	http.HandleFunc("/api/program/state", handleProgState)
	http.HandleFunc("/api/program/db-backup", handleDBBackup)
	http.HandleFunc("/api/program/db-restore", handleDBRestore)
	http.HandleFunc("/api/program/db-drill", handleDBDrill)
	http.HandleFunc("/api/av-exclude", handleAVExclude)
	http.HandleFunc("/api/av-clean", handleAVClean)
	http.HandleFunc("/api/ai/config", handleAIConfig)
	http.HandleFunc("/api/ai/test", handleAITest)
	http.HandleFunc("/api/ai/chat", handleAIChat)
	http.HandleFunc("/api/ai/explain", handleAIExplain)
	http.HandleFunc("/api/ai/providers", handleAIProviders)
	http.HandleFunc("/api/ai/models", handleAIModels)
	http.HandleFunc("/api/ai/balance", handleAIBalance)
	http.HandleFunc("/api/component-versions", handleComponentVersions)
	http.HandleFunc("/api/component-upgrade", handleComponentUpgrade)
	http.HandleFunc("/api/install-dryrun", handleInstallDryRun)
	http.HandleFunc("/api/report", handleReport)
	http.HandleFunc("/api/metrics", handleMetrics)
	http.HandleFunc("/api/diag", handleDiag)
	http.HandleFunc("/api/audit", handleAudit)
	http.HandleFunc("/api/lessons", handleLessons)
	http.HandleFunc("/api/memory", handleMemory)
	http.HandleFunc("/api/memory/import", handleMemoryImport)
	http.HandleFunc("/api/lesson/toggle", handleLessonToggle)
	http.HandleFunc("/api/dist/export", handleDistExport)
	http.HandleFunc("/api/self-update", handleSelfUpdate)

	port := findPort(18765)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	openURL := "http://" + addr + "/"
	go openBrowser(openURL)
	startGuardLoop()
	cleanupOldLogs()
	cleanupOldAudits()
	loadPersistedState()
	startStatePersister()
	// 启动后异步检查杀软白名单，未加则提示（点击顶部按钮一键加入）
	go func() {
		time.Sleep(3 * time.Second)
		if excluded, avail := defenderExcluded(); avail && !excluded && !avWhitelisted() {
			warn(scSys, "安全", "EnvKit 尚未加入 Defender 白名单，go/npm/mysqldump 子进程可能被误拦；点击顶部「杀软白名单」一键加入")
		}
	}()

	info(scSys, "服务", "控制台已启动：%s", openURL)
	// 黑窗口必须显眼地讲清"能不能关"：这是新用户最高频的困惑点
	fmt.Println()
	fmt.Println("======================================================")
	fmt.Println("  此窗口是 EnvKit 的服务进程，请保持开启！")
	fmt.Println("  · 关闭它会导致网页无法访问；右上角的 × 已禁用，误点不会出事")
	fmt.Println("  · 退出方法：按 Ctrl+C，会先自动停止前端/后端服务再退出")
	fmt.Println("  控制台页面：" + openURL + "（浏览器未自动打开时，复制此地址访问）")
	fmt.Println("======================================================")
	fmt.Println()
	// 本机 API 鉴权：/api/* 需携带随机令牌（页面注入），防浏览器内其它网页 CSRF
	startTaskWatchdog()
	if err := http.ListenAndServe(addr, securityMW(recoverMW(authMW(http.DefaultServeMux)))); err != nil {
		fail(scSys, "服务", "启动失败：%v", err)
		os.Exit(1)
	}
}

// securityMW 补安全响应头。
// 页面是单文件 + 全内联脚本，所以 CSP 必须放行 'unsafe-inline'；即便如此它仍然挡住了
// 外链脚本、object/embed、被嵌套进 iframe（点击劫持）这几类最常见的注入利用面。
func securityMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; connect-src 'self' ws://127.0.0.1:* ws://localhost:*; "+
				"object-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		next.ServeHTTP(w, r)
	})
}

// authMW 本机 API 鉴权：/api/* 需携带随机令牌（页面注入）。
// 除令牌外再加一道 Origin 校验——即使令牌因某种途径泄漏（浏览器历史里的 ?t= 、
// 被转发的链接），其它站点发起的跨站请求也会因为 Origin 不是本机控制台而被拒。
func authMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			tok := currentToken()
			if r.Header.Get("X-EnvKit-Token") != tok && r.URL.Query().Get("t") != tok {
				http.Error(w, "未授权的访问", http.StatusForbidden)
				return
			}
			if o := r.Header.Get("Origin"); o != "" && !sameOrigin(o) {
				warn(scSys, "安全", "已拒绝跨站请求：Origin=%s Path=%s", o, r.URL.Path)
				http.Error(w, "跨站请求已被拒绝", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin 判断 Origin 是否指向本机控制台（127.0.0.1 / localhost，任意端口）。
func sameOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "127.0.0.1" || host == "localhost" || host == "[::1]"
}

// recoverMW 捕获 handler 内的 panic：记录堆栈、返回 500，避免整个进程/连接被一次 panic 打断。
func recoverMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				fail(scSys, "服务", "请求 %s 发生 panic：%v\n%s", r.URL.Path, rec, debug.Stack())
				defer func() { _ = recover() }() // 已经写过响应头时忽略二次写
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("内部错误，已记录日志（" + r.URL.Path + "）"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html := strings.ReplaceAll(indexHTML, "__APP_VERSION__", appVersion+" · "+buildDate)
	html = strings.ReplaceAll(html, "__TOKEN__", currentToken())
	_, _ = w.Write([]byte(html))
}

// 静态嵌入资源（xterm 等）
func staticBytes(ct string, b []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", ct)
		_, _ = w.Write(b)
	}
}

func findPort(start int) int {
	for p := start; p < start+50; p++ {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			l.Close()
			return p
		}
	}
	return start
}

func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	case "darwin":
		cmd = exec.Command("open", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	_ = cmd.Start()
}
