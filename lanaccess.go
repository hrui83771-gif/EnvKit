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
//
// ## 文件划分（v3.7.2 拆分，纯搬家）
//
// 这个方案原本是一个 1590 行的文件。按职责切开后各块的关系仍然是上面那三道闸，
// **拆的只是文件，不是权限边界**：
//
//	lanaccess.go  配置段 / 设备类型 / 令牌与配对码（本文件，核心不变量）
//	lanaddr.go    本机地址探测与到达性判断（决定"手机该连哪个 IP"）
//	lanlimit.go   Host 白名单 + 失败限速（闸 2 与闸 3 的速率部分）
//	lanserve.go   监听生命周期 / 安全中间件 / **路由表 lanMux()**
//	lanpage.go    手机页面 / 配对 / 撤销 / 鉴权（闸 3 的入口）
//	lanapi.go     手机端只读 API（状态 / 日志 / 启停 / 对话）
//	lanconf.go    桌面端手机访问配置（开关 / 配对码 / 踢设备）
//
// 约束：**手机端的安全边界建立在「独立 mux + 路由少」上**，
// 拆文件时只移动函数，`lanMux()` 注册的路由一条都不能增删。
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
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
