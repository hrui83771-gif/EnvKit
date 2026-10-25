//go:build !windows

package platform

// InstallConsoleHook 非 Windows 平台无控制台关闭钩子（保持默认信号行为）。
func InstallConsoleHook(onClose func()) {}

// 非 Windows 平台没有「控制台窗口」这一层，托盘也不启用，这里保持空实现。
func ConsoleVisible() bool { return false }
func HideConsole()         {}
func ShowConsole()         {}
