//go:build !windows

package platform

// DiskFreeBytes 非 Windows 平台不采集磁盘指标（本工具面向 Windows 桌面，直接省略该行）。
func DiskFreeBytes() (uint64, bool) { return 0, false }
