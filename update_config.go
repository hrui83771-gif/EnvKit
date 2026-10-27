// update_config.go —— 自更新检查的设置端点 /api/update/config（设置页「界面与更新」卡）。
//
// 与 handleDevConfig / handlePetConfig 同一范式：GET 回显生效值，POST 按指针增量提交。
//
// 为什么 update_url 也在这里写：URL 虽然住在 Config 顶层（老字段），但界面上
// 它和"查不查、多久查、哪个版本别再提示"是同一张卡、同一个保存动作。
// 分成两个端点写只会多出"一半生效"的中间态，所以这里一并接住（见 body.URL）。
// 老页面 configBody() 仍会把 update_url 带进 /api/config 整包 —— 同一个输入框、
// 同一个值，不构成两个来源；要彻底拆掉得连老页面的保存条一起改。
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// updateView 是 GET 的响应体：设置 + 最近一次检查结果。
// 两者放一起是因为界面同一张卡里既显示开关、又显示"当前/最新版本"，
// 分两次请求会让"刚点完检查、版本号却没变"这类时序问题变多。
type updateView struct {
	Enabled       bool       `json:"enabled"`
	IntervalHours int        `json:"interval_hours"`
	IgnoreVersion string     `json:"ignore_version"`
	URL           string     `json:"url"`
	Result        updateInfo `json:"result"`
}

func updateViewOf() updateView {
	cfgMu.Lock()
	var u UpdateConfig
	if cfg.Update != nil {
		u = *cfg.Update
	}
	url := cfg.UpdateURL
	cfgMu.Unlock()

	updMu.Lock()
	res := upd
	updMu.Unlock()

	return updateView{
		Enabled:       u.on(),
		IntervalHours: u.interval(), // nil（没配过）也回显成默认值，否则界面显示 0 = 只在启动查
		IgnoreVersion: u.IgnoreVersion,
		URL:           url,
		Result:        res,
	}
}

// versionShaped 判断一个串像不像版本号（数字与点，至少含一个数字）。
// 忽略版本存的是"精确匹配 latest"的值，写成一句注释永远不会命中，
// 却会静静躺在配置里让人以为设过了 —— 所以形态不对就当场拒绝。
func versionShaped(s string) bool {
	if s == "" {
		return false
	}
	hasDigit := false
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
			hasDigit = true
		case c == '.' || c == '-' || c == '+' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			// 允许后缀（-beta.1 之类）
		default:
			return false
		}
	}
	return hasDigit
}

func handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, updateViewOf())
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}

	var body struct {
		Enabled       *bool   `json:"enabled"`
		IntervalHours *int    `json:"interval_hours"`
		IgnoreVersion *string `json:"ignore_version"`
		URL           *string `json:"url"`
		Recheck       bool    `json:"recheck"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "参数错误", 400)
		return
	}

	// 忽略版本先校验再进锁：写错格式应当场 400，而不是存进去等下次检查默默不命中。
	if body.IgnoreVersion != nil {
		v := strings.TrimSpace(*body.IgnoreVersion)
		if v != "" && !versionShaped(v) {
			http.Error(w, "忽略版本需形如 3.4.0（只允许数字、点和常见后缀）", 400)
			return
		}
	}
	// URL 形态同理：写成 "example.com/ver.json" 会等到下次检查才炸，
	// 而那时用户早就不在设置页了。
	if body.URL != nil {
		u := strings.TrimSpace(*body.URL)
		if u != "" && !(strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")) {
			http.Error(w, "更新地址需以 http:// 或 https:// 开头", 400)
			return
		}
		if rs := []rune(u); len(rs) > 500 {
			u = string(rs[:500])
		}
	}

	cfgMu.Lock()
	base := UpdateConfig{}
	if cfg.Update != nil {
		base = *cfg.Update
	}
	next := base
	var changed []string

	if body.Enabled != nil {
		was := next.on()
		if was != *body.Enabled {
			changed = append(changed, "自更新检查开关")
		}
		v := *body.Enabled
		next.Enabled = &v
	}
	if body.IntervalHours != nil {
		v := *body.IntervalHours
		if v < 0 {
			v = 0
		}
		// 上限一周：再稀就失去"及时知道"的意义，不如直接关掉检查。
		if v > 168 {
			v = 168
		}
		// 和「生效值」比而不是和「存的值」比：原本 nil（=默认 72）时，
		// 用户把它改成 72 不该算一次改动，也不该因此触发重查。
		if next.interval() != v {
			changed = append(changed, "复查间隔")
		}
		next.IntervalHours = &v
	}
	if body.IgnoreVersion != nil {
		v := strings.TrimSpace(*body.IgnoreVersion)
		if rs := []rune(v); len(rs) > 40 {
			v = string(rs[:40])
		}
		if next.IgnoreVersion != v {
			changed = append(changed, "忽略版本")
		}
		next.IgnoreVersion = v
	}

	nv := next
	cfg.Update = &nv
	// 更新地址和它一起写：URL 在 Config 顶层（老字段），行为开关在 update 段，
	// 但界面上是同一张卡、同一个保存动作，分开写只会多出"一半生效"的中间态。
	if body.URL != nil {
		u := strings.TrimSpace(*body.URL)
		if rs := []rune(u); len(rs) > 500 {
			u = string(rs[:500])
		}
		if cfg.UpdateURL != u {
			changed = append(changed, "更新地址")
		}
		cfg.UpdateURL = u
	}
	saved := cfg
	cfgMu.Unlock()

	if len(changed) > 0 {
		saveExternalConfig(saved)
		auditNow(actUser, "update_config", "config.json", "update", resOK,
			fmt.Sprintf("已修改：%s", strings.Join(changed, "、")))
	}
	// 改了开关或点「立即检查」都重查一次。异步：外网请求最长 8 秒（httpCDN），
	// 同步等会让前端按钮卡住，而这地方的结果本来就是"稍后刷新看到"。
	if len(changed) > 0 || body.Recheck {
		go runSelfUpdateCheck()
	}
	writeJSON(w, updateViewOf())
}
