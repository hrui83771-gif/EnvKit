//go:build !windows

package platform

// InstallConsoleHook 非 Windows 平台无控制台关闭钩子（保持默认信号行为）。
func InstallConsoleHook(onClose func()) {}

// 非 Windows 平台没有「控制台窗口」这一层，托盘也不启用，这里保持空实现。
func ConsoleVisible() bool { return false }
func HideConsole()         {}
func ShowConsole()         {}

// ConsoleVTEnabled 非 Windows 由终端自己管颜色；这里返回 false，
// 让 loghub 不加 ANSI —— 与 Windows 重定向流的口径一致（宁可不上色也不污染）。
func ConsoleVTEnabled() bool { return false }

// Colorize 非 Windows 原样返回，保证跨平台编译与单测一致。
func Colorize(level, line string, enabled bool) string { return line }

// ConfigureConsole 非 Windows 无窗口属性可设。
func ConfigureConsole(title string) {}
