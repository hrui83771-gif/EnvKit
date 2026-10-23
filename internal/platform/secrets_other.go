//go:build !windows

package platform

// 非 Windows 占位：DPAPI 是 Windows 专属，其它平台不加密（保持明文兼容）。

// DPAPIPrefix 是加密值的前缀标记；带此前缀的值视为密文。
const DPAPIPrefix = "dpapi:"

func DPAPIProtect(plain string) string { return "" }

func DPAPIUnprotect(s string) (string, error) { return s, nil }

func AcquireSingleInstance() bool { return true }
