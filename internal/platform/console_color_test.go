//go:build windows

package platform

import "testing"

// 颜色映射是纯函数，变异（把某级别改成空串/改错色码）必须在这里变红。
func TestConsoleColorLevels(t *testing.T) {
	cases := map[string]string{
		"INFO": "\x1b[36m", "OK": "\x1b[32m",
		"WARN": "\x1b[33m", "HINT": "\x1b[33m",
		"FAIL": "\x1b[31m", "ERR": "\x1b[31m", "ERROR": "\x1b[31m",
	}
	for level, want := range cases {
		if got := consoleColor(level); got != want {
			t.Errorf("%s: got %q want %q", level, got, want)
		}
	}
	if got := consoleColor("  ok  "); got != "\x1b[32m" {
		t.Errorf("大小写/空白归一: got %q", got)
	}
	if got := consoleColor("UNKNOWN"); got != "" {
		t.Errorf("未知级别应不上色: got %q", got)
	}
}

// 关键回归：enabled=false 必须原样返回，否则重定向流会被 ANSI 污染。
func TestColorizeDisabledIsPlain(t *testing.T) {
	const line = "[服务] 12:00:00 [INFO] 启动"
	if got := Colorize("INFO", line, false); got != line {
		t.Fatalf("关闭着色时不得插入转义码: got %q", got)
	}
	if got := Colorize("OK", line, true); got == line {
		t.Fatal("开启着色时 OK 级别必须上色")
	}
	if got := Colorize("UNKNOWN", line, true); got != line {
		t.Fatalf("未知级别即使开启也不上色: got %q", got)
	}
}
