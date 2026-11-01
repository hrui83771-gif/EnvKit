// 手机页面 / 配对 / 撤销 / 鉴权 —— 从 lanaccess.go 拆出（纯搬家，行为不变）。
// 手机端的安全边界是「独立 mux + 路由少」，拆文件只移动函数，
// 不新增任何路由。

package main

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

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
