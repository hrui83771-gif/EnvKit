//go:build windows

package platform

// Windows Job Object：把 EnvKit 启动的后台子进程（npm/node/go run 及其孙进程）全部
// 绑定到一个 Job 上，并设置 KILL_ON_JOB_CLOSE。这样无论 EnvKit 以何种方式退出
// （正常关闭、控制台窗口关闭、被任务管理器杀掉、崩溃），Windows 都会自动终止
// 整棵子进程树，避免孤儿 node/main.exe 继续占用端口。

import (
	"errors"
	"fmt"
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
	procIsProcessInJob           = kernel32.NewProc("IsProcessInJob")
)

const (
	jobObjectExtendedLimitInfoClass = 9
	jobObjectLimitKillOnJobClose    = 0x00002000
	processSetQuota                 = 0x0100
	processTerminate                = 0x0001
	processDupHandle                = 0x0040
	// processQueryLimitedInformation：IsProcessInJob 要求句柄带
	// PROCESS_QUERY_INFORMATION 或 PROCESS_QUERY_LIMITED_INFORMATION 之一，
	// 少了它调用直接失败（known=false），"先查是否已在别的 Job 里"就成了摆设：
	// 现象是"已在父 Job 内"这条最有用的线索永远查不出来，用户只会看到一句
	// 泛泛的 Access is denied。（实测：同进程同 pid，只差这个权限位，
	// known 从 false 变 true。）
	processQueryLimitedInformation = 0x1000
	// jobOpenAccess 打开子进程要用的权限集合：加入 Job + 终止 + 查询状态。
	jobOpenAccess = processSetQuota | processTerminate | processQueryLimitedInformation
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

// AssignToJob 把子进程加入 Job。返回 nil 表示兜底生效；
// 返回 errAlreadyInJob 表示环境限制（父 Job 存在，无法嵌套加入）。
//
// v3.3：**先确认它不在任何 Job 里**再加入。Windows 8 起进程一旦属于某个
// Job（VS Code / WorkBuddy / 任务计划 / 容器里启动 EnvKit 就会被塞进一个
// 父 Job），AssignProcessToJobObject 返回 ERROR_ACCESS_DENIED——旧实现失败
// 静默，于是"崩溃不留孤儿"的兜底在这些环境下**从来没生效过**，而日志里
// 一个字的线索都没有。现在把这个事实交回调用方记一行告警。
func AssignToJob(pid int) error {
	hProc, err := syscall.OpenProcess(jobOpenAccess, false, uint32(pid))
	if err != nil || hProc == 0 {
		return fmt.Errorf("OpenProcess(pid=%d) 失败：%v", pid, err)
	}
	defer syscall.CloseHandle(syscall.Handle(hProc))
	h := jobObject()
	if h == 0 {
		return fmt.Errorf("CreateJobObject 失败")
	}
	// 已经在别的 Job 里：嵌套加入会被拒（ERROR_ACCESS_DENIED），旧实现
	// 把这条静默吞掉，"EnvKit 退出即连带杀子进程"在这些环境里其实一直没生效，
	// 而日志里没有任何线索。这里返回一个可识别的原因，让调用方记一行告警。
	// 先查再试，是为了不把真正的权限问题伪装成"环境限制"。
	if in, known := processInJob(hProc); known && in {
		return errAlreadyInJob
	}
	if r, _, e := procAssignProcessToJobObject.Call(h, uintptr(hProc)); r == 0 {
		return fmt.Errorf("AssignProcessToJobObject(pid=%d) 失败：%v", pid, e)
	}
	return nil
}

// errAlreadyInJob 表示子进程已属于别的 Job（嵌套 Job 在 Win8+ 需要显式
// 设置 JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK，本程序不擅自拆别人的 Job）。
var errAlreadyInJob = errors.New("进程已在其它 Job 内")

// IsAlreadyInJob 供调用方区分"环境限制"与"真的失败"。
func IsAlreadyInJob(err error) bool { return errors.Is(err, errAlreadyInJob) }

// processInJob 查进程是否已在某个 Job 内。第二个返回值 false = 查询本身不可用。
func processInJob(hProc syscall.Handle) (bool, bool) {
	var in int32
	r, _, _ := procIsProcessInJob.Call(uintptr(hProc), 0, uintptr(unsafe.Pointer(&in)))
	if r == 0 {
		return false, false
	}
	return in != 0, true
}

// KeepJobAlive 让 Job 在 EnvKit 退出后继续存在：网页「退出程序」选"只退出助手、
// 服务继续运行"时，若不处理，EnvKit 进程结束会关掉 Job 最后一个句柄，
// KILL_ON_JOB_CLOSE 立刻强杀整棵子进程树——与服务继续跑的语义直接冲突。
// 解法是把 Job 句柄复制一份到仍在运行的子进程里：EnvKit 退出后句柄计数不为零，
// Job 不关闭、子进程存活；子进程自身结束时复制句柄随之释放，不会永久泄漏。
// （崩溃 / 任务管理器杀 EnvKit 时未做过复制，Job 照常兜底回收，行为不变。）
//
// alivePid 由调用方提供：返回一个仍在运行的子进程 pid（没有则返回 0）——
// 本包不感知业务的子进程表。
func KeepJobAlive(alivePid func() int) {
	jobMu.Lock()
	h := jobHnd
	jobMu.Unlock()
	if h == 0 {
		return // 本轮从没启动过子进程，Job 尚未创建，无需处理
	}
	if alivePid == nil {
		return
	}
	pid := alivePid()
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
