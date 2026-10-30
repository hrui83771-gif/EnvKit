package main

// ai_confirm_intent_test.go —— 预检意图那张确认卡，**投递口到底通不通**
//
// 这条用例来自一次实测（v3.6.1 评测 T15 实跑）：
// 模型说「现在直接启动后端」→ 事件流里出现
//   {"type":"confirm_request","tool":"start_service",
//    "tool_call_id":"intent_1793361861144732900",...}
// 用**事件流里那个 ID** 投递拒绝，得到 `404 {"reason":"no_waiter"}`，
// 连续 15 次、跨 3 秒都是如此。而这一轮随后等满了 10 分钟超时才收尾。
//
// 为什么这条必须单独测：现有 TestConfirmWaitsInRequestAndKeepsSingleToolCall
// 覆盖的是**模型发起的** tool_call 路径（call_test_1），而且它的 callID
// 是**从 aiWaiter 里读的**、不是从事件流里读的 —— 只要发卡与登记用了同一个
// 变量，测试就一定过；**而用户（和前端）拿到的是事件流里那个 ID**。
// 两者若不一致，产品表现为"点确认没反应，退回旧路径再跑一整轮"，
// 测试却全绿。所以这里刻意用**事件流里的 ID** 去投递。
//
// 断言两件事：
//  1. 正确 ID → 200 + ok:true（等待者确实登记上了）；
//  2. 错误 ID → 404（对照组，证明这条断言能红，不是恒真）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// sseRecorder 是并发安全的 ResponseWriter。
//
// httptest.ResponseRecorder 内部的 bytes.Buffer 不能一边写一边读
// （AI 回合在另一个 goroutine 写，测试这边要轮询已发出的 SSE 事件）。
type sseRecorder struct {
	mu sync.Mutex
	b  strings.Builder
	h  http.Header
}

func newSSERecorder() *sseRecorder { return &sseRecorder{h: http.Header{}} }

func (s *sseRecorder) Header() http.Header { return s.h }

func (s *sseRecorder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *sseRecorder) WriteHeader(int) {}

func (s *sseRecorder) Flush() {}

func (s *sseRecorder) body() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestConfirmIntentCardIsDeliverable 预检卡发出去之后，用事件流里的 ID 必须投得进去。
func TestConfirmIntentCardIsDeliverable(t *testing.T) {
	undo := withAIMissionEnv(t)
	defer undo()

	// 假上游：拒绝之后服务端还会继续这一轮，给一句话收尾就够了。
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n",
			`{"choices":[{"delta":{"content":"好的，等你说可以再启动。"},"finish_reason":"stop"}]}`)
		fmt.Fprintf(w, "data: %s\n\n",
			`{"choices":[],"usage":{"prompt_tokens":900,"completion_tokens":12}}`)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}))
	defer upstream.Close()

	oldAI := cfg.AI
	cfg.AI = &AIConfig{Enabled: true, Provider: "test", BaseURL: upstream.URL,
		APIKey: "k", Model: "test-model"}
	defer func() { cfg.AI = oldAI }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newSSERecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		// preflight=true：这句「启动后端服务」由本地意图路由确定性命中，不依赖模型
		aiRunLoop(ctx, w, w, []aiMsg{{Role: "user", Content: "启动后端服务"}}, false, true, "zh")
	}()

	// 等确认卡出现，并**从事件流里**取 ID —— 这才是前端/评测端拿到的那个。
	re := regexp.MustCompile(`"tool_call_id":"(intent_[0-9]+)"`)
	var callID string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m := re.FindStringSubmatch(w.body()); m != nil {
			callID = m[1]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if callID == "" {
		t.Fatalf("预检没有发确认卡；已输出：%s", truncateForTest(w.body(), 400))
	}

	deliver := func(id string) *httptest.ResponseRecorder {
		creq := httptest.NewRequest(http.MethodPost, "/api/ai/confirm",
			strings.NewReader(fmt.Sprintf(
				`{"tool_call_id":%q,"tool":"start_service","approved":false}`, id)))
		crec := httptest.NewRecorder()
		handleAIConfirm(crec, creq)
		return crec
	}

	// 对照组：错误 ID 必须 404（证明这条断言能红）
	if got := deliver("intent_0"); got.Code != http.StatusNotFound {
		t.Fatalf("错误 ID 应当 404，实际 %d：%s", got.Code, got.Body.String())
	}

	// 正题：事件流里的 ID 必须投得进去
	got := deliver(callID)
	if got.Code != http.StatusOK {
		var body struct {
			OK     bool   `json:"ok"`
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(got.Body.Bytes(), &body)
		t.Fatalf("预检卡的等待者没登记上：事件流给的 id=%s 投递得到 %d %s。\n"+
			"后果：前端只能退回旧路径（再跑一整轮，重新问一次模型），"+
			"评测端更是只能等满 %s 超时——一题 10 分钟。",
			callID, got.Code, body.Reason, aiConfirmWaitMax)
	}
	var ok struct {
		OK      bool `json:"ok"`
		Proceed bool `json:"proceed"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &ok); err != nil {
		t.Fatalf("回执不是 JSON：%s", got.Body.String())
	}
	if !ok.OK || ok.Proceed {
		t.Fatalf("拒绝回执应当 ok=true proceed=false，实际 %s", got.Body.String())
	}

	// 收尾：让回合自己结束，别把 goroutine 留在测试里
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		<-done
	}
}

func truncateForTest(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
