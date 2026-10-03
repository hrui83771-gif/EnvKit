package main

// ai_sse.go —— SSE 传输层：事件写入、连接就绪、收尾事件、客户端断开检测
//
// 从 ai_loop.go 拆出。拆的理由是职责边界而非行数：这四件事
// 与"AI 该调什么工具"完全无关，改主循环时不该看到它们。
//
// 客户端断开检测（clientGone）特别值得独立：它是整个循环里
// 唯一的"逃生阀"，关窗/刷新页面时靠它停止向上游要 token，
// 混在主循环里容易被改坏而不自知。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

func sseWrite(w http.ResponseWriter, fl http.Flusher, v map[string]any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(w, "data: %s\n\n", b)
	fl.Flush()
}
func aiChatReady(w http.ResponseWriter) (http.Flusher, bool) {
	if !aiSnap().ready() {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","text":"AI 助手未配置：请到「程序配置 → AI 助手」填写 Base URL 与 API Key"}`))
		return nil, false
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	fl.Flush()
	return fl, true
}

// clientGone 判断客户端是否已不在（context 取消 或 SSE 写失败）
func clientGone(ctx context.Context, w http.ResponseWriter) bool {
	if ctx.Err() != nil {
		return true
	}
	if ew, ok := w.(interface{ writeErr() error }); ok {
		return ew.writeErr() != nil
	}
	return false
}

// sseDoneUsage / sseDoneConfirm：回合收尾。有 usage 就在 done 前补一条独立 usage 事件
// （前端据此在回复尾部显示 token 统计并累计到桌宠"今日统计"），没有就只发 done。
func sseDoneUsage(w http.ResponseWriter, fl http.Flusher, u *aiUsage) {
	if u.Prompt > 0 || u.Completion > 0 {
		sseWrite(w, fl, map[string]any{"type": "usage", "usage": *u})
	}
	sseWrite(w, fl, map[string]any{"type": "done"})
}
func sseDoneConfirm(w http.ResponseWriter, fl http.Flusher, u *aiUsage) {
	if u.Prompt > 0 || u.Completion > 0 {
		sseWrite(w, fl, map[string]any{"type": "usage", "usage": *u})
	}
	sseWrite(w, fl, map[string]any{"type": "done", "reason": "awaiting_confirm"})
}
