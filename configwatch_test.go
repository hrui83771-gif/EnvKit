package main

// configwatch_test.go —— 热重载最容易错的三件事，逐条钉住：
//   1. 「自己写下去的」不能被当成「用户改了」再重载一遍（否则保存配置后立刻回滚）；
//   2. AI 段不许被 cfgApplyReload 换掉（它由 aiCfgMu 保护，换就是跨锁改数据）；
//   3. 解析失败的文件不许覆盖内存里的好配置（半截 JSON 是编辑器分块写的常态）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCfgDiskHashMarksSelfWrite(t *testing.T) {
	cfgMarkDisk([]byte(`{"app_name":"A"}`))
	if cfgDiskKnown() != cfgHash([]byte(`{"app_name":"A"}`)) {
		t.Fatal("登记后应能认出同一份内容")
	}
	if cfgDiskKnown() == cfgHash([]byte(`{"app_name":"B"}`)) {
		t.Fatal("不同内容不该被认成同一份")
	}
}

func TestCfgApplyReloadKeepsAIRunning(t *testing.T) {
	cfgMu.Lock()
	backup := cloneConfigDeep(cfg)
	cfg.AI = &AIConfig{Enabled: true, APIKey: "OLD-KEY", BaseURL: "https://old.example", Model: "old"}
	cfg.AppName = "旧名字"
	cfgMu.Unlock()
	defer func() { cfgMu.Lock(); cfg = backup; cfgMu.Unlock() }()

	next := Config{AppName: "新名字", AI: &AIConfig{Enabled: true, APIKey: "NEW-KEY"}}
	aiChanged, future := cfgApplyReload(next)
	if !aiChanged {
		t.Fatal("文件里的 AI 与内存不一致时必须回报 aiChanged（调用方要提示重启）")
	}
	if future {
		t.Fatal("同版本 schema 不该被判成未来版本")
	}

	cfgMu.Lock()
	defer cfgMu.Unlock()
	if cfg.AppName != "新名字" {
		t.Fatalf("普通字段应被热重载：得到 %q", cfg.AppName)
	}
	if cfg.AI == nil || cfg.AI.APIKey != "OLD-KEY" {
		t.Fatalf("AI 段被热重载覆盖了（跨锁改数据）：%+v", cfg.AI)
	}
}

// TestCfgApplyReloadKeepsLanDevices 已配对的手机不跟着文件走。
//
// 典型热重载场景是"用户拿编辑器改了别的字段"，而那个编辑器缓冲区往往在配对**之前**
// 就打开了：照抄文件里的 lan 段等于把刚配好的手机静默踢掉，且没有任何提示。
// 设备表由 EnvKit 自己维护（配对 / 移除是唯一写入口），这里必须保留内存里的那份。
func TestCfgApplyReloadKeepsLanDevices(t *testing.T) {
	cfgMu.Lock()
	backup := cloneConfigDeep(cfg)
	cfg.Lan = &LanConfig{
		Enabled: boolp(true),
		Port:    intp(18766),
		Devices: []LanDevice{
			{ID: "aaaaaaaa", Name: "已配对的手机", Hash: "deadbeef", Created: 111, Last: 222},
		},
	}
	cfgMu.Unlock()
	defer func() { cfgMu.Lock(); cfg = backup; cfgMu.Unlock() }()

	// 文件里：改端口，但 lan 段是配对前的样子（没有 devices）
	cfgApplyReload(Config{
		AppName: "新名字",
		Lan:     &LanConfig{Enabled: boolp(true), Port: intp(19000)},
	})

	cfgMu.Lock()
	defer cfgMu.Unlock()
	if cfg.Lan == nil {
		t.Fatal("文件里的 lan 段不该被丢掉")
	}
	if len(cfg.Lan.Devices) != 1 || cfg.Lan.Devices[0].ID != "aaaaaaaa" {
		t.Fatalf("设备表被热重载覆盖了（已配对的手机被静默踢掉）：%+v", cfg.Lan.Devices)
	}
	// 其余字段照常跟进
	if cfg.Lan.port() != 19000 {
		t.Fatalf("端口没跟上文件：%d", cfg.Lan.port())
	}
	if !cfg.Lan.on() {
		t.Fatal("开关没跟上文件")
	}
}

func TestCfgApplyReloadDetectsFutureSchema(t *testing.T) {
	cfgMu.Lock()
	backup := cloneConfigDeep(cfg)
	cfgMu.Unlock()
	defer func() { cfgMu.Lock(); cfg = backup; cfgMu.Unlock() }()

	_, future := cfgApplyReload(Config{SchemaVersion: configSchemaVersion + 1})
	if !future {
		t.Fatal("更高 schema 版本应被标记为 future，以便提示「按已知字段加载」")
	}
}

func TestCfgReloadIfChangedAppliesAndSkipsSelfWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfgMu.Lock()
	backup := cloneConfigDeep(cfg)
	cfgMu.Unlock()
	defer func() { cfgMu.Lock(); cfg = backup; cfgMu.Unlock() }()

	writeCfg := func(app string) []byte {
		t.Helper()
		b, err := json.Marshal(Config{AppName: app})
		if err != nil {
			t.Fatalf("构造测试配置失败：%v", err)
		}
		if err := os.WriteFile(path, b, 0644); err != nil {
			t.Fatalf("写测试配置失败：%v", err)
		}
		return b
	}

	// ① 外部改文件 → 应重载
	data := writeCfg("文件里的名字")
	cfgMarkDisk([]byte("这条基线是别的内容，确保哈希不同"))
	cfgReloadIfChanged(path)
	cfgMu.Lock()
	got := cfg.AppName
	cfgMu.Unlock()
	if got != "文件里的名字" {
		t.Fatalf("外部修改应触发重载，AppName 得到 %q", got)
	}

	// ② 自己刚写下去的那份（哈希已登记）→ 不许重载
	//    这条是回环抑制的核心：失效的话，用户每次点保存都会立刻被自己写的内容覆盖回去。
	cfgMarkDisk(data)
	cfgMu.Lock()
	cfg.AppName = "内存里改过的"
	cfgMu.Unlock()
	cfgReloadIfChanged(path)
	cfgMu.Lock()
	got = cfg.AppName
	cfgMu.Unlock()
	if got != "内存里改过的" {
		t.Fatalf("自写抑制失效：内存里的值被自己写回的文件覆盖成了 %q", got)
	}
}

func TestCfgReloadIfChangedIgnoresBrokenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"app_name":"半截`), 0644); err != nil {
		t.Fatal(err)
	}

	cfgMu.Lock()
	backup := cloneConfigDeep(cfg)
	cfg.AppName = "内存里的好配置"
	cfgMu.Unlock()
	defer func() { cfgMu.Lock(); cfg = backup; cfgMu.Unlock() }()

	cfgMarkDisk([]byte("别的基线"))
	cfgReloadIfChanged(path) // 内部会等 1 秒重试一次，然后放弃

	cfgMu.Lock()
	got := cfg.AppName
	cfgMu.Unlock()
	if got != "内存里的好配置" {
		t.Fatalf("解析失败的文件不许覆盖内存配置，得到 %q", got)
	}
}
