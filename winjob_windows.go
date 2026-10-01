package main

// Windows Job Object：把 EnvKit 启动的后台子进程（npm/node/go run 及其孙进程）全部
// 绑定到一个 Job 上，并设置 KILL_ON_JOB_CLOSE。这样无论 EnvKit 以何种方式退出
// （正常关闭、控制台窗口关闭、被任务管理器杀掉、崩溃），Windows 都会自动终止
// 整棵子进程树，避免孤儿 node/main.exe 继续占用端口。

import (
	"sync"
	"syscall"
	"unsafe"
)

var (
	jobMu  sync.Mutex
	jobHnd uintptr

	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	procDuplicateHandle          = kernel32.NewProc("DuplicateHandle")
	procGetCurrentProcess        = kernel32.NewProc("GetCurrentProcess")
)

const (
	jobObjectExtendedLimitInfoClass = 9
	jobObjectLimitKillOnJobClose    = 0x00002000
	processSetQuota                 = 0x0100
	processTerminate                = 0x0001
	processDupHandle                = 0x0040
)

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type basicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type extendedLimitInformation struct {
	BasicLimitInformation basicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

// jobObject 惰性创建全局 Job 对象。
func jobObject() uintptr {
	jobMu.Lock()
	defer jobMu.Unlock()
	if jobHnd != 0 {
		return jobHnd
	}
	h, _, _ := procCreateJobObjectW.Call(0, 0)
	if h == 0 {
		return 0
	}
	info := extendedLimitInformation{}
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	_, _, _ = procSetInformationJobObject.Call(
		h,
		jobObjectExtendedLimitInfoClass,
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
	)
	jobHnd = h
	return jobHnd
}

// assignToJob 把子进程加入 Job（失败静默，不影响正常启动流程）。
func assignToJob(pid int) {
	hProc, err := syscall.OpenProcess(processSetQuota|processTerminate, false, uint32(pid))
	if err != nil || hProc == 0 {
		return
	}
	defer syscall.CloseHandle(syscall.Handle(hProc))
	if h := jobObject(); h != 0 {
		_, _, _ = procAssignProcessToJobObject.Call(h, uintptr(hProc))
	}
}

// keepJobAlive 让 Job 在 EnvKit 退出后继续存在：网页「退出程序」选"只退出助手、
// 服务继续运行"时，若不处理，EnvKit 进程结束会关掉 Job 最后一个句柄，
// KILL_ON_JOB_CLOSE 立刻强杀整棵子进程树——与服务继续跑的语义直接冲突。
// 解法是把 Job 句柄复制一份到仍在运行的子进程里：EnvKit 退出后句柄计数不为零，
// Job 不关闭、子进程存活；子进程自身结束时复制句柄随之释放，不会永久泄漏。
// （崩溃 / 任务管理器杀 EnvKit 时未做过复制，Job 照常兜底回收，行为不变。）
func keepJobAlive() {
	jobMu.Lock()
	h := jobHnd
	jobMu.Unlock()
	if h == 0 {
		return // 本轮从没启动过子进程，Job 尚未创建，无需处理
	}
	procMu.Lock()
	pid := 0
	for _, c := range activeProcs {
		if c.Process != nil {
			pid = c.Process.Pid
			break
		}
	}
	procMu.Unlock()
	if pid == 0 {
		return
	}
	hProc, err := syscall.OpenProcess(processDupHandle, false, uint32(pid))
	if err != nil || hProc == 0 {
		return
	}
	defer syscall.CloseHandle(syscall.Handle(hProc))
	// DuplicateHandle(本进程, job句柄, 目标子进程, 不需要回传句柄值, 0, 不继承, 0)
	curProc, _, _ := procGetCurrentProcess.Call()
	_, _, _ = procDuplicateHandle.Call(curProc, h, uintptr(hProc), 0, 0, 0, 0)
}
