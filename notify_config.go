// notify_config.go —— 全局通知开关（config.json 的 "notify" 段，v3.4 设置页）。
//
// 一个开关管住所有「EnvKit 主动推送」：桌面气泡、Webhook 告警。
//
// 边界（这是本文件最需要说清的一件事）：
//   - 静音的是**主动推送** —— 告警、链端不可达、崩溃断路器、版本提示。
//     这些发生在用户没看着屏幕的时候，一直弹会让人直接关掉通知，于是告警就死了；
//     给一个总开关，比让人去任务管理器杀进程体面。
//   - 不静音的是**操作回执** —— "已在运行，已为你打开页面"、"快捷方式已创建"、
//     "测试告警"。那是用户刚点了按钮，静音掉等于让人以为按钮坏了。
//     判断标准只有一条：有没有人正在等这个结果。
//
// 与 handleDevConfig / handlePetConfig / handleUpdateConfig 同一范式：
// GET 回显、POST 指针增量、nil 段当作"未静音"。
package main

import (
	"encoding/json"
	"net/http"

	"envkit/internal/platform"
)

// NotifyConfig 全局通知开关。
//
// Muted 用值类型而非指针：这是布尔开关，"没写过"和"关着"语义相同，
// 不像数字那样需要区分 0 = 默认、0 = 用户设的 0。
type NotifyConfig struct {
	Muted bool `json:"muted"`
}

// notifyMuted 读当前是否静音。会取 cfgMu —— 调用方必须保证自己不在锁内，
// 尤其是不要在 logMu 下调用（写日志热路径上有人持 cfgMu，反过来取就是锁序反转）。
func notifyMuted() bool {
	cfgMu.Lock()
	n := cfg.Notify
	cfgMu.Unlock()
	return n != nil && n.Muted
}

// notifyUser 主动推送的统一出口：静音时不发。
//
// 所有新增的"主动告诉用户一件事"都要走这里，而不是直接调 platform.Notify；
// 直接调的那几处是操作回执，见文件头的边界说明。
func notifyUser(title, msg string) {
	if notifyMuted() {
		return
	}
	platform.Notify(title, msg)
}

func handleNotifyConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, map[string]any{"muted": notifyMuted()})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}

	// Muted 用指针：省略字段 = 不改（与其它端点一致，避免误传 false 把静音关掉）。
	var body struct {
		Muted *bool `json:"muted"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "参数错误", 400)
		return
	}
	if body.Muted == nil {
		writeJSON(w, map[string]any{"muted": notifyMuted()})
		return
	}

	cfgMu.Lock()
	base := NotifyConfig{}
	if cfg.Notify != nil {
		base = *cfg.Notify
	}
	changed := base.Muted != *body.Muted
	base.Muted = *body.Muted
	nv := base
	cfg.Notify = &nv
	saved := cfg
	cfgMu.Unlock()

	if changed {
		saveExternalConfig(saved)
		what := "关闭"
		if nv.Muted {
			what = "开启"
		}
		auditNow(actUser, "notify_config", "config.json", "notify", resOK,
			"全局通知静音已"+what)
		// 关掉静音时弹一条，让用户确认"通知真的回来了" —— 否则这个开关
		// 只能靠"等下次出事"来验证，而那时候已经晚了。
		if !nv.Muted {
			platform.Notify("EnvKit 通知已恢复", "告警与状态提示会重新出现在这里。")
		}
	}
	writeJSON(w, map[string]any{"muted": nv.Muted})
}
