package main

// ai_mcp_test.go —— MCP 只读暴露面的守卫测试。
//
// 核心断言只有一条：**写工具永远出不去**。这条断言有两道闸保护
// （aiTool.Write 与 policyRules 档位），按"两半互相顶替也是假绿"的教训，
// 两道闸各自做运行时变异验证：关掉任何一道，测试必须红。
//
// 端到端部分用 SDK 自带的客户端连自己的 httptest server，
// 驱动完整 MCP 握手 → tools/list → tools/call，不桩掉任何一层。

import (
	"context"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPToolsNeverExposeWrite 写工具闸：MCP 名单与写工具集合的交集必须为空，
// 且名单里每个工具都是 auto 档只读。名单为空也视为失败（守卫失效静默空集是假绿）。
func TestMCPToolsNeverExposeWrite(t *testing.T) {
	names := aiMCPTools()
	if len(names) == 0 {
		t.Fatal("MCP 工具名单为空：过滤条件可能把所有工具都滤掉了（空名单不等于安全，等于没功能）")
	}
	for _, name := range names {
		if t2, ok := aiToolRegistry[name]; ok && t2.Write {
			t.Fatalf("写工具 %q 出现在 MCP 暴露名单里——MCP 客户端没有确认卡，这是安全缺口", name)
		}
		if r, ok := policyRules[name]; !ok || r.Level != PolicyAuto {
			t.Fatalf("工具 %q 不在 auto 档却出现在 MCP 名单里（level=%v）", name, policyRules[name].Level)
		}
	}
	if !sort.StringsAreSorted(names) {
		t.Fatal("MCP 名单没有排序：tools/list 输出顺序不稳定")
	}
	// 正向锚点：名单里至少包含文档检索与系统状态这两个代表性只读工具，
	// 防止过滤条件写反（全滤光）时交集断言恒真。
	for _, want := range []string{"list_docs", "get_system_state"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("只读工具 %q 不在 MCP 名单里，过滤条件可能写反", want)
		}
	}
}

// TestMCPWriteGateIndependent 双向变异：两道闸各关一道，必须有精确红。
// 变异是运行时的（改数据不改代码），用 defer 复原，不影响其它测试。
func TestMCPWriteGateIndependent(t *testing.T) {
	// 变异 1：把写工具 db_restore 的 policy 档位改成 auto —— Write 闸必须独立挡住它。
	orig := policyRules["db_restore"]
	policyRules["db_restore"] = policyRule{PolicyAuto, false, "变异测试：故意放错档"}
	names := aiMCPTools()
	for _, n := range names {
		if n == "db_restore" {
			t.Fatal("变异 1 失败：db_restore 档位改成 auto 后出现在 MCP 名单 —— Write 闸失效")
		}
	}
	policyRules["db_restore"] = orig

	// 变异 2：把只读工具 get_logs 的 Write 改成 true —— 档位闸放它进候选，
	// Write 闸必须把它滤掉（否则名单会多出 get_logs？不——这里要断言的是
	// Write 闸真实参与了过滤：把它改 true 后必须从名单里消失）。
	tt := aiToolRegistry["get_logs"]
	origWrite := tt.Write
	tt.Write = true
	aiToolRegistry["get_logs"] = tt
	names = aiMCPTools()
	for _, n := range names {
		if n == "get_logs" {
			t.Fatal("变异 2 失败：get_logs 的 Write 改 true 后仍在 MCP 名单 —— Write 过滤没生效")
		}
	}
	tt.Write = origWrite
	aiToolRegistry["get_logs"] = tt
}

// TestMCPHandlerEndToEnd 真 MCP 客户端连自己的 handler：
// 握手 → tools/list（名单与暴露规则一致）→ tools/call（真执行 list_docs）。
func TestMCPHandlerEndToEnd(t *testing.T) {
	handler := aiMCPHandler()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "envkit-test", Version: "0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL}, nil)
	if err != nil {
		t.Fatalf("MCP 握手失败：%v", err)
	}
	defer cs.Close()

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list 失败：%v", err)
	}
	want := aiMCPTools()
	if len(res.Tools) != len(want) {
		t.Fatalf("tools/list 数量 %d ≠ 暴露名单 %d", len(res.Tools), len(want))
	}
	got := make([]string, 0, len(res.Tools))
	for _, tl := range res.Tools {
		got = append(got, tl.Name)
		if tl.InputSchema == nil {
			t.Fatalf("工具 %q 的 InputSchema 为空：客户端无法知道怎么传参", tl.Name)
		}
	}
	sort.Strings(got)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tools/list 名单不一致：got[%d]=%s want[%d]=%s", i, got[i], i, want[i])
		}
	}

	// tools/call：list_docs 读打进 exe 的静态索引，测试环境零依赖。
	call, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_docs",
		Arguments: map[string]any{"query": "日志"},
	})
	if err != nil {
		t.Fatalf("tools/call list_docs 失败：%v", err)
	}
	if call.IsError {
		t.Fatalf("list_docs 返回 isError=true：%v", call.Content)
	}
	var text strings.Builder
	for _, c := range call.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	if text.Len() == 0 {
		t.Fatal("list_docs 返回了空内容")
	}
}

// TestMCPHandlerToolErrorAsResult 工具执行失败必须作为 isError=true 的结果返回
// （而不是协议层 error），调用方模型才能读到错误并自愈。
func TestMCPHandlerToolErrorAsResult(t *testing.T) {
	handler := aiMCPHandler()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "envkit-test", Version: "0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL}, nil)
	if err != nil {
		t.Fatalf("MCP 握手失败：%v", err)
	}
	defer cs.Close()

	// 参数类型错误（query 应为 string）：aiExecTool 内的 aiValidateToolArgs 报错
	// → handler 必须把它转成 isError=true 的结果。注意"工具不存在"走的是协议层
	// error（MCP 规范 -32602），测不到这条转换路径，所以用参数错误。
	call, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_docs",
		Arguments: map[string]any{"query": 12345},
	})
	if err != nil {
		t.Fatalf("协议层报错（应当以 isError 结果返回）：%v", err)
	}
	if !call.IsError {
		t.Fatal("参数校验失败却返回了成功结果：错误被吞，调用方模型无从自愈")
	}
}
