// lanaccess.go —— 手机访问：局域网只读控制台 + AI 对话
//
// ## 为什么是"另开一个端口"，而不是把主控制台绑到 0.0.0.0
//
// 主控制台上注册着 90 多个端点：改配置、跑命令、开终端、导数据库、杀进程。
// 如果只是把它绑到 0.0.0.0 再按来源分流，那么**分流逻辑的任何一处疏漏都等于全权限泄漏**，
// 而这类疏漏在 90 个路由里几乎不可能靠人工审干净。
//
// 所以这里另起一个 net.Listener + 独立 ServeMux，上面只有 13 条路由
// （手机页 + 12 个 API，见 lanMux）。配置页、审计、导数据库那些 handler
// 在这个 mux 上根本不存在 —— 请求进来是 404，不是"被判断成不允许"。
// **能写错的表面积从 90 多个路由缩到 12 个 API。**
//
// 主控制台继续只听 127.0.0.1，一行没动。
//
// ## 三道闸（缺一不可）
//
//  1. 绑定：独立端口，只在开关打开时监听（关掉就 Close，不是"监听但拒绝"）。
//  2. Host 白名单：只认 回环 / 本机 LAN IP / 计算机名。防 DNS rebinding ——
//     否则攻击者把 evil.com 解析到你的局域网 IP，受害者一访问，Origin 与 Host 同源，
//     所有"同源"判断全过，而首页就在同一个域下。
//  3. 设备令牌：配对成功才发 cookie（HttpOnly，前端 JS 读不到），
//     磁盘只存 sha256，config.json 被看到也换不回令牌。
//
// ## 配对码 vs 密码
//
// 配对码是**进程级**的：每次启动随机 6 位，只显示在托盘里，重启即换。
// 密码是**用户设的**：bcrypt 存 hash，长期有效，忘了码时兜底。
// 手机端只有一个输入框，两种都认 —— 用户不需要理解这是两套机制。
//
// 6 位数字只有 100 万种组合，单靠它挡不住穷举，所以失败限速是**这个方案的一部分**，
// 不是可选项：同一 IP 5 次失败 → 锁 10 分钟。
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	lanDefaultPort = 18766
	lanCookieName  = "ek_lan"
	lanPairDigits  = 6
	lanMaxFail     = 5
	lanFailWindow  = 10 * time.Minute
	lanCoolDown    = 10 * time.Minute
	lanMaxDevices  = 8
	lanTokenBytes  = 32
	lanCookieDays  = 30
)

// lanDeviceTTL 设备多久没用就失效（与 cookie 的 MaxAge 取同一个数）。
//
// 为什么服务端也得判：30 天原来只是浏览器的 MaxAge，服务端**永不过期**。
// 后果是关掉开关并不会吊销已配对的手机（重新打开，老手机直接又能进），
// cookie 被备份/拷走时也没有任何时限。信任必须两端同时到期。
const lanDeviceTTL = int64(lanCookieDays) * 24 * 3600

// lanDeviceExpired 设备是否已超过信任期。
//
// Last <= 0 的老条目按"不过期"处理：升级时不能把用户已经配好的手机全踢掉，
// 它们下一次访问会被 lanTouch 补上时间戳。
func lanDeviceExpired(d LanDevice, now int64) bool {
	if d.Last <= 0 {
		return false
	}
	return now-d.Last > lanDeviceTTL
}

// ===== 配置段（config.json 的 "lan" 段）=====
//
// 与 dev/update/notify 同一范式：整段是指针，老配置里没有该段时为 nil。
// 字段也用指针语义的写法（Enabled/Port 为 *T）以免"保存时要区分没传和传了 0"。

// LanDevice 已配对的设备。只存令牌的 sha256 —— 明文令牌只在配对那一次响应里出现。
type LanDevice struct {
	ID      string `json:"id"`      // 8 位十六进制，设置页展示用
	Name    string `json:"name"`    // UA 摘要（"iPhone · Safari"）
	Hash    string `json:"hash"`    // sha256(令牌)
	Created int64  `json:"created"` // unix 秒
	Last    int64  `json:"last"`
}

// LanConfig 手机访问配置。
type LanConfig struct {
	Enabled  *bool       `json:"enabled,omitempty"`
	Port     *int        `json:"port,omitempty"`
	PassHash string      `json:"pass_hash,omitempty"` // bcrypt；空 = 未设密码，只认配对码
	Devices  []LanDevice `json:"devices,omitempty"`

	// v3.6.2：手机能看到/摸到哪些本地能力。
	// 三项都用 *bool：老配置里没有这三个字段，必须和"用户明确关掉"区分开
	// （缺省值见 localdev.go 的 lanAllowXxxOn）。
	AllowShot *bool `json:"allow_shot,omitempty"` // 截屏（只读，默认开）
	AllowClip *bool `json:"allow_clip,omitempty"` // 剪贴板（读写，默认关）
	AllowTerm *bool `json:"allow_term,omitempty"` // 本地终端（执行，默认关）

	// 配对时的第二个因子。RequireOTP 打开才启用；OTPSecret 是 base32 密钥。
	RequireOTP *bool  `json:"require_otp,omitempty"`
	OTPSecret  string `json:"otp_secret,omitempty"`
}

func (c LanConfig) on() bool { return c.Enabled != nil && *c.Enabled }

func (c LanConfig) port() int {
	if c.Port != nil && *c.Port >= 1024 && *c.Port <= 65535 {
		return *c.Port
	}
	return lanDefaultPort
}

// lanSnap 取一份副本。调用方一律用副本：持 cfgMu 时写日志是锁序反转（见 MEMORY 第 1 条）。
func lanSnap() LanConfig {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	if cfg.Lan == nil {
		return LanConfig{}
	}
	c := *cfg.Lan
	c.Devices = append([]LanDevice(nil), cfg.Lan.Devices...)
	return c
}

// lanPersist 落盘钩子。默认就是存 config.json；
// 单测替换成空操作 —— 否则跑一次测试就会把真实的 lan 段写进工作区配置。
var lanPersist = func(c Config) { saveExternalConfig(c) }

// lanMutate 改配置段并立即落盘，返回改完的副本。
func lanMutate(fn func(*LanConfig)) LanConfig {
	cfgMu.Lock()
	if cfg.Lan == nil {
		cfg.Lan = &LanConfig{}
	}
	fn(cfg.Lan)
	saved := *cfg.Lan
	saved.Devices = append([]LanDevice(nil), cfg.Lan.Devices...)
	whole := cfg
	cfgMu.Unlock()
	lanPersist(whole)
	return saved
}

// ===== 配对码（进程级）=====

var lanPairCode atomic.Value // string

func lanNewPairCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		// 取不到随机数时宁可给出一个不可能的码，也不要用时间/计数器凑一个可预测的
		return "------"
	}
	return fmt.Sprintf("%06d", n.Int64())
}

func lanPairCodeCur() string {
	v, _ := lanPairCode.Load().(string)
	return v
}

// lanRotatePairCode 换一个配对码（设置页「换一个码」按钮）。
func lanRotatePairCode() string {
	c := lanNewPairCode()
	lanPairCode.Store(c)
	return c
}

func lanNewToken() string {
	b := make([]byte, lanTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

func lanHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func lanIsDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ===== 设备令牌 =====

func lanTokenFrom(r *http.Request) string {
	if c, err := r.Cookie(lanCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	// 备选通道：给 curl / 脚本用（浏览器走 cookie，JS 读不到它）
	return strings.TrimSpace(r.Header.Get("X-EK-Lan"))
}

// lanDeviceFor 用**定长比较**查设备：令牌是指针式的长期凭证，
// 逐字节短路比较会把"前几位正确"暴露成时间差。
func lanDeviceFor(token string) (LanDevice, bool) {
	if token == "" {
		return LanDevice{}, false
	}
	want := []byte(lanHash(token))
	c := lanSnap()
	now := time.Now().Unix()
	for _, d := range c.Devices {
		if subtle.ConstantTimeCompare([]byte(d.Hash), want) == 1 {
			if lanDeviceExpired(d, now) {
				return LanDevice{}, false // 超过信任期：让手机重新配对，而不是静默续期
			}
			return d, true
		}
	}
	return LanDevice{}, false
}

// ===== Host 白名单（防 DNS rebinding）=====

var lanIPCache struct {
	mu sync.Mutex
	at time.Time
	v  []string
}

// lanReachCache 缓存"手机真连得上"的地址。与 lanIPCache 分开：
// 前者是 Host 白名单用的全量，后者是给用户看的可达子集，口径不同不能混用。
var lanReachCache struct {
	mu sync.Mutex
	at time.Time
	v  []string
}

// lanLocalIPs 返回本机所有非回环 IPv4。
//
// **仅供 Host 白名单用**（lanHostAllowed）——那个场景要的是"全部本机地址"，
// 少一个就等于把某个合法入口挡在门外。
//
// 注意：这里**不能**拿来生成给用户看的访问地址。实测本机有多张网卡：
// VMware 虚拟网卡（192.168.110.1 / 192.168.128.1）、若干 169.254.x.x 链路本地地址、
// 以及真正连着路由器的 WLAN（192.168.1.12）。手机只跟后者在同一个 WiFi 里，
// 前两类它根本路由不到 —— 一次性列出来，用户只能一个个试（实测"只有最后一个能用"）。
// 给用户看的地址走 lanReachableIPs()。
func lanLocalIPs() []string {
	lanIPCache.mu.Lock()
	defer lanIPCache.mu.Unlock()
	if lanIPCache.v != nil && time.Since(lanIPCache.at) < 30*time.Second {
		return lanIPCache.v
	}
	var out []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return lanIPCache.v
	}
	for _, in := range ifaces {
		addrs, err := in.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip4 := ipn.IP.To4(); ip4 != nil && !ip4.IsLoopback() {
				out = append(out, ip4.String())
			}
		}
	}
	lanIPCache.v, lanIPCache.at = out, time.Now()
	return out
}

// lanReachableIPs 返回**手机真的连得上**的局域网地址。
//
// ## 为什么不能直接列全部网卡地址
//
// 本机实测有三类 IPv4 同时存在：
//
//	192.168.1.12    ← WLAN，手机和它在同一个 WiFi 里，唯一可达
//	192.168.110.1   ← VMware 虚拟网卡，手机路由不到
//	192.168.128.1   ← 同上
//	169.254.x.x     ← 链路本地（APIPA），手机更连不上
//
// 一次性全列出来，用户只能逐个试 —— 实测反馈是"只有最后一个能用"。
//
// ## 判定口径：默认路由的出口网卡
//
// 手机能连上的前提是"手机和电脑在同一网段"，而最可能与手机同网段的网卡，
// 就是**默认路由所走的那一张**（家里路由器的 LAN 口通常就是默认网关那一侧）。
//
// 怎么找到它：不发任何包地"问"操作系统 —— 用 UDP connect 到一个公网地址，
// 然后读本地端地址。connect 只查路由表、不发送数据，所以：
//
//	· 不产生任何外连流量（符合"EnvKit 只有天气一个功能出网"的边界）；
//	· 本地端地址就是默认路由出口网卡上的地址。
//
// 拿不到（完全没网）时退回"按网卡名排除虚拟网卡"的启发式，并如实标注来源。
func lanReachableIPs() []string {
	lanReachCache.mu.Lock()
	defer lanReachCache.mu.Unlock()
	if lanReachCache.v != nil && time.Since(lanReachCache.at) < 30*time.Second {
		return lanReachCache.v
	}
	var out []string
	if ip := lanDefaultRouteIP(); ip != "" {
		out = []string{ip}
	} else {
		out = lanIPsExcludingVirtual()
	}
	lanReachCache.v, lanReachCache.at = out, time.Now()
	return out
}

// lanDefaultRouteIP 问操作系统"走默认路由时本机用什么 IP"。
//
// 用 UDP connect（不是 Dial 到 TCP）：UDP 的 connect 只做路由查找，
// 不发包、不握手，因此不会产生任何外连流量，也不会有超时等待。
func lanDefaultRouteIP() string {
	// 目标地址随便挑一个公网 IP —— 只用于查路由表，不会真发包。
	conn, err := net.DialTimeout("udp", "8.8.8.8:53", 800*time.Millisecond)
	if err != nil {
		return ""
	}
	defer conn.Close()
	ua, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || ua.IP == nil {
		return ""
	}
	ip4 := ua.IP.To4()
	if ip4 == nil || ip4.IsLoopback() || ip4.IsUnspecified() {
		return ""
	}
	// 169.254.x.x 是链路本地地址，手机同样连不上 —— 别把它当成出口地址。
	if ip4.IsLinkLocalUnicast() {
		return ""
	}
	return ip4.String()
}

// lanAddrCand 一张网卡上的一个 IPv4 候选（网卡名 + 地址）。
type lanAddrCand struct {
	Iface string
	IP    string
}

// lanFilterReachable 按"手机连得上"筛掉候选地址。**纯函数**（不碰系统状态）。
//
// 为什么抽出来：过滤规则本身是最容易写错又最不容易被察觉的部分
// （少一条过滤 = 用户多试一个地址；多一条过滤 = 用户连不上）。
// 抽成纯函数才能用**构造出来的**候选集去测 ——
// 只用本机真实网卡做测试的话，在没有 169.254 网卡的机器上这条断言就是空转的
// （变异验证时真的空转过一次：改坏过滤条件，测试照样绿）。
func lanFilterReachable(cands []lanAddrCand) []string {
	var out []string
	for _, c := range cands {
		ip4 := net.ParseIP(c.IP).To4()
		if ip4 == nil {
			continue // 不是合法 IPv4
		}
		if ip4.IsLoopback() {
			continue // 127.x —— 手机访问的是本机
		}
		if ip4.IsLinkLocalUnicast() {
			continue // 169.254.x —— 链路本地，手机路由不到
		}
		if ip4.IsUnspecified() {
			continue // 0.x —— 未指定
		}
		if lanVirtualIface(c.Iface) {
			continue // 虚拟网卡：手机路由不到
		}
		out = append(out, ip4.String())
	}
	return out
}

// lanVirtualIface 网卡名是否像虚拟网卡。
//
// 启发式，不保证准（网卡名是各厂商自定义的），所以只在主路径失效时用。
func lanVirtualIface(name string) bool {
	virtualHints := []string{"vmware", "virtualbox", "vethernet", "hyper-v", "loopback", "tailscale", "zerotier", "docker", "wsl", "bluetooth"}
	n := strings.ToLower(name)
	for _, h := range virtualHints {
		if strings.Contains(n, h) {
			return true
		}
	}
	return false
}

// lanIPsExcludingVirtual 兜底：没有默认路由时，按网卡名排除明显的虚拟网卡。
func lanIPsExcludingVirtual() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var cands []lanAddrCand
	for _, in := range ifaces {
		if in.Flags&net.FlagUp == 0 || in.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := in.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip4 := ipn.IP.To4(); ip4 != nil {
				cands = append(cands, lanAddrCand{Iface: in.Name, IP: ip4.String()})
			}
		}
	}
	return lanFilterReachable(cands)
}

// lanAddrURLs 生成给用户看的访问地址列表（只含真正可达的地址）。
func lanAddrURLs(port int) []string {
	var out []string
	for _, ip := range lanReachableIPs() {
		out = append(out, fmt.Sprintf("http://%s:%d/", ip, port))
	}
	return out
}

// lanHostAllowed 只认"这台机器的名字"：回环 / 本机网卡 IP / 计算机名。
//
// 不用"Origin == Host"这种相对判断 —— DNS rebinding 下它们天然相等。
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

//go:embed web/mobile.html
var lanPageHTML string

func lanWriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func lanPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if !lanHostAllowed(r) {
		http.Error(w, "请用本机的局域网地址访问（例如 http://192.168.x.x:端口/）", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, strings.ReplaceAll(lanPageHTML, "__LAN_VERSION__", appVersion))
}

// lanPairPost 配对：接受 6 位配对码 or 用户设的密码，成功则下发设备令牌。
func lanPairPost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !lanHostAllowed(r) {
		lanWriteJSON(w, http.StatusForbidden, map[string]any{"ok": false, "msg": "非法的访问地址"})
		return
	}
	if !lanSnap().on() {
		lanWriteJSON(w, http.StatusForbidden, map[string]any{"ok": false, "msg": "这台电脑没有开启手机访问"})
		return
	}
	ip := lanClientIP(r)
	if blocked, wait := lanRateBlocked(ip); blocked {
		lanWriteJSON(w, http.StatusTooManyRequests, map[string]any{
			"ok": false,
			"msg": fmt.Sprintf("尝试次数过多，请 %d 分钟后再试",
				int(wait.Minutes())+1),
		})
		return
	}

	var body struct {
		Code string `json:"code"`
		Totp string `json:"totp"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	code := strings.TrimSpace(body.Code)

	c := lanSnap()
	ok := false
	// 先比配对码：定长比较，且只在长度正好 6 位数字时比
	if len(code) == lanPairDigits && lanIsDigits(code) {
		if subtle.ConstantTimeCompare([]byte(code), []byte(lanPairCodeCur())) == 1 {
			ok = true
		}
	}
	// 再比密码（bcrypt 自带恒定时间比较）
	if !ok && c.PassHash != "" && code != "" {
		if bcrypt.CompareHashAndPassword([]byte(c.PassHash), []byte(code)) == nil {
			ok = true
		}
	}
	if !ok {
		lanRateFail(ip)
		auditNow(actUser, "lan_pair", ip, "", resDenied, "配对码或密码不正确")
		lanWriteJSON(w, http.StatusForbidden, map[string]any{"ok": false, "msg": "配对码或密码不正确"})
		return
	}
	// 第二个因子在**配对码通过之后**才判：配对码错的时候不该告诉对方
	// "这台机器还要求验证码"——那等于免费泄露一条策略信息。
	// 同理，need_otp 只在码对的情况下才会出现，手机端据此展开输入框。
	if lanBoolOr(c.RequireOTP, false) && c.OTPSecret != "" {
		if strings.TrimSpace(body.Totp) == "" {
			lanWriteJSON(w, http.StatusForbidden, map[string]any{
				"ok": false, "need_otp": true,
				"msg": "这台电脑开了二次验证，请输入口令器里的 6 位验证码",
			})
			return
		}
		passed, replay := lanOTPCode(c.OTPSecret, body.Totp)
		if !passed {
			msg := "验证码不正确"
			if replay {
				msg = "这个验证码刚用过，等口令器刷新出下一个再试"
			}
			lanRateFail(ip)
			auditNow(actUser, "lan_pair", ip, "", resDenied, "二次验证未通过："+msg)
			lanWriteJSON(w, http.StatusForbidden, map[string]any{"ok": false, "need_otp": true, "msg": msg})
			return
		}
	}
	lanRatePass(ip)

	tok := lanNewToken()
	if tok == "" {
		lanWriteJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": "生成设备令牌失败"})
		return
	}
	dev := LanDevice{
		ID:      lanHash(tok + "id")[:8],
		Name:    lanDeviceName(r.UserAgent()),
		Hash:    lanHash(tok),
		Created: time.Now().Unix(),
		Last:    time.Now().Unix(),
	}
	lanMutate(func(l *LanConfig) {
		// 先清掉过期的：它们连不上（lanDeviceFor 会拒），却还占着 8 个名额，
		// 继续摆在「已配对的设备」里也只会误导用户"这台还在用"。
		now := time.Now().Unix()
		keep := l.Devices[:0]
		for _, d := range l.Devices {
			if !lanDeviceExpired(d, now) {
				keep = append(keep, d)
			}
		}
		l.Devices = keep
		// 上限内先淘汰最久未用的：8 台足够一个家庭/小团队，
		// 再多说明该去看看是不是别人的设备也配对上了。
		if len(l.Devices) >= lanMaxDevices {
			oldest := 0
			for i := 1; i < len(l.Devices); i++ {
				if l.Devices[i].Last < l.Devices[oldest].Last {
					oldest = i
				}
			}
			l.Devices = append(l.Devices[:oldest], l.Devices[oldest+1:]...)
		}
		l.Devices = append(l.Devices, dev)
	})

	http.SetCookie(w, &http.Cookie{
		Name:     lanCookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true, // 前端 JS 读不到：XSS 也偷不走
		SameSite: http.SameSiteStrictMode,
		MaxAge:   lanCookieDays * 24 * 3600,
	})
	info(scSys, "手机访问", "新设备已配对：%s（来自 %s，当前共 %d 台）", dev.Name, ip, len(lanSnap().Devices))
	auditNow(actUser, "lan_pair", ip, dev.ID, resOK, "手机设备已配对："+dev.Name)
	lanWriteJSON(w, http.StatusOK, map[string]any{"ok": true, "device": dev.Name})
}

// lanDeviceName 把 UA 压成一句人话（"iPhone · Safari"），设置页里能认出来是哪台。
func lanDeviceName(ua string) string {
	os, br := "未知设备", "浏览器"
	switch {
	case strings.Contains(ua, "iPhone"):
		os = "iPhone"
	case strings.Contains(ua, "iPad"):
		os = "iPad"
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "Macintosh"):
		os = "Mac"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}
	switch {
	case strings.Contains(ua, "MicroMessenger"):
		br = "微信"
	case strings.Contains(ua, "Edg/"):
		br = "Edge"
	case strings.Contains(ua, "CriOS") || strings.Contains(ua, "Chrome/"):
		br = "Chrome"
	case strings.Contains(ua, "Firefox/"):
		br = "Firefox"
	case strings.Contains(ua, "Safari/"):
		br = "Safari"
	}
	return os + " · " + br
}

// lanUnpairPost 让这台手机注销（清 cookie + 从设备表移除）。
func lanUnpairPost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	tok := lanTokenFrom(r)
	if tok != "" {
		th := lanHash(tok)
		lanMutate(func(l *LanConfig) {
			keep := l.Devices[:0]
			for _, d := range l.Devices {
				if d.Hash != th {
					keep = append(keep, d)
				}
			}
			l.Devices = keep
		})
	}
	http.SetCookie(w, &http.Cookie{Name: lanCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	lanWriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ===== 鉴权 =====

// lanAuth 校验设备令牌，并把 Last 刷新（同时写回，设置页能看到"最近使用"）。
func lanAuth(w http.ResponseWriter, r *http.Request) (LanDevice, bool) {
	if !lanHostAllowed(r) {
		lanWriteJSON(w, http.StatusForbidden, map[string]any{"ok": false, "msg": "非法的访问地址"})
		return LanDevice{}, false
	}
	dev, ok := lanDeviceFor(lanTokenFrom(r))
	if !ok {
		lanWriteJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "msg": "未配对"})
		return LanDevice{}, false
	}
	lanTouch(dev.ID)
	return dev, true
}

// lanTouch 刷新最近使用时间。用节流：30 秒内不重复写盘（手机上 2s 轮询状态会很吵）。
var lanTouchMu sync.Mutex

func lanTouch(id string) {
	lanTouchMu.Lock()
	defer lanTouchMu.Unlock()
	now := time.Now().Unix()
	c := lanSnap()
	for _, d := range c.Devices {
		if d.ID == id {
			if now-d.Last < 30 {
				return
			}
			break
		}
	}
	lanMutate(func(l *LanConfig) {
		for i := range l.Devices {
			if l.Devices[i].ID == id {
				l.Devices[i].Last = now
			}
		}
	})
}

// ===== 只读状态 =====

func lanStateGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := lanAuth(w, r); !ok {
		return
	}
	// v3.6.2：状态一律取自 runtimeService（唯一状态源），**不再自己查 progState**。
	//
	// 旧实现按 {"web","backend"} 去查 progState，可 progState 里的键是
	// "web-start"/"backend-start"（多一个 -start 后缀，是"启动任务"而不是"服务"），
	// 于是 state[kv.key] 永远是空 —— 手机端**永远显示未启动**，用户说"点了没反应"。
	//
	// 更深一层的问题是口径：桌面端显示的状态由 runtimeService 算出五态
	// （starting/running/degraded/failed/stopped），手机端自己另算一套，
	// 两边迟早对不上。手机端拿到的必须是同一个口径。
	type svc struct {
		Key      string `json:"key"`
		Name     string `json:"name"`
		Phase    string `json:"phase"`
		Running  bool   `json:"running"`
		Verified bool   `json:"verified"`
		Msg      string `json:"msg"`
	}
	out := struct {
		OK       bool     `json:"ok"`
		Services []svc    `json:"services"`
		Stats    any      `json:"stats"`
		Version  string   `json:"version"`
		Logs     []string `json:"logs"`
		Port     int      `json:"port"`
		// v3.6.2：这台手机现在能用哪些本地能力。
		// 页面据此显示/隐藏整块卡片 —— 不给"点了才告诉你没开"的体验。
		Caps map[string]bool `json:"caps"`
	}{OK: true, Version: appVersion, Logs: []string{}}

	// 每次查状态都重算而不缓存：开关可能刚在电脑上被改掉，
	// 手机这边 3 秒轮询一次，改完一轮就能看到。
	if c := lanSnap(); c.on() {
		out.Caps = map[string]bool{
			"shot": lanAllowShotOn(c),
			"clip": lanAllowClipOn(c),
			"term": lanAllowTermOn(c) && conptyAvailable(),
			// 链端终端与本地终端同一个开关；额外要求链端 SSH 已配置，
			// 没配就不显示入口 —— 摆一个点进去必然报错的按钮不算"功能"。
			"ssh": lanAllowTermOn(c) && strings.TrimSpace(chainCfg().SSHHost) != "",
		}
	}

	for _, kv := range []struct{ key, name string }{{"web", "前端"}, {"backend", "后端"}} {
		st := runtimeService(kv.key)
		msg := st.LastVerify
		if msg == "" {
			msg = st.LastErr
		}
		out.Services = append(out.Services, svc{
			Key: kv.key, Name: kv.name,
			Phase: string(st.Phase), Running: st.Running, Verified: st.Verified, Msg: msg,
		})
	}
	out.Stats = procStatsSnapshotForLan()
	out.Port = listenPortCur
	lanWriteJSON(w, http.StatusOK, out)
}

// lanSSHWS 手机连链端 Linux 的 SSH 终端。
//
// ## 为什么和本地终端共用一个开关
//
// 用户会直觉认为"只开链端终端"比"开本机终端"安全 —— 事实相反：
// 链端那台机器上通常有 sudo，而手机一旦能登上去，本机这边的所有闸
// （写工具要确认、破坏性操作 428、AI 只读）全都不再是障碍。
// 所以两者是同一档权限，由一个开关统管，不给"看起来更安全"的错觉。
//
// 与桌面的 handleChainTermWS 走**同一份实现**（chainTermWS），
// 只换 upgrader：桌面那份只认回环 Origin，手机 Origin 是局域网地址。
func lanSSHWS(w http.ResponseWriter, r *http.Request) {
	dev, ok := lanFeature(w, r, lanAllowTermOn(lanSnap()), "终端")
	if !ok {
		return
	}
	if c := chainCfg(); strings.TrimSpace(c.SSHHost) == "" {
		http.Error(w, "链端 SSH 未配置：请先在电脑的「程序配置 → 链端」里填写主机与账号",
			http.StatusBadRequest)
		return
	}
	info(scSys, "手机访问", "%s 打开链端终端（%s）", dev.Name, chainCfg().SSHHost)
	auditNow(actUser, "lan_ssh", "chain", dev.ID, resOK, "手机打开链端终端："+dev.Name)
	chainTermWS(w, r, &lanWSUpgrader)
}

// lanLogsGet 手机端实时日志（只读）。
//
// 走 aiGetLogs 而不是另写一份切片：日志的"最近 N 行 + scope 前缀"只有一种口径，
// 两处各自实现迟早对不上（手机看到的行和 AI 看到的行不是同一批，排查时会互相打脸）。
func lanLogsGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := lanAuth(w, r); !ok {
		return
	}
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	switch scope {
	case "", "all", scInstall, scConfig, scStart, scChain, scSys, scEval:
	default:
		lanWriteJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "未知日志分区"})
		return
	}
	n := 200
	if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil {
		n = v
	}
	if n < 20 {
		n = 20
	}
	if n > 500 {
		n = 500
	}
	lanWriteJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"scope": scope,
		"lines": aiGetLogs(scope, n),
	})
}

// lanBriefGet 手机端的"统计 + 最近一份晨报"。
//
// 只读**已落盘**的报告，不现场生成：生成会写文件、统计磁盘、还可能触发轮转，
// 一个"看一眼"的请求不该有副作用。没有报告就如实说没有 —— 现编一份
// 会让用户以为系统每天早上都在出报告，其实只是他刚点了一下。
func lanBriefGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := lanAuth(w, r); !ok {
		return
	}
	out := map[string]any{"ok": true}
	// 统计：与桌面晨报同源（briefWorkStats 只读审计与轨迹），不自己另算
	// 否则"手机上的数"和"报告里的数"会不一致。
	wk := briefWorkStats(time.Now())
	out["stats"] = map[string]any{
		"ops":         wk.Ops,
		"ok":          wk.OK,
		"fail":        wk.Fail,
		"denied":      wk.Denied,
		"started":     wk.Started,
		"unverified":  wk.Unverified,
		"verify":      wk.VerifyCount,
		"verify_ok":   wk.VerifyOK,
		"top_actions": wk.Actions,
	}
	dir := briefingDirFn()
	name, at, text := lanBriefLatest(dir)
	out["report"] = name
	out["report_at"] = at
	out["text"] = text
	lanWriteJSON(w, http.StatusOK, out)
}

// lanBriefLatest 取目录里最新的一份晨报（按文件名 = 按时间倒序）。
// 返回文件名、生成时间（文件名里的 2006-01-02-1504）与正文。
func lanBriefLatest(dir string) (string, string, string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", ""
	}
	var best string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".md") || len(n) != len("2006-01-02-1504.md") {
			continue
		}
		if n > best {
			best = n
		}
	}
	if best == "" {
		return "", "", ""
	}
	b, err := os.ReadFile(filepath.Join(dir, best))
	if err != nil {
		return "", "", ""
	}
	text := string(b)
	// 手机屏幕上没人会滚三万字：截断并明说截断，比让页面卡住好。
	const lanBriefMax = 40000
	if len(text) > lanBriefMax {
		text = text[:lanBriefMax] + "\n\n（报告过长，已在手机端截断；完整内容见电脑上的「报告」页）"
	}
	at := strings.TrimSuffix(best, ".md")
	if ts, err := time.ParseInLocation("2006-01-02-1504", at, time.Local); err == nil {
		at = ts.Format("2006-01-02 15:04")
	}
	return best, at, text
}

// lanStartPost 启动前端 / 后端 / 两个都起（由 key 决定）。
//
// ## 复用桌面 handler，但不阻塞手机
//
// 复验必须走桌面的那一条路（handleWebStart / handleBackendStart 内部含
// "启动后当场复验"，自己拼一套必然会漏掉复验，那会让 ever_verified 变成假判据
// —— 这个坑 program.go 里记过）。
//
// 但**不能同步等它跑完**：handleWebStart 会等就绪复验（前端最长 45 秒），
// 手机浏览器那边的请求会一直挂着 —— 用户看到的是"点了没反应"，
// 而且有些手机浏览器/代理会直接超时掐掉请求。
//
// 所以：**动作照做，响应立刻回**。手机拿到"已受理"后靠 /api/lan/state 轮询看进展，
// 页面上显示"启动中，请耐心等待…"，直到服务端给出结论。
func lanStartPost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	dev, ok := lanAuth(w, r)
	if !ok {
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body)
	key := strings.TrimSpace(body.Key)
	if key != "web" && key != "backend" && key != "all" {
		lanWriteJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "key 只能是 web / backend / all"})
		return
	}
	info(scSys, "手机访问", "%s 从手机发起启动：%s", dev.Name, key)
	auditNow(actUser, "lan_start", key, dev.ID, resOK, "来自手机："+dev.Name)

	// 后台执行桌面那套逻辑，输出丢弃（桌面的响应是写给浏览器看的，手机只看 /state）。
	go lanStartDispatch(key)
	lanWriteJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"accepted": true,
		"msg":      "已受理，正在启动（进度看下面的服务状态）",
	})
}

// lanStart 分派表。抽成变量只为可测：分派错时页面照样回"已受理"，
// 用户只会看到某个服务一直"未启动"，唯一能提前抓到的地方是断言这张表
// （见 TestLanStartDispatchByKey）。
var (
	lanStartWeb     = handleWebStart
	lanStartBackend = handleBackendStart
)

// lanStartDispatch 按 key 起对应的服务。
//
// **旧实现校验了 key 却一律调 handleWebStart** —— 手机上点「后端」和「全部启动」
// 起来的都是前端：接口回"已受理"，后端一直显示未启动，看起来像服务本身的问题。
// 三个取值必须对应三种动作，这不是"多一个分支"，是这条 API 的全部语义。
//
// all 的顺序与 AI 的 start_service(all) 一致（先后端再前端）：前端起来后要连后端，
// 反过来会先看到一段连接失败。但这里**不**像 AI 那样把两组锁合成一次原子获取 ——
// 手机这条请求的响应已经先回了，两条腿各自持锁、各自复验即可；
// 跨组原子锁会把一次「全部启动」变成最长 90 秒独占两组资源，代价大于收益。
func lanStartDispatch(key string) {
	switch key {
	case "backend":
		lanStartBackend(lanDiscardWriter{}, lanBgStartRequest())
	case "all":
		lanStartBackend(lanDiscardWriter{}, lanBgStartRequest())
		lanStartWeb(lanDiscardWriter{}, lanBgStartRequest())
	default: // "web"（取值已由 handler 校验，不会有别的）
		lanStartWeb(lanDiscardWriter{}, lanBgStartRequest())
	}
}

// lanBgStartRequest 给后台任务造一份独立请求。
//
// 不能复用 lanStartPost 的 r：handler 一返回它的 context 就被取消、body 也读完了。
// 桌面那两个 handler 只读可选的 dir/script（都允许缺省），所以一份空 JSON 就够。
func lanBgStartRequest() *http.Request {
	r := &http.Request{
		Method: http.MethodPost,
		Header: http.Header{},
		Body:   io.NopCloser(strings.NewReader("{}")),
	}
	return r.WithContext(context.Background())
}

// lanDiscardWriter 丢弃后台任务的响应输出。
//
// 后台跑的 handleWebStart 会往 w 写 JSON；手机端不需要那份响应
// （它看的是 /api/lan/state），但也不能让它写到一个真正的 ResponseWriter 上 ——
// 那会 panic（HeaderMap 里没有的东西）。
type lanDiscardWriter struct{}

func (lanDiscardWriter) Header() http.Header         { return http.Header{} }
func (lanDiscardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (lanDiscardWriter) WriteHeader(int)             {}

func lanStopPost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	dev, ok := lanAuth(w, r)
	if !ok {
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body)
	key := strings.TrimSpace(body.Key)
	if key != "web" && key != "backend" {
		key = ""
	}
	info(scSys, "手机访问", "%s 从手机发起停止：%s", dev.Name, firstNonEmpty(key, "全部"))
	auditNow(actUser, "lan_stop", firstNonEmpty(key, "all"), dev.ID, resOK, "来自手机："+dev.Name)

	b, _ := json.Marshal(map[string]string{"key": key})
	r.Body = io.NopCloser(strings.NewReader(string(b)))
	handleStop(w, r)
}

// lanChat 手机上的 AI 对话。
//
// 与桌面走同一个主循环（流式、工具、历史都复用），区别只有一处：
// **只读模式**——模型只看得见非写工具，执行层再拦一次写工具。
// 想让 AI 帮你重启服务？那是「启动/停止」两个按钮的事，两件事分开，
// 模型说错话也弄不坏东西。
func lanChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if _, ok := lanAuth(w, r); !ok {
		return
	}
	ctx := context.WithValue(r.Context(), lanCtxKey{}, true)
	handleAIChat(w, r.WithContext(ctx))
}

type lanCtxKey struct{}

// lanMarked 判断这次请求来自手机端（只读）。
func lanMarked(r *http.Request) bool {
	v, _ := r.Context().Value(lanCtxKey{}).(bool)
	return v
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// procStatsSnapshotForLan 手机端要的资源画像：只留"谁在跑、吃多少"。
//
// 不直接复用 procStatsSnapshot()：那里每个进程带 60 点 Hist 和 PID，
// 手机 2 秒轮询一次纯属浪费流量，而 PID 也不该给一个只读客户端。
func procStatsSnapshotForLan() any {
	s := procStatsSnapshot()
	if s == nil {
		return nil
	}
	type row struct {
		Key     string  `json:"key"`
		Label   string  `json:"label"`
		Alive   bool    `json:"alive"`
		CPUPct  float64 `json:"cpu_pct"`
		RSSMB   float64 `json:"rss_mb"`
		Uptime  int64   `json:"uptime_sec,omitempty"`
		Growing bool    `json:"growing"`
	}
	out := struct {
		TS   int64 `json:"ts"`
		CPUs int   `json:"cpus"`
		Rows []row `json:"rows"`
	}{TS: s.TS, CPUs: s.CPUs}
	for _, p := range s.Procs {
		if p.Key == "self" || p.Key == "web" || p.Key == "backend" {
			out.Rows = append(out.Rows, row{
				Key: p.Key, Label: p.Label, Alive: p.Alive,
				CPUPct: p.CPUPct, RSSMB: p.RSSMB, Uptime: p.Uptime, Growing: p.Growing,
			})
		}
	}
	return out
}

// ===== 桌面侧管理端点 =====
//
// 挂在**主控制台** mux 上（受 authMW + 本机令牌保护），不是 LAN mux 上。
// 手机端永远看不到自己的开关和配对码 —— 那两样只能在电脑上看。

func handleLanConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		lanWriteJSON(w, http.StatusOK, lanConfigView(lanSnap()))
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}

	var body struct {
		Enabled       *bool   `json:"enabled"`
		Port          *int    `json:"port"`
		Password      *string `json:"password"`
		ClearPassword *bool   `json:"clear_password"`
		// v3.6.2：三个本地能力开关 + 二次验证。
		// 全是 *bool —— 缺省表示"这次没改这一项"，nil-guard merge 是唯一写入口。
		AllowShot  *bool `json:"allow_shot"`
		AllowClip  *bool `json:"allow_clip"`
		AllowTerm  *bool `json:"allow_term"`
		RequireOTP *bool `json:"require_otp"`
		NewOTP     *bool `json:"new_otp"`   // 重新生成 TOTP 密钥（旧口令器立即失效）
		ClearOTP   *bool `json:"clear_otp"` // 直接关掉二次验证并丢弃密钥
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&body); err != nil {
		http.Error(w, "参数错误", 400)
		return
	}
	if body.Port != nil && (*body.Port < 1024 || *body.Port > 65535) {
		lanWriteJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "端口需在 1024-65535"})
		return
	}
	// 密码最短 6 位：不是强度要求，是"别设成 1 然后以为设过了"。
	var newHash string
	if body.Password != nil && strings.TrimSpace(*body.Password) != "" {
		pw := *body.Password
		if len(pw) < 6 {
			lanWriteJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "密码至少 6 位"})
			return
		}
		h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		if err != nil {
			lanWriteJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": "密码加密失败"})
			return
		}
		newHash = string(h)
	}

	// 生成新密钥要在锁外做（totp.Generate 会走一次随机数），失败就整次请求失败，
	// 而不是"开关打开但密钥是空的"—— 那样用户会以为二次验证生效了，其实配不了对。
	var newSecret string
	if body.NewOTP != nil && *body.NewOTP {
		s, err := lanNewOTPSecret()
		if err != nil {
			lanWriteJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": "生成二次验证密钥失败"})
			return
		}
		newSecret = s
	}

	before := lanSnap()
	c := lanMutate(func(l *LanConfig) {
		if body.Enabled != nil {
			v := *body.Enabled
			l.Enabled = &v
		}
		if body.Port != nil {
			v := *body.Port
			l.Port = &v
		}
		if newHash != "" {
			l.PassHash = newHash
		}
		if body.ClearPassword != nil && *body.ClearPassword {
			l.PassHash = ""
		}
		if body.AllowShot != nil {
			v := *body.AllowShot
			l.AllowShot = &v
		}
		if body.AllowClip != nil {
			v := *body.AllowClip
			l.AllowClip = &v
		}
		if body.AllowTerm != nil {
			v := *body.AllowTerm
			l.AllowTerm = &v
		}
		if newSecret != "" {
			l.OTPSecret = newSecret
			// 换了密钥就顺手打开：用户的意图是"我要用二次验证"，
			// 只换密钥不开开关，等于让他再点一次才生效（少点一次不会更不安全）。
			on := true
			l.RequireOTP = &on
		}
		if body.RequireOTP != nil {
			// 打开时密钥必空 → 顺手生成一个，否则开关开了也配不了对。
			if *body.RequireOTP && l.OTPSecret == "" && newSecret == "" {
				if s, err := lanNewOTPSecret(); err == nil {
					l.OTPSecret = s
				}
			}
			v := *body.RequireOTP
			l.RequireOTP = &v
		}
		if body.ClearOTP != nil && *body.ClearOTP {
			l.OTPSecret = ""
			off := false
			l.RequireOTP = &off
		}
	})
	if body.ClearOTP != nil && *body.ClearOTP {
		lanOTPResetStep()
	}
	// 监听变更必须在锁外做（lanServeApply 会写日志）
	lanServeApply(c)
	if before.port() != c.port() && c.on() {
		warn(scSys, "手机访问", "端口已改为 %d，需重启 EnvKit 后生效（当前监听未切换，避免断开已在用的手机）", c.port())
	}

	changed := []string{}
	if body.Enabled != nil {
		changed = append(changed, fmt.Sprintf("开关=%v", c.on()))
	}
	if body.Port != nil {
		changed = append(changed, fmt.Sprintf("端口=%d", c.port()))
	}
	if newHash != "" {
		changed = append(changed, "已设置密码")
	}
	if body.ClearPassword != nil && *body.ClearPassword {
		changed = append(changed, "已清除密码")
	}
	// 三个能力开关：按"名字=新值"记，审计里能看出是打开还是关掉。
	for _, kv := range []struct {
		p    *bool
		name string
		now  bool
	}{
		{body.AllowShot, "看屏幕", lanAllowShotOn(c)},
		{body.AllowClip, "剪贴板", lanAllowClipOn(c)},
		{body.AllowTerm, "终端", lanAllowTermOn(c)},
	} {
		if kv.p != nil {
			changed = append(changed, fmt.Sprintf("%s=%v", kv.name, kv.now))
		}
	}
	if newSecret != "" {
		changed = append(changed, "已生成新的二次验证密钥")
	}
	if body.RequireOTP != nil {
		changed = append(changed, fmt.Sprintf("二次验证=%v", lanBoolOr(c.RequireOTP, false)))
	}
	if body.ClearOTP != nil && *body.ClearOTP {
		changed = append(changed, "已关闭二次验证")
	}
	if len(changed) > 0 {
		auditNow(actUser, "lan_config", "config.json", "lan", resOK, strings.Join(changed, "、"))
	}

	// 回显（与 GET 同一份，页面不用自己拼）
	lanWriteJSON(w, http.StatusOK, lanConfigView(lanSnap()))
}

// lanConfigView 桌面侧「手机访问」卡片的唯一数据来源（GET / POST 回显共用）。
//
// 三个端口字段各有各的用途，别合并：
//
//	port            配置里的端口（输入框显示这个）
//	listen_port     实际在监听的端口；0 = 没在监听
//	pending_restart 配置改了但还没重启 —— 此时地址/二维码按 listen_port 生成，
//	                界面必须明说"新端口要重启才生效"，否则用户会以为改坏了
func lanConfigView(c LanConfig) map[string]any {
	live := lanListeningPort()
	// 过期的设备不列出来：它们已经连不上（lanDeviceFor 会拒），
	// 摆在「已配对的设备」里只会让用户以为"这台还在用"。
	// 这里只做展示过滤，真正的清理在配对时（不在 GET 里写盘）。
	now := time.Now().Unix()
	devs := make([]LanDevice, 0, len(c.Devices))
	for _, d := range c.Devices {
		if !lanDeviceExpired(d, now) {
			devs = append(devs, d)
		}
	}
	return map[string]any{
		"ok":              true,
		"enabled":         c.on(),
		"port":            c.port(),
		"listen_port":     live,
		"pending_restart": c.on() && live != 0 && live != c.port(),
		"has_password":    c.PassHash != "",
		"pair_code":       lanPairCodeCur(),
		"devices":         devs,
		"addrs":           lanAddrURLs(lanDisplayPort(c)),
		"running":         lanServeRunning(),
		// v3.6.2 本地能力：三个开关 + 它们此刻的实际生效值。
		// 同时给出 raw（用户设定）和 effective（含缺省值），
		// 页面画开关用 raw 会缺省成"关闭"，但真实生效值可能是开的 —— 两个都返回，
		// 谁也别猜。
		"allow_shot":  rawBool(c.AllowShot),
		"allow_clip":  rawBool(c.AllowClip),
		"allow_term":  rawBool(c.AllowTerm),
		"eff_shot":    lanAllowShotOn(c),
		"eff_clip":    lanAllowClipOn(c),
		"eff_term":    lanAllowTermOn(c),
		"conpty":      conptyAvailable(),
		"require_otp": lanBoolOr(c.RequireOTP, false),
		"otp_secret":  c.OTPSecret,
		"otp_url":     lanOTPURL(c.OTPSecret),
	}
}

// rawBool 把"用户设过没有"和"值是多少"分开交给前端。
// nil（从没设过）返回 nil，JSON 里是 null —— 页面据此把开关画成缺省状态。
func rawBool(p *bool) any {
	if p == nil {
		return nil
	}
	return *p
}

// handleLanRotate 换一个配对码（手机上已配对的设备不受影响）。
func handleLanRotate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	code := lanRotatePairCode()
	info(scSys, "手机访问", "配对码已更换（旧码立即失效）")
	auditNow(actUser, "lan_rotate", "lan", "", resOK, "已更换配对码")
	lanWriteJSON(w, http.StatusOK, map[string]any{"ok": true, "pair_code": code})
}

// handleLanKick 踢掉一台已配对设备。
//
// 这是"手机丢了/送人了"的止损口子 —— 没有它，用户只能关掉整个开关，
// 而那会把家里所有设备一起断掉。
func handleLanKick(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body)
	id := strings.TrimSpace(body.ID)
	if id == "" {
		lanWriteJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": "缺少设备 id"})
		return
	}
	removed := ""
	lanMutate(func(l *LanConfig) {
		keep := l.Devices[:0]
		for _, d := range l.Devices {
			if d.ID == id {
				removed = d.Name
				continue
			}
			keep = append(keep, d)
		}
		l.Devices = keep
	})
	if removed == "" {
		lanWriteJSON(w, http.StatusNotFound, map[string]any{"ok": false, "msg": "没有这台设备"})
		return
	}
	info(scSys, "手机访问", "已移除设备：%s", removed)
	auditNow(actUser, "lan_kick", id, "", resOK, "已移除手机设备："+removed)
	lanWriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
