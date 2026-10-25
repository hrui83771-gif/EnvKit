//go:build windows

package platform

// shortcut_windows.go —— 创建桌面快捷方式（托盘菜单「创建桌面快捷方式」用）。
//
// 为什么不用 PowerShell + WScript.Shell：那要起一个子进程、加载 .NET，
// 冷启动 300ms+，而且被杀软盯得紧（新建 .lnk 是典型可疑行为）。
// 这里直接走 COM 的 IShellLink + IPersistFile，纯 syscall，零依赖、无子进程。
//
// 关键约束：**必须在 LockOSThread 的 goroutine 里调用**——COM 单元是线程级的，
// 在 Go 里 goroutine 可能被调度到别的 OS 线程，那样 CoInitialize 绑定的单元
// 与后面的 CoCreateInstance 就不在同一个线程上，会拿到 E_FAIL/RPC_E_WRONG_THREAD。
// 所以这里自己 runtime.LockOSThread。

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	modOle32 = syscall.NewLazyDLL("ole32.dll")

	procCoInitializeEx   = modOle32.NewProc("CoInitializeEx")
	procCoUninitialize   = modOle32.NewProc("CoUninitialize")
	procCoCreateInstance = modOle32.NewProc("CoCreateInstance")
)

// CLSID_ShellLink = {00021401-0000-0000-C000-000000000046}
var clsidShellLink = syscall.GUID{
	Data1: 0x00021401, Data2: 0x0000, Data3: 0x0000,
	Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46},
}

// IID_IShellLinkW = {000214F9-0000-0000-C000-000000000046}
var iidShellLinkW = syscall.GUID{
	Data1: 0x000214F9, Data2: 0x0000, Data3: 0x0000,
	Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46},
}

// IID_IPersistFile = {0000010b-0000-0000-C000-000000000046}
var iidPersistFile = syscall.GUID{
	Data1: 0x0000010B, Data2: 0x0000, Data3: 0x0000,
	Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46},
}

const (
	coinitApartmentThreaded = 0x2
	clsctxInprocServer      = 0x1
	rpcEChangedMode         = 0x80010106 // 已初始化成别的单元模型——无害，可继续
	swShownormal            = 1
)

// vtable 索引（IUnknown 占 0=QueryInterface / 1=AddRef / 2=Release，
// 之后严格按 shobjidl_core.h 里 IShellLinkW 的声明顺序）：
//
//	 3 GetPath            4 GetIDList          5 SetIDList
//	 6 GetDescription     7 SetDescription     8 GetWorkingDirectory
//	 9 SetWorkingDirectory  10 GetArguments    11 SetArguments
//	12 GetHotkey         13 SetHotkey         14 GetShowCmd
//	15 SetShowCmd        16 GetIconLocation   17 SetIconLocation
//	18 SetRelativePath   19 Resolve           20 SetPath
//
// IPersistFile 继承 IPersist（GetClassID=3）→ 4 IsDirty 5 Load 6 Save 7 SaveCompleted 8 GetCurFile
const (
	vtblQueryInterface = 0
	vtblRelease        = 2
	vtblSetDesc        = 7  // IShellLinkW::SetDescription
	vtblSetWorkingDir  = 9  // IShellLinkW::SetWorkingDirectory
	vtblSetIconLoc     = 17 // IShellLinkW::SetIconLocation
	vtblSetPath        = 20 // IShellLinkW::SetPath
	vtblPersistSave    = 6  // IPersistFile::Save
)

// DesktopShortcutPath 返回当前用户桌面上的快捷方式完整路径。
// 优先读注册表里的真实桌面位置（OneDrive 重定向 / 中文路径都能拿到），
// 失败再退到 %USERPROFILE%\Desktop。
func DesktopShortcutPath(name string) string {
	if name == "" {
		name = "EnvKit"
	}
	dir := desktopDir()
	return filepath.Join(dir, name+".lnk")
}

func desktopDir() string {
	// 注册表：HKCU\Software\Microsoft\Windows\CurrentVersion\Explorer\User Shell Folders\Desktop
	// 值里可能含 %USERPROFILE%，需要自己展开。
	if p := desktopFromRegistry(); p != "" {
		return p
	}
	if up := os.Getenv("USERPROFILE"); up != "" {
		return filepath.Join(up, "Desktop")
	}
	return "."
}

func desktopFromRegistry() string {
	advapi := syscall.NewLazyDLL("advapi32.dll")
	procRegOpenKeyEx := advapi.NewProc("RegOpenKeyExW")
	procRegQueryValueEx := advapi.NewProc("RegQueryValueExW")
	procRegCloseKey := advapi.NewProc("RegCloseKey")

	const (
		hkeyCurrentUser = 0x80000001
		keyQueryValue   = 0x0001
		regSZ           = 1
		regExpandSZ     = 2
		errorSuccess    = 0
	)

	sub, err := syscall.UTF16PtrFromString(`Software\Microsoft\Windows\CurrentVersion\Explorer\User Shell Folders`)
	if err != nil {
		return ""
	}
	var hkey syscall.Handle
	if r, _, _ := procRegOpenKeyEx.Call(uintptr(hkeyCurrentUser), uintptr(unsafe.Pointer(sub)),
		0, keyQueryValue, uintptr(unsafe.Pointer(&hkey))); r != errorSuccess {
		return ""
	}
	defer procRegCloseKey.Call(uintptr(hkey))

	name, err := syscall.UTF16PtrFromString("Desktop")
	if err != nil {
		return ""
	}
	buf := make([]uint16, syscall.MAX_PATH)
	size := uint32(len(buf) * 2)
	var typ uint32
	if r, _, _ := procRegQueryValueEx.Call(uintptr(hkey), uintptr(unsafe.Pointer(name)),
		0, uintptr(unsafe.Pointer(&typ)), uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size))); r != errorSuccess {
		return ""
	}
	if typ != regSZ && typ != regExpandSZ {
		return ""
	}
	raw := syscall.UTF16ToString(buf)
	if raw == "" {
		return ""
	}
	if typ == regExpandSZ {
		raw = expandEnv(raw)
	}
	return raw
}

// expandEnv 展开 %VAR% 形式的变量（注册表返回的是 REG_EXPAND_SZ）。
func expandEnv(s string) string {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	procExpand := kernel32.NewProc("ExpandEnvironmentStringsW")
	src, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return s
	}
	buf := make([]uint16, syscall.MAX_PATH*2)
	r, _, _ := procExpand.Call(uintptr(unsafe.Pointer(src)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r == 0 {
		return s
	}
	out := syscall.UTF16ToString(buf)
	if out == "" {
		return s
	}
	return out
}

// CreateDesktopShortcut 在桌面创建指向 exePath 的快捷方式，返回 .lnk 完整路径。
//
// 已存在时直接覆盖（IPersistFile::Save 会覆盖），所以重复点击无副作用。
// 图标优先用 exe 自身（资源里已嵌入 app.ico），这样快捷方式不会显示成白纸。
func CreateDesktopShortcut(exePath, name, desc string) (string, error) {
	abs, err := filepath.Abs(exePath)
	if err != nil {
		abs = exePath
	}
	lnk := DesktopShortcutPath(name)

	// COM 单元是线程级的：把当前 goroutine 钉在这个 OS 线程上。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	// S_OK / S_FALSE / RPC_E_CHANGED_MODE 都能继续用
	if hr != 0 && hr != 1 && hr != rpcEChangedMode {
		return "", os.NewSyscallError("CoInitializeEx", syscall.Errno(hr))
	}
	if hr != rpcEChangedMode {
		defer procCoUninitialize.Call()
	}

	var link unsafe.Pointer
	hr, _, _ = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidShellLink)),
		0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidShellLinkW)),
		uintptr(unsafe.Pointer(&link)),
	)
	if hr != 0 || link == nil {
		return "", os.NewSyscallError("CoCreateInstance(ShellLink)", syscall.Errno(hr))
	}
	defer callMethod(link, vtblRelease)

	if r := callPointer(link, vtblSetPath, abs); r != 0 {
		return "", os.NewSyscallError("SetPath", syscall.Errno(r))
	}
	if r := callPointer(link, vtblSetWorkingDir, filepath.Dir(abs)); r != 0 {
		return "", os.NewSyscallError("SetWorkingDirectory", syscall.Errno(r))
	}
	// 图标用 exe 自己的（资源里嵌了 app.ico，索引 0）
	if r := callTwoPointersInt(link, vtblSetIconLoc, abs, 0); r != 0 {
		return "", os.NewSyscallError("SetIconLocation", syscall.Errno(r))
	}
	if desc != "" {
		// 描述失败不算致命，忽略
		_ = callPointer(link, vtblSetDesc, desc)
	}

	var pf unsafe.Pointer
	hr = callQueryInterface(link, &pf)
	if hr != 0 || pf == nil {
		return "", os.NewSyscallError("QueryInterface(IPersistFile)", syscall.Errno(hr))
	}
	defer callMethod(pf, vtblRelease)

	// Save(path, TRUE) —— 第二个参数 remember=true
	if r := callSaveWideStr(pf, vtblPersistSave, lnk, 1); r != 0 {
		return "", os.NewSyscallError("IPersistFile::Save", syscall.Errno(r))
	}
	return lnk, nil
}

// ---- COM vtable 调用辅助 ----

// methodAddr 取出 vtable 里第 idx 个函数指针。
func methodAddr(obj unsafe.Pointer, idx int) uintptr {
	vtable := *(*unsafe.Pointer)(obj)
	return *(*uintptr)(unsafe.Add(vtable, uintptr(idx)*unsafe.Sizeof(uintptr(0))))
}

func callMethod(obj unsafe.Pointer, idx int) uintptr {
	r, _, _ := syscall.SyscallN(methodAddr(obj, idx), uintptr(obj))
	return r
}

func callPointer(obj unsafe.Pointer, idx int, wideStr string) uintptr {
	p, err := syscall.UTF16PtrFromString(wideStr)
	if err != nil {
		return 0x80070057 // E_INVALIDARG
	}
	r, _, _ := syscall.SyscallN(methodAddr(obj, idx), uintptr(obj), uintptr(unsafe.Pointer(p)))
	return r
}

func callTwoPointersInt(obj unsafe.Pointer, idx int, wideStr string, n int32) uintptr {
	p, err := syscall.UTF16PtrFromString(wideStr)
	if err != nil {
		return 0x80070057
	}
	r, _, _ := syscall.SyscallN(methodAddr(obj, idx), uintptr(obj),
		uintptr(unsafe.Pointer(p)), uintptr(n))
	return r
}

// callQueryInterface 调 IUnknown::QueryInterface 拿 IPersistFile。
func callQueryInterface(obj unsafe.Pointer, out *unsafe.Pointer) uintptr {
	r, _, _ := syscall.SyscallN(methodAddr(obj, vtblQueryInterface), uintptr(obj),
		uintptr(unsafe.Pointer(&iidPersistFile)), uintptr(unsafe.Pointer(out)))
	return r
}

// callSaveWideStr 调 IPersistFile::Save(pszFileName, fRemember)。
func callSaveWideStr(obj unsafe.Pointer, idx int, path string, remember uintptr) uintptr {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0x80070057
	}
	r, _, _ := syscall.SyscallN(methodAddr(obj, idx), uintptr(obj),
		uintptr(unsafe.Pointer(p)), remember)
	return r
}
