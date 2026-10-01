// alerts.go —— 告警渠道：桌面通知 + Webhook，带抑制窗口
//
// 为什么需要抑制：节点反复宕机时，之前只有桌面气泡 —— 每轮守护都弹，用户最后一定
// 把通知关掉，告警就死了。现在统一走 alertDispatch：同一告警在冷却窗口内只发一次
// Webhook（气泡仍然弹，不丢本地感知），并把"发了/抑制了"记进指标。
package main

import (
	"bytes"
	"fmt"
	"os"
	"sync"
	"time"
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
	notify(title, msg) // 本地感知不走抑制 —— 气泡 6 秒自动消失，刷屏成本低

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
