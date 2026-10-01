//go:build windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// diskFreeBytes 返回 exe 所在分区的剩余空间（字节）。失败返回 ok=false（指标行直接省略）。
func diskFreeBytes() (uint64, bool) {
	exe, err := os.Executable()
	if err != nil {
		return 0, false
	}
	root := filepath.VolumeName(exe) + `\`
	rootPtr, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return 0, false
	}
	var freeBytes, totalBytes, totalFree uint64
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetDiskFreeSpaceExW")
	r1, _, _ := proc.Call(
		uintptr(unsafe.Pointer(rootPtr)),
		uintptr(unsafe.Pointer(&freeBytes)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r1 == 0 {
		return 0, false
	}
	return freeBytes, true
}
