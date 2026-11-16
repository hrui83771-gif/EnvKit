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
)

type coord struct {
	X int16
	Y int16
}

type smallRect struct {
	L, T, R, B int16
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

// ConfigureConsole 设置窗口标题、Consolas 字体与合理尺寸（宽 108 × 高 30）。
// 全部 best-effort：任何一步失败都不影响服务运行；只在真控制台上做。
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
}
