//go:build windows

package main

import "os/exec"

// openDirPlatform 用资源管理器打开目录。
//
// 只 Start 不 Wait：explorer 的退出码不可靠（成功打开也可能返回非 0），
// 等它只会让请求多挂几百毫秒。
func openDirPlatform(dir string) error {
	return exec.Command("explorer.exe", dir).Start()
}
