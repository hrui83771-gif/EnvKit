package main

// lanaccess_test.go —— 手机访问的四条安全属性，逐条钉住：
//   1. 主机名白名单挡得住 DNS rebinding（"Origin 与 Host 同源"这种判断挡不住）；
//   2. 手机端的工具清单里**一个写工具都不能有**（这是"只读"的硬定义）；
//   3. 手机端 mux 上不存在配置/审计/终端这类路由（不是"被拒绝"，是压根没注册）；
//   4. 配对码与密码两条路都通，失败会被限速。
//
// 这几条都属于"错了不会报错、只会悄悄开门"的类型，所以必须有断言。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// lanTestSetup 把配置与落盘钩子都换成测试专用的，返回恢复函数。
func lanTestSetup(t *testing.T, c *LanConfig) {
	t.Helper()
	oldPersist := lanPersist
	lanPersist = func(Config) {} // 绝不写真实 config.json
	cfgMu.Lock()
	oldLan := cfg.Lan
	cfg.Lan = c
	cfgMu.Unlock()
	lanFailMu.Lock()
	lanFails = map[string]*lanFailRec{}
	lanFailMu.Unlock()
	lanPairCode.Store("482913")

	t.Cleanup(func() {
		cfgMu.Lock()
		cfg.Lan = oldLan
		cfgMu.Unlock()
		lanPersist = oldPersist
		lanFailMu.Lock()
		lanFails = map[string]*lanFailRec{}
		lanFailMu.Unlock()
	})
}

func boolp(b bool) *bool { return &b }

func intp(n int) *int { return &n }

// ---------- 1. 主机名白名单 ----------

func TestLanHostAllowedRejectsForeignHost(t *testing.T) {
	cases := []struct {
		host   string
		origin string
		want   bool
	}{
		{"127.0.0.1:18766", "", true},
		{"localhost:18766", "", true},
		{"[::1]:18766", "", true},
		// DNS rebinding 的样子：攻击者把 evil.com 解析到你的局域网 IP。
		// Origin 与 Host 会完全一致，所以"Origin == Host"的判断一律放行 ——
		// 只有"Host 必须是本机的某个名字"才拦得住。
		// 下面两条刻意带上与之同源的 Origin：这正是真实攻击请求的形状，
		// 少了 origin 就测不出"用同源判断代替白名单"这个错误（变异验证抓出来过）。
		{"evil.com:18766", "http://evil.com:18766", false},
		{"attacker.example", "http://attacker.example", false},
		{"192.0.2.9:18766", "", false}, // 不是本机网卡上的地址
		{"", "", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := lanHostAllowed(r); got != c.want {
			t.Errorf("Host=%q Origin=%q 期望 %v，得到 %v", c.host, c.origin, c.want, got)
		}
	}
}

// ---------- 2. 只读工具清单 ----------

func TestLanToolDefsHaveNoWriteTools(t *testing.T) {
	defs := aiToolDefsReadOnly()
	if len(defs) == 0 {
		t.Fatal("只读清单为空 —— 手机上的 AI 会什么也查不了")
	}
	for _, d := range defs {
		name := d.Function.Name
		tool, ok := aiToolRegistry[name]
		if !ok {
			t.Fatalf("清单里有未注册的工具：%s", name)
		}
		if tool.Write {
			t.Errorf("写工具「%s」出现在手机端清单里 —— 只读被破坏了", name)
		}
	}
	// 反向断言：全量清单里确实有写工具，否则这条测试是空转
	hasWrite := false
	for _, tool := range aiToolRegistry {
		if tool.Write {
			hasWrite = true
			break
		}
	}
	if !hasWrite {
		t.Fatal("注册表里根本没有写工具 —— 这条断言失去意义，请检查 aiTool.Write")
	}
	if len(defs) >= len(aiToolRegistry) {
		t.Fatalf("只读清单（%d）没有比全量（%d）少，说明过滤没生效", len(defs), len(aiToolRegistry))
	}
}

// ---------- 3. 手机端路由表 ----------

func TestLanMuxHasNoDesktopRoutes(t *testing.T) {
	m := lanMux()
	forbidden := []string{
		"/api/config", "/api/dev/config", "/api/ai/config", "/api/audit",
		"/api/db/query", "/api/ports/kill", "/api/program/exit", "/api/lan/config",
	}
	for _, p := range forbidden {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, p, strings.NewReader("{}")))
		// 命中 "/" 兜底会走 lanPage 并返回 404（非根路径），那也算"没有这个路由"
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s 在手机端 mux 上被处理了（code=%d）—— 配置/终端这类端点不能出现在手机端", p, rec.Code)
		}
	}
	// 正向：核心的几条要在
	for _, p := range []string{"/api/lan/pair", "/api/lan/state", "/api/lan/start", "/api/lan/stop", "/api/lan/chat"} {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code == http.StatusNotFound {
			t.Errorf("%s 没注册在手机端 mux 上", p)
		}
	}
}

// ---------- 4. 配对 ----------

func lanPairReq(code, ip string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(map[string]string{"code": code})
	r := httptest.NewRequest(http.MethodPost, "/api/lan/pair", strings.NewReader(string(b)))
	r.Host = "127.0.0.1:18766"
	r.RemoteAddr = ip + ":54321"
	r.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Safari/604.1")
	rec := httptest.NewRecorder()
	lanPairPost(rec, r)
	return rec
}

func TestLanPairWithCodeAndPassword(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("my-secret-pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	lanTestSetup(t, &LanConfig{Enabled: boolp(true), PassHash: string(hash)})

	// 正确配对码 → 200 + 下发 cookie
	rec := lanPairReq("482913", "192.168.1.10")
	if rec.Code != http.StatusOK {
		t.Fatalf("正确配对码应通过，得到 %d：%s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != lanCookieName {
		t.Fatalf("配对成功必须下发 %s cookie", lanCookieName)
	}
	if !cookies[0].HttpOnly {
		t.Error("设备令牌 cookie 必须是 HttpOnly —— 否则 XSS 能把它读走")
	}
	if cookies[0].Value == "" {
		t.Error("cookie 值为空")
	}

	// 正确密码 → 200（换一台设备，避免命中限速历史）
	if rec := lanPairReq("my-secret-pw", "192.168.1.11"); rec.Code != http.StatusOK {
		t.Fatalf("正确密码应通过，得到 %d", rec.Code)
	}

	// 错误码 → 403，且**不**下发 cookie
	rec = lanPairReq("000000", "192.168.1.12")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("错误配对码应 403，得到 %d", rec.Code)
	}
	if len(rec.Result().Cookies()) > 0 {
		t.Fatal("配对失败却下发了 cookie")
	}

	// 设备数应等于成功次数（2 次）
	if n := len(lanSnap().Devices); n != 2 {
		t.Fatalf("设备表应有 2 台，得到 %d", n)
	}
	// 磁盘上不许出现明文令牌
	for _, d := range lanSnap().Devices {
		if len(d.Hash) != 64 {
			t.Errorf("设备 %s 的 hash 长度 %d，应存 sha256 十六进制", d.ID, len(d.Hash))
		}
	}
}

func TestLanPairRateLimited(t *testing.T) {
	lanTestSetup(t, &LanConfig{Enabled: boolp(true)})
	ip := "10.0.0.9"
	for i := 0; i < lanMaxFail; i++ {
		if rec := lanPairReq("111111", ip); rec.Code != http.StatusForbidden {
			t.Fatalf("第 %d 次错误码应 403，得到 %d", i+1, rec.Code)
		}
	}
	// 第 6 次：即使码是对的也要被挡（否则 6 位码 100 万组合迟早被穷举）
	if rec := lanPairReq("482913", ip); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("超过 %d 次失败后应限速，得到 %d", lanMaxFail, rec.Code)
	}
	// 换一个 IP 不受影响：限速是按来源的，不能一个人试错把全家锁死
	if rec := lanPairReq("482913", "10.0.0.10"); rec.Code != http.StatusOK {
		t.Fatalf("别的 IP 不该被连坐，得到 %d", rec.Code)
	}
}

func TestLanPairRejectedWhenDisabled(t *testing.T) {
	lanTestSetup(t, &LanConfig{Enabled: boolp(false)})
	if rec := lanPairReq("482913", "192.168.1.20"); rec.Code != http.StatusForbidden {
		t.Fatalf("开关关闭时配对必须被拒，得到 %d", rec.Code)
	}
}

// ---------- 设备令牌 ----------

func TestLanDeviceTokenLookup(t *testing.T) {
	lanTestSetup(t, &LanConfig{Enabled: boolp(true)})

	// 走一次真实配对拿到令牌
	rec := lanPairReq("482913", "192.168.1.30")
	if rec.Code != http.StatusOK {
		t.Fatalf("配对失败：%d", rec.Code)
	}
	tok := rec.Result().Cookies()[0].Value

	if _, ok := lanDeviceFor(tok); !ok {
		t.Fatal("刚配对的令牌查不到设备")
	}
	if _, ok := lanDeviceFor(tok + "x"); ok {
		t.Fatal("篡改过的令牌不该通过")
	}
	if _, ok := lanDeviceFor(""); ok {
		t.Fatal("空令牌不该通过")
	}

	// 带 cookie 请求状态端点 → 200
	r := httptest.NewRequest(http.MethodGet, "/api/lan/state", nil)
	r.Host = "127.0.0.1:18766"
	r.AddCookie(&http.Cookie{Name: lanCookieName, Value: tok})
	rec2 := httptest.NewRecorder()
	lanStateGet(rec2, r)
	if rec2.Code != http.StatusOK {
		t.Fatalf("带合法令牌的状态请求应 200，得到 %d", rec2.Code)
	}

	// 不带 cookie → 401
	r3 := httptest.NewRequest(http.MethodGet, "/api/lan/state", nil)
	r3.Host = "127.0.0.1:18766"
	rec3 := httptest.NewRecorder()
	lanStateGet(rec3, r3)
	if rec3.Code != http.StatusUnauthorized {
		t.Fatalf("无令牌应 401，得到 %d", rec3.Code)
	}
}

// ---------- 配置边界 ----------

func TestLanPortBounds(t *testing.T) {
	if got := (LanConfig{}).port(); got != lanDefaultPort {
		t.Fatalf("未设置时应用默认端口 %d，得到 %d", lanDefaultPort, got)
	}
	bad := 80
	if got := (LanConfig{Port: &bad}).port(); got != lanDefaultPort {
		t.Fatalf("1024 以下应回落默认端口，得到 %d", got)
	}
	ok := 18888
	if got := (LanConfig{Port: &ok}).port(); got != 18888 {
		t.Fatalf("合法端口应原样返回，得到 %d", got)
	}
}

func TestLanDeviceName(t *testing.T) {
	cases := map[string]string{
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0) ... Safari/604.1":           "iPhone · Safari",
		"Mozilla/5.0 (Linux; Android 14) ... Chrome/120 Mobile Safari/537.36": "Android · Chrome",
		"Mozilla/5.0 (iPhone) ... MicroMessenger/8.0":                         "iPhone · 微信",
		"Mozilla/5.0 (Windows NT 10.0) ... Chrome/120 Safari/537":             "Windows · Chrome",
	}
	for ua, want := range cases {
		if got := lanDeviceName(ua); got != want {
			t.Errorf("UA 解析：期望 %q，得到 %q", want, got)
		}
	}
}

// ---------- 状态口径（v3.6.2） ----------

// TestLanStateUsesRuntimeService 手机端状态必须与桌面端**同一个口径**。
//
// v3.6.2 修的 bug：旧实现按 {"web","backend"} 查 progState，
// 而 progState 的键是 "web-start"/"backend-start"（那是"启动任务"不是"服务"），
// 于是 state[kv.key] 恒空 —— 手机端**永远显示未启动**。
//
// 这条测试钉的是"口径"而不是某个具体值：
// 让后端把 web 置为 running 且已复验，手机端拿到的 phase 必须是 running。
// 如果哪天下巴又自己查 progState，这条会立刻红。
func TestLanStateUsesRuntimeService(t *testing.T) {
	lanTestSetup(t, &LanConfig{Enabled: boolp(true)})
	rec := lanPairReq("482913", "192.168.1.40")
	if rec.Code != http.StatusOK {
		t.Fatalf("配对失败：%d", rec.Code)
	}
	tok := rec.Result().Cookies()[0].Value

	// 后端：web 跑着且复验通过
	svcMu.Lock()
	svcState["web"] = &SvcInfo{Running: true, PID: 4321, Since: "2026-10-31 10:00:00"}
	svcMu.Unlock()
	svcRecordVerify("web", "前端已就绪：端口 8080 在监听", "", true)
	defer func() {
		svcMu.Lock()
		svcState["web"] = &SvcInfo{}
		svcMu.Unlock()
		svcForgetVerify("web")
	}()

	r := httptest.NewRequest(http.MethodGet, "/api/lan/state", nil)
	r.Host = "127.0.0.1:18766"
	r.AddCookie(&http.Cookie{Name: lanCookieName, Value: tok})
	rec2 := httptest.NewRecorder()
	lanStateGet(rec2, r)
	if rec2.Code != http.StatusOK {
		t.Fatalf("状态请求应 200，得到 %d", rec2.Code)
	}
	var out struct {
		Services []struct {
			Key      string `json:"key"`
			Phase    string `json:"phase"`
			Running  bool   `json:"running"`
			Verified bool   `json:"verified"`
		} `json:"services"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &out); err != nil {
		t.Fatalf("状态响应不是合法 JSON：%v", err)
	}
	var web *struct {
		Key      string `json:"key"`
		Phase    string `json:"phase"`
		Running  bool   `json:"running"`
		Verified bool   `json:"verified"`
	}
	for i := range out.Services {
		if out.Services[i].Key == "web" {
			web = &out.Services[i]
		}
	}
	if web == nil {
		t.Fatal("状态里没有 web 这一项")
	}
	if web.Phase != string(phRunning) {
		t.Errorf("web 已跑且已复验，手机端 phase 应为 running，实际 %q", web.Phase)
	}
	if !web.Running || !web.Verified {
		t.Errorf("web 的 running/verified 应为 true，实际 running=%v verified=%v", web.Running, web.Verified)
	}
	// 反向断言：后端没跑时不能报 running
	svcMu.Lock()
	svcState["web"] = &SvcInfo{Running: false}
	svcMu.Unlock()
	svcForgetVerify("web")
	rec3 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, "/api/lan/state", nil)
	r2.Host = "127.0.0.1:18766"
	r2.AddCookie(&http.Cookie{Name: lanCookieName, Value: tok})
	lanStateGet(rec3, r2)
	var out2 struct {
		Services []struct {
			Key     string `json:"key"`
			Phase   string `json:"phase"`
			Running bool   `json:"running"`
		} `json:"services"`
	}
	_ = json.Unmarshal(rec3.Body.Bytes(), &out2)
	for _, s := range out2.Services {
		if s.Key == "web" && (s.Phase == string(phRunning) || s.Running) {
			t.Errorf("服务已停，手机端还报 %q/running=%v —— 状态口径错了", s.Phase, s.Running)
		}
	}
}

// ---------- 地址筛选（v3.6.2） ----------

// TestLanFilterReachable 过滤规则必须挡住"手机连不上"的地址。
//
// **用构造出来的候选集测，不用本机真实网卡** ——
// 只用真实网卡的话，在没有 169.254 网卡的机器上这条断言会空转
// （变异验证时真的空转过一次：把过滤条件改坏，测试照样绿，
//
//	那样这条测试就是"看起来有、其实抓不到任何东西"）。
//
// 构造的候选集刻意覆盖了本机实测到的每一类：
// VMware 虚拟网卡、169.254 链路本地、回环、0.0.0.0、虚拟网卡名、以及真正的 WLAN。
func TestLanFilterReachable(t *testing.T) {
	cands := []lanAddrCand{
		{Iface: "WLAN", IP: "192.168.1.12"}, // 真正的出口 —— 必须留下
		{Iface: "以太网", IP: "192.168.2.30"},  // 真实网卡 —— 也留下
		{Iface: "VMware Network Adapter VMnet1", IP: "192.168.110.1"},
		{Iface: "VMware Network Adapter VMnet8", IP: "192.168.128.1"},
		{Iface: "以太网 2", IP: "169.254.201.17"}, // 链路本地
		{Iface: "Loopback Pseudo-Interface 1", IP: "127.0.0.1"},
		{Iface: "以太网 3", IP: "0.0.0.0"},
		{Iface: "以太网 4", IP: "not-an-ip"},
	}
	got := lanFilterReachable(cands)
	want := []string{"192.168.1.12", "192.168.2.30"}
	if len(got) != len(want) {
		t.Fatalf("过滤后应剩 %v，实际 %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("过滤后应剩 %v，实际 %v", want, got)
		}
	}
	// 逐条点名：说清是哪一个没被挡住（报错要能直接指向问题）
	for _, bad := range []string{"192.168.110.1", "192.168.128.1", "169.254.201.17", "127.0.0.1", "0.0.0.0"} {
		for _, g := range got {
			if g == bad {
				t.Errorf("地址 %s 手机连不上，不该出现在访问地址里", bad)
			}
		}
	}
}

// TestLanVirtualIfaceNames 虚拟网卡名识别。
func TestLanVirtualIfaceNames(t *testing.T) {
	for _, n := range []string{"VMware Network Adapter VMnet1", "VirtualBox Host-Only", "vEthernet (WSL)", "Hyper-V Virtual Adapter", "loopback"} {
		if !lanVirtualIface(n) {
			t.Errorf("网卡名 %q 应被识别为虚拟网卡", n)
		}
	}
	for _, n := range []string{"WLAN", "以太网", "无线网卡", "Wi-Fi"} {
		if lanVirtualIface(n) {
			t.Errorf("网卡名 %q 是真实网卡，不该被判为虚拟", n)
		}
	}
}

// TestLanReachableIPsExcludeLinkLocal 给用户看的地址里不能有 169.254.x.x。
//
// 链路本地地址（APIPA）在手机上根本路由不到；把它列进"手机访问地址"，
// 用户只能一个个试 —— 实测反馈是"只有最后一个能用"。
//
// 两条路径都要查：主路径（默认路由出口）与兜底路径（按网卡名排除虚拟网卡）。
func TestLanReachableIPsExcludeLinkLocal(t *testing.T) {
	check := func(what string, ips []string) {
		for _, ip := range ips {
			p := parseIPv4(ip)
			if p == nil {
				t.Errorf("%s：地址 %q 不是合法 IPv4", what, ip)
				continue
			}
			if p[0] == 169 && p[1] == 254 {
				t.Errorf("%s：链路本地地址 %q 不能出现在手机访问地址里（手机路由不到）", what, ip)
			}
			if p[0] == 127 {
				t.Errorf("%s：回环地址 %q 不能出现在手机访问地址里", what, ip)
			}
			if p[0] == 0 {
				t.Errorf("%s：未指定地址 %q 不能出现", what, ip)
			}
		}
	}
	check("默认路由出口", lanReachableIPs())
	check("兜底路径", lanIPsExcludingVirtual())
}

// TestLanLocalIPsKeepsAllForHostWhitelist Host 白名单必须仍然认全部本机地址。
//
// 这两条路径的口径**故意不同**：
//
//	· Host 白名单要"全部本机地址"（少一个就等于把某个合法入口挡在门外）；
//	· 给用户看的访问地址要"手机真连得上的"（多一个就等于让用户逐个试）。
//
// 之前把它们混成一个函数，结果是"筛地址"顺手把白名单也筛了。
func TestLanLocalIPsKeepsAllForHostWhitelist(t *testing.T) {
	all := lanLocalIPs()
	if len(all) == 0 {
		t.Skip("本机没有非回环 IPv4，跳过")
	}
	// 白名单用的全量里允许出现链路本地（那是本机地址，只是不是手机能连的）
	found := map[string]bool{}
	for _, ip := range all {
		found[ip] = true
	}
	for _, ip := range lanReachableIPs() {
		if !found[ip] {
			t.Errorf("可达地址 %q 不在全量列表里 —— Host 白名单会把它挡在门外", ip)
		}
	}
}

// TestLanAddrURLsOnlyReachable 生成的访问网址必须全部来自可达子集。
func TestLanAddrURLsOnlyReachable(t *testing.T) {
	urls := lanAddrURLs(18766)
	if len(urls) == 0 {
		// 没有默认路由时允许为空（前端会如实提示"没检测到地址"），
		// 但绝不能返回 169.254 之类的不可达地址。
		return
	}
	for _, u := range urls {
		if !strings.Contains(u, ":18766/") {
			t.Errorf("访问网址 %q 的端口不对", u)
		}
		if strings.Contains(u, "169.254.") {
			t.Errorf("访问网址 %q 含链路本地地址，手机连不上", u)
		}
	}
}

// ---------- 5. 启动分派 ----------

// TestLanStartDispatchByKey 三个 key 必须对应三种动作。
//
// 这条测试是为一个"报错也看不出来"的 bug 补的：旧实现校验了 key 却**一律**调
// handleWebStart —— 手机上点「后端」/「全部启动」起来的都是前端，接口回"已受理"，
// 用户只看到后端一直没起来，很容易误判成后端本身有问题。
//
// 分派表本身没法从响应里验证（响应立刻回），所以只能在这里断言。
func TestLanStartDispatchByKey(t *testing.T) {
	oldWeb, oldBackend := lanStartWeb, lanStartBackend
	var calls []string
	var methods []string
	var bodies []string
	rec := func(name string) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, name)
			methods = append(methods, r.Method)
			// 后台任务用的是新建的请求，body 必须还没被读过。
			b, _ := io.ReadAll(r.Body)
			bodies = append(bodies, string(b))
			if r.Context().Err() != nil {
				t.Errorf("%s 收到的请求 context 已取消 —— handler 返回后它就不该再被使用", name)
			}
		}
	}
	lanStartWeb, lanStartBackend = rec("web"), rec("backend")
	t.Cleanup(func() { lanStartWeb, lanStartBackend = oldWeb, oldBackend })

	cases := []struct {
		key  string
		want []string
	}{
		{"web", []string{"web"}},
		{"backend", []string{"backend"}},
		// 先后端再前端：前端一起来就要连后端，反过来会先看到一段连接失败。
		{"all", []string{"backend", "web"}},
		// handler 已校验取值，这里只兜底：绝不能分派成"什么都不做"。
		{"", []string{"web"}},
	}
	for _, c := range cases {
		calls, methods, bodies = nil, nil, nil
		lanStartDispatch(c.key)
		if got := strings.Join(calls, ","); got != strings.Join(c.want, ",") {
			t.Errorf("key=%q 分派到 %q，期望 %q", c.key, got, strings.Join(c.want, ","))
			continue
		}
		for i := range calls {
			if methods[i] != http.MethodPost {
				t.Errorf("key=%q 传给 %s 的方法是 %s，桌面 handler 只接受 POST", c.key, calls[i], methods[i])
			}
			if bodies[i] != "{}" {
				t.Errorf("key=%q 传给 %s 的 body 是 %q，期望空对象 {}", c.key, calls[i], bodies[i])
			}
		}
	}
}

// TestLanStartPostRoutesByBodyKey 从 HTTP 这一层再钉一遍：手机发 {key:"backend"}，
// 真正被调起的必须是**后端**。
//
// 为什么光测分派表不够：真实 bug 出在 handler 的接线处（校验了 key，却一律调
// handleWebStart）。只在 lanStartDispatch 上断言，等于假设 handler 一定把 key
// 传对了 —— 而那正是当初错的地方。
func TestLanStartPostRoutesByBodyKey(t *testing.T) {
	const tok = "tok-route"
	lanTestSetup(t, &LanConfig{
		Enabled: boolp(true),
		Devices: []LanDevice{{ID: "dddddddd", Name: "测试手机", Hash: lanHash(tok),
			Created: time.Now().Unix(), Last: time.Now().Unix()}},
	})

	oldWeb, oldBackend := lanStartWeb, lanStartBackend
	t.Cleanup(func() { lanStartWeb, lanStartBackend = oldWeb, oldBackend })

	lanStartPostReq := func(key string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]string{"key": key})
		r := httptest.NewRequest(http.MethodPost, "/api/lan/start", strings.NewReader(string(b)))
		r.Host = "127.0.0.1:18766"
		r.AddCookie(&http.Cookie{Name: lanCookieName, Value: tok})
		rec := httptest.NewRecorder()
		lanStartPost(rec, r)
		return rec
	}

	for _, c := range []struct {
		key  string
		want []string
	}{
		{"backend", []string{"backend"}},
		{"all", []string{"backend", "web"}},
	} {
		got := make(chan string, 4)
		lanStartWeb = func(w http.ResponseWriter, r *http.Request) { got <- "web" }
		lanStartBackend = func(w http.ResponseWriter, r *http.Request) { got <- "backend" }

		rec := lanStartPostReq(c.key)
		if rec.Code != http.StatusOK {
			t.Fatalf("key=%q 应 200，得到 %d：%s", c.key, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"accepted":true`) {
			t.Fatalf("key=%q 的响应没说明「已受理」：%s", c.key, rec.Body.String())
		}

		var calls []string
		deadline := time.After(2 * time.Second)
		for len(calls) < len(c.want) {
			select {
			case s := <-got:
				calls = append(calls, s)
			case <-deadline:
				t.Fatalf("key=%q 只调起了 %v，期望 %v（分派丢了一步）", c.key, calls, c.want)
			}
		}
		if strings.Join(calls, ",") != strings.Join(c.want, ",") {
			t.Errorf("key=%q 调起了 %q，期望 %q", c.key, calls, c.want)
		}
	}

	// 非法 key：直接 400，且不许"顺手"起一个前端
	started := make(chan string, 1)
	lanStartWeb = func(w http.ResponseWriter, r *http.Request) { started <- "web" }
	if rec := lanStartPostReq("mine"); rec.Code != http.StatusBadRequest {
		t.Errorf("非法 key 应 400，得到 %d", rec.Code)
	}
	select {
	case s := <-started:
		t.Errorf("非法 key 却启动了一个服务：%s", s)
	case <-time.After(200 * time.Millisecond):
	}
}

// ---------- 6. 设备信任期 ----------

// TestLanDeviceTokenExpiresAfterTTL 30 天信任期必须由**服务端**执行。
//
// 只在 cookie 上设 MaxAge 是不够的：MaxAge 归浏览器管，服务端的设备表永不过期。
// 后果有两个，都很实在 ——
//   - 关掉手机访问不等于吊销：重新打开，老手机（甚至别人备份走的 cookie）直接又能进；
//   - 设备表只进不出，8 台上限会被早就没人用的条目占满，把新设备挤掉。
func TestLanDeviceTokenExpiresAfterTTL(t *testing.T) {
	now := time.Now().Unix()
	const tokFresh, tokOld, tokLegacy = "tok-fresh", "tok-old", "tok-legacy"
	lanTestSetup(t, &LanConfig{
		Enabled: boolp(true),
		Devices: []LanDevice{
			{ID: "aaaaaaaa", Name: "在用的手机", Hash: lanHash(tokFresh), Created: now - 3600, Last: now - 60},
			{ID: "bbbbbbbb", Name: "三个月前的手机", Hash: lanHash(tokOld), Created: now - lanDeviceTTL - 3600, Last: now - lanDeviceTTL - 3600},
			// 升级前写入的老条目没有 Last。不能因此把用户已配对的手机全踢掉 ——
			// 它们下一次访问会被 lanTouch 补上时间戳。
			{ID: "cccccccc", Name: "升级前的条目", Hash: lanHash(tokLegacy), Created: now - 86400},
		},
	})

	if _, ok := lanDeviceFor(tokFresh); !ok {
		t.Fatal("信任期内的设备必须还能进")
	}
	if _, ok := lanDeviceFor(tokLegacy); !ok {
		t.Fatal("Last=0 的老条目按不过期处理，否则升级会静默踢掉所有已配对手机")
	}
	if _, ok := lanDeviceFor(tokOld); ok {
		t.Fatal("超过信任期未使用的令牌必须失效（服务端判，不能只靠 cookie 的 MaxAge）")
	}

	// 设置页也不该把过期设备列成"已配对"：它已经连不上了。
	devsView, _ := lanConfigView(lanSnap())["devices"].([]LanDevice)
	if len(devsView) != 2 {
		t.Fatalf("设备列表应只显示 2 台仍有效的设备，得到 %d 台：%+v", len(devsView), devsView)
	}
	for _, d := range devsView {
		if d.ID == "bbbbbbbb" {
			t.Fatal("过期设备出现在设备列表里（用户会以为它还能用）")
		}
		if d.ID == "aaaaaaaa" && lanDeviceExpired(d, now) {
			t.Fatal("仍在信任期内的设备被误判成过期")
		}
	}

	// 配对时先清过期设备：它们已经连不上，却还占着 8 台上限的名额。
	lanMutate(func(l *LanConfig) {
		l.Devices = nil
		for _, name := range []string{"live-1", "live-2", "live-3", "live-4", "live-5", "dead-1", "dead-2", "dead-3"} {
			d := LanDevice{ID: lanHash(name + "-id")[:8], Name: name, Hash: lanHash(name), Created: now - 600, Last: now - 600}
			if strings.HasPrefix(name, "dead-") {
				d.Created = now - lanDeviceTTL - 600
				d.Last = now - lanDeviceTTL - 600
			}
			l.Devices = append(l.Devices, d)
		}
	})
	rec := lanPairReq("482913", "192.168.1.40")
	if rec.Code != http.StatusOK {
		t.Fatalf("配对失败：%d %s", rec.Code, rec.Body.String())
	}
	devs := lanSnap().Devices
	// 8 台里 5 台有效 + 新配 1 台 = 6。若没清过期设备，走的是"淘汰最久未用"，
	// 结果仍会是 8 台（过期的还在表里）。
	if len(devs) != 6 {
		t.Fatalf("配对前应清掉 3 台过期设备（期望 6 台），得到 %d 台", len(devs))
	}
	for _, d := range devs {
		if lanDeviceExpired(d, now) {
			t.Errorf("过期设备 %s（%s）还留在设备表里", d.ID, d.Name)
		}
	}
	// 新配对的令牌应立即可用
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != lanCookieName || cookies[0].Value == "" {
		t.Fatal("配对成功必须下发设备 cookie")
	}
	if _, ok := lanDeviceFor(cookies[0].Value); !ok {
		t.Fatal("刚配对的设备应立即可用")
	}
}

// ---------- 7. 端口回显 ----------

// TestLanConfigViewReportsPendingRestart 配置端口与实际监听端口不一致时，界面必须能知道。
//
// 改端口不热切换（会断掉正在用的手机），于是有一段时间"配置端口"和"真正在听的端口"
// 不同。旧实现把配置端口当成访问地址去生成二维码，状态却显示"已开启" ——
// 用户对着一个没人监听的端口反复扫码，只会得到"手机连不上电脑"。
func TestLanConfigViewReportsPendingRestart(t *testing.T) {
	oldLive := lanListeningPort()
	t.Cleanup(func() { lanPortLive.Store(int64(oldLive)) })
	lanTestSetup(t, &LanConfig{Enabled: boolp(true), Port: intp(18888)})
	t.Cleanup(func() { lanPortLive.Store(int64(oldLive)) }) // 恢复 cfg.Lan 之后别再留着假端口

	// 1) 正在监听老端口 18766，配置已改成 18888
	lanPortLive.Store(18766)
	v := lanConfigView(lanSnap())
	if v["port"] != 18888 {
		t.Errorf("port 应是配置端口 18888，得到 %v", v["port"])
	}
	if v["listen_port"] != 18766 {
		t.Errorf("listen_port 应是实际监听端口 18766，得到 %v", v["listen_port"])
	}
	if v["pending_restart"] != true {
		t.Errorf("配置端口与监听端口不同时必须标记 pending_restart，得到 %v", v["pending_restart"])
	}
	for _, u := range urlsOf(t, v["addrs"]) {
		if !strings.Contains(u, ":18766/") {
			t.Errorf("访问地址 %q 用了没人监听的配置端口", u)
		}
	}

	// 2) 重启后端口跟上 → 不再提示
	lanPortLive.Store(18888)
	v = lanConfigView(lanSnap())
	if v["listen_port"] != 18888 {
		t.Errorf("重启后 listen_port 应为 18888，得到 %v", v["listen_port"])
	}
	if v["pending_restart"] != false {
		t.Errorf("端口一致时不该提示重启，得到 %v", v["pending_restart"])
	}

	// 3) 没在监听（开关关闭）→ listen_port=0，也不提示重启
	lanPortLive.Store(0)
	v = lanConfigView(lanSnap())
	if v["listen_port"] != 0 {
		t.Errorf("未监听时 listen_port 应为 0，得到 %v", v["listen_port"])
	}
	if v["pending_restart"] != false {
		t.Errorf("未监听时不该提示重启，得到 %v", v["pending_restart"])
	}
}

// urlsOf 取出 lanConfigView 里的 addrs（类型是 []string，包在 any 里）。
func urlsOf(t *testing.T, v any) []string {
	t.Helper()
	urls, ok := v.([]string)
	if !ok {
		t.Fatalf("addrs 不是 []string：%T", v)
	}
	return urls
}

// parseIPv4 极简解析，测试用（解析失败返回 nil）。
func parseIPv4(s string) []int {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return nil
	}
	out := make([]int, 4)
	for i, p := range parts {
		n := 0
		if p == "" {
			return nil
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return nil
			}
			n = n*10 + int(c-'0')
			if n > 255 {
				return nil
			}
		}
		out[i] = n
	}
	return out
}
