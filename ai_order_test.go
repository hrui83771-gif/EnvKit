package main

// ai_order_test.go —— "先读请求体、后写响应头"的结构性断言。
//
// 真实故障（2026-10-25）：handleAIChat 原来第一行就 aiChatReady()，它会把 SSE
// 响应头 flush 出去，然后才去读 r.Body。实测（浏览器 + 真实网络）：
//     先读体后响应 → 发送 5476 / 收到 5476
//     先响应后读体 → 发送 5476 / 收到 0
// 表现是每条对话请求都「头到了、body 0 字节」，服务端只能用兜底头恢复几条消息，
// 于是模型失忆、缓存命中率被钉死。
//
// httptest 复现不了（body 早在内存里），所以这里用**源码顺序**把它钉住：
// 读取体必须在 aiChatReady 之前。

import (
	"os"
	"strings"
	"testing"
)

func TestHandlerReadsBodyBeforeWritingResponse(t *testing.T) {
	b, err := os.ReadFile("ai_loop.go")
	if err != nil {
		t.Fatalf("读 ai_loop.go 失败：%v", err)
	}
	src := string(b)
	i := strings.Index(src, "func handleAIChat(")
	if i < 0 {
		t.Fatal("找不到 handleAIChat")
	}
	// 截到 aiRunLoop 的调用处为止，够覆盖整个前置流程
	body := src[i:]
	if j := strings.Index(body, "aiRunLoop("); j > 0 {
		body = body[:j]
	}
	readAt := strings.Index(body, "io.ReadAll(")
	// 用带参数的调用形态定位：注释里也会出现 "aiChatReady()" 这个词，
	// 按裸名字找会命中注释，导致这条断言自己误判。
	readyAt := strings.Index(body, "aiChatReady(w)")
	if readAt < 0 {
		t.Fatal("handleAIChat 里找不到读取请求体的代码")
	}
	if readyAt < 0 {
		t.Fatal("handleAIChat 里找不到 aiChatReady（它应当存在，只是必须排在读体之后）")
	}
	if readyAt < readAt {
		t.Fatal("aiChatReady 出现在读取请求体之前 —— 它会 flush 响应头，" +
			"一旦响应先到，浏览器就不再发送请求体（实测：body 变成 0 字节）。" +
			"读取 r.Body 必须排在第一位。")
	}
}
