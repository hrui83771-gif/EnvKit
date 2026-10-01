package main

// 后台进程清理：停止 EnvKit 管理的服务，并清掉项目目录下遗留的
// node（webpack 开发服务器）/ main.exe（go run）/ go.exe 等后台进程。
// 按项目目录精准匹配命令行，避免误杀其他软件（如 Adobe）的同名 node 进程。

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// dirVariants 展开项目目录的路径写法变体：Windows 命令行里可能是 D:\a\b 也可能是 D:/a/b，
// 两种写法都要匹配（早期只匹配反斜杠，导致测试进程清不掉）；空串忽略，重复去掉。
func dirVariants(dirs ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range dirs {
		d = strings.TrimSpace(strings.TrimRight(d, "\\/"))
		if d == "" {
			continue
		}
		for _, v := range []string{d, strings.ReplaceAll(d, "\\", "/"), strings.ReplaceAll(d, "/", "\\")} {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}

// cleanupProcesses 返回 (清掉的进程数, 明细, 错误)
func cleanupProcesses() (int, []string, error) { // 1) 先停 EnvKit 自己管理的服务（走进程树 kill，最干净）
	stopByKey(scStart, "web", "web-start")
	stopByKey(scStart, "backend", "backend-start")
	time.Sleep(700 * time.Millisecond)

	// 路径变体：命令行里可能是 D:\a\b，也可能是 D:/a/b，统一都匹配
	dirs := dirVariants(cfg.Projects.FrontendDir, cfg.Projects.BackendDir)
	if len(dirs) == 0 {
		return 0, nil, nil
	}

	// 2) 按项目目录找遗留进程并结束
	report := filepath.Join(exeDir(), "_proc_clean.txt")
	_ = os.Remove(report)
	quoted := make([]string, 0, len(dirs))
	for _, d := range dirs {
		quoted = append(quoted, "'"+strings.ReplaceAll(d, "'", "''")+"'")
	}
	script := `$dirs = @(` + strings.Join(quoted, ",") + `)
$out = New-Object System.Collections.ArrayList
Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
  ($_.Name -in @('node.exe','main.exe','go.exe')) -and $_.CommandLine -and ($_.CommandLine -notlike '*.workbuddy*')
} | ForEach-Object {
  $c = $_.CommandLine.ToLower()
  $hit = $false
  foreach ($d in $dirs) { if ($d -ne '' -and $c.Contains($d.ToLower())) { $hit = $true } }
  if ($hit) {
    try { Stop-Process -Id $_.ProcessId -Force -ErrorAction Stop; [void]$out.Add("killed|$($_.ProcessId)|$($_.Name)") }
    catch { [void]$out.Add("failed|$($_.ProcessId)|$($_.Name)") }
  }
}
$out -join [char]10 | Out-File '` + strings.ReplaceAll(report, "'", "''") + `' -Encoding ascii
'ok'`

	ctxPS, cancelPS := cmdContext(3 * time.Minute)
	out, err := exec.CommandContext(ctxPS, "powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-EncodedCommand", psEncode(script)).CombinedOutput()
	cancelPS()
	if err != nil {
		msg := firstLines(strings.TrimSpace(string(out)), 300)
		// PowerShell/CIM 不可用（例如被安全策略禁用）时，退化为 tasklist + taskkill 兜底
		warn(scStart, "清理", "PowerShell 扫描失败：%v %s", err, msg)
		n, det := cleanupByTasklist()
		return n, det, nil
	}
	b, err := os.ReadFile(report)
	_ = os.Remove(report)
	if err != nil {
		return 0, nil, nil
	}
	var detail []string
	for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		ln = strings.TrimSpace(strings.TrimRight(ln, "\r"))
		if ln == "" {
			continue
		}
		parts := strings.Split(ln, "|")
		if len(parts) == 3 {
			if parts[0] == "killed" {
				detail = append(detail, fmt.Sprintf("%s (PID %s)", parts[2], parts[1]))
			} else {
				detail = append(detail, fmt.Sprintf("%s PID %s 结束失败", parts[2], parts[1]))
			}
		}
	}
	killed := 0
	for _, d := range detail {
		if !strings.Contains(d, "结束失败") {
			killed++
		}
	}
	return killed, detail, nil
}

// aiCleanupProcesses 供 AI 工具与按钮共用的入口（带日志）
func aiCleanupProcesses() (string, error) {
	h, granted := beginTaskH("清理后台进程", true)
	if !granted {
		return "", fmt.Errorf("有任务正在执行，请稍候")
	}
	defer h.Done()
	fin := auditStart(actUser, "cleanup_processes", "web/backend", "")
	info(scStart, "清理", "停止 EnvKit 管理的服务，并清理项目目录下的遗留进程...")
	n, detail, err := cleanupProcesses()
	if err != nil {
		fail(scStart, "清理", "%v", err)
		fin(resFail, err.Error())
		return "", err
	}
	fin(resOK, fmt.Sprintf("清理 %d 个", n))
	for _, d := range detail {
		info(scStart, "清理", "已结束 %s", d)
	}
	msg := fmt.Sprintf("已停止 EnvKit 管理的服务；清理项目遗留进程 %d 个", n)
	if n == 0 {
		msg = "已停止 EnvKit 管理的服务；未发现遗留进程"
	}
	ok(scStart, "清理", "%s", msg)
	return msg + "。端口 8080/8888 若仍被占用，请稍候片刻再启动。", nil
}

func handleCleanupProcs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	go func() { _, _ = aiCleanupProcesses() }()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"started":true}`))
}

// cleanupByTasklist 兜底：主通道不可用时，按进程名清理 —— 只处理项目专有名字（main.exe / go.exe）。
// 刻意不处理 node.exe：node 是通用运行时，按名字杀会误伤其它软件（如 Adobe Creative Cloud）。
func cleanupByTasklist() (int, []string) {
	var killed []string
	for _, name := range []string{"main.exe", "go.exe"} {
		out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq "+name, "/FO", "CSV", "/NH").Output()
		if err != nil {
			continue
		}
		for _, ln := range strings.Split(string(out), "\n") {
			ln = strings.TrimSpace(strings.Trim(ln, "\r"))
			if ln == "" || !strings.Contains(ln, name) {
				continue
			}
			fields := strings.Split(strings.Trim(ln, "\""), "\",\"")
			if len(fields) < 2 {
				continue
			}
			pid := strings.Trim(fields[1], "\" ")
			if pid == "" {
				continue
			}
			if err := exec.Command("taskkill", "/F", "/PID", pid).Run(); err == nil {
				killed = append(killed, fmt.Sprintf("%s (PID %s)", name, pid))
			}
		}
	}
	if len(killed) == 0 {
		info(scStart, "清理", "兜底清理未发现可结束的进程")
	}
	warn(scStart, "清理", "兜底模式不处理 node.exe（避免误杀其它软件的 node）；若前端进程仍在，请改用「停止全部」或重启 EnvKit")
	return len(killed), killed
}
