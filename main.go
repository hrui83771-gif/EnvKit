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

	"envkit/internal/platform"
)

// 内嵌的是「干净模板」而非 config.json —— config.json 是本机实配（含服务器 IP、
// AI Key 等），一旦被 embed 就会随 exe 分发给他人。运行时优先读 exe 同目录的
// config.json，读不到才回落到这份模板。改默认值请改 config.dist.json 后重新构建。
//
//go:embed config.dist.json
var embeddedConfig []byte

// 版本信息（build.bat 用 -ldflags 注入）
var (
	appVersion = "3.8.0"
	buildDate  = "2026-11-01"
)

//go:embed web/index.html
var indexHTML string

// 托盘图标：直接嵌入仓库里的 app.ico，不依赖 exe 同目录是否还留着这个文件。
//
//go:embed app.ico
var appIcon []byte

// xterm.js 内嵌终端静态资源
var (
	//go:embed web/xterm/xterm.js
	xtermJS []byte
	//go:embed web/xterm/addon-fit.js
	xtermFitJS []byte
	//go:embed web/xterm/xterm.css
	xtermCSS []byte
)

// marked（Markdown 渲染）与 highlight.js（代码高亮）——与 xterm 同样的离线内嵌，
// 不走外网 CDN：本工具经常在没外网的客户现场跑，缺这两个文件只是少点样式，
// 但页面不能因此报警或白屏。
var (
	//go:embed web/vendor/marked.min.js
	markedJS []byte
	//go:embed web/vendor/highlight.min.js
	highlightJS []byte
	// 手机端调试台（Tencent/vConsole）：仅在地址带 ?debug 时由手机页动态加载，
	// 平时不请求，不给普通用户增加流量与干扰。走既有 embed + staticBytes 套路。
	//go:embed web/vendor/vconsole.min.js
	vconsoleJS []byte
)

// 首次引导 / 产品导览与 P1-4 操作教学：Swiper 12（翻页容器）、Anime.js v4（动画编排）、
// driver.js（控件高亮）。都是官方 UMD / IIFE 发布产物，原样内嵌、未经修改，
// 版本与许可全文见 THIRD-PARTY-NOTICE.md。
// 注意：embed 进的是 exe —— 只跑 web_build.py 不会更新二进制，必须 go build。
var (
	//go:embed web/vendor/swiper-bundle.min.js
	swiperJS []byte
	//go:embed web/vendor/swiper-bundle.min.css
	swiperCSS []byte
	//go:embed web/vendor/anime.umd.min.js
	animeJS []byte
	//go:embed web/vendor/driver.js.iife.js
	driverTourJS []byte
	//go:embed web/vendor/driver.css
	driverTourCSS []byte
)

// 让「真实发生的数据变化」看得见（v3.8.1）：auto-animate 给列表的真实增删重排
// 加 FLIP 过渡，jsdiff + diff2html 渲染配置导入前的逐行差异，hotkeys-js 管快捷键。
// 全部是同域 /vendor/ 路由，不经 CDN；版本 / 许可全文 / SHA256 见
// THIRD-PARTY-NOTICE.md。auto-animate 上游只发 ESM，这里是本项目包过 IIFE 的版本。
var (
	//go:embed web/vendor/auto-animate.iife.js
	autoAnimateJS []byte
	//go:embed web/vendor/diff2html.min.js
	diff2htmlJS []byte
	//go:embed web/vendor/diff2html.min.css
	diff2htmlCSS []byte
	//go:embed web/vendor/diff.min.js
	jsdiffJS []byte
	//go:embed web/vendor/hotkeys.min.js
	hotkeysJS []byte
)

func main() {
	if !platform.AcquireSingleInstance() {
		// 已有实例在跑：找到它并直接打开控制台页面（双击场景必须给可见反馈）。
		// 此刻还没跑 loadConfig()（cfg 为空），而 loadConfig 可能写盘（版本迁移），
		// 双开时不该两个进程一起写 config.json —— 用 serverPortHint 只读拿端口设置。
		hintStart, hintLimit := serverPortHint()
		for p := hintStart; p < hintLimit; p++ {
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
				platform.Notify("EnvKit 已在运行", "已为你打开正在运行的控制台页面")
				os.Exit(0)
			}
		}
		platform.Notify("EnvKit 已在运行", "请使用已打开的 EnvKit 窗口；若找不到，请在任务管理器结束 EnvKit.exe 后重试")
		fmt.Println("EnvKit 已在运行（单实例保护）。")
		os.Exit(1)
	}
	// 令牌带签发时间：超过 apiTokenTTL 后自动轮换（见 state.go 的 currentToken）
	apiToken = newToken()
	tokenIssued = time.Now()
	loadConfig()
	// 把日志配额推进原子快照：loghub 的写日志热路径不许取 cfgMu（锁序反转），
	// 只在启动与设置页保存后各推一次。
	refreshLogQuota(devSnap())
	os.MkdirAll(cfg.InstallDir, 0755)
	isAdminFlag = isAdmin()
	if !isAdminFlag {
		// 一个进程只提示一次：原先 warn 写在 for 循环里，配置里若挂了多个
		// mysql 组件，同一秒会打出多条一字不差的消息（看起来像"五个分区都报错"）。
		// 级别用 HINT 不用 WARN：非管理员启动是常态、不是启动失败，
		// 而且 WARN 会被"最近错误"收集器当成待处理故障（实测被 AI 误判成环境问题）。
		needAdmin := false
		for _, c := range cfg.Components {
			if c.Special == "mysql" {
				needAdmin = true
				break
			}
		}
		if needAdmin {
			hint(scSys, "权限", "当前不是管理员身份：安装/注册 MySQL 服务需要管理员权限，用到这一步时右键 EnvKit.exe 选「以管理员身份运行」即可；其余功能不受影响。")
		}
	}

	go updateLoop() // 自版本检查（未配置 update_url 或已关闭时静默跳过；间隔可配）

	// 图片附件目录（<exeDir>/aiattach）24 小时自动清理。
	// 启动先扫一遍：用户关掉 EnvKit 的那些天里没人扫，重启时该清的当场清掉。
	aiAttachStartGC()
	// 文档附件目录（<exeDir>/aidoc）同一套规则：拖进来的 docx/pdf 抽出的正文存这里。
	aiDocStartGC()

	// 进程资源后台采样（2s 一轮）+ 配置文件热重载（监听 exe 同目录）。
	// 两者都只读、都不改变启动流程；监听必须在 loadConfig 之后接线，
	// 否则首次事件会把"启动时读到的内容"当成外部改动。
	procStatsStart()
	cfgWatchStart()

	// 手机访问：生成本次进程的配对码；开关已打开时另起一个监听（0.0.0.0:18766）。
	// 主控制台仍然只听 127.0.0.1 —— 两个 mux 不是同一个对象，见 lanaccess.go 开头。
	lanInit()

	// 控制台关闭钩子：点窗口 X 先弹确认，退出前停掉启动的子进程（internal/platform）
	// Ctrl+C / 关窗口事件 → 停子进程 → 退出，绝不留无窗口的后台孤儿。
	platform.InstallConsoleHook(func() {
		shutdownChildren()
		os.Exit(0)
	})

	http.HandleFunc("/", serveIndex)
	http.HandleFunc("/api/config", handleConfig)
	http.HandleFunc("/api/check", handleCheck)
	http.HandleFunc("/api/pet/weather", handlePetWeather)
	http.HandleFunc("/api/pet/config", handlePetConfig) // 桌宠开关（天气默认关闭）
	http.HandleFunc("/api/install", handleInstall)
	http.HandleFunc("/api/state", handleState)
	http.HandleFunc("/api/events", handleEvents)
	http.HandleFunc("/api/pick-dir", handlePickDir)
	http.HandleFunc("/api/pick-file", handlePickFile)
	http.HandleFunc("/api/ls", handleLS)
	http.HandleFunc("/api/paths", handlePaths)        // 真实目录（exe / 日志 / 备份）——界面"打开目录"用
	http.HandleFunc("/api/open/path", handleOpenPath) // 在资源管理器中打开一个目录（只读）
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
	http.HandleFunc("/vendor/marked.min.js", staticBytes("application/javascript; charset=utf-8", markedJS))
	http.HandleFunc("/vendor/highlight.min.js", staticBytes("application/javascript; charset=utf-8", highlightJS))
	http.HandleFunc("/vendor/swiper-bundle.min.js", staticBytes("application/javascript; charset=utf-8", swiperJS))
	http.HandleFunc("/vendor/swiper-bundle.min.css", staticBytes("text/css; charset=utf-8", swiperCSS))
	http.HandleFunc("/vendor/anime.umd.min.js", staticBytes("application/javascript; charset=utf-8", animeJS))
	http.HandleFunc("/vendor/driver.js.iife.js", staticBytes("application/javascript; charset=utf-8", driverTourJS))
	http.HandleFunc("/vendor/driver.css", staticBytes("text/css; charset=utf-8", driverTourCSS))
	http.HandleFunc("/vendor/auto-animate.iife.js", staticBytes("application/javascript; charset=utf-8", autoAnimateJS))
	http.HandleFunc("/vendor/diff2html.min.js", staticBytes("application/javascript; charset=utf-8", diff2htmlJS))
	http.HandleFunc("/vendor/diff2html.min.css", staticBytes("text/css; charset=utf-8", diff2htmlCSS))
	http.HandleFunc("/vendor/diff.min.js", staticBytes("application/javascript; charset=utf-8", jsdiffJS))
	http.HandleFunc("/vendor/hotkeys.min.js", staticBytes("application/javascript; charset=utf-8", hotkeysJS))
	http.HandleFunc("/api/config/export", handleConfigExport)
	http.HandleFunc("/api/config/import", handleConfigImport)
	http.HandleFunc("/api/config/backups", handleConfigBackups)
	http.HandleFunc("/api/config/restore", handleConfigRestore)
	http.HandleFunc("/api/uistate", handleUIState)
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
	http.HandleFunc("/api/proc/stats", handleProcStats) // 进程资源（只读，后台采样的快照）
	http.HandleFunc("/api/win/events", handleWinEvents) // Windows 事件日志（只读，30s 缓存）
	http.HandleFunc("/api/program/cleanup", handleCleanupProcs)
	http.HandleFunc("/api/program/breaker-reset", handleBreakerReset)
	http.HandleFunc("/api/program/state", handleProgState)
	http.HandleFunc("/api/program/db-backup", handleDBBackup)
	http.HandleFunc("/api/program/db-restore", handleDBRestore)
	http.HandleFunc("/api/program/db-drill", handleDBDrill)
	http.HandleFunc("/api/av-exclude", handleAVExclude)
	http.HandleFunc("/api/av-clean", handleAVClean)
	http.HandleFunc("/api/ai/config", handleAIConfig)
	http.HandleFunc("/api/ai/debug", handleAIDebug)
	http.HandleFunc("/api/ai/test", handleAITest)
	http.HandleFunc("/api/ai/chat", handleAIChat)
	http.HandleFunc("/api/ai/attach", handleAIAttach)   // 图片附件：拖入/粘贴的图片落盘，消息里只留引用
	http.HandleFunc("/api/ai/extract", handleAIExtract) // 文档抽文本：docx/xlsx/pptx/pdf → 正文落 aidoc/，消息里只留引用
	http.HandleFunc("/api/ai/confirm", handleAIConfirm) // v3.2：确认卡就地等待的投递口
	http.HandleFunc("/api/ai/echo", handleAIEcho)       // v3.2：传输自检（请求体有没有被吃）
	http.HandleFunc("/api/ai/explain", handleAIExplain)
	http.HandleFunc("/api/ai/providers", handleAIProviders)
	http.HandleFunc("/api/ai/models", handleAIModels)
	http.HandleFunc("/api/ai/balance", handleAIBalance)
	http.HandleFunc("/api/component-versions", handleComponentVersions)
	http.HandleFunc("/api/component-upgrade", handleComponentUpgrade)
	http.HandleFunc("/api/install-dryrun", handleInstallDryRun)
	http.HandleFunc("/api/report", handleReport)
	// v3.9 P0：全链路体检（只读编排 + 一句交付结论 + Markdown 导出）
	http.HandleFunc("/api/healthcheck", handleHealthCheck)
	http.HandleFunc("/api/healthcheck/export", handleHealthCheckExport)
	http.HandleFunc("/api/metrics", handleMetrics)
	http.HandleFunc("/api/diag", handleDiag)
	http.HandleFunc("/api/audit", handleAudit)
	// 轨迹查询：只读，仅主控制台。手机端不给（手机端是"只读 + 有限功能"的面，
	// 轨迹是调优用的，放上去只会让手机页面多一个几乎不会点的入口）。
	http.HandleFunc("/api/traces", handleTraces)
	http.HandleFunc("/api/trace", handleTraceOne)
	http.HandleFunc("/api/stats", handleStats) // v2.7 审计统计看板（30 天时序）
	http.HandleFunc("/api/lessons", handleLessons)
	http.HandleFunc("/api/launch", handleLaunch)
	http.HandleFunc("/api/runtime/state", handleRuntimeState)
	http.HandleFunc("/api/guard/status", handleGuardStatus)
	http.HandleFunc("/api/memory", handleMemory)
	http.HandleFunc("/api/memory/import", handleMemoryImport)
	http.HandleFunc("/api/lesson/toggle", handleLessonToggle)
	// 行动（Agent 闭环 Phase 1 T4）：列表/操作、SSE、入站触发（默认关）
	http.HandleFunc("/api/missions", handleMissions)
	http.HandleFunc("/api/missions/events", handleMissionEvents)
	http.HandleFunc("/api/trigger", handleMissionTrigger)
	// Agent 闭环 Phase 2：预案库 + 晨报读取/下载
	http.HandleFunc("/api/playbooks", handlePlaybooks)
	http.HandleFunc("/api/missions/report", handleMissionReport)
	// Agent 闭环 Phase 3：成长指标（只读，口径见 growth.go）
	http.HandleFunc("/api/growth", handleGrowth)
	// 设置页（v3.4）分段保存端点：告警 / 行动 / 开发者段各走各的通道。
	// 老页面的整包保存（POST /api/config）永不携带这三段 —— mergeFormConfig
	// 的保留逻辑兜底，两处配置面因此可以并存而不互相覆盖。
	http.HandleFunc("/api/alerts/config", handleAlertConfig)
	http.HandleFunc("/api/alerts/test", handleAlertTest)
	http.HandleFunc("/api/notify/config", handleNotifyConfig)
	http.HandleFunc("/api/missions/config", handleMissionConfig)
	http.HandleFunc("/api/update/config", handleUpdateConfig)
	http.HandleFunc("/api/dev/config", handleDevConfig)
	http.HandleFunc("/api/eval/status", handleEvalStatus) // v3.4 评测看板（只读）
	http.HandleFunc("/api/eval/run", handleEvalRun)       // v3.4 四档评测（破坏性档走 428）
	http.HandleFunc("/api/dist/export", handleDistExport)
	http.HandleFunc("/api/self-update", handleSelfUpdate)
	// 手机访问（v3.6.2）：开关/端口/密码/设备管理都只在本机控制台上。
	// 手机端走的是另一个 mux（见 lanaccess.go），这些端点在那上面不存在。
	http.HandleFunc("/api/lan/config", handleLanConfig)
	http.HandleFunc("/api/lan/rotate", handleLanRotate)
	http.HandleFunc("/api/lan/kick", handleLanKick)
	http.HandleFunc("/api/lan/qr", handleLanQR)
	http.HandleFunc("/api/lan/fw", handleLanFW)

	// 控制台端口来自 dev 段（默认 18765，被占用则顺延到 port_scan_limit）
	dv := devSnap()
	port := findPort(dv.serverPort(), dv.portScanLimit())
	// 记下实际端口：评测脚本硬编码 127.0.0.1:18765，端口顺延后必须能被
	// /api/eval/status 发现并对齐（否则脚本会连到别的实例，卡在取 token）。
	listenPortCur = port
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	openURL := "http://" + addr + "/"
	go openBrowser(openURL)
	startGuardLoop()
	startMissionScheduler() // 行动定时产出（巡检/晨报，只读；配置 missions.enabled）
	cleanupOldLogs()
	cleanupOldAudits()
	cleanupOldTraces()    // v3.4：轨迹保留期接入 dev 段（此前函数存在但从未被调用）
	cleanupOldShadows()   // v3.0 T2.8：影子流与审计同保留策略
	missionStartupSweep() // Agent 闭环 Phase 1：重启降级 running→paused + 过期清理
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
	fmt.Println("  · 退出方法：按 Ctrl+C，或右键任务栏托盘图标选「退出 EnvKit」（都会先停服务）")
	fmt.Println("  · 托盘右键还能：打开控制台 / 程序配置 / 打开日志目录 / 隐藏窗口到托盘")
	fmt.Println("  控制台页面：" + openURL + "（浏览器未自动打开时，复制此地址访问）")
	fmt.Println("======================================================")
	fmt.Println()
	// 本机 API 鉴权：/api/* 需携带随机令牌（页面注入），防浏览器内其它网页 CSRF
	startTaskWatchdog()
	// 服务放到后台跑：Windows 的托盘消息循环必须占用主 goroutine
	//（systray 在 init 里 LockOSThread），否则托盘图标会出现但点击无反应。
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- http.ListenAndServe(addr, securityMW(recoverMW(authMW(http.DefaultServeMux))))
	}()
	go func() {
		if err := <-serveErr; err != nil {
			fail(scSys, "服务", "启动失败：%v", err)
			os.Exit(1)
		}
	}()
	// 托盘：非 Windows / 图标缺失时立即返回，退化为原来的纯控制台模式。
	// 「退出」必须在这里停子进程——托盘没有 Ctrl+C，少了这一步就会留一堆孤儿。
	platform.RunTray(appIcon, platform.TrayActions{
		OpenConsole: func() { openBrowser(openURL) },
		OpenConfig:  func() { openBrowser(openURL + "#config") },
		OpenLogs:    func() { platform.OpenFolder(exeDir()) },
		LanBadge: func() string {
			c := lanSnap()
			if !c.on() {
				return "手机访问：未开启"
			}
			if !lanServeRunning() {
				return "手机访问：未监听（端口被占用？看日志）"
			}
			return "手机访问：配对码 " + lanPairCodeCur()
		},
		OpenLan: func() { openBrowser(openURL + "#config") },
		CreateShortcut: func() {
			// 用 os.Executable 的绝对路径，不用 exeDir()+文件名拼——临时改过名的 exe
			// （比如 EnvKit_ab.exe）也能指向它自己，快捷方式不会失效。
			self, err := os.Executable()
			if err != nil {
				warn(scSys, "服务", "创建桌面快捷方式失败：取不到 exe 路径（%v）", err)
				return
			}
			lnk, err := platform.CreateDesktopShortcut(self, "EnvKit", "EnvKit 统一开发环境助手")
			if err != nil {
				warn(scSys, "服务", "创建桌面快捷方式失败：%v", err)
				platform.Notify("EnvKit", "创建桌面快捷方式失败："+err.Error())
				return
			}
			info(scSys, "服务", "已在桌面创建快捷方式：%s", lnk)
			platform.Notify("EnvKit", "桌面快捷方式已创建")
		},
		Failed: func(err error) {
			warn(scSys, "服务", "托盘未能启动：%v（控制台与网页照常可用）", err)
			fmt.Println()
			fmt.Println("  [托盘不可用] " + err.Error())
			fmt.Println("  控制台和网页不受影响；窗口会保持显示，不会被藏到托盘里。")
			fmt.Println("  退出请按 Ctrl+C，或在网页里点退出；任务栏恢复后会自动补回图标。")
			fmt.Println()
		},
		Recovered: func(note string) {
			info(scSys, "服务", "托盘图标已恢复：%s（菜单照常可用）", note)
		},
		Ready: func() {
			info(scSys, "服务", "托盘菜单已就绪：右键图标可打开控制台 / 配置 / 日志，或退出")
			cfgMu.Lock()
			hidden := cfg.TrayHidden
			cfgMu.Unlock()
			if hidden {
				// 上次是"后台运行"状态：沿用，但必须等托盘就绪后再藏窗口——
				// 托盘没起来就藏窗口，用户将没有任何入口能把窗口找回来。
				platform.HideConsole()
				info(scSys, "服务", "按上次的选择隐藏窗口到托盘；右键托盘图标可显示窗口或退出")
			}
		},
		ToggleWindow: func() bool {
			if platform.ConsoleVisible() {
				platform.HideConsole()
				setTrayHidden(true)
				info(scSys, "服务", "窗口已隐藏到托盘（后台运行），右键托盘图标可恢复或退出")
				return true
			}
			platform.ShowConsole()
			setTrayHidden(false)
			info(scSys, "服务", "控制台窗口已恢复")
			return false
		},
		Exit: func() {
			info(scSys, "服务", "从托盘退出：正在停止前端/后端服务")
			shutdownChildren()
			os.Exit(0)
		},
	})
	// 没有托盘（非 Windows 或托盘不可用）：阻塞在服务循环上，行为与旧版一致。
	if err := <-serveErr; err != nil {
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

// setTrayHidden 记住窗口显隐状态（下次启动沿用）。
// 只在真正变化时写盘，避免托盘来回点时反复重写 config.json。
func setTrayHidden(on bool) {
	cfgMu.Lock()
	if cfg.TrayHidden == on {
		cfgMu.Unlock()
		return
	}
	cfg.TrayHidden = on
	snap := cfg
	cfgMu.Unlock()
	saveExternalConfig(snap)
}

// findPort 从 start 起找第一个可监听的端口，上界 limit（不含）。
// start/limit 来自 dev 段（server_port / port_scan_limit，默认 18765 / 18815）。
// 找不到时回落 start（此时 ListenAndServe 会失败并给出可读报错，而不是静默换端口）。
func findPort(start, limit int) int {
	if limit <= start {
		limit = start + devDefaultPortScanSpan
	}
	for p := start; p < limit; p++ {
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
