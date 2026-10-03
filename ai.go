package main

// EnvKit AI 助手：配置与密钥管理（OpenAI 兼容协议，Key 走 DPAPI 加密存储）。
// 对话循环见 ai_loop.go，工具注册表见 ai_tools.go。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type AIConfig struct {
	Enabled     bool    `json:"enabled"`
	Provider    string  `json:"provider"`
	BaseURL     string  `json:"base_url"`
	APIKey      string  `json:"api_key"`
	Model       string  `json:"model"`
	Temperature float64 `json:"temperature"`
	MaxTokens   int     `json:"max_tokens"`
	// MemoryEnabled 记忆层总开关（v2.0）。默认关闭——新能力不该在升级后立刻改变
	// 既有 Agent 行为，须由使用者显式确认才介入提示词。
	MemoryEnabled bool `json:"memory_enabled"`
	// Quirks 按模型记录上游参数怪癖（自动学习，避免每次请求都踩同一个坑）
	Quirks map[string]AIQuirk `json:"quirks,omitempty"`
}

// aiMemoryOn 记忆层是否启用。未启用时：快照不注入、记忆类工具不注册、面板只读。
func aiMemoryOn() bool {
	return aiSnap().MemoryEnabled
}

// AIQuirk 某个模型的上游参数怪癖（全部可自动学习并持久化）
type AIQuirk struct {
	TempMode       string `json:"temp_mode,omitempty"`        // "" 用配置值 | "one" 固定 1 | "none" 不发送
	MaxTokensField string `json:"max_tokens_field,omitempty"` // "" 用 max_tokens | "max_completion_tokens"
	NoTools        bool   `json:"no_tools,omitempty"`         // 模型不支持 function calling
}

// quirkRead 只读版本（值接收者，供快照使用，不会并发写 map）
func (a AIConfig) quirkRead() AIQuirk {
	if a.Quirks == nil {
		return AIQuirk{}
	}
	return a.Quirks[a.Model]
}

func (a *AIConfig) setQuirk(q AIQuirk) {
	if a.Quirks == nil {
		a.Quirks = map[string]AIQuirk{}
	}
	a.Quirks[a.Model] = q
}

// aiPayload 统一构造请求体：按模型怪癖决定是否携带 temperature / 用哪个 token 字段 / 是否带 tools
func aiPayload(msgs []aiMsg, tools []aiUpstreamTool, q AIQuirk, tempOverride float64, stream bool) map[string]any {
	ac := aiSnap()
	p := map[string]any{"model": ac.Model, "messages": msgs}
	switch q.TempMode {
	case "one":
		p["temperature"] = 1.0
	case "none":
		// 明确不发 temperature
	default:
		if tempOverride >= 0 {
			p["temperature"] = tempOverride
		} else {
			p["temperature"] = ac.Temperature
		}
	}
	if q.MaxTokensField == "max_completion_tokens" {
		p["max_completion_tokens"] = ac.MaxTokens
	} else {
		p["max_tokens"] = ac.MaxTokens
	}
	if tools != nil && !q.NoTools {
		p["tools"] = tools
	}
	if stream {
		p["stream"] = true
		// DeepSeek 专属：让最后一笔回传 usage（token 用量）。其他自定义服务商不加持，
		// 避免个别实现遇到不认识的字段直接 400。
		if isDeepSeekBase(ac.BaseURL) {
			p["stream_options"] = map[string]any{"include_usage": true}
		}
	}
	return p
}

// isDeepSeekBase 判断 Base URL 是否指向 DeepSeek 官方（余额 / usage 细分接口为 DeepSeek 专有）
func isDeepSeekBase(base string) bool {
	h := urlHostOf(base)
	return h == "api.deepseek.com" || strings.HasSuffix(h, ".deepseek.com")
}

// aiFixParamError 依据上游报错自适应参数（通用规则，不绑定厂商）。
// 返回新的怪癖、是否已调整（可重试）、给用户看的提示。
func aiFixParamError(msg string) (AIQuirk, bool, string) {
	low := strings.ToLower(msg)
	q := aiSnap().quirkRead()
	switch {
	case strings.Contains(low, "temperature"):
		if q.TempMode == "" {
			q.TempMode = "one"
			return q, true, "上游拒绝当前 temperature，已改用 1 重试"
		}
		if q.TempMode == "one" {
			q.TempMode = "none"
			return q, true, "上游仍拒绝 temperature，已改为不发送该参数重试"
		}
		return q, false, ""
	case strings.Contains(low, "max_completion_tokens"):
		if q.MaxTokensField != "max_completion_tokens" {
			q.MaxTokensField = "max_completion_tokens"
			return q, true, "该模型要求用 max_completion_tokens，已自动切换"
		}
		return q, false, ""
	case strings.Contains(low, "max_tokens"):
		if q.MaxTokensField != "max_completion_tokens" {
			q.MaxTokensField = "max_completion_tokens"
			return q, true, "该模型不接受 max_tokens，已改用 max_completion_tokens 重试"
		}
		return q, false, ""
	case strings.Contains(low, "tool") && (strings.Contains(low, "support") || strings.Contains(low, "unsupported") || strings.Contains(low, "not allowed") || strings.Contains(low, "unknown")):
		if !q.NoTools {
			q.NoTools = true
			return q, true, "该模型不支持工具调用，已降级为纯问答（写操作请点界面按钮）"
		}
		return q, false, ""
	}
	return q, false, ""
}

// aiProviderPreset 服务商预置：新增服务商/模型只需更新此列表；任意服务商都可用「自定义」手填。
type aiProviderPreset struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	BaseURL string   `json:"base_url"`
	Models  []string `json:"models"`
}

var aiProviderPresets = []aiProviderPreset{
	{"deepseek", "DeepSeek", "https://api.deepseek.com", []string{"deepseek-chat", "deepseek-reasoner"}},
	{"custom", "自定义", "", nil},
}

func handleAIProviders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"providers": aiProviderPresets})
}

// urlHostOf 提取 URL 的 host（小写）；解析失败或无 host 返回空串
func urlHostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Host)
}

// handleAIBalance 查询 DeepSeek 账户余额（GET /api/ai/balance）。
// 余额接口是 DeepSeek 专有：Base URL 不是 DeepSeek 官方时直接返回 supported=false，
// 前端显示灰字「仅 DeepSeek 支持」，不报错。Key 在后端（DPAPI），只在此代理调用，
// 永不回传前端——响应里只有余额数值与币种。
func handleAIBalance(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	ac := aiSnap()
	if !isDeepSeekBase(ac.BaseURL) || ac.APIKey == "" {
		_ = json.NewEncoder(w).Encode(map[string]any{"supported": false})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(strings.TrimSpace(ac.BaseURL), "/")+"/user/balance", nil)
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"supported": true, "ok": false, "error": err.Error()})
		return
	}
	req.Header.Set("Authorization", "Bearer "+ac.APIKey)
	resp, err := httpShort.Do(req)
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"supported": true, "ok": false, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		_ = json.NewEncoder(w).Encode(map[string]any{"supported": true, "ok": false, "error": fmt.Sprintf("HTTP %d", resp.StatusCode)})
		return
	}
	var bj struct {
		IsAvailable  bool `json:"is_available"`
		BalanceInfos []struct {
			Currency     string `json:"currency"`
			TotalBalance string `json:"total_balance"`
		} `json:"balance_infos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&bj); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"supported": true, "ok": false, "error": err.Error()})
		return
	}
	cur, total := "", ""
	if len(bj.BalanceInfos) > 0 {
		cur, total = bj.BalanceInfos[0].Currency, bj.BalanceInfos[0].TotalBalance
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"supported": true, "ok": true, "available": bj.IsAvailable, "currency": cur, "total": total})
}

// handleAIModels 代理服务商的 GET /models（在线拉取在售模型，天然跟随官方更新）。
// 安全约束：API Key 只在与「已保存的 Base URL」同 host 时附带——防止用户被诱导
// 填一个恶意"自定义服务商"地址后，一点拉取列表就把密钥发往陌生域名。
func handleAIModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		BaseURL string `json:"base_url"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	acModels := aiSnap()
	base := strings.TrimRight(strings.TrimSpace(body.BaseURL), "/")
	if base == "" {
		base = strings.TrimRight(acModels.BaseURL, "/")
	}
	attachKey := urlHostOf(base) != "" && urlHostOf(base) == urlHostOf(acModels.BaseURL)
	// 兼容两种填法：base 或完整 endpoint（与 modelsEndpoint 保持同一套规则）
	probe := AIConfig{BaseURL: base}
	modelsURL := probe.modelsEndpoint()
	req, err := http.NewRequest("GET", modelsURL, nil)
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if attachKey {
		req.Header.Set("Authorization", "Bearer "+acModels.keyPlain())
	}
	resp, err := httpShort.Do(req)
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		hint := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if resp.StatusCode == 401 && !attachKey {
			hint += "：该地址与当前保存的服务商不一致，未附带 API Key（防止密钥发往陌生域名）。请先在配置中保存该 Base URL，再拉取模型列表。"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": hint, "key_attached": attachKey})
		return
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "响应解析失败：" + err.Error()})
		return
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "models": ids})
}

// aiCfgMu 保护 cfg.AI：配置接口（HTTP 写）与对话循环（多 goroutine 读）会并发访问，
// 实测 -race 可复现 DATA RACE（handleAIConfig 写 vs aiRunLoop 读）。
var aiCfgMu sync.RWMutex

// aiSnap 返回 AI 配置快照（值拷贝）。读方一律用它，避免与写方竞争。
func aiSnap() AIConfig {
	aiCfgMu.RLock()
	defer aiCfgMu.RUnlock()
	if cfg.AI == nil {
		return AIConfig{}
	}
	return *cfg.AI
}

// aiMutate 在写锁内修改 AI 配置
func aiMutate(fn func(*AIConfig)) {
	aiCfgMu.Lock()
	defer aiCfgMu.Unlock()
	if cfg.AI == nil {
		cfg.AI = &AIConfig{}
	}
	fn(cfg.AI)
}

func (a AIConfig) ready() bool {
	return a.Enabled && a.APIKey != "" && a.BaseURL != "" && a.Model != ""
}

// endpoint 兼容两种常见填法：
//
//	https://api.x.com/v1                    -> https://api.x.com/v1/chat/completions
//	https://api.x.com/v1/chat/completions   -> 原样使用（避免重复拼接导致 url.not_found）
func (a AIConfig) endpoint() string {
	u := strings.TrimRight(strings.TrimSpace(a.BaseURL), "/")
	if strings.HasSuffix(u, "/chat/completions") || strings.HasSuffix(u, "/completions") {
		return u
	}
	return u + "/chat/completions"
}

// modelsEndpoint 由 Base URL 推导 /models 地址（拉取模型列表用），同样兼容两种填法
func (a AIConfig) modelsEndpoint() string {
	u := strings.TrimRight(strings.TrimSpace(a.BaseURL), "/")
	u = strings.TrimSuffix(u, "/chat/completions")
	u = strings.TrimSuffix(u, "/completions")
	return u + "/models"
}

func (a AIConfig) keyPlain() string {
	if p, err := dpapiUnprotect(a.APIKey); err == nil {
		return p
	}
	return a.APIKey // 兼容未加密的历史配置
}

func handleAIConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method == http.MethodGet {
		writeAIConfig(w)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		// Enabled 用指针而不是 bool：其它字段都遵循"空值不覆盖"，
		// 而 bool 的零值 false 没法区分"用户要关掉"和"这次没带这个字段"。
		// 用 bool 的话，任何一次局部保存（脚本、将来的局部更新前端）
		// 都会把 AI 助手静默关掉——本项目就被这样误关过一次。
		Enabled     *bool    `json:"enabled"`
		Provider    string   `json:"provider"`
		BaseURL     string   `json:"base_url"`
		APIKey      string   `json:"api_key"`
		Model       string   `json:"model"`
		Temperature *float64 `json:"temperature"`
		// MemoryEnabled 同样用指针：记忆层开关被误关掉和 Enabled 是同一类事故
		// （局部保存把总开关抹成 false），必须显式传才改。
		MemoryEnabled *bool `json:"memory_enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "参数错误", 400)
		return
	}
	if body.Provider == "" && body.BaseURL == "" && body.APIKey == "" &&
		body.Model == "" && body.Temperature == nil && body.Enabled == nil &&
		body.MemoryEnabled == nil {
		// 空提交直接当读操作处理：绝不允许"什么都没传"覆盖掉已存配置。
		writeAIConfig(w)
		return
	}
	aiMutate(func(a *AIConfig) {
		if body.Enabled != nil {
			a.Enabled = *body.Enabled
		}
		if body.MemoryEnabled != nil {
			a.MemoryEnabled = *body.MemoryEnabled
		}
		if strings.TrimSpace(body.Provider) != "" {
			a.Provider = strings.TrimSpace(body.Provider)
		}
		if strings.TrimSpace(body.BaseURL) != "" {
			a.BaseURL = strings.TrimRight(strings.TrimSpace(body.BaseURL), "/")
		}
		if strings.TrimSpace(body.Model) != "" {
			a.Model = strings.TrimSpace(body.Model)
		}
		if strings.TrimSpace(body.APIKey) != "" {
			a.APIKey = protectIfPlain(strings.TrimSpace(body.APIKey)) // DPAPI 加密落盘
		}
		if body.Temperature != nil && *body.Temperature >= 0 {
			a.Temperature = *body.Temperature
		}
		if a.Temperature <= 0 && body.Temperature == nil {
			// 保持历史默认；0 视为未设置时用 0.3
			a.Temperature = 0.3
		}
		saveExternalConfig(cfg)
	})
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// writeAIConfig 输出 AI 配置（key 只给掩码，绝不回传明文）。
func writeAIConfig(w http.ResponseWriter) {
	ac := aiSnap()
	masked := ""
	if k := ac.keyPlain(); len(k) > 8 {
		masked = k[:4] + "****" + k[len(k)-4:]
	} else if k != "" {
		masked = "****"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"enabled": ac.Enabled, "provider": ac.Provider, "base_url": ac.BaseURL,
		"model": ac.Model, "key_set": ac.APIKey != "", "key_masked": masked,
		"temperature": ac.Temperature, "max_tokens": ac.MaxTokens,
		"memory_enabled": ac.MemoryEnabled,
		"quirk":          ac.quirkRead(),
	})
}

func handleAITest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	acReady := aiSnap()
	if !acReady.ready() {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "请先填写 API Key 并启用"})
		return
	}
	start := time.Now()
	_, err := aiCallLLM([]aiMsg{{Role: "user", Content: "回复 ok"}}, nil)
	lat := time.Since(start).Milliseconds()
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": firstLines(err.Error(), 300), "latency_ms": lat})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "latency_ms": lat})
}
