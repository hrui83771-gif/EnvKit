//go:build windows

// console_windows.go —— 控制台窗口生命周期管理：
//
// 点 X 的问题是系统级的：控制台收到 CTRL_CLOSE_EVENT 后，无论处理器返回什么，
// 原窗口都会被销毁——"询问后留下"只会得到一个没有窗口的后台进程（v1.10.1 实测踩坑）。
// 所以正确做法是直接把 X 灰掉（点不了），退出统一走 Ctrl+C：
// Ctrl+C → 停掉启动的前端/后端子进程 → 退出，不留孤儿进程。

package main

import (
	"os"
	"syscall"
)

const (
	ctrlCEvent     = 0
	ctrlBreakEvent = 1
	ctrlCloseEvent = 2
)

const (
	scClose     = 0xF060 // 系统菜单「关闭」项（X 按钮同一命令）
	mfByCommand = 0x0000
	mfGrayed    = 0x0001
	mfDisabled  = 0x0002
)

var (
	modKernel32    = syscall.NewLazyDLL("kernel32.dll")
	procSetCtrlHnd = modKernel32.NewProc("SetConsoleCtrlHandler")
	procGetConWnd  = modKernel32.NewProc("GetConsoleWindow")
	modUser32      = syscall.NewLazyDLL("user32.dll")
	procGetSysMenu = modUser32.NewProc("GetSystemMenu")
	procEnableMI   = modUser32.NewProc("EnableMenuItem")
)

// installConsoleHook 注册控制台事件处理器并把 X 按钮灰掉（进程生命周期内调一次）。
func installConsoleHook() {
	cb := syscall.NewCallback(consoleCtrlHandler)
	procSetCtrlHnd.Call(uintptr(cb), 1)
	disableCloseButton()
}

func consoleCtrlHandler(ctype uint32) uintptr {
	// 无论哪种关闭事件（Ctrl+C、点 X、终端标签页关闭、关机），窗口都保不住。
	// 唯一正确的姿势：把子进程停干净后退出，绝不让 EnvKit 变成没有窗口的
	// 后台进程——那正是 v1.10.1 实测"我找不到我的窗口了"的根因。
	shutdownChildren()
	os.Exit(0)
	return 0 // 不可达，满足编译器
}

// disableCloseButton 把控制台系统菜单的「关闭」项和右上角 X 一起灰掉，
// 从根源上杜绝"点 X → 窗口没了进程还在"的困惑。
func disableCloseButton() {
	hwnd, _, _ := procGetConWnd.Call()
	if hwnd == 0 {
		return
	}
	menu, _, _ := procGetSysMenu.Call(hwnd, 0)
	if menu == 0 {
		return
	}
	procEnableMI.Call(menu, scClose, mfByCommand|mfGrayed|mfDisabled)
}

// shutdownChildren 停掉本实例启动的前端/后端子进程（与「全部停止」按钮同一套机制，
// 保证 stoppingProcs 标记与监控 goroutine 状态一致，不留孤儿进程）。
func shutdownChildren() {
	stopByKey(scStart, "web", "web-start")
	stopByKey(scStart, "backend", "backend-start")
}
