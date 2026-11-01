package main

// lantotp.go —— 配对时的第二个因子（可选，默认关）
//
// ## 为什么值得加
//
// 配对码是 6 位数字，只有 100 万种组合。现在靠"同 IP 5 次失败锁 10 分钟"兜底，
// 这能拖慢穷举，但拖不住两种情况：码被人瞥见过（托盘上就摆着）、
// 以及手机在公共 WiFi 上连过一次。TOTP 把第二个因子放进**另一台设备**
// （你手机上的口令器），泄漏一个不等于泄漏两个。
//
// ## 为什么不默认开
//
// 大多数人就是在家里的 WiFi 上临时看一眼服务状态。强制装口令器，
// 结果是这个功能被放弃，而不是被用得更安全。安全机制的默认值应该
// 匹配"多数人真实的使用场景"，剩下的交给愿意多拧一圈的人。
//
// ## 一次性（防重放）
//
// 30 秒窗口内同一个码理论上有两次机会被用（skew=1 时更多）。
// 这里记住"已经用过的时间步"，只允许**严格向前**推进 ——
// 代价是同一分钟内第二台手机得等下一个码，收益是截获的码不能重放。

import (
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

const (
	lanOTPIssuer = "EnvKit"
	lanOTPStep   = 30 // 秒；RFC 6238 的缺省周期
)

// lanOTPLastStep 已接受的最大时间步。进程级 —— 与配对码同生命周期，
// 重启后归零没有风险（重启本身就会换配对码）。
var lanOTPLastStep atomic.Int64

// lanOTPAccount 口令器里显示的名字：EnvKit (lan-主机名)，多台电脑能分辨。
func lanOTPAccount() string {
	h, err := os.Hostname()
	if err != nil || strings.TrimSpace(h) == "" {
		h = "pc"
	}
	return "lan-" + h
}

// lanNewOTPSecret 生成一个新的 TOTP 密钥（base32）。
func lanNewOTPSecret() (string, error) {
	k, err := totp.Generate(totp.GenerateOpts{
		Issuer:      lanOTPIssuer,
		AccountName: lanOTPAccount(),
	})
	if err != nil {
		return "", err
	}
	return k.Secret(), nil
}

// lanOTPURL 生成 otpauth:// 的二维码内容（桌面端用它画二维码）。
//
// 用库拼而不是自己写字符串：密钥、period/digits/algorithm 必须和
// lanOTPCode 里 ValidateCustom 用的**完全一致**。手拼迟早会漏一个参数，
// 而症状是"扫进去的码永远验证不通过"—— 那时没人会怀疑到 URL 上。
func lanOTPURL(secret string) string {
	if secret == "" {
		return ""
	}
	k, err := otp.NewKeyFromURL("otpauth://totp/" + otpPathEscape(lanOTPIssuer+":"+lanOTPAccount()) +
		"?secret=" + secret + "&issuer=" + otpPathEscape(lanOTPIssuer) +
		"&period=30&digits=6&algorithm=SHA1")
	if err != nil {
		return ""
	}
	return k.URL()
}

// otpPathEscape 只转义 otpauth:// 路径里真正会出问题的字符。
// 主机名带空格/中文时，不转义会让口令器解析出半截 issuer。
func otpPathEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, " ", "%20"), "#", "%23")
}

// lanOTPCode 校验 6 位验证码。
//
// 返回 (是否通过, 是否因为"刚用过"被拒)。两个值分开返回是为了给用户一句
// 说得通的话：码错和码刚用过要区别对待，否则用户会以为口令器坏了。
func lanOTPCode(secret, code string) (ok bool, replay bool) {
	secret = strings.TrimSpace(secret)
	code = strings.TrimSpace(code)
	if secret == "" || len(code) != 6 || !lanIsDigits(code) {
		return false, false
	}
	now := time.Now().UTC()
	pass, err := totp.ValidateCustom(code, secret, now, totp.ValidateOpts{
		Period:    lanOTPStep,
		Skew:      1,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil || !pass {
		return false, false
	}
	// 走到这里说明码本身对。再做一次"只许向前"的推进：
	// 同一个时间步（含 skew 带来的邻步）已经用过就判重放。
	step := now.Unix() / lanOTPStep
	for {
		prev := lanOTPLastStep.Load()
		if step <= prev {
			return false, true
		}
		if lanOTPLastStep.CompareAndSwap(prev, step) {
			return true, false
		}
	}
}

// lanOTPResetStep 关掉 TOTP 时重置，避免"关掉再开"之后第一个码一直被当成重放。
func lanOTPResetStep() { lanOTPLastStep.Store(0) }
