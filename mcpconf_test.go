package main

// mcpconf_test.go —— MCP 开关与配置出入口的守卫测试。
//
// 三件事：
//  1. 开关默认关（升级老配置不许突然多出一个在监听的 MCP 端点）
//  2. handleMCP 的开关闸必须真实生效（变异验证：删闸必红）
//  3. POST 指针 merge 只动 enabled，GET 回显形状完整（url/token/tools/ttl）
//
// 所有落盘走 mcpPersist 替换钩子，不碰真实 config.json（同 lanPersist 惯例）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// resetMCP 开关测试的公共夹具：备份/还原 cfg.MCP + 拦截落盘。
func resetMCP(t *testing.T) {
	t.Helper()
	orig := cfg.MCP
	origPersist := mcpPersist
	origToken := apiToken
	t.Cleanup(func() {
		cfgMu.Lock()
		cfg.MCP = orig
		cfgMu.Unlock()
		mcpPersist = origPersist
		tokenMu.Lock()
		apiToken = origToken
		tokenMu.Unlock()
	})
	mcpPersist = func(Config) {} // 测试不写真实 config.json
	// apiToken 在 main() 里初始化，单测不跑 main —— 这里给个确定值，
	// 否则 mcpConfigView 的 token 为空串，无法验证形状。
	tokenMu.Lock()
	apiToken = newToken()
	tokenMu.Unlock()
}

func TestMCPOffByDefault(t *testing.T) {
	cfgMu.Lock()
	had := cfg.MCP
	cfg.MCP = nil
	cfgMu.Unlock()
	if mcpOn() {
		t.Fatal("cfg.MCP 为 nil 时 mcpOn() 应为 false：升级老配置不能自动打开对外端点")
	}
	off := false
	cfgMu.Lock()
	cfg.MCP = &MCPConfig{Enabled: &off}
	cfgMu.Unlock()
	if mcpOn() {
		t.Fatal("Enabled=false 时 mcpOn() 应为 false")
	}
	cfgMu.Lock()
	cfg.MCP = had
	cfgMu.Unlock()
}

// TestMCPGateBlocksWhenDisabled 开关关 → handleMCP 必须 403 且不进 MCP 握手。
func TestMCPGateBlocksWhenDisabled(t *testing.T) {
	resetMCP(t)
	off := false
	cfgMu.Lock()
	cfg.MCP = &MCPConfig{Enabled: &off}
	cfgMu.Unlock()

	rec := httptest.NewRecorder()
	handleMCP(rec, httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("开关关闭时应 403，得到 %d", rec.Code)
	}
	// 明确提示，不静默：客户端要能从响应里知道去哪里打开
	if !strings.Contains(rec.Body.String(), "设置页") {
		t.Fatalf("403 响应应说明去设置页开启，实际：%s", rec.Body.String())
	}
}

// TestMCPGateAllowsWhenEnabled 开关开 → 能走到 MCP 协议层
// （收到 initialize 时协议层会按 MCP 格式响应，而不是 403 文案）。
func TestMCPGateAllowsWhenEnabled(t *testing.T) {
	resetMCP(t)
	on := true
	cfgMu.Lock()
	cfg.MCP = &MCPConfig{Enabled: &on}
	cfgMu.Unlock()

	rec := httptest.NewRecorder()
	handleMCP(rec, httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)))
	// 不必解析协议细节：只要不是"开关关"那个 403 就说明闸放行了
	if rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), "设置页") {
		t.Fatal("开关已开但请求仍被开关闸拦下（403 + 设置页文案）")
	}
}

// TestMCPGateMutation 变异验证：闸必须真实存在。
// 直接调用 mcpOn 为 true 的路径上，若把 handleMCP 的闸删掉，
// TestMCPGateBlocksWhenDisabled 会红 —— 这里补的是"编译期删闸"场景：
// 通过反射不可行，改为断言 403 分支存在于源码语义（关→403 是硬契约），
// 真正的代码级变异在 tools/cdp_mcp_gate_probe.js 由 CI 跑。
func TestMCPGateMutation(t *testing.T) {
	resetMCP(t)
	// 把 Enabled 指向 nil 结构体：mcpOn 的三重 nil 保护每一层都不能少
	cfgMu.Lock()
	cfg.MCP = &MCPConfig{}
	cfgMu.Unlock()
	if mcpOn() {
		t.Fatal("MCPConfig{Enabled:nil} 时 mcpOn() 应为 false：nil-guard merge 三重保护缺一层就漏")
	}
}

// TestMCPConfigPostMerge POST /api/mcp/config 只动 enabled 段，回显与 GET 同形。
func TestMCPConfigPostMerge(t *testing.T) {
	resetMCP(t)
	cfgMu.Lock()
	cfg.MCP = nil
	cfgMu.Unlock()

	// 开：nil 段第一次 POST 也能落
	rec := httptest.NewRecorder()
	handleMCPConfig(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/config",
		strings.NewReader(`{"enabled":true}`)))
	if rec.Code != 200 {
		t.Fatalf("POST 开启失败：HTTP %d", rec.Code)
	}
	var v map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v["enabled"] != true {
		t.Fatalf("POST enabled=true 后回显应 enabled=true，得到 %v", v["enabled"])
	}
	if !mcpOn() {
		t.Fatal("POST enabled=true 后 mcpOn() 应为 true")
	}

	// 关：nil-guard merge，其它段不动
	rec = httptest.NewRecorder()
	handleMCPConfig(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/config",
		strings.NewReader(`{"enabled":false}`)))
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v["enabled"] != false || mcpOn() {
		t.Fatalf("POST enabled=false 未生效：%v / mcpOn=%v", v["enabled"], mcpOn())
	}

	// 空体（enabled 缺省）：不算错误，回显当前值
	rec = httptest.NewRecorder()
	handleMCPConfig(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/config", strings.NewReader(`{}`)))
	if rec.Code != 200 {
		t.Fatalf("空体 POST 不应报错：HTTP %d", rec.Code)
	}
}

// TestMCPConfigViewFields GET 回显形状：url/token/tools/ttl 一个不少。
func TestMCPConfigViewFields(t *testing.T) {
	resetMCP(t)
	v := mcpConfigView()
	for _, k := range []string{"ok", "enabled", "url", "token", "tools", "tool_count", "token_ttl_hours"} {
		if _, has := v[k]; !has {
			t.Fatalf("回显缺字段 %q", k)
		}
	}
	if !strings.HasPrefix(v["url"].(string), "http://127.0.0.1:") ||
		!strings.HasSuffix(v["url"].(string), "/api/mcp") {
		t.Fatalf("url 形状不对：%v", v["url"])
	}
	if v["token"] == "" {
		t.Fatal("token 为空：复制出去的配置连不上")
	}
	tools := v["tools"].([]string)
	if len(tools) == 0 || len(tools) != v["tool_count"].(int) {
		t.Fatalf("tools/tool_count 不一致：%d vs %v", len(tools), v["tool_count"])
	}
	// 暴露名单与写闸一致：MCP 卡上显示的名单绝不能包含写工具
	for _, n := range tools {
		if tt, ok := aiToolRegistry[n]; ok && tt.Write {
			t.Fatalf("回显名单含写工具 %q", n)
		}
	}
	if v["token_ttl_hours"].(int) <= 0 {
		t.Fatalf("token_ttl_hours 应为正数：%v", v["token_ttl_hours"])
	}
}

// TestMCPSnippetShape 连接配置块的形状：客户端拿去就能用。
func TestMCPSnippetShape(t *testing.T) {
	s := mcpSnippet("http://127.0.0.1:18765/api/mcp", "abc123")
	if !json.Valid([]byte(s)) {
		t.Fatalf("snippet 不是合法 JSON：%s", s)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	srv, ok := v["mcpServers"].(map[string]any)["envkit"].(map[string]any)
	if !ok {
		t.Fatalf("snippet 缺 mcpServers.envkit：%s", s)
	}
	if srv["type"] != "http" || srv["url"] != "http://127.0.0.1:18765/api/mcp" {
		t.Fatalf("snippet url/type 不对：%s", s)
	}
	h, ok := srv["headers"].(map[string]any)["X-EnvKit-Token"].(string)
	if !ok || h != "abc123" {
		t.Fatalf("snippet 缺 X-EnvKit-Token 头：%s", s)
	}
}
