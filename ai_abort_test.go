package main

// ai_abort_test.go —— 「本地收尾」与「上游故障」必须分得开。
//
// 真实反馈（2026-10-25）：用户在界面上看到
//   AI 服务返回错误：read tcp ...: use of closed network connection
// 而这其实是**我们自己**在客户端断开/超时/心跳失败时关掉了 resp.Body，
// 读取 goroutine 随之拿到 net.ErrClosed。把它当上游故障弹红，等于把
// "我主动收尾"说成"服务坏了"——用户会白白去查网络。

import "testing"

func TestAbortErrorsAreRecognized(t *testing.T) {
	// 本地主动关闭 / 取消：一律按正常收尾处理
	for _, s := range []string{
		"read tcp 192.168.1.12:57711->112.28.173.74:443: use of closed network connection",
		"read tcp 127.0.0.1:1->127.0.0.1:2: use of closed network connection",
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
