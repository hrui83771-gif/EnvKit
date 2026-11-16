//go:build windows

package platform

import (
	"strings"
	"unicode/utf16"
	"unsafe"
)

// 控制台配色：只在「输出目标确实是控制台」时启用 ANSI 转义。
// 关键约束：EnvKit 的 stdout 常被重定向到文件/管道（CDP 测试、服务日志）。
// 给重定向流写 \x1b[...] 会把转义码污染进日志文件，所以必须先探测句柄类型。

const (
	enableVT = 0x4000           // ENABLE_VIRTUAL_TERMINAL_PROCESSING
	stdOut   = ^uintptr(0) - 10 // STD_OUTPUT_HANDLE = -11
)

var (
	procGetConsoleMode  = modKernel32.NewProc("GetConsoleMode")
	procSetConsoleMode  = modKernel32.NewProc("SetConsoleMode")
	procSetConsoleTitle = modKernel32.NewProc("SetConsoleTitleW")
	procGetStdHandle    = modKernel32.NewProc("GetStdHandle")
	procSetCurrFont     = modKernel32.NewProc("SetCurrentConsoleFontEx")
	procGetCurrFont     = modKernel32.NewProc("GetCurrentConsoleFontEx")
	procSetScrBufSize   = modKernel32.NewProc("SetConsoleScreenBufferSize")
	procSetWindowInfo   = modKernel32.NewProc("SetConsoleWindowInfo")
	procGetScrBufInfoEx = modKernel32.NewProc("GetConsoleScreenBufferInfoEx")
	procSetScrBufInfoEx = modKernel32.NewProc("SetConsoleScreenBufferInfoEx")
	procFillChar        = modKernel32.NewProc("FillConsoleOutputCharacterW")
	procFillAttr        = modKernel32.NewProc("FillConsoleOutputAttribute")
	procSetCursorPos    = modKernel32.NewProc("SetConsoleCursorPosition")
)

type coord struct {
	X int16
	Y int16
}

type smallRect struct {
	L, T, R, B int16
}

// consoleScreenBufferInfoEx 对应 CONSOLE_SCREEN_BUFFER_INFOEX（Windows 7+）。
// ColorTable 是 16 个 COLORREF；wAttributes 低 4 位前景 / 高 4 位背景索引。
type consoleScreenBufferInfoEx struct {
	CbSize               uint32
	DwSize               coord
	DwCursorPosition     coord
	WAttributes          uint16
	SrWindow             smallRect
	DwMaximumWindowSize  coord
	WPopupAttributes     uint16
	BFullscreenSupported uint32
	ColorTable           [16]uint32
}

// rgb 把 (r,g,b) 打包成 COLORREF（Win32 是 0x00BBGGRR，不是常见的 RRGGBB）。
func rgb(r, g, b uint32) uint32 { return r | g<<8 | b<<16 }

// consolePalette 是浅色主题的 16 色调色板。索引与 ANSI 色码对齐：
// 0=默认背景、7=默认前景，1/2/3/6 对应 Colorize 的红/绿/黄/青。
// 关键：默认背景必须是浅色；级别色要压暗，尤其黄色（原生 #ffff00 在浅底上
// 几乎看不见）。青也换成深蓝，否则 INFO 一整行会糊在纸白上。
// 纯函数，便于单测把"黑底变白纸"这件事钉死。
func consolePalette() [16]uint32 {
	return [16]uint32{
		rgb(250, 248, 245), // 0 背景：与网页 #faf8f5 同一张纸
		rgb(176, 35, 24),   // 1 红 FAIL / ERR
		rgb(6, 118, 71),    // 2 绿 OK
		rgb(180, 95, 6),    // 3 黄 WARN / HINT（压成琥珀）
		rgb(30, 90, 170),   // 4 蓝
		rgb(140, 55, 120),  // 5 品红
		rgb(27, 78, 140),   // 6 青 → 深蓝 INFO（真青在浅底上没对比）
		rgb(35, 39, 46),    // 7 前景：近黑但不纯黑
		rgb(96, 100, 110),  // 8 亮黑 → 中灰
		rgb(210, 60, 45),   // 9 亮红
		rgb(12, 150, 90),   // 10 亮绿
		rgb(205, 120, 20),  // 11 亮黄
		rgb(40, 110, 200),  // 12 亮蓝
		rgb(165, 75, 150),  // 13 亮品红
		rgb(30, 110, 160),  // 14 亮青
		rgb(70, 76, 86),    // 15 亮白 → 深灰（别在浅底上隐形）
	}
}

// applyLightTheme 重映射调色板 + 清屏。
// 不依赖 ANSI/VT：老 conhost 上照样能把黑窗换成浅色，这是本轮修复的关键——
// 之前只给日志插 ANSI 色码，VT 不可用时整窗依然是黑的。
// 清屏是必须的：ConfigureConsole 之前的启动日志已经用旧黑底画在缓冲区里了，
// 不清就留下一条条黑底文字，视觉上还是"黑乎乎的窗口"。
func applyLightTheme(h uintptr) {
	var info consoleScreenBufferInfoEx
	info.CbSize = uint32(unsafe.Sizeof(info))
	if r, _, _ := procGetScrBufInfoEx.Call(h, uintptr(unsafe.Pointer(&info))); r == 0 {
		return
	}
	info.ColorTable = consolePalette()
	info.WAttributes = 0x07 // 前景索引 7（深色）／背景索引 0（纸白）
	info.WPopupAttributes = 0x07
	if r, _, _ := procSetScrBufInfoEx.Call(h, uintptr(unsafe.Pointer(&info))); r == 0 {
		return
	}
	cells := uint32(info.DwSize.X) * uint32(info.DwSize.Y)
	if cells == 0 {
		return
	}
	origin := uintptr(uint32(0) | uint32(0)<<16)
	var written uint32
	procFillChar.Call(h, uintptr(' '), uintptr(cells), origin, uintptr(unsafe.Pointer(&written)))
	procFillAttr.Call(h, uintptr(0x07), uintptr(cells), origin, uintptr(unsafe.Pointer(&written)))
	procSetCursorPos.Call(h, origin)
}

// consoleFontInfoEx 对应 CONSOLE_FONT_INFOEX（Windows 7+）。
// FaceName 是 UTF-16，必须定长 32 才能与 Win32 结构对齐。
type consoleFontInfoEx struct {
	CbSize     uint32
	FontIndex  uint32
	DwFontSize coord
	FontFamily uint32
	FontWeight uint32
	FaceName   [32]uint16
}

// ConsoleVTEnabled 输出目标是否为支持 VT 的控制台。
// 探测失败（句柄是文件/管道）一律返回 false —— 宁可不上色，也不污染重定向流。
func ConsoleVTEnabled() bool {
	h, _, _ := procGetStdHandle.Call(stdOut)
	if h == 0 || h == uintptr(^uintptr(0)) {
		return false
	}
	var mode uint32
	if r, _, _ := procGetConsoleMode.Call(h, uintptr(unsafe.Pointer(&mode))); r == 0 {
		return false // 不是控制台
	}
	if r, _, _ := procSetConsoleMode.Call(h, uintptr(mode|enableVT)); r == 0 {
		return false // 老 conhost 不支持 VT
	}
	return true
}

// consoleColor 纯函数：级别 → ANSI 色码。便于单测覆盖，不依赖 Win32。
// 返回空串表示不上色（未知级别、或明确不需要强调的 INFO 之外场景）。
func consoleColor(level string) string {
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "INFO":
		return "\x1b[36m" // 青：常规信息
	case "OK":
		return "\x1b[32m" // 绿：成功
	case "WARN", "HINT":
		return "\x1b[33m" // 黄：提醒/告警
	case "FAIL", "ERR", "ERROR":
		return "\x1b[31m" // 红：失败
	}
	return ""
}

// Colorize 给一行文字着色；enabled=false 或级别未知时原样返回。
// 用函数而非全局开关，是为了让 loghub 决定「什么时候允许上色」，平台层只管怎么上色。
func Colorize(level, line string, enabled bool) string {
	if !enabled {
		return line
	}
	c := consoleColor(level)
	if c == "" {
		return line
	}
	return c + line + "\x1b[0m"
}

// ConfigureConsole 设置窗口标题、Consolas 字体、合理尺寸（宽 108 × 高 30）
// 与浅色配色。全部 best-effort：任何一步失败都不影响服务运行；只在真控制台上做。
func ConfigureConsole(title string) {
	h, _, _ := procGetStdHandle.Call(stdOut)
	if h == 0 || h == uintptr(^uintptr(0)) {
		return
	}
	var mode uint32
	if r, _, _ := procGetConsoleMode.Call(h, uintptr(unsafe.Pointer(&mode))); r == 0 {
		return // 重定向流：不要碰它的「窗口属性」
	}
	if title != "" {
		t := utf16.Encode([]rune(title + "\x00"))
		procSetConsoleTitle.Call(uintptr(unsafe.Pointer(&t[0])))
	}
	// 字体：先读当前配置保留字重，只改 FaceName 与字号。
	var fi consoleFontInfoEx
	fi.CbSize = uint32(unsafe.Sizeof(fi))
	if r, _, _ := procGetCurrFont.Call(h, 0, uintptr(unsafe.Pointer(&fi))); r != 0 {
		face := utf16.Encode([]rune("Consolas\x00"))
		copy(fi.FaceName[:], face)
		fi.DwFontSize = coord{X: 0, Y: 16} // 高度 16px，宽度 0 = 自动
		procSetCurrFont.Call(h, 0, uintptr(unsafe.Pointer(&fi)))
	}
	// 尺寸：先扩缓冲区再缩窗口，顺序反了会被系统拒绝。
	// COORD 是两个 int16 按 X 低位、Y 高位打包成一个 DWORD 传值。
	bufSize := uintptr(uint32(108) | uint32(600)<<16)
	procSetScrBufSize.Call(h, bufSize)
	var rc smallRect
	rc.L, rc.T, rc.R, rc.B = 0, 0, 107, 29
	procSetWindowInfo.Call(h, 1, uintptr(unsafe.Pointer(&rc)))
	// 放最后：前面的尺寸/字体变更可能重绘，配色和清屏要在它们之后落定。
	applyLightTheme(h)
}
