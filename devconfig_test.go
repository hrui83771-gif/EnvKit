package main

// devconfig_test.go —— 开发者/告警/行动配置段（设置页 v3.4）守护测试。
//
// 钉的是三条不能回退的约束：
//  1. 老页面的整包保存**抹不掉** dev 段（mergeFormConfig 的 nil 保护）——
//     这是"两处配置面并存"的地基，破了就会出现"改一次目录、调参全没"；
//  2. normalizeConfig 钳制脏值时不得写穿共享指针（saveExternalConfig 传的是浅拷贝，
//     写穿 = 无锁改全局内存，与并发 devSnap 读构成数据竞态）；
//  3. 段端点的指针字段语义：只改一项不得静默清零其它项，越界必须当场 400。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withCfgFile 把落盘重定向到临时目录（测试绝不能写真实 config.json），并登记
// 全局段的还原。同 weather_test.go 的 withPetCfg 手法。
func withCfgFile(t *testing.T, restore func()) {
	t.Helper()
	dir := t.TempDir()
	oldPath := configFilePath
	configFilePath = func() string { return filepath.Join(dir, "config.json") }
	t.Cleanup(func() {
		configFilePath = oldPath
		restore()
	})
}

// withDevCfg 切换全局 dev 段 + 落盘重定向。
func withDevCfg(t *testing.T, d *DevConfig) {
	t.Helper()
	oldDev := cfg.Dev
	cfg.Dev = d
	withCfgFile(t, func() { cfg.Dev = oldDev })
}

// ---------- 1. 老页保存抹不掉 dev 段 ----------

func TestMergeFormConfigKeepsDev(t *testing.T) {
	cur := Config{Dev: &DevConfig{ServerPort: 19000, TurnTimeoutSecs: 600}}

	// 老页面 configBody() 不回传 dev：指针为空必须沿用原值
	if got := mergeFormConfig(Config{}, cur); got.Dev == nil || got.Dev.ServerPort != 19000 {
		t.Fatalf("老页保存不得抹掉 dev 段： %+v", got.Dev)
	}
	// 整包导入路径带上了 dev 段：按提交值走（指针判空而非无条件保留的原因）
	in := Config{Dev: &DevConfig{ServerPort: 20000}}
	if got := mergeFormConfig(in, cur); got.Dev == nil || got.Dev.ServerPort != 20000 {
		t.Fatalf("表单自带 dev 段时应放行： %+v", got.Dev)
	}
}

// ---------- 2. 归一化的深拷贝与钳制 ----------

func TestNormalizeConfigClonesDev(t *testing.T) {
	src := &DevConfig{TurnTimeoutSecs: -5, TraceRetentionDays: -1, SummaryAfterTokens: 5000, SummaryKeepTokens: 9000}
	c := Config{Dev: src}
	normalizeConfig(&c)
	if c.Dev == src {
		t.Fatalf("normalizeConfig 必须深拷贝 dev 段：写穿共享指针 = 无锁改全局内存")
	}
	if c.Dev.TurnTimeoutSecs != devDefaultTurnTimeoutSecs || c.Dev.TraceRetentionDays != devDefaultTraceRetentionDays {
		t.Fatalf("负数应按未设置处理并落回默认值： %+v", *c.Dev)
	}
	if c.Dev.SummaryKeepTokens >= c.Dev.SummaryAfterTokens {
		t.Fatalf("保留量 ≥ 触发阈值时应自动压回：after=%d keep=%d",
			c.Dev.SummaryAfterTokens, c.Dev.SummaryKeepTokens)
	}
	if src.TurnTimeoutSecs != -5 || src.TraceRetentionDays != -1 || src.SummaryKeepTokens != 9000 {
		t.Fatalf("被归一化的源段不应被改动： %+v", *src)
	}
}

// ---------- 3. 校验：越界必须硬拦 ----------

func TestCheckDevConfigRejectsOutOfRange(t *testing.T) {
	bad := DevConfig{ServerPort: 70000, PortScanLimit: 18000, TurnTimeoutSecs: 10, AuditRetentionDays: 4000}
	ps := hardProblems(checkDevConfig(bad))
	if len(ps) < 4 {
		t.Fatalf("越界值应全部被硬拦截，只拦下 %d 项：%+v", len(ps), ps)
	}
	// 经由整包 Config 路径也要能拦（checkConfig 的挂载点）
	if ps := hardProblems(checkConfig(Config{Dev: &bad})); len(ps) == 0 {
		t.Fatalf("checkConfig 应透传 dev 段硬错误")
	}
	// 默认值（含 resolved 补全）不该报任何硬错误
	if ps := hardProblems(checkDevConfig(DevConfig{}.resolved())); len(ps) != 0 {
		t.Fatalf("默认值不应触发硬错误：%+v", ps)
	}
}

// ---------- 4. 默认值 + JSON 契约 ----------

func TestDevDefaultsAndJSONContract(t *testing.T) {
	d := DevConfig{}.resolved()
	if d.SummaryAfterTokens != 5000 || d.SummaryKeepTokens != 3000 || d.SummarizeKeepMsgs != 12 ||
		d.TurnTimeoutSecs != 360 || d.AuditRetentionDays != 30 || d.TraceRetentionDays != 14 ||
		d.ShadowRetentionDays != 30 || d.MissionDeadlineDays != 30 || d.MissionKeepDays != 30 ||
		d.ServerPort != 18765 || d.PortScanLimit != 18815 {
		t.Fatalf("默认值变了？改这里前先确认设置页「恢复默认」的目标值：%+v", d)
	}
	// 读取侧兜底：手写 0/负数不得读出 0
	zero := DevConfig{}
	if zero.turnTimeout() != 360*time.Second || zero.traceRetentionDays() != 14 || zero.missionKeep() != 30*24*time.Hour {
		t.Fatalf("零值读取应回落默认：%+v", zero)
	}

	// JSON 字段名钉死：设置页 app.js 逐个读这些 key，写错 = 结构体对、线上没有
	b, _ := json.Marshal(d)
	for _, k := range []string{
		"summary_after_tokens", "summary_keep_tokens", "summarize_keep_msgs", "turn_timeout_secs",
		"audit_retention_days", "trace_retention_days", "shadow_retention_days",
		"mission_deadline_days", "mission_keep_days", "server_port", "port_scan_limit",
	} {
		if !strings.Contains(string(b), `"`+k+`"`) {
			t.Fatalf("dev 段缺 JSON 字段 %q：%s", k, b)
		}
	}
}

// ---------- /api/dev/config ----------

func postDevCfg(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handleDevConfig(rec, httptest.NewRequest(http.MethodPost, "/api/dev/config", strings.NewReader(body)))
	return rec
}

func TestDevConfigPartialPostKeepsOtherFields(t *testing.T) {
	withDevCfg(t, &DevConfig{TurnTimeoutSecs: 600, ServerPort: 19000})

	// 只改摘要阈值 → 端口/超时不能被清零（指针字段语义，AI 段当年的坑）
	if rec := postDevCfg(t, `{"summary_after_tokens":8000}`); rec.Code != 200 {
		t.Fatalf("POST 失败：%d %s", rec.Code, rec.Body.String())
	}
	if d := devSnap(); d.TurnTimeoutSecs != 600 || d.ServerPort != 19000 || d.SummaryAfterTokens != 8000 {
		t.Fatalf("只改一项时其它项必须保留： %+v", d)
	}
	// GET 回显生效值（落盘回读）
	rec := httptest.NewRecorder()
	handleDevConfig(rec, httptest.NewRequest(http.MethodGet, "/api/dev/config", nil))
	var got DevConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("GET 响应不是 dev 段 JSON：%v", err)
	}
	if got.TurnTimeoutSecs != 600 || got.ServerPort != 19000 || got.SummaryAfterTokens != 8000 {
		t.Fatalf("落盘回读不一致：%+v", got)
	}
}

func TestDevConfigGetReturnsDefaults(t *testing.T) {
	withDevCfg(t, nil) // 老配置：dev 段缺失
	rec := httptest.NewRecorder()
	handleDevConfig(rec, httptest.NewRequest(http.MethodGet, "/api/dev/config", nil))
	var got DevConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("GET 响应不是 dev 段 JSON：%v", err)
	}
	if got.ServerPort != 18765 || got.TraceRetentionDays != 14 {
		t.Fatalf("dev 段缺失时 GET 应回默认生效值：%+v", got)
	}
}

func TestDevConfigRejectsOutOfRangeAndBadJSON(t *testing.T) {
	withDevCfg(t, &DevConfig{ServerPort: 19000})
	if rec := postDevCfg(t, `{"server_port":70000}`); rec.Code != 400 {
		t.Fatalf("越界端口应 400，实际 %d", rec.Code)
	}
	if devSnap().ServerPort != 19000 {
		t.Fatalf("被拒绝的请求不得改动配置： %+v", devSnap())
	}
	if rec := postDevCfg(t, `{不是 JSON`); rec.Code != 400 {
		t.Fatalf("坏 JSON 应 400，实际 %d", rec.Code)
	}
	for _, m := range []string{http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		handleDevConfig(rec, httptest.NewRequest(m, "/api/dev/config", nil))
		if rec.Code != 405 {
			t.Fatalf("%s 应 405，实际 %d", m, rec.Code)
		}
	}
}

// ---------- /api/alerts/config ----------

func TestAlertConfigEndpoint(t *testing.T) {
	oldAlert := cfg.Alert
	withCfgFile(t, func() { cfg.Alert = oldAlert })
	cfg.Alert = AlertConfig{}

	// GET：冷却 0 回显生效值 300
	rec := httptest.NewRecorder()
	handleAlertConfig(rec, httptest.NewRequest(http.MethodGet, "/api/alerts/config", nil))
	var got AlertConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.CooldownSecs != 300 {
		t.Fatalf("冷却 0 应回显生效值 300：%+v err=%v", got, err)
	}

	// 只改冷却 → webhook 保留（指针字段语义）
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		handleAlertConfig(r, httptest.NewRequest(http.MethodPost, "/api/alerts/config", strings.NewReader(body)))
		return r
	}
	if r := post(`{"webhook":"https://example.com/hook?key=S3CRET"}`); r.Code != 200 {
		t.Fatalf("POST 失败：%d %s", r.Code, r.Body.String())
	}
	if r := post(`{"cooldown_secs":600}`); r.Code != 200 {
		t.Fatalf("POST 失败：%d %s", r.Code, r.Body.String())
	}
	if cfg.Alert.CooldownSecs != 600 || cfg.Alert.Webhook != "https://example.com/hook?key=S3CRET" {
		t.Fatalf("只改冷却时 webhook 必须保留：%+v", cfg.Alert)
	}

	// 非 http(s) 的 webhook 当场拒绝，且不动配置
	if r := post(`{"webhook":"file:///etc/passwd"}`); r.Code != 400 {
		t.Fatalf("非法 scheme 应 400，实际 %d", r.Code)
	}
	if cfg.Alert.Webhook != "https://example.com/hook?key=S3CRET" {
		t.Fatalf("被拒绝的请求不得改动配置：%+v", cfg.Alert)
	}

	// 审计脱敏：webhook 自带 key，落盘只留主机
	if m := maskHook(cfg.Alert.Webhook); strings.Contains(m, "S3CRET") {
		t.Fatalf("审计摘要不得泄露 webhook key：%s", m)
	}
}

// ---------- /api/missions/config ----------

func TestMissionConfigEndpoint(t *testing.T) {
	oldMission := cfg.Mission
	withCfgFile(t, func() { cfg.Mission = oldMission })
	off := false
	cfg.Mission = MissionConfig{Enabled: &off, PatrolIntervalMin: 30, BriefingAt: "09:00"}

	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		handleMissionConfig(r, httptest.NewRequest(http.MethodPost, "/api/missions/config", strings.NewReader(body)))
		return r
	}
	// 只改晨报时刻 → 开关/间隔保留；非法格式当场 400
	if r := post(`{"briefing_at":"10:30"}`); r.Code != 200 {
		t.Fatalf("POST 失败：%d %s", r.Code, r.Body.String())
	}
	if cfg.Mission.BriefingAt != "10:30" || cfg.Mission.on() || cfg.Mission.PatrolIntervalMin != 30 {
		t.Fatalf("只改晨报时其它项必须保留：%+v", cfg.Mission)
	}
	if r := post(`{"briefing_at":"25:00"}`); r.Code != 400 {
		t.Fatalf("非法时刻应 400，实际 %d", r.Code)
	}
	// 打开开关：Enabled 指针必须真的翻转（零值表达不了"关"，这是用指针的全部理由）
	if r := post(`{"enabled":true}`); r.Code != 200 {
		t.Fatalf("POST 失败：%d %s", r.Code, r.Body.String())
	}
	if !cfg.Mission.on() {
		t.Fatalf("enabled=true 应打开定时产出：%+v", cfg.Mission)
	}
}

// missionCfgEqual 按值比较（Enabled 是指针，直接 == 比的是地址）。
func TestMissionCfgEqual(t *testing.T) {
	on, off := true, false
	a := MissionConfig{Enabled: nil, PatrolIntervalMin: 60}
	b := MissionConfig{Enabled: &on, PatrolIntervalMin: 60}
	if !missionCfgEqual(a, b) {
		t.Fatalf("nil 与显式 true 都表示开，应相等")
	}
	c := MissionConfig{Enabled: &off, PatrolIntervalMin: 60}
	if missionCfgEqual(a, c) {
		t.Fatalf("显式 false 与默认开不应相等")
	}
}
