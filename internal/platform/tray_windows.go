//go:build windows

package platform

// tray_windows.go —— 托盘图标与右键菜单（Windows）。
//
// 为什么放在 platform 包：它是叶子包，不 import 任何业务代码；
// 菜单要做的四件事（开控制台 / 开配置 / 开日志目录 / 退出）都由 main 以回调注入。
//
// 关键约束：**必须在主 goroutine 调用 RunTray**。github.com/getlantern/systray 在
// 包 init 里做了 runtime.LockOSThread()，窗口与消息循环属于同一个 OS 线程；
// 从别的 goroutine 调用会得到一个没有消息泵的托盘（图标出现但点击无反应）。

import (
	"os/exec"

	"github.com/getlantern/systray"
)

// TrayActions 托盘菜单要触发的动作（由 main 注入）。
type TrayActions struct {
	OpenConsole func()
	OpenConfig  func()
	OpenLogs    func()
	Exit        func()
	Ready       func() // 托盘创建成功（日志/自检用；菜单此时已可用）
	// CreateShortcut 在桌面创建指向当前 exe 的快捷方式。
	// 由 main 注入（platform 是叶子包，不碰业务里的 exe 路径与日志）。
	CreateShortcut func()
	// ToggleWindow 切换控制台窗口显隐，返回切换后是否处于隐藏态。
	// 由 main 实现并负责持久化（platform 是叶子包，不碰 config）。
	ToggleWindow func() bool
}

// RunTray 创建托盘并进入消息循环（阻塞，直到进程退出）。
// icon 为空时直接返回，调用方退化为纯控制台模式。
func RunTray(icon []byte, act TrayActions) {
	if len(icon) == 0 {
		return
	}
	systray.Run(func() {
		systray.SetIcon(icon)
		systray.SetTooltip("EnvKit 环境助手")

		mOpen := systray.AddMenuItem("打开控制台", "在浏览器中打开控制台页面")
		mCfg := systray.AddMenuItem("程序配置", "直接打开「程序配置」页")
		mLogs := systray.AddMenuItem("打开日志目录", "在资源管理器中打开 EnvKit 所在目录")
		mShortcut := systray.AddMenuItem("创建桌面快捷方式", "在桌面创建一个指向当前 EnvKit.exe 的快捷方式")
		systray.AddSeparator()
		mHide := systray.AddMenuItem("隐藏窗口到托盘", "隐藏控制台窗口，EnvKit 继续在后台运行")
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("退出 EnvKit", "停止前端/后端服务后退出")

		go func() {
			for {
				select {
				case <-mOpen.ClickedCh:
					if act.OpenConsole != nil {
						act.OpenConsole()
					}
				case <-mCfg.ClickedCh:
					if act.OpenConfig != nil {
						act.OpenConfig()
					}
				case <-mLogs.ClickedCh:
					if act.OpenLogs != nil {
						act.OpenLogs()
					}
				case <-mShortcut.ClickedCh:
					if act.CreateShortcut != nil {
						act.CreateShortcut()
					}
				case <-mHide.ClickedCh:
					if act.ToggleWindow != nil {
						if act.ToggleWindow() {
							mHide.SetTitle("显示控制台窗口")
						} else {
							mHide.SetTitle("隐藏窗口到托盘")
						}
					}
				case <-mQuit.ClickedCh:
					if act.Exit != nil {
						act.Exit() // 内部会停子进程并 os.Exit(0)
					}
					return
				}
			}
		}()
		if act.Ready != nil {
			act.Ready()
		}
	}, func() {})
}

// OpenFolder 用资源管理器打开目录（托盘「打开日志目录」用）。
func OpenFolder(path string) {
	_ = exec.Command("explorer", path).Start()
}
