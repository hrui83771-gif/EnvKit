// dev_config.go —— 开发者配置段端点 /api/dev/config（设置页「开发者配置」tab）。
//
// 与 handlePetConfig（weather.go）同一范式：GET 回显生效值、POST 按指针字段增量提交。
//
// 为什么不复用 POST /api/config：那个接口是"整个表单替换 + 硬校验"，而老页面的
// configBody() 根本不回传 dev 段（靠 mergeFormConfig 的 nil 保护才没被自动保存抹掉）。
// 独立出入口后两页只在"谁写谁的段"这一层耦合：基础配置改目录、开发者配置改调参，
// 互不携带、互不覆盖。
//
// 所有字段都是指针：零值 false/0 无法区分"用户要设成 0"和"没传这个字段"，
// 直接用值类型会让任何一次只改端口的保存把其它调参项静默归零（AI 段踩过同样的坑）。
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

func handleDevConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		cfgMu.Lock()
		var d DevConfig
		if cfg.Dev != nil {
			d = *cfg.Dev
		}
		cfgMu.Unlock()
		// 回显生效值：没设置的项显示内置默认，界面上的输入框才有数字可填
		writeJSON(w, d.resolved())
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}

	var body struct {
		SummaryAfterTokens *int `json:"summary_after_tokens"`
		SummaryKeepTokens  *int `json:"summary_keep_tokens"`
		SummarizeKeepMsgs  *int `json:"summarize_keep_msgs"`
		TurnTimeoutSecs    *int `json:"turn_timeout_secs"`

		AuditRetentionDays  *int `json:"audit_retention_days"`
		TraceRetentionDays  *int `json:"trace_retention_days"`
		ShadowRetentionDays *int `json:"shadow_retention_days"`
		MissionDeadlineDays *int `json:"mission_deadline_days"`
		MissionKeepDays     *int `json:"mission_keep_days"`

		ServerPort    *int `json:"server_port"`
		PortScanLimit *int `json:"port_scan_limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "参数错误", 400)
		return
	}

	cfgMu.Lock()
	base := DevConfig{}
	if cfg.Dev != nil {
		base = *cfg.Dev
	}
	next := base
	var changed []string
	set := func(name string, dst *int, src *int) {
		if src == nil {
			return
		}
		if *dst != *src {
			changed = append(changed, name)
		}
		*dst = *src
	}
	set("摘要触发阈值", &next.SummaryAfterTokens, body.SummaryAfterTokens)
	set("摘要保留量", &next.SummaryKeepTokens, body.SummaryKeepTokens)
	set("摘要至少保留条数", &next.SummarizeKeepMsgs, body.SummarizeKeepMsgs)
	set("单轮超时秒数", &next.TurnTimeoutSecs, body.TurnTimeoutSecs)
	set("审计保留天数", &next.AuditRetentionDays, body.AuditRetentionDays)
	set("轨迹保留天数", &next.TraceRetentionDays, body.TraceRetentionDays)
	set("影子保留天数", &next.ShadowRetentionDays, body.ShadowRetentionDays)
	set("行动超期天数", &next.MissionDeadlineDays, body.MissionDeadlineDays)
	set("行动保留天数", &next.MissionKeepDays, body.MissionKeepDays)
	set("控制台端口", &next.ServerPort, body.ServerPort)
	set("端口上界", &next.PortScanLimit, body.PortScanLimit)

	normalizeDev(&next)
	if hard := hardProblems(checkDevConfig(next)); len(hard) > 0 {
		cfgMu.Unlock()
		var msgs []string
		for _, p := range hard {
			msgs = append(msgs, p.String())
		}
		auditNow(actUser, "dev_config", "config.json", "dev", resDenied, strings.Join(msgs, "；"))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "problems": hard})
		return
	}
	dv := next
	cfg.Dev = &dv
	saved := cfg
	cfgMu.Unlock()

	if len(changed) > 0 {
		saveExternalConfig(saved)
		auditNow(actUser, "dev_config", "config.json", "dev", resOK,
			fmt.Sprintf("已修改：%s", strings.Join(changed, "、")))
	}
	// 回显生效值（normalize 可能压过用户输入，比如保留量 ≥ 触发阈值时的自动修正）
	writeJSON(w, dv.resolved())
}

// serverPortHint 单实例阶段的端口提示（main() 在 loadConfig 之前调用）。
// 那时 cfg 还是空的，而 loadConfig 可能写盘（版本迁移/回退重写正本）——
// 双开时不该两个进程一起写 config.json，所以这里对 config.json 做一次
// **只读**解析拿 dev 段端口设置；文件缺失/解析失败一律回落内置默认。
func serverPortHint() (start, limit int) {
	start, limit = devDefaultServerPort, devDefaultServerPort+devDefaultPortScanSpan
	b, err := os.ReadFile(configFilePath())
	if err != nil {
		return
	}
	var probe struct {
		Dev *DevConfig `json:"dev"`
	}
	if json.Unmarshal(b, &probe) != nil || probe.Dev == nil {
		return
	}
	d := probe.Dev.resolved()
	return d.serverPort(), d.portScanLimit()
}
