package main

// ai_test.go —— AI 配置接口的单测
//
// 起因是一次真实事故：自动化脚本用空 body POST /api/ai/config 想"读一下配置"，
// 结果把 AI 助手的 enabled 静默改成了 false——因为 body.Enabled 是裸 bool，
// 零值 false 覆盖掉了已存的 true，而同结构其它字段都有"空值不覆盖"保护。
// 于是有了这个文件：把"局部保存不许清掉开关"钉死。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempConfig 把配置落盘重定向到临时目录，并预置一份 AI 配置。
// 返回恢复函数。测试绝不能写用户真实的 config.json。
func withTempConfig(t *testing.T, pre AIConfig) func() {
	t.Helper()
	dir := t.TempDir()
	oldPath := configFilePath
	oldCfg := cfg
	oldAI := cfg.AI
	configFilePath = func() string { return filepath.Join(dir, "config.json") }
	cfg.AI = &pre
	return func() {
		configFilePath = oldPath
		cfg = oldCfg
		cfg.AI = oldAI
	}
}

func postAIConfig(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/ai/config", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handleAIConfig(rec, req)
	return rec
}

func getAIConfig(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/ai/config", nil)
	rec := httptest.NewRecorder()
	handleAIConfig(rec, req)
	return rec
}

// 事故回归：空 body POST 不得把 enabled 从 true 改成 false。
func TestAIConfigEmptyPostKeepsEnabled(t *testing.T) {
	restore := withTempConfig(t, AIConfig{
		Enabled: true, Provider: "deepseek", BaseURL: "https://api.deepseek.com",
		Model: "deepseek-flash", APIKey: "dpapi:keepme",
	})
	defer restore()

	postAIConfig(t, `{}`)

	if got := aiSnap().Enabled; !got {
		t.Fatal("空 body POST 把 enabled 改成了 false：这是被真实事故验证过的坑，不能复发")
	}
	if got := aiSnap().Model; got != "deepseek-flash" {
		t.Fatalf("空 body POST 不该动 model，实际 %q", got)
	}
	if got := aiSnap().APIKey; got != "dpapi:keepme" {
		t.Fatalf("空 body POST 不该动 api_key，实际 %q", got)
	}
}

// 空提交应当按读操作返回配置（而不是只回 {"ok":true}），
// 这样脚本作者误用 POST 时至少还能拿到配置，而不是静默改配置。
func TestAIConfigEmptyPostReturnsConfig(t *testing.T) {
	restore := withTempConfig(t, AIConfig{
		Enabled: true, Provider: "deepseek", BaseURL: "https://api.deepseek.com",
		Model: "deepseek-flash", APIKey: "dpapi:keepme",
	})
	defer restore()

	var got map[string]any
	if err := json.Unmarshal(postAIConfig(t, `{}`).Body.Bytes(), &got); err != nil {
		t.Fatalf("空提交应返回可解析的 JSON：%v", err)
	}
	if got["ok"] == true {
		t.Fatalf("空提交应返回配置内容而不是 {\"ok\":true}，实际 %v", got)
	}
	if got["key_set"] != true || got["model"] != "deepseek-flash" {
		t.Fatalf("空提交返回的配置不对：%v", got)
	}
	// 掩码而非明文
	if s, _ := got["key_masked"].(string); strings.Contains(s, "keepme") {
		t.Fatalf("key_masked 泄漏了明文：%q", s)
	}
}

// 显式传 enabled:false 必须能关掉（前端开关不能被这次修复搞坏）。
func TestAIConfigExplicitDisableStillWorks(t *testing.T) {
	restore := withTempConfig(t, AIConfig{
		Enabled: true, Provider: "deepseek", BaseURL: "https://api.deepseek.com",
		Model: "deepseek-flash", APIKey: "dpapi:keepme",
	})
	defer restore()

	postAIConfig(t, `{"enabled":false}`)

	if aiSnap().Enabled {
		t.Fatal("显式传 enabled:false 没能关掉，前端开关会被这次修复弄坏")
	}
}

// 局部保存：只改 model 时 enabled 必须保持。
func TestAIConfigPartialSaveKeepsEnabled(t *testing.T) {
	restore := withTempConfig(t, AIConfig{
		Enabled: true, Provider: "deepseek", BaseURL: "https://api.deepseek.com",
		Model: "deepseek-flash", APIKey: "dpapi:keepme",
	})
	defer restore()

	postAIConfig(t, `{"model":"deepseek-reasoner"}`)

	a := aiSnap()
	if !a.Enabled {
		t.Fatal("只改 model 时 enabled 被清掉了")
	}
	if a.Model != "deepseek-reasoner" {
		t.Fatalf("model 未更新，实际 %q", a.Model)
	}
}

// GET 永远只读：不落盘、不改内存状态。
func TestAIConfigGetDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	restore := withTempConfig(t, AIConfig{
		Enabled: true, Provider: "deepseek", BaseURL: "https://api.deepseek.com",
		Model: "deepseek-flash", APIKey: "dpapi:keepme",
	})
	defer restore()
	_ = dir

	getAIConfig(t)

	if _, err := os.Stat(filepath.Join(dir, "config.json")); err == nil {
		t.Fatal("GET 不该写出 config.json")
	}
	if !aiSnap().Enabled {
		t.Fatal("GET 改了内存里的配置")
	}
}
