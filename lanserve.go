// 监听生命周期 / 安全中间件 / 路由表 —— 从 lanaccess.go 拆出（纯搬家，行为不变）。
// 手机端的安全边界是「独立 mux + 路由少」，拆文件只移动函数，
// 不新增任何路由。

package main

import (
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

var lanSrv atomic.Value // *http.Server

// lanPortLive 当前**实际**在监听的端口；0 = 没在监听。
//
// 为什么不能直接用配置里的 port：改端口不热切换（会断开正在用的手机），
// 于是"配置端口"和"真正在听的那个端口"会短暂不一致。旧实现拿配置端口去生成
// 访问地址和二维码，状态还显示"已开启" —— 用户对着一个没人监听的端口反复试，
// 只会得到"手机连不上电脑"。
var lanPortLive atomic.Int64

// lanListeningPort 实际监听端口；0 表示没在监听。
func lanListeningPort() int { return int(lanPortLive.Load()) }

// lanDisplayPort 生成"给手机用"的地址时该用哪个端口。
//
// 正在监听 → 用实际端口（配置改了但没重启时，配置端口上没人）。
// 没在监听 → 用配置端口（此时没有"实际"可言，界面上也不会展示地址）。
func lanDisplayPort(c LanConfig) int {
	if p := lanListeningPort(); p != 0 {
		return p
	}
	return c.port()
}

// lanServeRunning 当前是否在监听（设置页显示用）。
func lanServeRunning() bool {
	v, _ := lanSrv.Load().(*http.Server)
	return v != nil
}

// lanApplyMu 串行化 lanServeApply。
//
// 它现在有三个调用点：启动（lanInit）、设置页保存、配置热重载（configwatch）。
// 后两个是并发的 —— 「读 lanSrv 判空 → Listen → 存回去」不是原子操作，
// 两个 goroutine 同时看到空就会有一个 Listen 失败，日志里冒出
// "端口无法监听"，用户会以为端口被占用。锁住整段即可（动作很短）。
var lanApplyMu sync.Mutex

// lanServeApply 按配置起停监听。幂等：重复调用只会在"该起/该停"时动作。
//
// 注意：这里**不重启已运行的监听**。改端口需要先 Close 再 Listen，
// 而 Close 会让正在用的手机掉线 —— 设置页改端口时明确提示"重启 EnvKit 后生效"，
// 比悄悄断开用户正在用的连接更好。
func lanServeApply(c LanConfig) {
	lanApplyMu.Lock()
	defer lanApplyMu.Unlock()
	want := c.on()
	cur, _ := lanSrv.Load().(*http.Server)
	if !want {
		if cur != nil {
			_ = cur.Close()
			lanSrv.Store((*http.Server)(nil))
			lanPortLive.Store(0)
			info(scSys, "手机访问", "已关闭（端口不再监听）")
		}
		return
	}
	if cur != nil {
		return // 已在跑；端口变更走重启
	}
	port := c.port()
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		warn(scSys, "手机访问", "端口 %d 无法监听（%v）——手机访问保持关闭，请换一个端口", port, err)
		return
	}
	srv := &http.Server{
		Handler:           lanSecurityMW(lanMux()),
		ReadHeaderTimeout: 10 * time.Second,
	}
	lanSrv.Store(srv)
	lanPortLive.Store(int64(port))
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			warn(scSys, "手机访问", "监听中断：%v", err)
			lanSrv.Store((*http.Server)(nil))
			lanPortLive.Store(0)
		}
	}()
	var ips []string
	for _, u := range lanAddrURLs(port) {
		ips = append(ips, u)
	}
	if len(ips) == 0 {
		info(scSys, "手机访问", "已开启，监听 0.0.0.0:%d（没找到手机能连上的地址：电脑可能没连 WiFi/网线？）", port)
	} else {
		info(scSys, "手机访问", "已开启：手机访问 %s ，配对码 %s", ips[0], lanPairCodeCur())
	}
}

// lanInit 进程启动时调用一次：生成配对码，若开关已开则起监听。
func lanInit() {
	lanRotatePairCode()
	lanServeApply(lanSnap())
}

// ===== 中间件 / 路由 =====

func lanSecurityMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		// 手机页自包含（内联 CSS/JS），CSP 与主控制台同一套口径
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		next.ServeHTTP(w, r)
	})
}

// lanMux 手机端路由表 —— 首页 + 12 个 API，多一条都要先问清楚。
//
// 想加新能力时先问：它需要"执行"还是"查看"？手机上只该有启停、对话、
// 三个**显式开关控制**的本地能力（截屏/剪贴板/终端，链端 SSH 与本地终端共用一个开关），
// 以及两条**只读**的观察入口（日志、统计+晨报）。
// 其余的（改配置、跑命令、看审计、导数据库）一律不加，
// 否则这里会慢慢长成第二个主控制台。
//
// 判断标准不是"这条路由危险不危险"，而是"它能不能改变这台机器的状态"。
// 只读的观察入口不需要开关：手机本来就该能看；需要开关的一律是"能改"的。
func lanMux() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("/", lanPage)
	m.HandleFunc("/api/lan/pair", lanPairPost)
	m.HandleFunc("/api/lan/unpair", lanUnpairPost)
	m.HandleFunc("/api/lan/state", lanStateGet)
	m.HandleFunc("/api/lan/start", lanStartPost)
	m.HandleFunc("/api/lan/stop", lanStopPost)
	m.HandleFunc("/api/lan/chat", lanChat)
	// v3.7.0 本地能力：三个都受 Allow* 开关控制，默认值见 localdev.go
	m.HandleFunc("/api/lan/shot", lanShotGet)
	m.HandleFunc("/api/lan/clip", lanClipAny)
	m.HandleFunc("/api/lan/term", lanTermWS)
	// 链端 SSH 终端：与本地终端同一个开关、同一份实现（chainTermWS），只换 upgrader。
	m.HandleFunc("/api/lan/ssh", lanSSHWS)
	// 日志报告区：只读的"最近日志"与"统计 + 最近一份晨报"。
	// 不加开关：这两条都是读，手机本来就该能看；真正需要开关的是"能改"的能力。
	m.HandleFunc("/api/lan/logs", lanLogsGet)
	m.HandleFunc("/api/lan/brief", lanBriefGet)
	// 终端用的 xterm.js 走本 mux 自取：手机页面不能引用主控制台的 /xterm/*，
	// 那些路径在这个 mux 上不存在（不是"被拦"，是根本没有）。
	m.HandleFunc("/xterm/xterm.js", staticBytes("application/javascript; charset=utf-8", xtermJS))
	m.HandleFunc("/xterm/addon-fit.js", staticBytes("application/javascript; charset=utf-8", xtermFitJS))
	m.HandleFunc("/xterm/xterm.css", staticBytes("text/css; charset=utf-8", xtermCSS))
	return m
}
