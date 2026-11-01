// 桌面端手机访问配置（开关 / 配对码 / 踢设备） —— 从 lanaccess.go 拆出（纯搬家，行为不变）。
// 手机端的安全边界是「独立 mux + 路由少」，拆文件只移动函数，
// 不新增任何路由。

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

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
