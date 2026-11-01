//go:build !windows

package main

import "fmt"

// openDirPlatform 非 Windows 平台没有资源管理器：如实报"不支持"，
// 不要静默成功——前端会以为窗口弹出去了。
func openDirPlatform(dir string) error {
	return fmt.Errorf("当前系统不支持在资源管理器中打开目录")
}
