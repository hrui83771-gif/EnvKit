//go:build !windows

// console_other.go —— 非 Windows 平台无控制台关闭钩子（保持默认信号行为）。

package main

// installConsoleHook 空实现，见 console_windows.go。
func installConsoleHook() {}

// shutdownChildren 与 Windows 版一致，服务于网页「退出程序」的"连带停服务"分支
// （非 Windows 没有 Job 兜底，退出路径完全依赖主动停止）。
func shutdownChildren() {
	stopByKey(scStart, "web", "web-start")
	stopByKey(scStart, "backend", "backend-start")
}
