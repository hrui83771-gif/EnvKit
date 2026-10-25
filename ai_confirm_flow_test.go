package main

// ai_confirm_flow_test.go —— 确认卡"原地等待"的端到端行为（假上游，不联网）
//
// 这条用例直接复现过一次真实故障：确认后上游返回
// `Duplicate value for 'tool_call_id' of call_xxx in message[5]`。
// 原因是写工具分支自己 append 了 tool_call，循环末尾的公共尾巴又 append 了一次。
//
// 它同时钉住三件产品行为：
//  1. 确认**不结束回合**——决定到达后，同一个流里继续输出；
//  2. 一次确认只产生**一个** tool_call_id、**一条** tool 结果；
//  3. 确认后的下一次上游请求**带着 role=tool 的结果**（模型不忘事）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConfirmWaitsInRequestAndKeepsSingleToolCall(t *testing.T) {
	const toolName = "zz_test_write"

	var mu sync.Mutex
	var captured [][]aiMsg

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []aiMsg `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		captured = append(captured, req.Messages)
		n := len(captured)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			// 首轮：模型发起一个写工具调用
			fmt.Fprintf(w, "data: %s\n\n",
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_test_1","type":"function","function":{"name":"`+toolName+`","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
			fmt.Fprintf(w, "data: %s\n\n",
				`{"choices":[],"usage":{"prompt_tokens":1200,"completion_tokens":10,"prompt_cache_hit_tokens":1000,"prompt_cache_miss_tokens":200}}`)
		} else {
			fmt.Fprintf(w, "data: %s\n\n",
				`{"choices":[{"delta":{"content":"已按你的确认执行完毕。"},"finish_reason":"stop"}]}`)
			fmt.Fprintf(w, "data: %s\n\n",
				`{"choices":[],"usage":{"prompt_tokens":1500,"completion_tokens":20,"prompt_cache_hit_tokens":1400,"prompt_cache_miss_tokens":100}}`)
		}
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}))
	defer upstream.Close()

	// 假写工具：过 Write 标记即走确认分支
	aiToolRegistry[toolName] = aiTool{
		Desc:   "测试用写工具",
		Schema: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}},
		Write:  true,
		Execute: func(map[string]any) (string, error) {
			return "写操作已执行：ok", nil
		},
	}
	defer delete(aiToolRegistry, toolName)

	oldAI := cfg.AI
	cfg.AI = &AIConfig{Enabled: true, Provider: "test", BaseURL: upstream.URL, APIKey: "k", Model: "test-model"}
	defer func() { cfg.AI = oldAI }()

	rec := httptest.NewRecorder()
	msgs := []aiMsg{{Role: "user", Content: "run the confirmation flow"}}
	done := make(chan struct{})
	// preflight=true 跳过"确认前补一句结论"的额外调用（那条会多打一次上游，
	// 让假上游的响应序列错位）；英文输入不会命中本地意图路由。
	go func() {
		defer close(done)
		aiRunLoop(context.Background(), rec, rec, msgs, false, true, "zh")
	}()

	// 等它进入"等确认"
	var callID string
	for i := 0; i < 500; i++ {
		aiWaiterMu.Lock()
		if aiWaiter != nil {
			callID = aiWaiter.callID
		}
		aiWaiterMu.Unlock()
		if callID != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if callID == "" {
		t.Fatal("回合没有进入确认等待（既没发卡也没等人）")
	}
	if callID != "call_test_1" {
		t.Fatalf("等待的 callID 应当来自上游的 tool_call，实际 %q", callID)
	}

	// 模拟用户点"确认"
	creq := httptest.NewRequest(http.MethodPost, "/api/ai/confirm",
		strings.NewReader(fmt.Sprintf(`{"tool_call_id":%q,"tool":%q,"approved":true}`, callID, toolName)))
	crec := httptest.NewRecorder()
	handleAIConfirm(crec, creq)
	if crec.Code != http.StatusOK {
		t.Fatalf("确认投递失败：%d %s", crec.Code, crec.Body.String())
	}

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("确认后回合没有继续并结束（说明它还在等，或者已经 return 了）")
	}

	out := rec.Body.String()
	if !strings.Contains(out, "已按你的确认执行完毕") {
		t.Fatalf("确认后应当在同一个流里继续输出模型回答，实际流内容尾部：%s", tailOf(out, 400))
	}
	if !strings.Contains(out, "已执行") {
		t.Fatalf("应当在流里给出确定性的执行结果行，实际：%s", tailOf(out, 400))
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) < 2 {
		t.Fatalf("应当至少有两次上游请求（首轮 + 确认后），实际 %d 次", len(captured))
	}
	// ① 任何一次请求里，同一条 assistant 消息都不能出现重复 tool_call_id
	for ri, ms := range captured {
		seen := map[string]int{}
		for _, m := range ms {
			for _, tc := range m.ToolCalls {
				seen[tc.ID]++
			}
		}
		for id, n := range seen {
			if n > 1 {
				t.Fatalf("第 %d 次上游请求里 tool_call_id %q 出现 %d 次 —— 上游会直接 400（Duplicate value for 'tool_call_id'）", ri+1, id, n)
			}
		}
	}
	// ② 确认后的那次请求必须带着工具结果（否则模型"忘了自己做过什么"）
	found := false
	for _, m := range captured[1] {
		if m.Role == "tool" && m.ToolCallID == "call_test_1" {
			found = true
			if !strings.Contains(m.Content, "写操作已执行") {
				t.Fatalf("tool 结果内容不对：%q", m.Content)
			}
		}
	}
	if !found {
		t.Fatal("确认后的请求里没有 role=tool 的结果消息")
	}
}

func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// TestBatchGrantSkipsFollowUpCards 批量授权的端到端行为：
// 用户在第一张卡上勾"后续同类不再问" → 模型接着发起的两次同类写操作**不再弹卡**，
// 但仍然各自执行、各自留痕。三次动作只应产生一张确认卡。
func TestBatchGrantSkipsFollowUpCards(t *testing.T) {
	const tool = "start_service"

	origReg, hadReg := aiToolRegistry[tool]
	aiToolRegistry[tool] = aiTool{
		Desc:   "测试桩：启动服务",
		Schema: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}},
		Write:  true,
		Execute: func(map[string]any) (string, error) {
			return "已启动（测试桩）", nil
		},
	}
	defer func() {
		if hadReg {
			aiToolRegistry[tool] = origReg
		} else {
			delete(aiToolRegistry, tool)
		}
	}()

	var mu sync.Mutex
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n <= 3 {
			id := fmt.Sprintf("call_b%d", n)
			fmt.Fprintf(w, "data: %s\n\n",
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"`+id+`","type":"function","function":{"name":"`+tool+`","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
		} else {
			fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"content":"三个步骤都做完了。"},"finish_reason":"stop"}]}`)
		}
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}))
	defer upstream.Close()

	oldAI := cfg.AI
	cfg.AI = &AIConfig{Enabled: true, Provider: "test", BaseURL: upstream.URL, APIKey: "k", Model: "test-model"}
	defer func() { cfg.AI = oldAI }()

	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		aiRunLoop(context.Background(), rec, rec,
			[]aiMsg{{Role: "user", Content: "bring the services up"}}, false, true, "zh")
	}()

	var callID string
	for i := 0; i < 500; i++ {
		aiWaiterMu.Lock()
		if aiWaiter != nil {
			callID = aiWaiter.callID
		}
		aiWaiterMu.Unlock()
		if callID != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if callID == "" {
		t.Fatal("第一张确认卡没有出现")
	}

	creq := httptest.NewRequest(http.MethodPost, "/api/ai/confirm",
		strings.NewReader(fmt.Sprintf(`{"tool_call_id":%q,"tool":%q,"approved":true,"batch":true}`, callID, tool)))
	crec := httptest.NewRecorder()
	handleAIConfirm(crec, creq)
	if crec.Code != http.StatusOK {
		t.Fatalf("确认投递失败：%d %s", crec.Code, crec.Body.String())
	}

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("回合没有结束")
	}

	out := rec.Body.String()
	if n := strings.Count(out, `"type":"confirm_request"`); n != 1 {
		t.Fatalf("三次同类动作只应弹一张卡，实际 %d 张", n)
	}
	if n := strings.Count(out, `"type":"grant_used"`); n != aiGrantMaxUses {
		t.Fatalf("应当有 %d 次批量授权放行，实际 %d 次", aiGrantMaxUses, n)
	}
	if n := strings.Count(out, "已启动（测试桩）"); n < 3 {
		t.Fatalf("三次动作都应当真的执行过（结果出现 3 次），实际 %d 次", n)
	}
	if !strings.Contains(out, "三个步骤都做完了") {
		t.Fatal("批量执行完之后模型应当还能收尾作答")
	}
	mu.Lock()
	total := calls
	mu.Unlock()
	if total < 4 {
		t.Fatalf("应当至少 4 次上游请求（3 次工具 + 1 次收尾），实际 %d", total)
	}
}
