// filesel.go —— 目录/文件选择（原生对话框 + 内置浏览器）与 Windows 辅助（管理员/环境变量）
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// ---------- 系统文件夹/文件选择（-STA + 环境变量 + 临时文件，避免引号/编码问题） ----------
func psFilePicker(kind, initial, filter string) string {
	outFile := filepath.Join(os.TempDir(), fmt.Sprintf("envkit_pick_%d.txt", time.Now().UnixNano()))
	defer func() { _ = os.Remove(outFile) }()

	var script string
	if kind == "dir" {
		script = `Add-Type -AssemblyName System.Windows.Forms
$f = New-Object System.Windows.Forms.OpenFileDialog
$f.Title = 'Select folder'
$f.Filter = 'All|*.*'
$f.ValidateNames = $false
$f.CheckFileExists = $false
$f.CheckPathExists = $true
$f.FileName = 'Select this folder'
if (Test-Path $env:ENVKIT_INIT) { $f.InitialDirectory = $env:ENVKIT_INIT }
if ($f.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
  [IO.File]::WriteAllText($env:ENVKIT_OUT, (Split-Path -Parent $f.FileName), (New-Object System.Text.UTF8Encoding($false)))
}`
	} else {
		script = `Add-Type -AssemblyName System.Windows.Forms
$f = New-Object System.Windows.Forms.OpenFileDialog
$f.Title = 'Select file'
$f.Filter = $env:ENVKIT_FILTER
if (Test-Path $env:ENVKIT_INIT) { $f.InitialDirectory = $env:ENVKIT_INIT }
if ($f.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
  [IO.File]::WriteAllText($env:ENVKIT_OUT, $f.FileName, (New-Object System.Text.UTF8Encoding($false)))
}`
	}

	cmd := exec.Command("powershell.exe", "-NoProfile", "-STA", "-ExecutionPolicy", "Bypass", "-Command", script)
	env := append(os.Environ(), "ENVKIT_OUT="+outFile)
	if initial != "" {
		env = append(env, "ENVKIT_INIT="+initial)
	}
	if filter != "" {
		env = append(env, "ENVKIT_FILTER="+filter)
	}
	cmd.Env = env
	_ = cmd.Run()

	data, err := os.ReadFile(outFile)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(data))
	s = strings.TrimPrefix(s, "\ufeff") // 去掉可能的 UTF-8 BOM
	return strings.TrimSpace(s)
}

// 内置目录浏览器：列驱动器 / 子目录 / 文件
func listDrives() []string {
	var ds []string
	for c := 'A'; c <= 'Z'; c++ {
		d := string(c) + ":\\"
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			ds = append(ds, d)
		}
	}
	return ds
}

func handleLS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	p := r.URL.Query().Get("path")
	mode := r.URL.Query().Get("mode")
	ext := strings.ToLower(r.URL.Query().Get("ext"))

	resp := map[string]interface{}{"drives": listDrives(), "path": "", "parent": "", "dirs": []string{}, "files": []string{}}
	if p != "" {
		entries, err := os.ReadDir(p)
		if err != nil {
			resp["error"] = err.Error()
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		var dirs, files []string
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, e.Name())
			} else if mode == "file" {
				if ext == "" || strings.HasSuffix(strings.ToLower(e.Name()), ext) {
					files = append(files, e.Name())
				}
			}
		}
		sort.Strings(dirs)
		sort.Strings(files)
		parent := filepath.Dir(p)
		if parent == p {
			parent = ""
		}
		resp["path"] = p
		resp["parent"] = parent
		resp["dirs"] = dirs
		resp["files"] = files
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func handlePickDir(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	var body struct {
		Initial string `json:"initial"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	init := body.Initial
	if init != "" {
		if fi, err := os.Stat(init); err != nil || !fi.IsDir() {
			init = ""
		}
	}
	p := psFilePicker("dir", init, "")
	if p != "" {
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			p = ""
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"path": p})
}

func handlePickFile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	var body struct {
		Initial string `json:"initial"`
		Filter  string `json:"filter"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	init := body.Initial
	if init != "" {
		if fi, err := os.Stat(init); err != nil || !fi.IsDir() {
			init = ""
		}
	}
	filter := body.Filter
	if filter == "" {
		filter = "SQL 文件 (*.sql)|*.sql|所有文件 (*.*)|*.*"
	}
	p := psFilePicker("file", init, filter)
	_ = json.NewEncoder(w).Encode(map[string]string{"path": p})
}

// ---------- Windows 辅助 ----------
func isAdmin() bool {
	if runtime.GOOS != "windows" {
		return true
	}
	out, err := exec.Command("powershell.exe", "-NoProfile", "-Command",
		"([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "True"
}

func psRun(script string) (string, error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", script)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func addToUserPath(dir string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	script := fmt.Sprintf(`$p=[Environment]::GetEnvironmentVariable('PATH','User'); if($p -notlike "*%s*"){ [Environment]::SetEnvironmentVariable('PATH', ($p.TrimEnd(';')+';'+'%s'), 'User'); Write-Output 'ADDED' } else { Write-Output 'EXISTS' }`, dir, dir)
	_, err := psRun(script)
	return err
}

func setEnvVar(k, v string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	script := fmt.Sprintf(`[Environment]::SetEnvironmentVariable('%s','%s','User'); Write-Output 'OK'`, k, v)
	_, err := psRun(script)
	return err
}

func runIn(dir, exe string, args ...string) (string, error) {
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
