package main

// ai_probe_test.go —— 传输自检接口的契约。
//
// 这条接口是给"用户点一下就知道请求体有没有被吃掉"用的，
// 所以它必须：只接受 POST、如实回报收到的字节数、不回显内容。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEchoReportsExactLength(t *testing.T) {
	body := `{"probe":"` + strings.Repeat("A", 4000) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/ai/echo", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-EnvKit-Body-Len", "4014")
	rec := httptest.NewRecorder()
	handleAIEcho(rec, req)

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON：%v / %s", err, rec.Body.String())
	}
	if ok, _ := out["ok"].(bool); !ok {
		t.Fatalf("应当 ok=true，实际 %v", out)
	}
	if got := int(out["len"].(float64)); got != len(body) {
		t.Fatalf("长度应如实回报：发送 %d 字节，回报 %d", len(body), got)
	}
	if _, ok := out["sha"]; !ok {
		t.Fatal("应当回报哈希前缀（用于确认内容一致，而不是随便回个长度）")
	}
	if s, _ := out["sha"].(string); len(s) != 16 {
		t.Fatalf("哈希前缀应当是 16 位，实际 %q", s)
	}
	// 绝不回显内容：这条接口不承担"数据回显"的职责
	if strings.Contains(rec.Body.String(), strings.Repeat("A", 50)) {
		t.Fatal("不得回显请求内容")
	}
}

func TestEchoRejectsGet(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/ai/echo", nil)
	rec := httptest.NewRecorder()
	handleAIEcho(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("只接受 POST，实际 %d", rec.Code)
	}
}

func TestEchoEmptyBodyStillAnswers(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/ai/echo", strings.NewReader(""))
	rec := httptest.NewRecorder()
	handleAIEcho(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if got := int(out["len"].(float64)); got != 0 {
		t.Fatalf("空 body 应当回报 0 字节，实际 %d", got)
	}
}
