// portv6_test.go —— 端口探测的 IPv6 覆盖。
//
// 背景（2026-11-22 真实缺陷）：vite/Node 17+ 默认把 localhost 绑到 [::1]，
// 复验层三处探测（netstat 扫描、TCP 拨测、HTTP 握手）原先只覆盖 IPv4，
// 前端"明明 ready 了"复验却判 unverified——服务是好的，系统说没验证到。
// 三个测试分别钉住三条路径，夹具全部用真实监听器，不注入假实现。
package main

import (
	"net"
	"net/http"
	"strings"
	"testing"
)

// v6Listener 起一个只绑 IPv6 回环的真实监听器；[::1] 不可用的机器（罕见）跳过。
func v6Listener(t *testing.T) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("本机 IPv6 回环不可用，跳过：%v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, func() { _ = ln.Close() }
}

// TestParseNetstatListenersV4AndV6 纯函数：v4 行、v6 行、UDP 行、垃圾行同一口径解析。
func TestParseNetstatListenersV4AndV6(t *testing.T) {
	out := strings.Join([]string{
		"",
		"  活动连接",
		"  协议  本地地址          外部地址        状态           PID",
		"TCP    0.0.0.0:3306           0.0.0.0:0              LISTENING       3308",
		"TCP    127.0.0.1:18765        127.0.0.1:0            LISTENING       9001",
		"TCP    [::1]:5173             [::]:0                 LISTENING       24700",
		"TCP    [::]:135               [::]:0                 LISTENING       1234",
		"TCP    1.2.3.4:9999           5.6.7.8:10000          ESTABLISHED     4321",
		"UDP    0.0.0.0:5353           *:*                    8888",
		"TCP    not-an-address         0.0.0.0:0              LISTENING       1111",
		"",
	}, "\n")
	occ := parseNetstatListeners(out)

	if pids := occ[5173]; len(pids) != 1 || pids[0] != 24700 {
		t.Errorf("IPv6 [::1]:5173 必须解析出 pid 24700，实际 %v", occ[5173])
	}
	if pids := occ[3306]; len(pids) != 1 || pids[0] != 3308 {
		t.Errorf("IPv4 0.0.0.0:3306 必须解析出 pid 3308，实际 %v", occ[3306])
	}
	if pids := occ[135]; len(pids) != 1 || pids[0] != 1234 {
		t.Errorf("IPv6 [::]:135 必须解析出 pid 1234，实际 %v", occ[135])
	}
	if pids := occ[18765]; len(pids) != 1 || pids[0] != 9001 {
		t.Errorf("IPv4 127.0.0.1:18765 必须解析出 pid 9001，实际 %v", occ[18765])
	}
	if _, ok := occ[9999]; ok {
		t.Error("ESTABLISHED 行不能算进监听表")
	}
	if _, ok := occ[5353]; ok {
		t.Error("UDP 行不能算进监听表")
	}
	if _, ok := occ[0]; ok {
		t.Error("解析失败的行不能算进监听表")
	}
	if len(occ) != 4 {
		t.Errorf("监听表应恰好 4 个端口，实际 %d：%v", len(occ), occ)
	}
}

// TestDialLocalIPv6OnlyListener 真实夹具：服务只绑 [::1]，dialLocal 必须拨得通。
// 旧实现只拨 127.0.0.1 —— 对这个夹具必然失败（变异验证的红就落在这条断言上）。
func TestDialLocalIPv6OnlyListener(t *testing.T) {
	port, close := v6Listener(t)
	defer close()
	if !dialLocal(port) {
		t.Errorf("只绑 [::1]:%d 的服务，dialLocal 必须能探到（IPv6 回环拨测缺失）", port)
	}
}

// TestDialLocalV4StillWorks 防回归：v4-only 监听仍要探得到（先 v4 后 v6 的顺序不能破）。
func TestDialLocalV4StillWorks(t *testing.T) {
	port, close := occPort(t)
	defer close()
	if !dialLocal(port) {
		t.Errorf("绑 127.0.0.1:%d 的服务，dialLocal 必须能探到", port)
	}
}

// TestHTTPProbeIPv6Only 真实 HTTP 服务只绑 [::1]，httpProbe 必须拿到响应码。
func TestHTTPProbeIPv6Only(t *testing.T) {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("本机 IPv6 回环不可用，跳过：%v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	srv := &http.Server{Handler: mux}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close(); <-errCh })

	got := httpProbe(port)
	if !strings.HasPrefix(got, "HTTP 2") {
		t.Errorf("只绑 [::1]:%d 的 HTTP 服务必须被探到，实际 %q", port, got)
	}
}

// TestProbeServicePortRecordsHitHost 观察器握手必须带回命中的 host。
// 只绑 [::1] 的服务上，v4 必不通、v6 必通——返回值第三项就是 UI 链接的依据。
func TestProbeServicePortRecordsHitHost(t *testing.T) {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("本机 IPv6 回环不可用，跳过：%v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("vite"))
	})
	srv := &http.Server{Handler: mux}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close(); <-errCh })

	_, code, host := probeServicePort(port, []string{"/"})
	if code != 200 || host != "::1" {
		t.Errorf("只绑 [::1]:%d 的服务应命中 v6：code=%d host=%q", port, code, host)
	}
}

// TestRuntimeServiceExternalURLUsesHitHost 外部实例链接用握手命中的 host。
// 2026-11-23 缺陷：vite 只绑 [::1]，链接却硬编码 127.0.0.1，UI 点开打不开。
func TestRuntimeServiceExternalURLUsesHitHost(t *testing.T) {
	withObservedForTest(t, "web", observedInternal{observedService: observedService{
		Service: "web", State: "external", Port: 5173, PID: 4242,
		OwnerName: "node.exe", Health: "ok", Host: "::1",
	}})
	withManagedStateForTest(t, "web", &SvcInfo{})

	st := runtimeService("web")
	if st.URL != "http://[::1]:5173/" {
		t.Errorf("url=%q, want http://[::1]:5173/（握手命中的 v6 地址）", st.URL)
	}
}
