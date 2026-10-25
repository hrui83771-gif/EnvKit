//go:build !windows

package platform

// shortcut_other.go —— 非 Windows 平台没有 .lnk，桌面快捷方式无从创建。

import "fmt"

// CreateDesktopShortcut 非 Windows 平台返回错误（托盘本身也不启用）。
func CreateDesktopShortcut(exePath, name, desc string) (string, error) {
	return "", fmt.Errorf("当前平台不支持创建桌面快捷方式")
}

// DesktopShortcutPath 非 Windows 平台无意义，返回空串以便调用方判断。
func DesktopShortcutPath(name string) string { return "" }
