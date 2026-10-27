// alerts.go —— 告警渠道：桌面通知 + Webhook，带抑制窗口
//
// 为什么需要抑制：节点反复宕机时，之前只有桌面气泡 —— 每轮守护都弹，用户最后一定
// 把通知关掉，告警就死了。现在统一走 alertDispatch：同一告警在冷却窗口内只发一次
// Webhook（气泡仍然弹，不丢本地感知），并把"发了/抑制了"记进指标。
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"envkit/internal/platform"
)

// AlertConfig 告警配置（config.json 的 "alerts" 段）。
type AlertConfig struct {
	// Webhook 接收端 URL（POST JSON）。支持企业微信/钉钉/飞书群机器人的"自定义"通道
	// 需要的格式不同，这里发通用 JSON：{"app","version","host","title","message","time"}。
	// 若要接企微/钉钉，中间加一层转换或使用其"其他格式"接口。
	Webhook string `json:"webhook"`
	// CooldownSecs 同一标题的告警在此窗口内只发一次 Webhook（0 → 默认 300 秒）。
	CooldownSecs int `json:"cooldown_secs"`
}

var alertMu sync.Mutex
var alertLast = map[string]time.Time{} // 告警标题 → 上次发送时间

// alertDispatch 发出一条运维告警：桌面气泡（总是）+ Webhook（受抑制窗口约束）。
func alertDispatch(title, msg string) {
	platform.Notify(title, msg) // 本地感知不走抑制 —— 气泡 6 秒自动消失，刷屏成本低

	cfgMu.Lock()
	hook := cfg.Alert.Webhook
	cd := cfg.Alert.CooldownSecs
	cfgMu.Unlock()
	if cd <= 0 {
		cd = 300
	}
	if hook == "" {
		metAlert("webhook", "disabled")
		return
	}
	alertMu.Lock()
	if last, ok := alertLast[title]; ok && time.Since(last) < time.Duration(cd)*time.Second {
		alertMu.Unlock()
		metAlert("webhook", "suppressed")
		info(scSys, "告警", "「%s」在抑制窗口内，跳过 Webhook（%d 秒冷却）", title, cd)
		return
	}
	alertLast[title] = time.Now()
	alertMu.Unlock()

	sendWebhookAlert(hook, title, msg)
}

// sendWebhookAlert 向配置的 URL POST 通用 JSON 告警。失败只记日志/审计，不影响主流程。
func sendWebhookAlert(hook, title, msg string) {
	host, _ := os.Hostname()
	payload := fmt.Sprintf(`{"app":"EnvKit","version":%q,"host":%q,"title":%q,"message":%q,"time":%q}`,
		appVersion, host, title, msg, time.Now().Format("2006-01-02 15:04:05"))
	fin := auditStart(actSys, "alert_webhook", title, hook[:minStr(len(hook), 64)])
	resp, err := httpAPI.Post(hook, "application/json", bytes.NewReader([]byte(payload)))
	if err != nil {
		warn(scSys, "告警", "Webhook 发送失败：%v", err)
		metAlert("webhook", "error")
		fin(resFail, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		warn(scSys, "告警", "Webhook 返回异常状态码 %d", resp.StatusCode)
		metAlert("webhook", "error")
		fin(resFail, fmt.Sprintf("HTTP %d", resp.StatusCode))
		return
	}
	ok(scSys, "告警", "Webhook 已发送：%s", title)
	metAlert("webhook", "sent")
	fin(resOK, "")
}

// minStr 返回较小的那个数（避免为了一处 min 引入 constraints 依赖）。
func minStr(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------- 配置段端点（设置页「基础配置 → 告警」） ----------

// handleAlertConfig 告警配置的独立出入口（/api/alerts/config）。
//
// 与 handlePetConfig 同范式：GET 回显生效值、POST 指针字段增量提交。
// 独立出入口的理由：老页面的表单不含告警字段（mergeFormConfig 有条件保留兜底），
// 设置页上线后告警配置有了真正的编辑入口，就该有自己的保存通道 —— 不与整包
// 表单保存混在一起，才不会出现"改个安装目录把 webhook 抹了"这类连带伤害。
func handleAlertConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		cfgMu.Lock()
		ac := cfg.Alert
		cfgMu.Unlock()
		if ac.CooldownSecs <= 0 {
			ac.CooldownSecs = 300 // 回显生效值（alertDispatch 里 0 → 300）
		}
		writeJSON(w, ac)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}

	var body struct {
		Webhook      *string `json:"webhook"`
		CooldownSecs *int    `json:"cooldown_secs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "参数错误", 400)
		return
	}

	cfgMu.Lock()
	before := cfg.Alert
	if body.Webhook != nil {
		h := strings.TrimSpace(*body.Webhook)
		// 非空时必须是 http(s) 地址：发给 file:// 或裸主机名会在告警那一刻才炸，
		// 而告警链路恰恰是最不该"用的时候才发现坏"的地方。
		if h != "" && !(strings.HasPrefix(h, "http://") || strings.HasPrefix(h, "https://")) {
			cfgMu.Unlock()
			http.Error(w, "Webhook 需以 http:// 或 https:// 开头", 400)
			return
		}
		if hs := []rune(h); len(hs) > 500 {
			h = string(hs[:500])
		}
		cfg.Alert.Webhook = h
	}
	if body.CooldownSecs != nil {
		cd := *body.CooldownSecs
		if cd < 0 {
			cd = 0
		}
		if cd > 86400 {
			cd = 86400 // 抑制窗口上限一天：再大等于永久闭嘴
		}
		cfg.Alert.CooldownSecs = cd
	}
	after := cfg.Alert
	saved := cfg
	cfgMu.Unlock()

	if saved.Alert != before {
		saveExternalConfig(saved)
		auditNow(actUser, "alert_config", "config.json", "alerts", resOK,
			fmt.Sprintf("webhook=%s 冷却=%ds", maskHook(after.Webhook), after.CooldownSecs))
	}
	writeJSON(w, after)
}

// maskHook 审计/日志里不落完整 Webhook：飞书/企微/钉钉的 webhook URL 自带
// key，写进审计文件等于把凭据抄一份到旁边。只留"发到哪个主机"。
func maskHook(u string) string {
	if u == "" {
		return "(未设置)"
	}
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Scheme + "://" + p.Host + "/…"
	}
	return "(非法 URL)"
}

// handleAlertTest 发一条测试告警，用于配完 webhook 后当场确认链路通。
//
// 为什么值得单开一个端点：告警链路最怕"配好了但从来没响过"，真出事那天
// 才发现地址写错。设置页上点一下就能验证，比事后翻日志便宜得多。
func handleAlertTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	cfgMu.Lock()
	hook := cfg.Alert.Webhook
	cfgMu.Unlock()

	if strings.TrimSpace(hook) == "" {
		// 没配 webhook 也弹一次气泡：证明"本机感知"这段是通的，
		// 否则用户会把"什么都没发生"理解成整个告警链路坏了。
		platform.Notify("EnvKit 测试告警", "当前未配置 Webhook，所以只弹了这条气泡。")
		writeJSON(w, map[string]any{"ok": true, "webhook": false, "host": ""})
		return
	}
	// 标题带时间戳：alertDispatch 按标题做冷却抑制，固定标题第二次点就没反应，
	// 而"点了没反应"会被读成链路坏了 —— 测试恰恰不该在这上面撒谎。
	alertDispatch("EnvKit 测试告警 "+time.Now().Format("15:04:05"), "收到这条说明告警链路可用。")
	writeJSON(w, map[string]any{"ok": true, "webhook": true, "host": maskHook(hook)})
}
