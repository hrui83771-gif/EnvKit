package main

// openpath.go —— 在资源管理器里「打开看一眼」（v3.7.1 审计项）。
//
// 为什么值得单开一个端点：备份目录 / 日志目录此前只能在界面上读到路径，
// 想真去看一眼得手抄到地址栏。这不是新能力，是把已有的路径用起来。
//
// 安全边界（三条，缺一条都不该开）：
//  1. 只接受**已存在**的绝对路径，且必须是目录。相对路径会被解析到进程的
//     工作目录——那是什么不可预判，不能接受。
//  2. 只调用 `explorer.exe <dir>`，不执行目录里的任何东西。打开目录本身
//     不会运行其中的程序。
//  3. 与其它 /api/* 一样过令牌 + 同源校验（main.go 的 authMW）；手机端的
//     是另一个 mux，压根没有注册这条路由（404，不是"被拒绝"）。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// openDirInExplorer 真正打开目录。抽成变量供单测替换：
// 真拉起 explorer 会闪窗、依赖桌面会话，CI 上必挂。
var openDirInExplorer = openDirPlatform

// handleOpenPath POST {path} → 在资源管理器打开该目录。
func handleOpenPath(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "参数错误", 400)
		return
	}
	dir, err := resolveOpenDir(body.Path)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	if err := openDirInExplorer(dir); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "打开失败：" + err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "path": dir})
}

// resolveOpenDir 校验并规整要打开的目录。规则集中在一处，测试直接钉它——
// 校验散落在 handler 里时，"加了一个入口忘了校验"就是下次的事故。
func resolveOpenDir(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("路径为空")
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("只接受绝对路径：%s", p)
	}
	p = filepath.Clean(p)
	fi, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("目录不存在：%s", p)
		}
		return "", fmt.Errorf("无法访问：%v", err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("不是目录：%s", p)
	}
	return p, nil
}

// handlePaths GET → 界面要显示的三个真实目录。
//
// 备份目录留空时由服务端给出 exe 同级的 backups/：前端自己拼路径会拼出
// 一个不存在的目录（工作目录 ≠ exe 目录），然后"打开"按钮报"目录不存在"。
func handlePaths(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	exeDir := "."
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	_ = json.NewEncoder(w).Encode(map[string]string{
		"exe_dir":    exeDir,
		"log_dir":    logDir(),
		"backup_dir": backupDir(),
	})
}
