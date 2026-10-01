package main

// 敏感信息保护（Windows）：
//  1. DPAPI 加密 config.json 中的密码字段 —— 用 CryptProtectData（当前用户作用域），
//     本机本用户可解密，配置文件被拷到别的机器/别的账户就是密文废料。
//  2. 单实例互斥 —— 防止双开导致 config.json 互相覆盖。

import (
	"encoding/base64"
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

const dpapiPrefix = "dpapi:"

var (
	crypt32                = syscall.NewLazyDLL("crypt32.dll")
	procCryptProtectData   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")

	k32             = syscall.NewLazyDLL("kernel32.dll")
	procCreateMutex = k32.NewProc("CreateMutexW")
	procLocalFree   = k32.NewProc("LocalFree")
)

type dataBlob struct {
	cbData uint32
	pbData *byte
}

// dpapiProtect 加密；失败返回 ""（调用方保底存原文）。
func dpapiProtect(plain string) string {
	if plain == "" {
		return ""
	}
	b := []byte(plain)
	in := dataBlob{uint32(len(b)), &b[0]}
	var out dataBlob
	r, _, _ := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return ""
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	raw := unsafe.Slice(out.pbData, out.cbData)
	return dpapiPrefix + base64.StdEncoding.EncodeToString(raw)
}

// dpapiUnprotect 解密；无前缀原样返回（兼容明文），解不开报错。
func dpapiUnprotect(s string) (string, error) {
	if !strings.HasPrefix(s, dpapiPrefix) {
		return s, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, dpapiPrefix))
	if err != nil {
		return "", err
	}
	in := dataBlob{uint32(len(raw)), &raw[0]}
	var out dataBlob
	r, _, _ := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return "", fmt.Errorf("DPAPI 解密失败（可能来自其他机器/用户）")
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return string(unsafe.Slice(out.pbData, out.cbData)), nil
}

// acquireSingleInstance 拿到互斥锁返回 true；已有实例在跑返回 false。
func acquireSingleInstance() bool {
	name, _ := syscall.UTF16PtrFromString(`Local\EnvKit-single-instance`)
	h, _, err := procCreateMutex.Call(0, 1, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return true // 创建失败不拦截，宁可用端口避让兜底
	}
	const errAlreadyExists = syscall.Errno(183)
	return err != errAlreadyExists
}
