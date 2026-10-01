package main

// Windows 桌面通知：NotifyIcon 气泡（Toast 对无注册 AppId 的进程不可靠）。

import (
	"encoding/base64"
	"encoding/binary"
	"os/exec"
	"runtime"
	"unicode/utf16"
)

// notify 弹出桌面气泡通知；失败静默（通知只是辅助，不影响主流程）。
func notify(title, msg string) {
	if runtime.GOOS != "windows" {
		return
	}
	ps := `$ErrorActionPreference='SilentlyContinue';` +
		`Add-Type -AssemblyName System.Windows.Forms;` +
		`$n=New-Object System.Windows.Forms.NotifyIcon;` +
		`$n.Icon=[System.Drawing.SystemIcons]::Information;` +
		`$n.Visible=$true;` +
		`$n.ShowBalloonTip(6000,[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('` + b64(title) + `')),[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('` + b64(msg) + `')),[System.Windows.Forms.ToolTipIcon]::Info);` +
		`Start-Sleep -Seconds 7;$n.Dispose()`
	// -EncodedCommand 规避引号/中文/路径坑（UTF-16LE + base64）
	enc := base64.StdEncoding.EncodeToString(utf16Bytes(ps))
	_ = exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-EncodedCommand", enc).Start()
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func utf16Bytes(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, len(u)*2)
	for i, v := range u {
		binary.LittleEndian.PutUint16(b[i*2:], v)
	}
	return b
}
