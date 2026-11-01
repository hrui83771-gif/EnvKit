// lanqr.go —— 手机访问的配对二维码
//
// ## 为什么要有二维码
//
// 用户实测反馈：配对要"打开手机 → 输入网址 → 再输入 6 位配对码"，
// 两步手输，错一位就得重来。二维码把**网址和配对码一起**编码进去，
// 手机扫一下就带着码进页面 —— 一次扫描完成两件事。
//
// ## 边界
//
// 这个端点挂在**主控制台** mux 上（受 authMW + 本机令牌保护），
// 不是 LAN mux —— 手机端根本不需要生成二维码的能力。
// 二维码内容是本机的局域网地址 + 配对码，只有在本机屏幕上才看得到。
package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/skip2/go-qrcode"
)

// lanPairURL 组装"带配对码的访问地址"。
//
// 配对码走 URL 查询参数（?code=），手机端落地页会读它并自动填入。
// 用 URL 参数而不是路径，是为了让 LAN mux 的路由表保持原样（6 条路由不变）。
//
// 安全边界：配对码本身是 6 位数字、进程级、每次启动即换，
// 且拿到它还得先能连到这个局域网端口。它出现在 URL 里只意味着
// "任何看到这个二维码的人都能进来" —— 而二维码本来就只显示在本机屏幕上。
func lanPairURL(port int, code string) string {
	ips := lanReachableIPs()
	if len(ips) == 0 {
		return ""
	}
	base := fmt.Sprintf("http://%s:%d/", ips[0], port)
	if code == "" {
		return base
	}
	return base + "?" + url.Values{"code": {code}}.Encode()
}

// handleLanQR 返回二维码（PNG）。
//
// GET /api/lan/qr          配对二维码（网址 + 当前配对码）
// GET /api/lan/qr?otp=1    二次验证密钥的 otpauth:// 二维码（给口令器扫）
//
// **不接受外部传入的内容** —— 二维码里编什么由服务端决定。接受调用方指定内容
// 等于开一个"任意文本转二维码"的接口，虽不致命但是白开的攻击面。
// otp=1 这个分支同样只读服务端自己的密钥，不多给一比特控制权。
func handleLanQR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	c := lanSnap()

	var target string
	if r.URL.Query().Get("otp") == "1" {
		if c.OTPSecret == "" {
			lanWriteJSON(w, http.StatusNotFound, map[string]any{"ok": false, "msg": "还没有生成二次验证密钥"})
			return
		}
		target = lanOTPURL(c.OTPSecret)
		if target == "" {
			lanWriteJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": "生成 otpauth 地址失败"})
			return
		}
	} else {
		// 用**实际**监听端口：改端口后没重启时，配置端口上没人监听，
		// 把那个端口编进二维码等于让用户扫出一个打不开的地址。
		target = lanPairURL(lanDisplayPort(c), lanPairCodeCur())
		if target == "" {
			lanWriteJSON(w, http.StatusServiceUnavailable, map[string]any{
				"ok":  false,
				"msg": "没找到手机能连上的局域网地址（电脑连 WiFi 或网线了吗？）",
			})
			return
		}
	}
	png, err := qrcode.Encode(target, qrcode.Medium, 320)
	if err != nil {
		lanWriteJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":  false,
			"msg": "二维码生成失败：" + err.Error(),
		})
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

// lanPairURLForLog 给日志用的纯文本地址（去掉配对码）。
//
// 日志会落盘、会被审计记录，配对码不该出现在那里 ——
// 它是"进门钥匙"，日志文件被翻到就等于钥匙泄露。
func lanPairURLForLog(port int) string {
	ips := lanReachableIPs()
	if len(ips) == 0 {
		return ""
	}
	return strings.TrimSuffix(fmt.Sprintf("http://%s:%d/", ips[0], port), "/")
}
