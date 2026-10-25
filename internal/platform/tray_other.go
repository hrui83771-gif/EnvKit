//go:build !windows

package platform

// tray_other.go —— 非 Windows 平台没有托盘：保持原有控制台行为，调用方退化为阻塞服务。

// TrayActions 与 Windows 版保持同一形状（main 不需要分平台写代码）。
type TrayActions struct {
	OpenConsole    func()
	OpenConfig     func()
	OpenLogs       func()
	Exit           func()
	Ready          func()
	CreateShortcut func()
	ToggleWindow   func() bool
}

// RunTray 非 Windows 直接返回，main 会回到正常的阻塞式服务循环。
func RunTray(icon []byte, act TrayActions) {}

// OpenFolder 非 Windows 平台无需实现（托盘不启用）。
func OpenFolder(path string) {}
