package main

// ai_abort_test.go —— 「本地收尾」与「上游故障」必须分得开。
//
// 真实反馈（2026-10-25）：用户在界面上看到
//   AI 服务返回错误：read tcp ...: use of closed network connection
// 而这其实是**我们自己**在客户端断开/超时/心跳失败时关掉了 resp.Body，
// 读取 goroutine 随之拿到 net.ErrClosed。把它当上游故障弹红，等于把
// "我主动收尾"说成"服务坏了"——用户会白白去查网络。

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// [DONE] 之后不采信 readErr —— 但只能在这个前提下放宽。
// 少了 sawDone 判据，真正的上游断流（unexpected EOF）会被当成"我们关的"而静默丢掉。
func TestUpstreamReadErrRespectsDoneFlag(t *testing.T) {
	if got := aiUpstreamReadErr(http.ErrBodyReadAfterClose, true); got != "" {
		t.Fatalf("[DONE] 之后读到已关闭的 body 不该报错，got=%q", got)
	}
	if got := aiUpstreamReadErr(io.ErrUnexpectedEOF, false); got == "" {
		t.Fatal("没收到 [DONE] 时的真实断流必须报出来，不能被吞掉")
	}
	if got := aiUpstreamReadErr(nil, false); got != "" {
		t.Fatalf("没有读取错误就不该有 upErr，got=%q", got)
	}
}

func TestAbortErrorsAreRecognized(t *testing.T) {
	// 本地主动关闭 / 取消：一律按正常收尾处理
	for _, s := range []string{
		"read tcp 192.168.1.12:57711->112.28.173.74:443: use of closed network connection",
		"read tcp 127.0.0.1:1->127.0.0.1:2: use of closed network connection",
		// 2026-10-30 真实反馈：用户在对话里看到「AI 服务返回错误：http: read on
		// closed response body」。上游发完 [DONE] 后我们 Close body 叫醒扫描 goroutine，
		// 它拿到 http.ErrBodyReadAfterClose —— body 的 closed 标志只由本地 Close 设置，
		// 上游永远造不出这条错误，所以它必然是我们自己关的。
		"http: read on closed response body",
		"context canceled",
		"context deadline exceeded",
		"operation was canceled",
		"the request was aborted",
	} {
		if !aiIsAbortErr(s) {
			t.Fatalf("应当识别为本地收尾：%q", s)
		}
	}
}

func TestRealUpstreamErrorsAreNotSwallowed(t *testing.T) {
	// 真正的上游故障必须照常报错，不能被这条规则吞掉
	for _, s := range []string{
		"unexpected EOF",
		"connection reset by peer",
		"HTTP 502 Bad Gateway",
		"upstream connect error or disconnect/reset before headers",
		"i/o timeout",
		"",
	} {
		if aiIsAbortErr(s) {
			t.Fatalf("不应被识别为本地收尾：%q", s)
		}
	}
}

// 根因证据（不是推测）：上游发完 [DONE] 但连接不关时，我们 Close body 去叫醒
// 仍在 Scan 的读取方 —— 它拿到的就是 http.ErrBodyReadAfterClose，
// 文本正是用户在界面上看到的「http: read on closed response body」。
// 有了这条，把该错误归到"本地收尾"就不是猜的。
func TestCloseAfterDoneProducesBodyReadAfterClose(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
		select { // 故意不主动结束：模拟"上游发完 [DONE] 但连接还开着"
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "data: [DONE]" {
			break
		}
	}
	resp.Body.Close() // 主循环的收尾动作
	for sc.Scan() {   // 扫描 goroutine 此刻还在读
	}
	// 这里**不能用 errors.Is 比 http.ErrBodyReadAfterClose**：一路上会被包成
	// 别的类型，Is 判不出来（实测 false，而文本就是那一句）。这也是 aiIsAbortErr
	// 按文本匹配的原因——那一族错误的类型不统一，只有文本是稳定的。
	got := ""
	if sc.Err() != nil {
		got = sc.Err().Error()
	}
	if !strings.Contains(got, "read on closed response body") {
		t.Fatalf("期望 http: read on closed response body，实际 %q", got)
	}
	if !aiIsAbortErr(got) {
		t.Fatalf("这条错误必须被认成「本地收尾」，否则用户又会看到红字：%q", got)
	}
}
