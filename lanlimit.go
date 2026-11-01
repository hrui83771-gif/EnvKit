// Host 白名单 + 失败限速 —— 从 lanaccess.go 拆出（纯搬家，行为不变）。
// 手机端的安全边界是「独立 mux + 路由少」，拆文件只移动函数，
// 不新增任何路由。

package main

import (
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

func lanHostAllowed(r *http.Request) bool {
	h := r.Host
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	if h == "" {
		return false
	}
	if h == "127.0.0.1" || h == "localhost" || h == "::1" {
		return true
	}
	for _, ip := range lanLocalIPs() {
		if strings.EqualFold(h, ip) {
			return true
		}
	}
	if hn, err := os.Hostname(); err == nil && hn != "" && strings.EqualFold(h, hn) {
		return true
	}
	return false
}

// ===== 失败限速 =====

type lanFailRec struct {
	n     int
	at    time.Time
	until time.Time
}

var (
	lanFailMu sync.Mutex
	lanFails  = map[string]*lanFailRec{}
)

func lanClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func lanRateBlocked(ip string) (bool, time.Duration) {
	lanFailMu.Lock()
	defer lanFailMu.Unlock()
	rec := lanFails[ip]
	if rec == nil {
		return false, 0
	}
	if time.Now().Before(rec.until) {
		return true, time.Until(rec.until)
	}
	return false, 0
}

func lanRateFail(ip string) {
	lanFailMu.Lock()
	defer lanFailMu.Unlock()
	rec := lanFails[ip]
	if rec == nil || time.Since(rec.at) > lanFailWindow {
		rec = &lanFailRec{}
		lanFails[ip] = rec
	}
	rec.n++
	rec.at = time.Now()
	if rec.n >= lanMaxFail {
		rec.until = time.Now().Add(lanCoolDown)
		rec.n = 0
	}
}

func lanRatePass(ip string) {
	lanFailMu.Lock()
	delete(lanFails, ip)
	lanFailMu.Unlock()
}

// ===== 服务端：独立监听 =====
