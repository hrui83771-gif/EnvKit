package main

// mcpconf.go —— MCP 服务开关与连接配置的出入口 /api/mcp/config（v3.8）。
//
// 与 handleLanConfig / handleDevConfig 同一范式：GET 回显生效值、
// POST 按指针字段增量提交（nil-guard merge 是唯一写入口）。
//
// ## 为什么开关放这里而不是 /api/config
//
// 同 Pet/Lan/Dev 的理由：老页面 configBody() 不回传 mcp 段，
// 整包保存会在任何一次自动保存里把它抹成零值。独立出入口 + mergeFormConfig
// 指针保护，两边只在"谁写谁的段"这一层耦合。
//
// ## 为什么 GET 里带令牌
//
// 「复制连接配置」按钮需要一份**此刻**能用的配置（URL + 令牌），粘贴到
// Claude Desktop / Cursor 就能连。令牌与控制台页面共用（12 小时惰性轮换），
// 页面刷新拿新令牌，MCP 客户端不会——所以配置块必须每次实时生成，
// 且如实标注有效期。这个接口本身就在令牌闸后面（authMW），
// 拿到令牌的人才能读到令牌，没有扩大暴露面。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

func handleMCPConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, mcpConfigView())
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}

	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body); err != nil {
		http.Error(w, "参数错误", 400)
		return
	}
	if body.Enabled == nil {
		writeJSON(w, mcpConfigView())
		return
	}

	cfgMu.Lock()
	if cfg.MCP == nil {
		cfg.MCP = &MCPConfig{}
	}
	before := mcpOnLocked()
	v := *body.Enabled
	cfg.MCP.Enabled = &v
	after := v
	saved := cfg
	cfgMu.Unlock()

	if before != after {
		mcpPersist(saved)
		state := "关闭"
		if after {
			state = "开启"
		}
		auditNow(actUser, "mcp_config", "config.json", "mcp", resOK, "MCP 服务"+state)
		info(scSys, "MCP", "对外工具服务已%s（端点 /api/mcp）", state)
	}
	// 回显与 GET 同一份，页面不用自己拼
	writeJSON(w, mcpConfigView())
}

// mcpPersist 落盘钩子。默认存 config.json；单测替换成空操作，
// 否则跑一次测试就会把真实的 mcp 段写进工作区 config.json（同 lanPersist 惯例）。
var mcpPersist = func(c Config) { saveExternalConfig(c) }

// mcpOnLocked 在已持 cfgMu 时读开关（避免 mcpOn 二次加锁死锁）。
func mcpOnLocked() bool {
	return cfg.MCP != nil && cfg.MCP.Enabled != nil && *cfg.MCP.Enabled
}

// mcpConfigView 设置页「MCP 集成」卡的唯一数据来源（GET / POST 回显共用）。
func mcpConfigView() map[string]any {
	enabled := mcpOn()
	port := listenPortCur
	if port == 0 {
		// 测试环境 / 启动早期：退回配置端口，保证 URL 仍是完整可拼的
		port = devSnap().serverPort()
	}
	tools := aiMCPTools()
	url := fmt.Sprintf("http://127.0.0.1:%d/api/mcp", port)
	token := currentToken()
	return map[string]any{
		"ok":         true,
		"enabled":    enabled,
		"url":        url,
		"token":      token,
		"tools":      tools,
		"tool_count": len(tools),
		// 连接配置块：后端拼好（mcpSnippet 是唯一实现），前端只负责复制。
		// 两边各拼一份 JSON 的话，形状对不上时谁也发现不了。
		"snippet": mcpSnippet(url, token),
		// 令牌与页面共用、超时会惰性轮换（见 state.go currentToken）：
		// 这里把小时数如实给出去，界面照实标注，不假装"长期有效"。
		"token_ttl_hours": int(apiTokenTTL.Hours()),
	}
}

// mcpSnippet 按 MCP 客户端通用格式拼连接配置。
// 独立成纯函数：单测直接断形状，不用起 HTTP。
func mcpSnippet(url, token string) string {
	// 用 map 构造再 Marshal 而不是 fmt.Sprintf 拼 JSON：
	// token 是十六进制串不会带特殊字符，但这样写连"将来加字段要转义"的坑都一起堵了。
	b, _ := json.MarshalIndent(map[string]any{
		"mcpServers": map[string]any{
			"envkit": map[string]any{
				"type":    "http",
				"url":     url,
				"headers": map[string]string{"X-EnvKit-Token": token},
			},
		},
	}, "", "  ")
	return string(b)
}
