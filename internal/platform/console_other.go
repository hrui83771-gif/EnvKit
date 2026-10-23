//go:build !windows

package platform

// InstallConsoleHook 非 Windows 平台无控制台关闭钩子（保持默认信号行为）。
func InstallConsoleHook(onClose func()) {}
