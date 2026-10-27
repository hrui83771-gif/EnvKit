// linewriter_test.go —— 子进程输出切行器（lineWriter）的契约测试。
//
// 这一组测试的存在理由：v3.4 之前 `\r` 被一律当成"终端重绘"丢弃半行，
// 而 Windows 子进程的输出几乎全是 CRLF —— 于是整行被吞，日志里只剩
// "子进程无任何输出即退出（疑似杀毒软件拦截）"。这个误判让人去查杀毒软件、
// 去改白名单，查一百次也查不出来，因为**问题在自己这边的切行器**。
// 所以下面每条都钉住一种"输出必须能被看见"的形态。
package main

import (
	"strings"
	"testing"
)

// newTestWriter 造一个不落盘到用户目录的 writer（日志指到临时目录）。
func newTestWriter(t *testing.T) *lineWriter {
	t.Helper()
	old := logDirOverride
	// t.TempDir 的清理是后进先出：这里注册的 cleanup 会先执行（关句柄），
	// 它注册的 RemoveAll 后执行 —— 顺序反了就会"文件被占用删不掉"。
	logDirOverride = t.TempDir()
	t.Cleanup(func() {
		closeAllLogSinks()
		logDirOverride = old
	})
	return &lineWriter{scope: "test", tag: "t", svcKey: ""}
}

func TestLineWriterKeepsCRLFLines(t *testing.T) {
	w := newTestWriter(t)
	// Windows 子进程的典型输出：每行以 \r\n 结束
	if _, err := w.Write([]byte("Error: Cannot find module 'npm-cli.js'\r\n第二行\r\n")); err != nil {
		t.Fatal(err)
	}
	w.flush()
	if w.n != 2 {
		t.Fatalf("CRLF 的两行都必须被看见，实际 n=%d tail=%v", w.n, w.tail)
	}
	if !strings.Contains(w.tail[0], "Cannot find module") {
		t.Fatalf("真实报错必须原样保留，实际 %v", w.tail)
	}
}

func TestLineWriterCollapsesRedraw(t *testing.T) {
	w := newTestWriter(t)
	// 进度条：单独 \r 是重绘，只该留最后一次
	if _, err := w.Write([]byte(" 50%\r 75%\r100%\n")); err != nil {
		t.Fatal(err)
	}
	w.flush()
	if w.n != 1 {
		t.Fatalf("重绘应折叠成一行，实际 n=%d tail=%v", w.n, w.tail)
	}
	if !strings.Contains(w.tail[0], "100%") {
		t.Fatalf("应保留最后一次重绘，实际 %v", w.tail)
	}
}

func TestLineWriterHandlesSplitCRLF(t *testing.T) {
	// 管道分片会把 "\r\n" 拆到两次 Write：这是最容易被写出 bug 的边界。
	// 两行内容故意不同 —— 相邻重复会被折叠，用同样的行测不出切行是否正确。
	w := newTestWriter(t)
	_, _ = w.Write([]byte("abc\r"))
	_, _ = w.Write([]byte("\nxyz\r\n"))
	w.flush()
	if w.n != 2 {
		t.Fatalf("跨分片的 CRLF 也要正确切行，实际 n=%d tail=%v", w.n, w.tail)
	}
}

func TestLineWriterFlushesTailWithoutNewline(t *testing.T) {
	w := newTestWriter(t)
	_, _ = w.Write([]byte("没有换行结尾"))
	if !w.hasOutput() {
		t.Fatal("flush 之前 hasOutput 就该看到缓冲内容（否则会被判成零输出）")
	}
	w.flush()
	if w.n != 1 || w.tail[0] != "没有换行结尾" {
		t.Fatalf("结尾无换行的内容也必须交出去，实际 n=%d tail=%v", w.n, w.tail)
	}
}

func TestLineWriterSkipsBlankAndDuplicate(t *testing.T) {
	// 原有行为：空行与相邻重复行折叠（很多工具会刷同一行，全留就是噪声）
	w := newTestWriter(t)
	_, _ = w.Write([]byte("same\r\nsame\r\n\r\nother\r\n"))
	w.flush()
	if w.n != 2 {
		t.Fatalf("相邻重复与空行应折叠，实际 n=%d tail=%v", w.n, w.tail)
	}
}

func TestLineWriterHasOutputOnCRLFOnly(t *testing.T) {
	// 回归钉子：这一条正是"exit status 1 但日志说无输出"的最小复现
	w := newTestWriter(t)
	_, _ = w.Write([]byte("boom\r\n"))
	w.flush()
	if !w.hasOutput() {
		t.Fatal("只有 CRLF 输出时 hasOutput 必须为 true —— 否则会误报'零输出秒退'")
	}
	if w.n != 1 {
		t.Fatalf("应计 1 行，实际 %d", w.n)
	}
}
