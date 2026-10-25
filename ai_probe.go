package main

// ai_probe.go —— 传输自检：把"请求体有没有完整到达"变成一次点击就能回答的问题。
//
// 背景：线上反复出现「请求体未到达」，但页面发出的 body 是完整的（CDP 抓包证明：
// 4 条消息、204 字节、历史齐全）。那问题就在浏览器到服务端之间——本机代理、
// 嵌入预览容器、浏览器扩展，任何一种都可能吃掉 POST body。
//
// 让用户去开 DevTools 看 Payload 不现实，所以给一个自检：
//   页面 POST 一段已知长度的随机 ASCII → 服务端把"实际收到多少字节"回给页面
//   → 页面显示"发送 N / 收到 M"。
//
//   一致 = 传输健康（那丢失只发生在特定请求上，比如带自定义头的）；
//   不一致 = 中间层在截断，而且**字节数本身就是规律**（比如恰好截到 8KB）。
//
// 这条接口只回长度与哈希前缀，不回显内容，避免变成任意数据回显。

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"time"
)

// handleAIEcho 回显"我到底收到多少字节"，用于定位请求体在传输途中被截断。
//
// ?flush=1：**先写响应头并 flush，再读 body** —— 复刻 /api/ai/chat 的真实顺序。
// 这是为了验证一个假设：如果浏览器在收到响应头之后就放弃发送剩余 body，
// 那么"先响应后读体"的接口就会稳定地读到 0 字节，而"先读体后响应"的接口不会。
func handleAIEcho(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Query().Get("flush") == "1" {
		w.WriteHeader(http.StatusOK)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		// 给客户端一点时间"看到响应"再继续读，尽量把竞态放大
		time.Sleep(30 * time.Millisecond)
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	sum := sha256.Sum256(raw)
	writeJSON(w, map[string]any{
		"ok":             true,
		"len":            len(raw),
		"sha":            hex.EncodeToString(sum[:])[:16],
		"declared":       r.Header.Get("X-EnvKit-Body-Len"),
		"content_length": r.ContentLength,
	})
}
