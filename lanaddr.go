// 本机地址探测与到达性判断 —— 从 lanaccess.go 拆出（纯搬家，行为不变）。
// 手机端的安全边界是「独立 mux + 路由少」，拆文件只移动函数，
// 不新增任何路由。

package main

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

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
