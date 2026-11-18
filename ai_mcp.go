package main

// ai_mcp.go —— MCP（Model Context Protocol）服务端
//
// 通过 /api/mcp（Streamable HTTP）把 EnvKit 的**只读**工具暴露给任意 MCP 客户端
// （Claude Desktop、Cursor、其它 Agent 框架等），让外部助手能直接问 EnvKit
// 「环境什么状态」「日志说了什么」「文档里怎么配」——不用再切到 EnvKit 界面。
//
// ## 安全边界（为什么只暴露只读工具）
//
// 写工具（启停服务、备份/恢复、杀进程）在 EnvKit 的 AI 对话里走「确认卡续跑」：
// PolicyGate 判档 → 前端弹确认 → 用户点头 → 才真正执行。这条链的确认环节
// 在 AI 对话循环（aiRunLoop）里，MCP 客户端**没有确认卡可弹**——
// 如果把写工具暴露出去，等价于给外部助手一张"免确认执行卡"。
// 所以这里的规则是结构性的，不是约定：
//
//	暴露集合 = registry 里 Write==false ∧ policyRules 里 Level==PolicyAuto
//
// 两个条件缺一不可（见 ai_mcp_test.go 的双向变异：只看一半会漏）。
// 新增只读工具只要在 policyRules 里登记为 auto 就自动出现在 MCP 侧，
// 不用改这里；写工具无论怎么登记都出不去。
//
// ## 其余闸门全部复用，不建第二套
//
//   - 令牌闸：/api/mcp 走 DefaultServeMux，外层 securityMW(recoverMW(authMW(...)))
//     自动生效——MCP 客户端同样要带 X-EnvKit-Token。
//   - 参数校验：aiExecTool 内的 aiValidateToolArgs（jsonschema/v6），MCP 侧不重写。
//   - 只读上下文（手机端）闸：aiExecTool 自带，MCP 调用同样受限。
//   - 轨迹/审计：aiExecTool 内统一记录，actor 沿用 actAI。
//
// 参数校验为什么放在 handler 而不是靠 SDK 自动校验：Server.AddTool 的 raw
// 形式不做输入校验（校验责任在 handler），正好——我们已经在 aiExecTool 里
// 有一套带中文字段说明的校验，MCP 客户端拿到的是同一份错误文案。

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// aiMCPTools 计算 MCP 侧应暴露的工具名单（按名字排序，保证输出稳定、可测）。
// 规则见文件头注释：Write 闸与 policy 档位闸都要过。
func aiMCPTools() []string {
	names := make([]string, 0, len(aiToolRegistry))
	for name, t := range aiToolRegistry {
		if t.Write {
			continue // 写工具一律不暴露：MCP 客户端没有确认卡，绕不开就不给
		}
		r, ok := policyRules[name]
		if !ok || r.Level != PolicyAuto {
			continue // 未登记 / 非自动档的也不暴露：缺省确认是安全缺省
		}
		names = append(names, name)
	}
	// 排序：map 遍历顺序不定，tools/list 的顺序要稳定（对测试与客户端缓存都友好）。
	sort.Strings(names)
	return names
}

// aiMCPHandler 构造挂在 /api/mcp 上的 Streamable HTTP handler。
// 每个 EnvKit 请求共用同一个 *mcp.Server（无会话内状态，工具全是只读）。
func aiMCPHandler() http.Handler {
	srv := mcp.NewServer(&mcp.Implementation{Name: "envkit", Version: appVersion}, nil)
	for _, name := range aiMCPTools() {
		t := aiToolRegistry[name]
		// schema 序列化失败的兜底：跳过该工具，比带着坏 schema 上线好。
		// 正常情况下不会失败——registry 的 schema 全是 map[string]any 字面量。
		schema, err := json.Marshal(t.Schema)
		if err != nil {
			auditNow(actAI, "mcp_tool_skip", name, "", resDenied, "schema 序列化失败："+err.Error())
			continue
		}
		tool := &mcp.Tool{
			Name:        name,
			Description: t.Desc,
			InputSchema: json.RawMessage(schema),
		}
		// 闭包捕获迭代变量 name：go 1.22+ 每轮迭代是新变量，无循环变量坑。
		srv.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args map[string]any
			if len(req.Params.Arguments) > 0 {
				if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
					// 参数不是合法 JSON 对象：作为工具错误回给客户端（isError=true），
					// 让调用方模型能读到原因并自行修正，而不是收到协议层异常。
					return mcpTextErr("参数必须是 JSON 对象：" + err.Error()), nil
				}
			}
			out, execErr := aiExecTool(name, args)
			if execErr != nil {
				return mcpTextErr(execErr.Error()), nil
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: out}},
			}, nil
		})
	}
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
}

// mcpTextErr 把工具执行错误包成 isError=true 的文本结果。
// MCP 规范：工具失败应作为结果返回（isError:true），不是协议层 error——
// 后者表示"这次 RPC 本身没完成"，客户端模型看不到错误内容就无法自愈。
func mcpTextErr(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

// handleMCP 惰性构造 handler：工具表在所有 init 都跑完之后才是最终形状，
// 包级变量在 main 前求值有顺序风险，所以推迟到首个请求。
func handleMCP(w http.ResponseWriter, r *http.Request) {
	mcpHandlerOnce.Do(func() { mcpHandler = aiMCPHandler() })
	mcpHandler.ServeHTTP(w, r)
}

var (
	mcpHandlerOnce sync.Once
	mcpHandler     http.Handler
)
