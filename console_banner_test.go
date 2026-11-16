package main

import (
	"strings"
	"testing"
)

// 横幅三要素：版本、地址、状态。少一个，用户第一眼就答不出
// 「这是哪个 EnvKit / 去哪操作 / 这窗口能不能关」。
func TestBannerLinesHasThreeFacts(t *testing.T) {
	lines := bannerLines("http://127.0.0.1:18765/", "3.8.0", false)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"3.8.0", "http://127.0.0.1:18765/", "运行中", "退出"} {
		if !strings.Contains(joined, want) {
			t.Errorf("横幅缺少关键事实 %q:\n%s", want, joined)
		}
	}
	if len(lines) < 6 {
		t.Fatalf("横幅行数过少: %d", len(lines))
	}
}

// 关闭颜色时整个横幅不得含 ANSI 转义码（重定向到文件/管道会污染日志）。
func TestBannerLinesPlainWhenDisabled(t *testing.T) {
	for _, l := range bannerLines("http://x/", "1.0", false) {
		if strings.Contains(l, "\x1b[") {
			t.Fatalf("关闭着色却含转义码: %q", l)
		}
	}
}
