package main

// uistate_test.go —— v3.9「结论跨刷新/重启」与「配置备份恢复」的后端判据。
//
// 这里钉的是最容易悄悄退化成"只在内存里"的三件事：
//   · 改过配置没复检 → 刷新/重启后仍然要显示「待复检」；
//   · 体检报告 → 重启后还在，且必须自报"这是上次运行留下的"；
//   · 备份恢复 → 只认 1/2/3 三个槽位，损坏的备份绝不拿去覆盖正本。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempState 把 state.json 指向临时目录，并在结束时还原全局内存状态。
func withTempState(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	oldPath := statePath
	statePath = func() string { return filepath.Join(dir, "state.json") }
	hcMu.Lock()
	oldCur, oldHist := hcCur, hcHistory
	hcMu.Unlock()
	uiMu.Lock()
	oldStale, oldAt := cfgStaleState, cfgStaleAt
	uiMu.Unlock()
	t.Cleanup(func() {
		statePath = oldPath
		hcMu.Lock()
		hcCur, hcHistory = oldCur, oldHist
		hcMu.Unlock()
		uiMu.Lock()
		cfgStaleState, cfgStaleAt = oldStale, oldAt
		uiMu.Unlock()
	})
	return dir
}

func TestCfgStaleSurvivesReload(t *testing.T) {
	withTempState(t)
	setCfgStale(true)
	if on, _ := getCfgStale(); !on {
		t.Fatal("setCfgStale(true) 后读不到 true")
	}
	// 模拟重启：内存全清，只剩磁盘
	uiMu.Lock()
	cfgStaleState, cfgStaleAt = false, ""
	uiMu.Unlock()
	loadPersistedState()
	on, at := getCfgStale()
	if !on {
		t.Fatal("刷新/重启后「待复检」丢了——旧结论又会以「已通过」的面貌出现")
	}
	if at == "" {
		t.Fatal("恢复出来的待复检没带时间，界面没法解释是什么时候改的")
	}
}

func TestCfgStaleClearsOnRecheck(t *testing.T) {
	withTempState(t)
	setCfgStale(true)
	setCfgStale(false)
	uiMu.Lock()
	cfgStaleState = true // 假装重启时读到了旧值
	uiMu.Unlock()
	loadPersistedState()
	if on, _ := getCfgStale(); on {
		t.Fatal("复检过的状态被旧磁盘值复活了")
	}
}

func TestHCHistoryRestoresReportAndMarksIt(t *testing.T) {
	withTempState(t)
	rep := HCReport{
		Status: "done", FinishedAt: "2026-11-07 20:00:00", Version: "3.9.0",
		Pass: 20, Fail: 2, Deliverable: false, Conclusion: "暂不能交付",
		Items: []HCItem{
			{ID: "db.connect", Group: hcgDB, Title: "MySQL 可连接", Status: hcFail, Detail: "拒绝访问"},
			{ID: "env.go", Group: hcgEnv, Title: "Go 版本", Status: "pass"},
		},
	}
	hcMu.Lock()
	hcCur, hcHistory = rep, []HCReport{rep}
	hcMu.Unlock()
	savePersistedState()
	// 模拟重启
	hcMu.Lock()
	hcCur, hcHistory = HCReport{Status: "idle"}, nil
	hcMu.Unlock()
	loadPersistedState()
	hcMu.RLock()
	got, n := hcCur, len(hcHistory)
	hcMu.RUnlock()
	if got.Status != "done" || len(got.Items) != 2 || got.Fail != 2 {
		t.Fatalf("重启后报告丢了：%+v", got)
	}
	if !got.Restored {
		t.Fatal("恢复出来的报告没标 Restored——会把旧证据冒充成本次结论")
	}
	if n != 1 {
		t.Fatalf("历史条数 = %d, want 1", n)
	}
}

func TestHCStepKeepsRealTrace(t *testing.T) {
	withTempState(t)
	r := HCReport{Status: "running", Total: hcTotalItems}
	hcStep(&r, "环境组件")
	hcStep(&r, "项目目录")
	if len(r.Steps) != 2 {
		t.Fatalf("留痕 %d 条, want 2", len(r.Steps))
	}
	if r.Steps[0].Done != 1 || r.Steps[0].Title != "环境组件" || r.Steps[0].AtMs == 0 {
		t.Fatalf("第一条留痕不对：%+v", r.Steps[0])
	}
	if r.Steps[1].AtMs < r.Steps[0].AtMs {
		t.Fatal("阶段时间戳倒退，前端回放会乱序")
	}
	if r.Done != 2 {
		t.Fatalf("留痕不该改动真实完成数：Done=%d", r.Done)
	}
}

func TestUIStateHandlerExposesStaleAndHistory(t *testing.T) {
	withTempState(t)
	setCfgStale(true)
	hcMu.Lock()
	hcHistory = []HCReport{{
		Status: "done", FinishedAt: "2026-11-07 20:00:00", Fail: 1,
		Items: []HCItem{{ID: "db.connect", Title: "MySQL 可连接", Status: hcFail}},
	}}
	hcMu.Unlock()
	w := httptest.NewRecorder()
	handleUIState(w, httptest.NewRequest(http.MethodGet, "/api/uistate", nil))
	var got struct {
		CfgStale bool `json:"cfg_stale"`
		History  []struct {
			Fail       int      `json:"fail"`
			FailIDs    []string `json:"fail_ids"`
			FailTitles []string `json:"fail_titles"`
		} `json:"hc_history"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是 JSON：%v / %s", err, w.Body.String())
	}
	if !got.CfgStale || len(got.History) != 1 {
		t.Fatalf("uistate 缺状态或历史：%+v", got)
	}
	if got.History[0].Fail != 1 || len(got.History[0].FailIDs) != 1 ||
		got.History[0].FailIDs[0] != "db.connect" || len(got.History[0].FailTitles) != 1 {
		t.Fatalf("历史摘要没带失败项，前端没法做对比：%+v", got.History[0])
	}
}

func TestConfigBackupSlotWhitelist(t *testing.T) {
	for _, bad := range []int{-1, 0, 4, 99, 9999} {
		if _, ok := configBackupSuffix(bad); ok {
			t.Fatalf("槽位 %d 不该被接受", bad)
		}
		if _, _, err := readConfigBackup(bad); err == nil {
			t.Fatalf("readConfigBackup(%d) 本该直接拒绝", bad)
		}
	}
	for slot, want := range map[int]string{1: ".bak", 2: ".bak2", 3: ".bak3"} {
		got, ok := configBackupSuffix(slot)
		if !ok || got != want {
			t.Fatalf("槽位 %d → %q/%v, want %q", slot, got, ok, want)
		}
	}
}

func TestConfigBackupRejectsCorruptAndNeverLeaksSecrets(t *testing.T) {
	dir := t.TempDir()
	old := configFilePath
	configFilePath = func() string { return filepath.Join(dir, "config.json") }
	defer func() { configFilePath = old }()

	if err := os.WriteFile(filepath.Join(dir, "config.json.bak"), []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readConfigBackup(1); err == nil {
		t.Fatal("损坏的备份必须拒绝，不能拿半份配置覆盖正本")
	}
	w := httptest.NewRecorder()
	handleConfigBackups(w, httptest.NewRequest(http.MethodGet, "/api/config/backups", nil))
	body := w.Body.String()
	if !strings.Contains(body, `"readable":false`) {
		t.Fatalf("损坏备份该在列表里标不可读：%s", body)
	}
	// 摘要只回脱敏字段：写一份带口令的配置，确认口令一个字都不出后端
	good := Config{}
	good.MySQL.Password = "SUPER_SECRET_DB"
	good.Projects.MySQLPass = "SUPER_SECRET_PROJ"
	good.Projects.FrontendDir = `D:\proj\web`
	good.Projects.DBName = "farm"
	good.Chain.ChainHost = "192.168.1.9"
	good.AI = &AIConfig{APIKey: "SUPER_SECRET_KEY"}
	data, _ := json.Marshal(good)
	if err := os.WriteFile(filepath.Join(dir, "config.json.bak2"), data, 0644); err != nil {
		t.Fatal(err)
	}
	c, _, err := readConfigBackup(2)
	if err != nil {
		t.Fatal(err)
	}
	sum := configBackupSummary(c)
	for _, secret := range []string{"SUPER_SECRET_DB", "SUPER_SECRET_PROJ", "SUPER_SECRET_KEY"} {
		if strings.Contains(sum, secret) {
			t.Fatalf("摘要泄露了敏感字段：%s", sum)
		}
	}
	if !strings.Contains(sum, "farm") || !strings.Contains(sum, "192.168.1.9") {
		t.Fatalf("摘要该让用户认得出是哪套环境：%s", sum)
	}
}

func TestConfigRestoreRequiresConfirm(t *testing.T) {
	dir := t.TempDir()
	old := configFilePath
	configFilePath = func() string { return filepath.Join(dir, "config.json") }
	defer func() { configFilePath = old }()
	good := Config{}
	good.SchemaVersion = configSchemaVersion
	good.Projects.DBName = "farm_from_backup"
	data, _ := json.Marshal(good)
	if err := os.WriteFile(filepath.Join(dir, "config.json.bak"), data, 0644); err != nil {
		t.Fatal(err)
	}
	cfgMu.Lock()
	before := cfg.Projects.DBName
	cfgMu.Unlock()
	// 不带 confirm：服务端必须拦在 428，且不能改动当前配置
	w := httptest.NewRecorder()
	handleConfigRestore(w, httptest.NewRequest(http.MethodPost, "/api/config/restore",
		strings.NewReader(`{"slot":1}`)))
	if w.Code != 428 {
		t.Fatalf("未确认的恢复返回 %d, want 428（前端点不点都绕不过服务端闸门）", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"needConfirm":true`) {
		t.Fatalf("428 响应缺确认契约：%s", w.Body.String())
	}
	cfgMu.Lock()
	after := cfg.Projects.DBName
	cfgMu.Unlock()
	if before != after {
		t.Fatal("未确认的恢复竟然改了当前配置")
	}
	// 越界槽位一律 400；多传一个 path 也不能改变读取目标（后端根本不解析它）
	for _, bad := range []string{`{"slot":0,"confirm":true}`, `{"slot":9,"confirm":true}`,
		`{"slot":-1,"confirm":true}`} {
		w2 := httptest.NewRecorder()
		handleConfigRestore(w2, httptest.NewRequest(http.MethodPost, "/api/config/restore", strings.NewReader(bad)))
		if w2.Code != 400 {
			t.Fatalf("非法槽位 %s 返回 %d, want 400", bad, w2.Code)
		}
	}
	w3 := httptest.NewRecorder()
	handleConfigRestore(w3, httptest.NewRequest(http.MethodPost, "/api/config/restore",
		strings.NewReader(`{"slot":1,"path":"C:/Windows/win.ini"}`)))
	if w3.Code != 428 || strings.Contains(w3.Body.String(), "win.ini") {
		t.Fatalf("伪造路径影响了读取目标：%d / %s", w3.Code, w3.Body.String())
	}
}
