//go:build !windows

package main

// 非 Windows 占位：DPAPI 是 Windows 专属，其它平台不加密（保持明文兼容）。

const dpapiPrefix = "dpapi:"

func dpapiProtect(plain string) string { return "" }

func dpapiUnprotect(s string) (string, error) { return s, nil }

func acquireSingleInstance() bool { return true }
