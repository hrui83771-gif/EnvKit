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

// 浅色主题是纯函数调色板：默认背景必须浅、默认前景必须深，
// 四个日志级别色也必须在浅底上读得清（不依赖 VT，老 conhost 同样生效）。
// 变异：把 p[0] 改回黑色，或把某个级别色换成亮黄，本测试必红。
func TestConsolePaletteIsLight(t *testing.T) {
	p := consolePalette()
	lum := func(c uint32) uint32 { return (c & 0xff) + ((c >> 8) & 0xff) + ((c >> 16) & 0xff) }
	if got := rgb(1, 2, 3); got != 0x030201 {
		t.Fatalf("COLORREF 应为 0x00BBGGRR: got %#x", got)
	}
	bg, fg := p[0], p[7]
	if lum(bg) < 600 {
		t.Fatalf("默认背景应是浅色，实际 %06x", bg)
	}
	if lum(fg) > 200 {
		t.Fatalf("默认前景应是深色，实际 %06x", fg)
	}
	if lum(bg) <= lum(fg) {
		t.Fatal("前景必须比背景暗，否则日志全糊在一片白里")
	}
	for _, i := range []int{1, 2, 3, 6} { // FAIL / OK / HINT / INFO
		if lum(p[i]) >= lum(bg)-150 {
			t.Errorf("级别色索引 %d 在浅底上对比不足: %06x", i, p[i])
		}
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
