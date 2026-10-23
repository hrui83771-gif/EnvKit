//go:build !windows

package platform

// 非 Windows 平台占位：Job Object 是 Windows 专属机制，其它平台不做子进程绑定。

func AssignToJob(pid int) { _ = pid }

// KeepJobAlive 非 Windows 无 Job 机制，子进程本就不随 EnvKit 退出而被杀，无需处理。
func KeepJobAlive(alivePid func() int) {}
