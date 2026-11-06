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
	"time"

	"github.com/getlantern/systray"
)

// TrayActions 托盘菜单要触发的动作（由 main 注入）。
type TrayActions struct {
	OpenConsole func()
	OpenConfig  func()
	OpenLogs    func()
	Exit        func()
	Ready       func() // 托盘创建成功（日志/自检用；菜单此时已可用）
	// Failed 托盘初始化失败（本地 fork 的 OnInitError 回调）。
	// 上游 v1.2.2 失败时只打日志就 return，宿主完全感知不到托盘没了；
	// 现在由宿主明确告知用户「控制台照常、退出走哪里」，窗口也保持可见。
	Failed func(error)
	// CreateShortcut 在桌面创建指向当前 exe 的快捷方式。
	// 由 main 注入（platform 是叶子包，不碰业务里的 exe 路径与日志）。
	CreateShortcut func()
	// ToggleWindow 切换控制台窗口显隐，返回切换后是否处于隐藏态。
	// 由 main 实现并负责持久化（platform 是叶子包，不碰 config）。
	ToggleWindow func() bool
	// LanBadge 返回「手机访问」菜单项的标题（如 "手机访问：配对码 482913"）。
	// 由 main 定时调用刷新 —— 配对码是进程级的，运行中换码后要能立刻看到。
	LanBadge func() string
	// OpenLan 点击「手机访问」时做什么（main 打开设置页里那一节）。
	OpenLan func()
}

// RunTray 创建托盘并进入消息循环（阻塞，直到进程退出）。
// icon 为空时直接返回，调用方退化为纯控制台模式。
func RunTray(icon []byte, act TrayActions) {
	if len(icon) == 0 {
		return
	}
	// 失败回调必须在 systray.Run 之前注册：Run → Register → registerSystray
	// 是同步链路，回调就在那条链里触发。
	if act.Failed != nil {
		systray.OnInitError(act.Failed)
	}
	systray.Run(func() {
		systray.SetIcon(icon)
		systray.SetTooltip("EnvKit 环境助手")

		mOpen := systray.AddMenuItem("打开控制台", "在浏览器中打开控制台页面")
		mCfg := systray.AddMenuItem("程序配置", "直接打开「程序配置」页")
		mLogs := systray.AddMenuItem("打开日志目录", "在资源管理器中打开 EnvKit 所在目录")
		mShortcut := systray.AddMenuItem("创建桌面快捷方式", "在桌面创建一个指向当前 EnvKit.exe 的快捷方式")
		systray.AddSeparator()
		mLan := systray.AddMenuItem("手机访问：未开启", "手机端配对信息（配对码只在电脑上显示）")
		systray.AddSeparator()
		mHide := systray.AddMenuItem("隐藏窗口到托盘", "隐藏控制台窗口，EnvKit 继续在后台运行")
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("退出 EnvKit", "停止前端/后端服务后退出")

		// 标题刷新：配对码会在运行中更换（设置页「换一个码」），
		// 不刷新就会出现"手机上输托盘里那个码，却提示不对"。
		if act.LanBadge != nil {
			go func() {
				for {
					if t := act.LanBadge(); t != "" {
						mLan.SetTitle(t)
					}
					time.Sleep(2 * time.Second)
				}
			}()
		}

		go func() {
			for {
				select {
				case <-mOpen.ClickedCh:
					if act.OpenConsole != nil {
						act.OpenConsole()
					}
				case <-mLan.ClickedCh:
					if act.OpenLan != nil {
						act.OpenLan()
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
