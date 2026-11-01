//go:build !windows

package main

// localterm_other.go —— 非 Windows 平台上的占位实现。
//
// ConPTY 是 Windows 的 API，conpty 这个库本身只在 windows 构建。
// 留这个文件是为了让 GOOS=linux/darwin 的 `go vet` / `go build` 不至于
// 直接报 "build constraints exclude all Go files" —— 本项目主要跑在 Windows，
// 但构建链不该被一个功能焊死在某个平台上。

import "net/http"

func lanTermWS(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "本地终端只在 Windows 上可用", http.StatusNotImplemented)
}

func conptyAvailable() bool { return false }
