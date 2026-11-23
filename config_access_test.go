package main

// config_access_test.go —— 配置快照/更新访问器的边界测试。
//
// 这三条都是"曾经会静默出错"的地方：
//  1. 值拷贝 Config 只复制了头，切片/指针仍与全局共享 —— 改副本会改到全局；
//  2. applyConfigDefaults 写的是全局 cfg —— 导入配置时顺手改了正在运行的内存；
//  3. 导入文件里空着的密码字段必须保留本机密码，不能被默认值 123456 顶掉。

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCloneConfigDeepIsolated(t *testing.T) {
	yes := true
	no := false
	port := 18766
	src := Config{
		Projects: Projects{
			ScanPorts:  []int{80, 443},
			WebScripts: []string{"dev"},
		},
		Services: []Service{{
			ID:        "web",
			DependsOn: []string{"db"},
			Probe: ProbeSpec{
				Kind:  "composite",
				Parts: []ProbeSpec{{Kind: "port", Port: 3000}},
			},
			Verify: VerifySpec{Evidence: []string{"port"}},
		}},
		Components: []Component{{
			Name:      "node",
			CheckArgs: []string{"--version"},
			ExtraEnv:  map[string]string{"A": "1"},
		}},
		AI:     &AIConfig{APIKey: "secret", Quirks: map[string]AIQuirk{"m": {NoTools: true}}},
		Dev:    &DevConfig{TurnTimeoutSecs: 120},
		Lan:    &LanConfig{Enabled: &yes, Port: &port, AllowClip: &no, Devices: []LanDevice{{ID: "dev1"}}},
		MCP:    &MCPConfig{Enabled: &yes},
		Update: &UpdateConfig{Enabled: &yes, IntervalHours: &port},
		Notify: &NotifyConfig{Muted: true},
		Mission: MissionConfig{
			Enabled: &yes,
		},
	}
	// want 必须由独立于 cloneConfigDeep 的路径得到：JSON round-trip。
	// 不能拿 cloneConfigDeep(src) 当基准——浅拷贝下 src/want/c 共享同一底层
	// 对象，改 c 时三者一起变，"两个克隆互比"结构性恒真，改坏了也绿。
	raw, err := json.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	var want Config
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(src, want) {
		t.Fatalf("JSON 快照基准与源不一致，本测试不可用\n源: %+v\n基准: %+v", src, want)
	}

	c := cloneConfigDeep(src)
	c.Projects.ScanPorts[0] = 9999
	c.Projects.WebScripts[0] = "mut"
	c.Services[0].DependsOn[0] = "mut"
	c.Services[0].Probe.Parts[0].Port = 1
	c.Services[0].Verify.Evidence[0] = "mut"
	c.Components[0].CheckArgs[0] = "mut"
	c.Components[0].ExtraEnv["A"] = "mut"
	c.AI.APIKey = "mut"
	c.AI.Quirks["m"] = AIQuirk{NoTools: false}
	c.Dev.TurnTimeoutSecs = 1
	*c.Lan.Enabled = false
	*c.Lan.Port = 1
	*c.Lan.AllowClip = true
	c.Lan.Devices[0].ID = "mut"
	*c.MCP.Enabled = false
	*c.Update.Enabled = false
	*c.Update.IntervalHours = 1
	c.Notify.Muted = false
	*c.Mission.Enabled = false

	if reflect.DeepEqual(c, want) {
		t.Fatal("副本修改没生效，测试夹具本身无效")
	}
	if !reflect.DeepEqual(src, want) {
		t.Fatalf("深拷贝不彻底：改副本影响到了原配置\n原: %+v\n后: %+v", want, src)
	}
}

func TestApplyDefaultsToDoesNotTouchGlobal(t *testing.T) {
	before := cfgSnapshot()

	var in Config
	applyDefaultsTo(&in)
	if in.InstallDir != `C:\EnvKit\tools` {
		t.Errorf("导入副本应拿到默认安装目录，得 %q", in.InstallDir)
	}
	if in.AI == nil || in.AI.Provider != "deepseek" {
		t.Errorf("导入副本应拿到 AI 默认值，得 %+v", in.AI)
	}

	if after := cfgSnapshot(); !reflect.DeepEqual(before, after) {
		t.Errorf("applyDefaultsTo 不应修改全局配置\n前: %+v\n后: %+v", before, after)
	}
}

func TestConfigImportAppliesDefaultsAndKeepsPassword(t *testing.T) {
	dir := t.TempDir()
	oldPath := configFilePath
	configFilePath = func() string { return filepath.Join(dir, "config.json") }
	defer func() { configFilePath = oldPath }()

	// 种一份"本机正在用"的配置：默认值齐全，但每个字段都带可识别的标记。
	cfgMu.Lock()
	cfg = Config{}
	applyDefaultsTo(&cfg)
	cfg.AppName = "global-app"
	cfg.InstallDir = `C:\GlobalTools`
	cfg.Projects.DBName = "farmtrace"
	cfg.Projects.MySQLPass = "global-pass"
	cfg.SchemaVersion = configSchemaVersion
	cfgMu.Unlock()

	// 导入文件模拟"脱敏导出"：默认值字段全空，密码也空。
	body := cfgSnapshot()
	body.InstallDir = ""
	body.Projects.MySQLPass = ""
	body.AppName = "imported"
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/config/import", bytes.NewReader(data))
	w := httptest.NewRecorder()
	handleConfigImport(w, req)
	if w.Code != 200 {
		t.Fatalf("导入应成功，得 %d：%s", w.Code, w.Body.String())
	}

	got := cfgSnapshot()
	// 导入是"整份替换"：补默认值后的导入副本成为新的全局配置。
	if got.InstallDir != `C:\EnvKit\tools` {
		t.Errorf("导入副本应补齐默认安装目录，得 %q", got.InstallDir)
	}
	if got.Projects.MySQLPass != "global-pass" {
		t.Errorf("导入文件里空密码应保留本机密码，得 %q", got.Projects.MySQLPass)
	}
	if got.AppName != "imported" {
		t.Errorf("导入的显式字段应生效，得 %q", got.AppName)
	}

	// 落盘的那份也必须带默认值（而不是把空字段原样写回去）。
	var saved Config
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.InstallDir != `C:\EnvKit\tools` {
		t.Errorf("落盘配置应补齐默认安装目录，得 %q", saved.InstallDir)
	}
}
