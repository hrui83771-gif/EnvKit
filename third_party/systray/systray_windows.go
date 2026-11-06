// +build windows

package systray

import (
	"crypto/md5"
	"encoding/hex"
	"fmt" // ENVKIT: addIcon 的失败诊断
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time" // ENVKIT: addIcon 的就绪等待与重试退避
	"unsafe"

	"golang.org/x/sys/windows"
)

// Helpful sources: https://github.com/golang/exp/blob/master/shiny/driver/internal/win32

var (
	g32                     = windows.NewLazySystemDLL("Gdi32.dll")
	pCreateCompatibleBitmap = g32.NewProc("CreateCompatibleBitmap")
	pCreateCompatibleDC     = g32.NewProc("CreateCompatibleDC")
	pDeleteDC               = g32.NewProc("DeleteDC")
	pSelectObject           = g32.NewProc("SelectObject")

	k32              = windows.NewLazySystemDLL("Kernel32.dll")
	pGetModuleHandle = k32.NewProc("GetModuleHandleW")

	s32              = windows.NewLazySystemDLL("Shell32.dll")
	pShellNotifyIcon = s32.NewProc("Shell_NotifyIconW")

	u32                    = windows.NewLazySystemDLL("User32.dll")
	pCreateMenu            = u32.NewProc("CreateMenu")
	pCreatePopupMenu       = u32.NewProc("CreatePopupMenu")
	pCreateWindowEx        = u32.NewProc("CreateWindowExW")
	pDefWindowProc         = u32.NewProc("DefWindowProcW")
	pRemoveMenu            = u32.NewProc("RemoveMenu")
	pDestroyWindow         = u32.NewProc("DestroyWindow")
	pDispatchMessage       = u32.NewProc("DispatchMessageW")
	pDrawIconEx            = u32.NewProc("DrawIconEx")
	pFindWindow            = u32.NewProc("FindWindowW")   // ENVKIT: 等任务栏窗口就绪
	pFindWindowEx          = u32.NewProc("FindWindowExW") // ENVKIT: 通知区域子窗口是否就绪
	pIsWindow              = u32.NewProc("IsWindow")      // ENVKIT: 失败诊断里核实宿主窗口有效
	pGetCursorPos          = u32.NewProc("GetCursorPos")
	pGetDC                 = u32.NewProc("GetDC")
	pGetMessage            = u32.NewProc("GetMessageW")
	pGetSystemMetrics      = u32.NewProc("GetSystemMetrics")
	pInsertMenuItem        = u32.NewProc("InsertMenuItemW")
	pLoadCursor            = u32.NewProc("LoadCursorW")
	pLoadIcon              = u32.NewProc("LoadIconW")
	pLoadImage             = u32.NewProc("LoadImageW")
	pPostMessage           = u32.NewProc("PostMessageW")
	pPostQuitMessage       = u32.NewProc("PostQuitMessage")
	pRegisterClass         = u32.NewProc("RegisterClassExW")
	pRegisterWindowMessage = u32.NewProc("RegisterWindowMessageW")
	pReleaseDC             = u32.NewProc("ReleaseDC")
	pSetForegroundWindow   = u32.NewProc("SetForegroundWindow")
	pSetMenuInfo           = u32.NewProc("SetMenuInfo")
	pSetMenuItemInfo       = u32.NewProc("SetMenuItemInfoW")
	pShowWindow            = u32.NewProc("ShowWindow")
	pTrackPopupMenu        = u32.NewProc("TrackPopupMenu")
	pTranslateMessage      = u32.NewProc("TranslateMessage")
	pUnregisterClass       = u32.NewProc("UnregisterClassW")
	pUpdateWindow          = u32.NewProc("UpdateWindow")
)

// Contains window class information.
// It is used with the RegisterClassEx and GetClassInfoEx functions.
// https://msdn.microsoft.com/en-us/library/ms633577.aspx
type wndClassEx struct {
	Size, Style                        uint32
	WndProc                            uintptr
	ClsExtra, WndExtra                 int32
	Instance, Icon, Cursor, Background windows.Handle
	MenuName, ClassName                *uint16
	IconSm                             windows.Handle
}

// Registers a window class for subsequent use in calls to the CreateWindow or CreateWindowEx function.
// https://msdn.microsoft.com/en-us/library/ms633587.aspx
func (w *wndClassEx) register() error {
	w.Size = uint32(unsafe.Sizeof(*w))
	res, _, err := pRegisterClass.Call(uintptr(unsafe.Pointer(w)))
	if res == 0 {
		return err
	}
	return nil
}

// Unregisters a window class, freeing the memory required for the class.
// https://msdn.microsoft.com/en-us/library/ms644899.aspx
func (w *wndClassEx) unregister() error {
	res, _, err := pUnregisterClass.Call(
		uintptr(unsafe.Pointer(w.ClassName)),
		uintptr(w.Instance),
	)
	if res == 0 {
		return err
	}
	return nil
}

// Contains information that the system needs to display notifications in the notification area.
// Used by Shell_NotifyIcon.
// https://msdn.microsoft.com/en-us/library/windows/desktop/bb773352(v=vs.85).aspx
// https://msdn.microsoft.com/en-us/library/windows/desktop/bb762159
type notifyIconData struct {
	Size                       uint32
	Wnd                        windows.Handle
	ID, Flags, CallbackMessage uint32
	Icon                       windows.Handle
	Tip                        [128]uint16
	State, StateMask           uint32
	Info                       [256]uint16
	Timeout, Version           uint32
	InfoTitle                  [64]uint16
	InfoFlags                  uint32
	GuidItem                   windows.GUID
	BalloonIcon                windows.Handle
}

func (nid *notifyIconData) add() error {
	const NIM_ADD = 0x00000000
	res, _, err := pShellNotifyIcon.Call(
		uintptr(NIM_ADD),
		uintptr(unsafe.Pointer(nid)),
	)
	if res == 0 {
		return err
	}
	return nil
}

func (nid *notifyIconData) modify() error {
	const NIM_MODIFY = 0x00000001
	res, _, err := pShellNotifyIcon.Call(
		uintptr(NIM_MODIFY),
		uintptr(unsafe.Pointer(nid)),
	)
	if res == 0 {
		return err
	}
	return nil
}

func (nid *notifyIconData) delete() error {
	const NIM_DELETE = 0x00000002
	res, _, err := pShellNotifyIcon.Call(
		uintptr(NIM_DELETE),
		uintptr(unsafe.Pointer(nid)),
	)
	if res == 0 {
		return err
	}
	return nil
}

// Contains information about a menu item.
// https://msdn.microsoft.com/en-us/library/windows/desktop/ms647578(v=vs.85).aspx
type menuItemInfo struct {
	Size, Mask, Type, State     uint32
	ID                          uint32
	SubMenu, Checked, Unchecked windows.Handle
	ItemData                    uintptr
	TypeData                    *uint16
	Cch                         uint32
	BMPItem                     windows.Handle
}

// The POINT structure defines the x- and y- coordinates of a point.
// https://msdn.microsoft.com/en-us/library/windows/desktop/dd162805(v=vs.85).aspx
type point struct {
	X, Y int32
}

// Contains information about loaded resources
type winTray struct {
	instance,
	icon,
	cursor,
	window windows.Handle

	loadedImages   map[string]windows.Handle
	muLoadedImages sync.RWMutex
	// menus keeps track of the submenus keyed by the menu item ID, plus 0
	// which corresponds to the main popup menu.
	menus   map[uint32]windows.Handle
	muMenus sync.RWMutex
	// menuOf keeps track of the menu each menu item belongs to.
	menuOf   map[uint32]windows.Handle
	muMenuOf sync.RWMutex
	// menuItemIcons maintains the bitmap of each menu item (if applies). It's
	// needed to show the icon correctly when showing a previously hidden menu
	// item again.
	menuItemIcons   map[uint32]windows.Handle
	muMenuItemIcons sync.RWMutex
	visibleItems    map[uint32][]uint32
	muVisibleItems  sync.RWMutex

	nid   *notifyIconData
	muNID sync.RWMutex
	wcex  *wndClassEx

	wmSystrayMessage,
	wmTaskbarCreated uint32
}

// Loads an image from file and shows it in tray.
// Shell_NotifyIcon: https://msdn.microsoft.com/en-us/library/windows/desktop/bb762159(v=vs.85).aspx
func (t *winTray) setIcon(src string) error {
	const NIF_ICON = 0x00000002

	h, err := t.loadIconFrom(src)
	if err != nil {
		return err
	}

	t.muNID.Lock()
	defer t.muNID.Unlock()
	t.nid.Icon = h
	t.nid.Flags |= NIF_ICON
	t.nid.Size = uint32(unsafe.Sizeof(*t.nid))

	return t.nid.modify()
}

// Sets tooltip on icon.
// Shell_NotifyIcon: https://msdn.microsoft.com/en-us/library/windows/desktop/bb762159(v=vs.85).aspx
func (t *winTray) setTooltip(src string) error {
	const NIF_TIP = 0x00000004
	b, err := windows.UTF16FromString(src)
	if err != nil {
		return err
	}

	t.muNID.Lock()
	defer t.muNID.Unlock()
	copy(t.nid.Tip[:], b[:])
	t.nid.Flags |= NIF_TIP
	t.nid.Size = uint32(unsafe.Sizeof(*t.nid))

	return t.nid.modify()
}

var wt winTray

// WindowProc callback function that processes messages sent to a window.
// https://msdn.microsoft.com/en-us/library/windows/desktop/ms633573(v=vs.85).aspx
func (t *winTray) wndProc(hWnd windows.Handle, message uint32, wParam, lParam uintptr) (lResult uintptr) {
	const (
		WM_RBUTTONUP  = 0x0205
		WM_LBUTTONUP  = 0x0202
		WM_COMMAND    = 0x0111
		WM_ENDSESSION = 0x0016
		WM_CLOSE      = 0x0010
		WM_DESTROY    = 0x0002
	)
	switch message {
	case WM_COMMAND:
		menuItemId := int32(wParam)
		// https://docs.microsoft.com/en-us/windows/win32/menurc/wm-command#menus
		if menuItemId != -1 {
			systrayMenuItemSelected(uint32(wParam))
		}
	case WM_CLOSE:
		pDestroyWindow.Call(uintptr(t.window))
		t.wcex.unregister()
	case WM_DESTROY:
		// same as WM_ENDSESSION, but throws 0 exit code after all
		defer pPostQuitMessage.Call(uintptr(int32(0)))
		fallthrough
	case WM_ENDSESSION:
		t.muNID.Lock()
		if t.nid != nil {
			t.nid.delete()
		}
		t.muNID.Unlock()
		systrayExit()
	case t.wmSystrayMessage:
		switch lParam {
		case WM_RBUTTONUP, WM_LBUTTONUP:
			t.showMenu()
		}
	case t.wmTaskbarCreated: // on explorer.exe restarts
		// ENVKIT: Explorer 刚重启时任务栏常常还没建好，直接 nid.add() 会静默失败；
		// 走 addIcon() 的就绪等待 + 重试，图标才能真正回来。若初始化时因为
		// addIcon 失败而没建菜单，图标回来后在这里补建，避免「有图标没菜单」。
		t.muNID.Lock()
		addErr := t.addIcon()
		t.muNID.Unlock()
		if addErr == nil && !trayMenuReady {
			finishInit()
		}
	default:
		// Calls the default window procedure to provide default processing for any window messages that an application does not process.
		// https://msdn.microsoft.com/en-us/library/windows/desktop/ms633572(v=vs.85).aspx
		lResult, _, _ = pDefWindowProc.Call(
			uintptr(hWnd),
			uintptr(message),
			uintptr(wParam),
			uintptr(lParam),
		)
	}
	return
}

func (t *winTray) initInstance() error {
	const IDI_APPLICATION = 32512
	const IDC_ARROW = 32512 // Standard arrow
	// https://msdn.microsoft.com/en-us/library/windows/desktop/ms633548(v=vs.85).aspx
	const SW_HIDE = 0
	const CW_USEDEFAULT = 0x80000000
	// https://msdn.microsoft.com/en-us/library/windows/desktop/ms632600(v=vs.85).aspx
	const (
		WS_CAPTION     = 0x00C00000
		WS_MAXIMIZEBOX = 0x00010000
		WS_MINIMIZEBOX = 0x00020000
		WS_OVERLAPPED  = 0x00000000
		WS_SYSMENU     = 0x00080000
		WS_THICKFRAME  = 0x00040000

		WS_OVERLAPPEDWINDOW = WS_OVERLAPPED | WS_CAPTION | WS_SYSMENU | WS_THICKFRAME | WS_MINIMIZEBOX | WS_MAXIMIZEBOX
	)
	// https://msdn.microsoft.com/en-us/library/windows/desktop/ff729176
	const (
		CS_HREDRAW = 0x0002
		CS_VREDRAW = 0x0001
	)
	const NIF_MESSAGE = 0x00000001

	// https://msdn.microsoft.com/en-us/library/windows/desktop/ms644931(v=vs.85).aspx
	const WM_USER = 0x0400

	const (
		className  = "SystrayClass"
		windowName = ""
	)

	t.wmSystrayMessage = WM_USER + 1
	t.visibleItems = make(map[uint32][]uint32)
	t.menus = make(map[uint32]windows.Handle)
	t.menuOf = make(map[uint32]windows.Handle)
	t.menuItemIcons = make(map[uint32]windows.Handle)

	taskbarEventNamePtr, _ := windows.UTF16PtrFromString("TaskbarCreated")
	// https://msdn.microsoft.com/en-us/library/windows/desktop/ms644947
	res, _, err := pRegisterWindowMessage.Call(
		uintptr(unsafe.Pointer(taskbarEventNamePtr)),
	)
	t.wmTaskbarCreated = uint32(res)

	t.loadedImages = make(map[string]windows.Handle)

	instanceHandle, _, err := pGetModuleHandle.Call(0)
	if instanceHandle == 0 {
		return err
	}
	t.instance = windows.Handle(instanceHandle)

	// https://msdn.microsoft.com/en-us/library/windows/desktop/ms648072(v=vs.85).aspx
	iconHandle, _, err := pLoadIcon.Call(0, uintptr(IDI_APPLICATION))
	if iconHandle == 0 {
		return err
	}
	t.icon = windows.Handle(iconHandle)

	// https://msdn.microsoft.com/en-us/library/windows/desktop/ms648391(v=vs.85).aspx
	cursorHandle, _, err := pLoadCursor.Call(0, uintptr(IDC_ARROW))
	if cursorHandle == 0 {
		return err
	}
	t.cursor = windows.Handle(cursorHandle)

	classNamePtr, err := windows.UTF16PtrFromString(className)
	if err != nil {
		return err
	}

	windowNamePtr, err := windows.UTF16PtrFromString(windowName)
	if err != nil {
		return err
	}

	t.wcex = &wndClassEx{
		Style:      CS_HREDRAW | CS_VREDRAW,
		WndProc:    windows.NewCallback(t.wndProc),
		Instance:   t.instance,
		Icon:       t.icon,
		Cursor:     t.cursor,
		Background: windows.Handle(6), // (COLOR_WINDOW + 1)
		ClassName:  classNamePtr,
		IconSm:     t.icon,
	}
	if err := t.wcex.register(); err != nil {
		return err
	}

	windowHandle, _, err := pCreateWindowEx.Call(
		uintptr(0),
		uintptr(unsafe.Pointer(classNamePtr)),
		uintptr(unsafe.Pointer(windowNamePtr)),
		uintptr(WS_OVERLAPPEDWINDOW),
		uintptr(CW_USEDEFAULT),
		uintptr(CW_USEDEFAULT),
		uintptr(CW_USEDEFAULT),
		uintptr(CW_USEDEFAULT),
		uintptr(0),
		uintptr(0),
		uintptr(t.instance),
		uintptr(0),
	)
	if windowHandle == 0 {
		return err
	}
	t.window = windows.Handle(windowHandle)

	pShowWindow.Call(
		uintptr(t.window),
		uintptr(SW_HIDE),
	)

	pUpdateWindow.Call(
		uintptr(t.window),
	)

	t.muNID.Lock()
	defer t.muNID.Unlock()
	// ENVKIT: 一次成型。上游只带 NIF_MESSAGE（Icon/Tip 全空），要等 onReady
	// 里的 SetIcon 才能补上图标；一旦 NIM_ADD 被拒，onReady 根本不跑，通知区域
	// 里就永远是个空壳。这里先用系统默认图标 + ASCII 提示占位，随后 SetIcon /
	// SetTooltip 会替换成宿主真正的内容。本机实测带 NIF_ICON|NIF_TIP 的
	// NIM_ADD 比空条目稳（2026-11-06 .gotmp/traydiag 探针 A/D/F/G 组）。
	t.nid = &notifyIconData{
		Wnd:             windows.Handle(t.window),
		ID:              100,
		Flags:           NIF_MESSAGE | 0x00000002 /* NIF_ICON */ | 0x00000004 /* NIF_TIP */,
		CallbackMessage: t.wmSystrayMessage,
		Icon:            t.icon,
	}
	if tip, err := windows.UTF16FromString("EnvKit"); err == nil {
		copy(t.nid.Tip[:], tip)
	}
	t.nid.Size = uint32(unsafe.Sizeof(*t.nid))

	return t.addIcon()
}

// ENVKIT —— 托盘图标添加：等通知区域就绪 + 换键重试 + 接管已有条目。
//
// 上游 v1.2.2 在 initInstance() 末尾直接 `return t.nid.add()`；失败只 log 一行
// 就 return，既不调 systrayReady()（菜单不建、宿主无感），也没有任何恢复动作。
//
// 2026-11-06 本机 .gotmp/traydiag 逐步 Win32 探针的客观事实：
//   - 首次 Shell_NotifyIcon(NIM_ADD) 成功；
//   - **同一个 ID 再 NIM_ADD 必失败，返回 0x80004005 "Unspecified error"** ——
//     这不是"调用前残留的 COM 错误码"（上一版注释写错了），而是这个 API 在
//     条目已存在 / 被拒时自己的返回值；同 ID 跨 hWnd 也一样失败（G 组）；
//   - 条目已存在时 NIM_MODIFY 成功；条目不存在时 NIM_MODIFY 必失败（F 组），
//     所以拿 MODIFY 探测"是否已存在"不会假成功；
//   - NIM_DELETE 后用**同一个 ID** 再 NIM_ADD 仍然失败（B 组），别指望删了重加；
//   - 换一个图标 ID 通常能成功（C/G 组），带 NIF_ICON|NIF_TIP 一次成型更稳（D 组）。
//
// 上一版只做了「等 Shell_TrayWnd + 对同一个 ID=100 重试 6 次」，而失败恰恰是
// 这个 ID 被占，重试一万次也不会变 —— 这就是 11-06 第二次实测仍然失败的原因。
// 现在每轮失败后换 ID，并先用 MODIFY 接管可能已存在的条目。
//
// 下面几个包级变量是给本模块包内单元测试注入桩用的（replace 指向的模块不会被
// 宿主 go test ./... 自动跑到），默认值就是真实调用，产品路径行为不变。
var (
	iconAddOp       = func(n *notifyIconData) error { return n.add() }
	iconModifyOp    = func(n *notifyIconData) error { return n.modify() }
	iconReadyProbe  = func() (shell, region uintptr) { return probeTrayReady() }
	iconRetryDelay  = 800 * time.Millisecond
	iconMaxAttempts = 6
	iconLastNote    string
	onInitRecovered func(string)
)

// OnInitRecovered 让宿主在「走了兜底才把图标加回来」时收到一条可读说明。
// 首次 NIM_ADD 直接成功不触发（宿主已有 Ready 日志）。
func OnInitRecovered(fn func(string)) { onInitRecovered = fn }

func onoff(v uintptr) string {
	if v != 0 {
		return "存在"
	}
	return "未出现"
}

func probeTrayReady() (shell, region uintptr) {
	cls, _ := windows.UTF16PtrFromString("Shell_TrayWnd")
	shell, _, _ = pFindWindow.Call(uintptr(unsafe.Pointer(cls)), 0)
	if shell != 0 {
		sub, _ := windows.UTF16PtrFromString("TrayNotifyWnd")
		region, _, _ = pFindWindowEx.Call(shell, 0, uintptr(unsafe.Pointer(sub)), 0)
	}
	return shell, region
}

func (t *winTray) recoveredNote(note string) {
	iconLastNote = note
	if onInitRecovered != nil {
		onInitRecovered(note)
	}
}

// addIcon 调用方（initInstance / wndProc）负责持有 muNID，本函数不自己加锁。
func (t *winTray) addIcon() error {
	firstID := t.nid.ID
	var facts []string
	var lastErr error
	for i := 0; i < iconMaxAttempts; i++ {
		shell, _ := iconReadyProbe()
		if shell != 0 {
			id := t.nid.ID
			if err := iconAddOp(t.nid); err == nil {
				if i > 0 || id != firstID {
					t.recoveredNote(fmt.Sprintf("第 %d 轮用图标 ID=%d 的 NIM_ADD 成功", i+1, id))
				}
				return nil
			} else {
				lastErr = err
			}
			// 条目已存在时 MODIFY 能接管；实测条目不存在时 MODIFY 必失败，
			// 所以这里不会把"没加上"误判成成功。
			if iconModifyOp(t.nid) == nil {
				t.recoveredNote(fmt.Sprintf("NIM_MODIFY 接管了已有的图标条目（ID=%d）", id))
				return nil
			}
			facts = append(facts, fmt.Sprintf("ID=%d 的 ADD/MODIFY 均被拒", id))
			// 关键：换键。同一个 ID 反复重试只会撞同一份残留条目。
			t.nid.ID++
			if err := iconAddOp(t.nid); err == nil {
				t.recoveredNote(fmt.Sprintf("原 ID=%d 被占，换到 ID=%d 后 NIM_ADD 成功", id, t.nid.ID))
				return nil
			} else {
				lastErr = err // 保留真实底层错误，别拿自己拼的说明冒充
			}
			facts = append(facts, fmt.Sprintf("换到 ID=%d 仍被拒", t.nid.ID))
			t.nid.ID++ // 下一轮再换一个
		} else {
			facts = append(facts, fmt.Sprintf("第 %d 轮 Shell_TrayWnd 未出现", i+1))
		}
		if i < iconMaxAttempts-1 && iconRetryDelay > 0 {
			time.Sleep(iconRetryDelay)
		}
	}
	shell, region := iconReadyProbe()
	windowOK, _, _ := pIsWindow.Call(uintptr(t.window))
	state := fmt.Sprintf("任务栏窗口%s、通知区域窗口%s、宿主窗口%s",
		onoff(shell), onoff(region), onoff(windowOK))
	last := "未调用 Shell_NotifyIcon"
	if lastErr != nil {
		last = lastErr.Error()
	}
	return fmt.Errorf("托盘图标添加失败：%s；已尝试图标 ID %d..%d（%d 轮）；逐轮事实 %s；末次底层信息 %s",
		state, firstID, t.nid.ID, iconMaxAttempts, strings.Join(facts, " / "), last)
}

func (t *winTray) createMenu() error {
	const MIM_APPLYTOSUBMENUS = 0x80000000 // Settings apply to the menu and all of its submenus

	menuHandle, _, err := pCreatePopupMenu.Call()
	if menuHandle == 0 {
		return err
	}
	t.menus[0] = windows.Handle(menuHandle)

	// https://msdn.microsoft.com/en-us/library/windows/desktop/ms647575(v=vs.85).aspx
	mi := struct {
		Size, Mask, Style, Max uint32
		Background             windows.Handle
		ContextHelpID          uint32
		MenuData               uintptr
	}{
		Mask: MIM_APPLYTOSUBMENUS,
	}
	mi.Size = uint32(unsafe.Sizeof(mi))

	res, _, err := pSetMenuInfo.Call(
		uintptr(t.menus[0]),
		uintptr(unsafe.Pointer(&mi)),
	)
	if res == 0 {
		return err
	}
	return nil
}

func (t *winTray) convertToSubMenu(menuItemId uint32) (windows.Handle, error) {
	const MIIM_SUBMENU = 0x00000004

	res, _, err := pCreateMenu.Call()
	if res == 0 {
		return 0, err
	}
	menu := windows.Handle(res)

	mi := menuItemInfo{Mask: MIIM_SUBMENU, SubMenu: menu}
	mi.Size = uint32(unsafe.Sizeof(mi))
	t.muMenuOf.RLock()
	hMenu := t.menuOf[menuItemId]
	t.muMenuOf.RUnlock()
	res, _, err = pSetMenuItemInfo.Call(
		uintptr(hMenu),
		uintptr(menuItemId),
		0,
		uintptr(unsafe.Pointer(&mi)),
	)
	if res == 0 {
		return 0, err
	}
	t.muMenus.Lock()
	t.menus[menuItemId] = menu
	t.muMenus.Unlock()
	return menu, nil
}

func (t *winTray) addOrUpdateMenuItem(menuItemId uint32, parentId uint32, title string, disabled, checked bool) error {
	// https://msdn.microsoft.com/en-us/library/windows/desktop/ms647578(v=vs.85).aspx
	const (
		MIIM_FTYPE   = 0x00000100
		MIIM_BITMAP  = 0x00000080
		MIIM_STRING  = 0x00000040
		MIIM_SUBMENU = 0x00000004
		MIIM_ID      = 0x00000002
		MIIM_STATE   = 0x00000001
	)
	const MFT_STRING = 0x00000000
	const (
		MFS_CHECKED  = 0x00000008
		MFS_DISABLED = 0x00000003
	)
	titlePtr, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return err
	}

	mi := menuItemInfo{
		Mask:     MIIM_FTYPE | MIIM_STRING | MIIM_ID | MIIM_STATE,
		Type:     MFT_STRING,
		ID:       uint32(menuItemId),
		TypeData: titlePtr,
		Cch:      uint32(len(title)),
	}
	mi.Size = uint32(unsafe.Sizeof(mi))
	if disabled {
		mi.State |= MFS_DISABLED
	}
	if checked {
		mi.State |= MFS_CHECKED
	}
	t.muMenuItemIcons.RLock()
	hIcon := t.menuItemIcons[menuItemId]
	t.muMenuItemIcons.RUnlock()
	if hIcon > 0 {
		mi.Mask |= MIIM_BITMAP
		mi.BMPItem = hIcon
	}

	var res uintptr
	t.muMenus.RLock()
	menu, exists := t.menus[parentId]
	t.muMenus.RUnlock()
	if !exists {
		menu, err = t.convertToSubMenu(parentId)
		if err != nil {
			return err
		}
		t.muMenus.Lock()
		t.menus[parentId] = menu
		t.muMenus.Unlock()
	} else if t.getVisibleItemIndex(parentId, menuItemId) != -1 {
		// We set the menu item info based on the menuID
		res, _, err = pSetMenuItemInfo.Call(
			uintptr(menu),
			uintptr(menuItemId),
			0,
			uintptr(unsafe.Pointer(&mi)),
		)
	}

	if res == 0 {
		// Menu item does not already exist, create it
		t.muMenus.RLock()
		submenu, exists := t.menus[menuItemId]
		t.muMenus.RUnlock()
		if exists {
			mi.Mask |= MIIM_SUBMENU
			mi.SubMenu = submenu
		}
		t.addToVisibleItems(parentId, menuItemId)
		position := t.getVisibleItemIndex(parentId, menuItemId)
		res, _, err = pInsertMenuItem.Call(
			uintptr(menu),
			uintptr(position),
			1,
			uintptr(unsafe.Pointer(&mi)),
		)
		if res == 0 {
			t.delFromVisibleItems(parentId, menuItemId)
			return err
		}
		t.muMenuOf.Lock()
		t.menuOf[menuItemId] = menu
		t.muMenuOf.Unlock()
	}

	return nil
}

func (t *winTray) addSeparatorMenuItem(menuItemId, parentId uint32) error {
	// https://msdn.microsoft.com/en-us/library/windows/desktop/ms647578(v=vs.85).aspx
	const (
		MIIM_FTYPE = 0x00000100
		MIIM_ID    = 0x00000002
		MIIM_STATE = 0x00000001
	)
	const MFT_SEPARATOR = 0x00000800

	mi := menuItemInfo{
		Mask: MIIM_FTYPE | MIIM_ID | MIIM_STATE,
		Type: MFT_SEPARATOR,
		ID:   uint32(menuItemId),
	}

	mi.Size = uint32(unsafe.Sizeof(mi))

	t.addToVisibleItems(parentId, menuItemId)
	position := t.getVisibleItemIndex(parentId, menuItemId)
	t.muMenus.RLock()
	menu := uintptr(t.menus[parentId])
	t.muMenus.RUnlock()
	res, _, err := pInsertMenuItem.Call(
		menu,
		uintptr(position),
		1,
		uintptr(unsafe.Pointer(&mi)),
	)
	if res == 0 {
		return err
	}

	return nil
}

func (t *winTray) hideMenuItem(menuItemId, parentId uint32) error {
	// https://docs.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-removemenu
	const MF_BYCOMMAND = 0x00000000
	const ERROR_SUCCESS syscall.Errno = 0

	t.muMenus.RLock()
	menu := uintptr(t.menus[parentId])
	t.muMenus.RUnlock()
	res, _, err := pRemoveMenu.Call(
		menu,
		uintptr(menuItemId),
		MF_BYCOMMAND,
	)
	if res == 0 && err.(syscall.Errno) != ERROR_SUCCESS {
		return err
	}
	t.delFromVisibleItems(parentId, menuItemId)

	return nil
}

func (t *winTray) showMenu() error {
	const (
		TPM_BOTTOMALIGN = 0x0020
		TPM_LEFTALIGN   = 0x0000
	)
	p := point{}
	res, _, err := pGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	if res == 0 {
		return err
	}
	pSetForegroundWindow.Call(uintptr(t.window))

	res, _, err = pTrackPopupMenu.Call(
		uintptr(t.menus[0]),
		TPM_BOTTOMALIGN|TPM_LEFTALIGN,
		uintptr(p.X),
		uintptr(p.Y),
		0,
		uintptr(t.window),
		0,
	)
	if res == 0 {
		return err
	}

	return nil
}

func (t *winTray) delFromVisibleItems(parent, val uint32) {
	t.muVisibleItems.Lock()
	defer t.muVisibleItems.Unlock()
	visibleItems := t.visibleItems[parent]
	for i, itemval := range visibleItems {
		if val == itemval {
			t.visibleItems[parent] = append(visibleItems[:i], visibleItems[i+1:]...)
			break
		}
	}
}

func (t *winTray) addToVisibleItems(parent, val uint32) {
	t.muVisibleItems.Lock()
	defer t.muVisibleItems.Unlock()
	if visibleItems, exists := t.visibleItems[parent]; !exists {
		t.visibleItems[parent] = []uint32{val}
	} else {
		newvisible := append(visibleItems, val)
		sort.Slice(newvisible, func(i, j int) bool { return newvisible[i] < newvisible[j] })
		t.visibleItems[parent] = newvisible
	}
}

func (t *winTray) getVisibleItemIndex(parent, val uint32) int {
	t.muVisibleItems.RLock()
	defer t.muVisibleItems.RUnlock()
	for i, itemval := range t.visibleItems[parent] {
		if val == itemval {
			return i
		}
	}
	return -1
}

// Loads an image from file to be shown in tray or menu item.
// LoadImage: https://msdn.microsoft.com/en-us/library/windows/desktop/ms648045(v=vs.85).aspx
func (t *winTray) loadIconFrom(src string) (windows.Handle, error) {
	const IMAGE_ICON = 1               // Loads an icon
	const LR_LOADFROMFILE = 0x00000010 // Loads the stand-alone image from the file
	const LR_DEFAULTSIZE = 0x00000040  // Loads default-size icon for windows(SM_CXICON x SM_CYICON) if cx, cy are set to zero

	// Save and reuse handles of loaded images
	t.muLoadedImages.RLock()
	h, ok := t.loadedImages[src]
	t.muLoadedImages.RUnlock()
	if !ok {
		srcPtr, err := windows.UTF16PtrFromString(src)
		if err != nil {
			return 0, err
		}
		res, _, err := pLoadImage.Call(
			0,
			uintptr(unsafe.Pointer(srcPtr)),
			IMAGE_ICON,
			0,
			0,
			LR_LOADFROMFILE|LR_DEFAULTSIZE,
		)
		if res == 0 {
			return 0, err
		}
		h = windows.Handle(res)
		t.muLoadedImages.Lock()
		t.loadedImages[src] = h
		t.muLoadedImages.Unlock()
	}
	return h, nil
}

func (t *winTray) iconToBitmap(hIcon windows.Handle) (windows.Handle, error) {
	const SM_CXSMICON = 49
	const SM_CYSMICON = 50
	const DI_NORMAL = 0x3
	hDC, _, err := pGetDC.Call(uintptr(0))
	if hDC == 0 {
		return 0, err
	}
	defer pReleaseDC.Call(uintptr(0), hDC)
	hMemDC, _, err := pCreateCompatibleDC.Call(hDC)
	if hMemDC == 0 {
		return 0, err
	}
	defer pDeleteDC.Call(hMemDC)
	cx, _, _ := pGetSystemMetrics.Call(SM_CXSMICON)
	cy, _, _ := pGetSystemMetrics.Call(SM_CYSMICON)
	hMemBmp, _, err := pCreateCompatibleBitmap.Call(hDC, cx, cy)
	if hMemBmp == 0 {
		return 0, err
	}
	hOriginalBmp, _, _ := pSelectObject.Call(hMemDC, hMemBmp)
	defer pSelectObject.Call(hMemDC, hOriginalBmp)
	res, _, err := pDrawIconEx.Call(hMemDC, 0, 0, uintptr(hIcon), cx, cy, 0, uintptr(0), DI_NORMAL)
	if res == 0 {
		return 0, err
	}
	return windows.Handle(hMemBmp), nil
}

// ENVKIT: 上游 registerSystray() 失败后只 log.Errorf 就 return，宿主拿不到任何信号，
// 于是出现「进程活着、控制台正常、托盘图标和菜单凭空消失」的静默降级。
// OnInitError 让宿主注册失败回调，只在初始化失败时调用一次。
// 这是 Windows 专用导出；非 Windows 的 platform 桩不会调用它。
var (
	onInitError func(error)
	// registerSystray 与 wndProc 都跑在被 LockOSThread 锁住的同一 OS 线程上
	//（Run → Register → nativeLoop 全在主 goroutine），所以这里不需要原子操作。
	trayMenuReady bool
)

func OnInitError(fn func(error)) { onInitError = fn }

func finishInit() {
	if trayMenuReady {
		return
	}
	if err := wt.createMenu(); err != nil {
		log.Errorf("Unable to create menu: %v", err)
		if onInitError != nil {
			onInitError(fmt.Errorf("托盘菜单创建失败：%w", err))
		}
		return
	}
	trayMenuReady = true
	systrayReady()
}

func registerSystray() {
	if err := wt.initInstance(); err != nil {
		log.Errorf("Unable to init instance: %v", err)
		// 刻意不调 finishInit：图标没加上就通知宿主 ready，宿主会把控制台窗口
		// 藏起来，用户就彻底没有入口了。等 Explorer 重启发的 TaskbarCreated
		// 里 addIcon() 成功后再补建菜单。
		if onInitError != nil {
			onInitError(err)
		}
		return
	}
	finishInit()
}

func nativeLoop() {
	// Main message pump.
	m := &struct {
		WindowHandle windows.Handle
		Message      uint32
		Wparam       uintptr
		Lparam       uintptr
		Time         uint32
		Pt           point
	}{}
	for {
		ret, _, err := pGetMessage.Call(uintptr(unsafe.Pointer(m)), 0, 0, 0)

		// If the function retrieves a message other than WM_QUIT, the return value is nonzero.
		// If the function retrieves the WM_QUIT message, the return value is zero.
		// If there is an error, the return value is -1
		// https://msdn.microsoft.com/en-us/library/windows/desktop/ms644936(v=vs.85).aspx
		switch int32(ret) {
		case -1:
			log.Errorf("Error at message loop: %v", err)
			return
		case 0:
			return
		default:
			pTranslateMessage.Call(uintptr(unsafe.Pointer(m)))
			pDispatchMessage.Call(uintptr(unsafe.Pointer(m)))
		}
	}
}

func quit() {
	const WM_CLOSE = 0x0010

	pPostMessage.Call(
		uintptr(wt.window),
		WM_CLOSE,
		0,
		0,
	)
}

func iconBytesToFilePath(iconBytes []byte) (string, error) {
	bh := md5.Sum(iconBytes)
	dataHash := hex.EncodeToString(bh[:])
	iconFilePath := filepath.Join(os.TempDir(), "systray_temp_icon_"+dataHash)

	if _, err := os.Stat(iconFilePath); os.IsNotExist(err) {
		if err := ioutil.WriteFile(iconFilePath, iconBytes, 0644); err != nil {
			return "", err
		}
	}
	return iconFilePath, nil
}

// SetIcon sets the systray icon.
// iconBytes should be the content of .ico for windows and .ico/.jpg/.png
// for other platforms.
func SetIcon(iconBytes []byte) {
	iconFilePath, err := iconBytesToFilePath(iconBytes)
	if err != nil {
		log.Errorf("Unable to write icon data to temp file: %v", err)
		return
	}
	if err := wt.setIcon(iconFilePath); err != nil {
		log.Errorf("Unable to set icon: %v", err)
		return
	}
}

// SetTemplateIcon sets the systray icon as a template icon (on macOS), falling back
// to a regular icon on other platforms.
// templateIconBytes and iconBytes should be the content of .ico for windows and
// .ico/.jpg/.png for other platforms.
func SetTemplateIcon(templateIconBytes []byte, regularIconBytes []byte) {
	SetIcon(regularIconBytes)
}

// SetTitle sets the systray title, only available on Mac and Linux.
func SetTitle(title string) {
	// do nothing
}

func (item *MenuItem) parentId() uint32 {
	if item.parent != nil {
		return uint32(item.parent.id)
	}
	return 0
}

// SetIcon sets the icon of a menu item. Only works on macOS and Windows.
// iconBytes should be the content of .ico/.jpg/.png
func (item *MenuItem) SetIcon(iconBytes []byte) {
	iconFilePath, err := iconBytesToFilePath(iconBytes)
	if err != nil {
		log.Errorf("Unable to write icon data to temp file: %v", err)
		return
	}

	h, err := wt.loadIconFrom(iconFilePath)
	if err != nil {
		log.Errorf("Unable to load icon from temp file: %v", err)
		return
	}

	h, err = wt.iconToBitmap(h)
	if err != nil {
		log.Errorf("Unable to convert icon to bitmap: %v", err)
		return
	}
	wt.muMenuItemIcons.Lock()
	wt.menuItemIcons[uint32(item.id)] = h
	wt.muMenuItemIcons.Unlock()

	err = wt.addOrUpdateMenuItem(uint32(item.id), item.parentId(), item.title, item.disabled, item.checked)
	if err != nil {
		log.Errorf("Unable to addOrUpdateMenuItem: %v", err)
		return
	}
}

// SetTooltip sets the systray tooltip to display on mouse hover of the tray icon,
// only available on Mac and Windows.
func SetTooltip(tooltip string) {
	if err := wt.setTooltip(tooltip); err != nil {
		log.Errorf("Unable to set tooltip: %v", err)
		return
	}
}

func addOrUpdateMenuItem(item *MenuItem) {
	err := wt.addOrUpdateMenuItem(uint32(item.id), item.parentId(), item.title, item.disabled, item.checked)
	if err != nil {
		log.Errorf("Unable to addOrUpdateMenuItem: %v", err)
		return
	}
}

// SetTemplateIcon sets the icon of a menu item as a template icon (on macOS). On Windows, it
// falls back to the regular icon bytes and on Linux it does nothing.
// templateIconBytes and regularIconBytes should be the content of .ico for windows and
// .ico/.jpg/.png for other platforms.
func (item *MenuItem) SetTemplateIcon(templateIconBytes []byte, regularIconBytes []byte) {
	item.SetIcon(regularIconBytes)
}

func addSeparator(id uint32) {
	err := wt.addSeparatorMenuItem(id, 0)
	if err != nil {
		log.Errorf("Unable to addSeparator: %v", err)
		return
	}
}

func hideMenuItem(item *MenuItem) {
	err := wt.hideMenuItem(uint32(item.id), item.parentId())
	if err != nil {
		log.Errorf("Unable to hideMenuItem: %v", err)
		return
	}
}

func showMenuItem(item *MenuItem) {
	addOrUpdateMenuItem(item)
}
