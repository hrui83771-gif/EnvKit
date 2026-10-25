package main

// ai_body_test.go —— 请求体是否完整到达（对应线上那条
// 「请求体未到达（被本机代理/预览容器拦截）」的告警）。
//
// 这条告警一旦误判，服务端会用回退头里的**最后一句**重建上下文：
// 历史全丢、模型当场失忆、这一轮缓存也全部作废。所以必须有个用例把
// 「正常请求体不会被误判成丢失」钉住。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestChatBodyReachesLoopIntact(t *testing.T) {
	var mu sync.Mutex
	var got [][]aiMsg

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []aiMsg `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		got = append(got, req.Messages)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}))
	defer upstream.Close()

	oldAI := cfg.AI
	cfg.AI = &AIConfig{Enabled: true, Provider: "test", BaseURL: upstream.URL, APIKey: "k", Model: "test-model"}
	defer func() { cfg.AI = oldAI }()

	body := `{"messages":[{"role":"user","content":"第一句"},{"role":"assistant","content":"第二句"},{"role":"user","content":"第三句"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/ai/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handleAIChat(rec, req)

	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("上游一次都没被调用")
	}
	ms := got[0]
	// 循环会把 system 放在最前、把"环境快照"追加在最后，所以从第 2 条开始校验
	if len(ms) < 4 {
		t.Fatalf("请求体被吞了：上游只收到 %d 条消息", len(ms))
	}
	if ms[0].Role != "system" {
		t.Fatalf("第 1 条应当是 system，实际 %q", ms[0].Role)
	}
	want := []string{"第一句", "第二句", "第三句"}
	for i, w := range want {
		if ms[i+1].Content != w {
			t.Fatalf("第 %d 条消息应原样到达，实际 %q", i+1, ms[i+1].Content)
		}
	}
}

// TestBodyRecoveredFromHeader：body 丢失时用回退头里的 recent 恢复多轮上下文。
// 只恢复一句会让模型失忆、重新问一遍；恢复最近几条才能接着干活。
func TestBodyRecoveredFromHeader(t *testing.T) {
	var mu sync.Mutex
	var got [][]aiMsg

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []aiMsg `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		got = append(got, req.Messages)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}))
	defer upstream.Close()

	oldAI := cfg.AI
	cfg.AI = &AIConfig{Enabled: true, Provider: "test", BaseURL: upstream.URL, APIKey: "k", Model: "test-model"}
	defer func() { cfg.AI = oldAI }()

	// 请求体为空，但回退头里带 4 条最近消息
	fb := `{"last":"第三句","recent":[{"r":"user","c":"第一句"},{"r":"assistant","c":"第二句"},{"r":"user","c":"第三句"}],"lang":"zh"}`
	req := httptest.NewRequest(http.MethodPost, "/api/ai/chat", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-EnvKit-Chat-Fallback", url.QueryEscape(fb))
	rec := httptest.NewRecorder()
	handleAIChat(rec, req)

	if out := rec.Body.String(); !strings.Contains(out, `"type":"body_recovered"`) {
		t.Fatalf("应当把「已降级恢复」告诉界面，实际流内容：%s", firstLines(out, 300))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("上游一次都没被调用（说明空 body 直接把回合掐了）")
	}
	ms := got[0]
	if len(ms) < 4 {
		t.Fatalf("应当恢复 3 条历史（+system），实际只有 %d 条", len(ms))
	}
	if ms[1].Content != "第一句" || ms[3].Content != "第三句" {
		t.Fatalf("恢复出来的上下文不对：%q / %q", ms[1].Content, ms[3].Content)
	}
}
