//go:build windows

package platform

// winjob_windows_test.go —— Job Object 的句柄权限回归。
//
// 背景（2026-10-26 复审实测）：AssignToJob 旧实现只用
// PROCESS_SET_QUOTA|PROCESS_TERMINATE 打开子进程，然后调 IsProcessInJob
// 判断"它是不是已经在别的 Job 里"。但 IsProcessInJob 要求句柄带
// PROCESS_QUERY_INFORMATION / PROCESS_QUERY_LIMITED_INFORMATION 之一，
// 少了这个权限位调用直接失败 → known=false → "已在父 Job 内"这条线索
// 永远查不出来，用户只能看到一句泛泛的 Access is denied。
//
// 同一个进程、同一个 pid，只差权限位，结果从 known=false 变 known=true ——
// 这个测试把这条钉死：以后谁改 OpenProcess 的权限集合，这里会立刻红。

import (
	"os"
	"syscall"
	"testing"
)

func TestAssignToJobAccessAllowsInJobQuery(t *testing.T) {
	pid := uint32(os.Getpid())

	h, err := syscall.OpenProcess(jobOpenAccess, false, pid)
	if err != nil {
		t.Fatalf("用 jobOpenAccess 打开进程失败：%v（IsProcessInJob 就没法工作）", err)
	}
	defer syscall.CloseHandle(h)

	if _, known := processInJob(h); !known {
		t.Fatal("jobOpenAccess 打开的句柄应能查询 Job 归属；" +
			"少了 PROCESS_QUERY_LIMITED_INFORMATION 会让 AssignToJob 的" +
			"「已在父 Job 内」判断永远返回 known=false")
	}

	// 对照组：旧的权限集合（不含查询位）应当查不了 —— 这正是当初的缺陷。
	if h2, err := syscall.OpenProcess(processSetQuota|processTerminate, false, pid); err == nil {
		defer syscall.CloseHandle(h2)
		if _, known := processInJob(h2); known {
			t.Log("注意：这个平台上旧权限集合也能查询 Job 归属（与 Windows 桌面版实测不同）")
		}
	}
}
