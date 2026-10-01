package main

// 一键加入 Windows Defender 白名单：
// 把 EnvKit 所在目录加入 ExclusionPath、EnvKit.exe 加入 ExclusionProcess，
// 防止实时防护误拦 EnvKit 派生的 go / npm / mysqldump 子进程（零输出秒退）。
// 需要管理员权限：非管理员时经 UAC（Start-Process -Verb RunAs）提权执行，
// Windows 设计上无法跳过用户确认。

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func psEncode(script string) string {
	return base64.StdEncoding.EncodeToString(utf16Bytes(script))
}

func exeDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	return "."
}

// defenderExcluded 返回 (目录是否已在排除项, Defender 是否可用)。
// 注意：非管理员上下文的 Get-MpPreference 可能读不到本地管理员刚加的排除项，
// 所以本函数只能作"快速判断"，权威结果以提权脚本写回的 _mp_ok.txt 为准。
func defenderExcluded() (bool, bool) {
	dir := exeDir()
	script := fmt.Sprintf(
		`$ErrorActionPreference='SilentlyContinue'; $p=Get-MpPreference; if($p -eq $null){exit 2}; if($p.ExclusionPath -contains '%s'){exit 0}else{exit 1}`, dir)
	ctxPS, cancelPS := cmdContext(3 * time.Minute)
	defer cancelPS()
	err := exec.CommandContext(ctxPS, "powershell", "-NoProfile", "-EncodedCommand", psEncode(script)).Run()
	if err == nil {
		return true, true
	}
	if ee, ok := err.(*exec.ExitError); ok {
		switch ee.ExitCode() {
		case 1:
			return false, true
		case 2:
			return false, false
		}
	}
	return false, false
}

// avMarkerFile 白名单成功标记（提权脚本验证通过后写入，持久化避免每次启动重复提示）。
func avMarkerFile() string { return filepath.Join(exeDir(), "av_whitelist.marker") }

// avWhitelisted 白名单是否已确认生效。
func avWhitelisted() bool {
	if _, err := os.Stat(avMarkerFile()); err == nil {
		return true
	}
	if mpOkFileContains(exeDir()) {
		return true
	}
	ok, _ := defenderExcluded()
	return ok
}

// mpOkFileContains 提权脚本回传的排除项列表是否包含本目录。
func mpOkFileContains(dir string) bool {
	b, err := os.ReadFile(filepath.Join(exeDir(), "_mp_ok.txt"))
	if err != nil {
		return false
	}
	return strings.Contains(string(b), dir)
}

func markWhitelisted(dir string) {
	ok(scSys, "白名单", "已加入 Defender 排除项：%s（目录 + EnvKit.exe 进程），go/npm/mysqldump 不会再被误拦", dir)
	_ = os.WriteFile(avMarkerFile(), []byte(dir), 0644)
	_ = os.Remove(filepath.Join(exeDir(), "_mp_ok.txt"))
	_ = os.Remove(filepath.Join(exeDir(), "_mp_err.txt"))
}

func handleAVExclude(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method == http.MethodGet {
		excluded := avWhitelisted()
		_, avail := defenderExcluded()
		_, _ = fmt.Fprintf(w, `{"excluded":%v,"defender":%v}`, excluded, avail)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if err := whitelistTask(); err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	_, _ = w.Write([]byte(`{"started":true}`))
}

// whitelistTask 加入 Defender 白名单（供按钮与 AI 工具共用）；同步等待确认（最长 90s）。
func whitelistTask() error {
	h, granted := beginTaskH("加入杀软白名单", true)
	if !granted {
		return fmt.Errorf("有任务正在执行，请稍候")
	}
	done := make(chan struct{})
	go func() {
		defer h.Done()
		defer close(done)
		fin := auditStart(actUser, "av_exclude", exeDir(), "")
		defer func() {
			if avWhitelisted() {
				fin(resOK, "已在 Defender 排除项中")
			} else {
				fin(resFail, "未确认加入排除项")
			}
		}()
		dir := exeDir()
		// 权威判定：提权脚本回传的排除项列表包含本目录（上次已成功则直接确认）
		if mpOkFileContains(dir) {
			markWhitelisted(dir)
			return
		}
		excluded, avail := defenderExcluded()
		if !avail {
			fail(scSys, "白名单", "未检测到 Windows Defender（可能安装了第三方杀软），请在其设置中手动添加排除目录：%s", dir)
			return
		}
		if excluded || avWhitelisted() {
			markWhitelisted(dir)
			return
		}
		inner := fmt.Sprintf(
			`$ErrorActionPreference='Stop'; try { Add-MpPreference -ExclusionPath '%s' -ExclusionProcess 'EnvKit.exe'; (Get-MpPreference).ExclusionPath -join ';' | Out-File '%s' -Encoding utf8 } catch { $_.Exception.Message | Out-File '%s' -Encoding utf8; exit 1 }`,
			dir, filepath.Join(dir, "_mp_ok.txt"), filepath.Join(dir, "_mp_err.txt"))
		_ = os.Remove(filepath.Join(dir, "_mp_ok.txt"))
		_ = os.Remove(filepath.Join(dir, "_mp_err.txt"))
		if isAdminFlag {
			warn(scSys, "白名单", "以管理员身份直接写入 Defender 排除项...")
			ctxA, cancelA := cmdContext(3 * time.Minute)
			err := exec.CommandContext(ctxA, "powershell", "-NoProfile", "-EncodedCommand", psEncode(inner)).Run()
			cancelA()
			if err != nil {
				fail(scSys, "白名单", "写入失败：%v", err)
				return
			}
		} else {
			outer := fmt.Sprintf(
				`Start-Process powershell -Verb RunAs -WindowStyle Hidden -ArgumentList @('-NoProfile','-EncodedCommand','%s')`, psEncode(inner))
			warn(scSys, "白名单", "正在请求管理员权限，请在弹出的 UAC 窗口点「是」...")
			if err := exec.Command("powershell", "-NoProfile", "-EncodedCommand", psEncode(outer)).Start(); err != nil {
				fail(scSys, "白名单", "启动提权进程失败：%v", err)
				return
			}
		}
		// 轮询验证（等用户点 UAC，最长 90s）。注意：非管理员 Get-MpPreference 可能读不到新条目，
		// 所以提权脚本写回的 _mp_ok.txt 才是权威证据。
		for i := 0; i < 45; i++ {
			time.Sleep(2 * time.Second)
			if mpOkFileContains(dir) {
				markWhitelisted(dir)
				return
			}
			if ex, _ := defenderExcluded(); ex {
				markWhitelisted(dir)
				return
			}
		}
		// 超时：回读提权脚本留下的诊断文件
		if b, err := os.ReadFile(filepath.Join(dir, "_mp_err.txt")); err == nil && len(b) > 0 {
			msg := strings.TrimSpace(string(b))
			fail(scSys, "白名单", "Defender 拒绝写入：%s。若提示防篡改保护（Tamper Protection），请改用安全中心界面手动添加一次：%s", firstLines(msg, 200), dir)
			return
		}
		if mpOkFileContains(dir) {
			// 用户在验证窗口后才点「是」：排除项实际已写入，下次启动不再提示
			markWhitelisted(dir)
			return
		}
		fail(scSys, "白名单", "超时未确认（可能 UAC 弹窗被挡住或点了「否」）。可重试，或手动在安全中心 → 病毒和威胁防护 → 排除项 添加目录：%s", dir)
	}()
	<-done
	return nil
}

// ---------- 清除流氓软件 PUA:Win32/Softcnapp（WROberlay64.dll / TaoWnRecoveryer） ----------
const cleanupScript = `$r=@()
try { Remove-MpThreat -ErrorAction Stop; $r += 'Remove-MpThreat: OK' } catch { $r += 'Remove-MpThreat: ' + $_.Exception.Message }
$files = @()
foreach($d in @('C:\Windows\System32','C:\Windows\SysWOW64','C:\Windows')){ if(Test-Path $d){ Get-ChildItem -Path $d -Filter 'WROberlay*' -File -ErrorAction SilentlyContinue | ForEach-Object { $files += $_.FullName } } }
foreach($f in @('C:\Windows\System32\WROberlay.dll','C:\Windows\System32\WROberlay64.dll','C:\Windows\SysWOW64\WROberlay64.dll','C:\Windows\SysWOW64\WROberlay32.dll')){ if($files -notcontains $f){ $files += $f } }
foreach($f in ($files | Select-Object -Unique)){
  if(Test-Path $f){
    try { & takeown.exe /f $f /a 2>&1 | Out-Null } catch {}
    try { & icacls.exe $f /grant '*S-1-5-32-544:(F)' 2>&1 | Out-Null } catch {}
    try { Remove-Item -Force $f -ErrorAction Stop; $r += 'deleted ' + $f }
    catch {
      try { $n = (Split-Path $f -Leaf) + '.quarantine'; Rename-Item -Path $f -NewName $n -Force -ErrorAction Stop; $r += 'renamed-quarantine (loaded, delete after reboot) ' + $f }
      catch { $r += 'FAILED ' + $f + ' :: ' + $_.Exception.Message }
    }
  }
}
$clsids = @('HKLM:\SOFTWARE\CLASSES\CLSID\{A44E254D-195E-4EC1-ADDF-D3770A02A3B3}','HKLM:\SOFTWARE\WOW6432Node\CLASSES\CLSID\{A44E254D-195E-4EC1-ADDF-D3770A02A3B3}','HKLM:\SOFTWARE\Classes\WOW6432Node\CLASSES\CLSID\{A44E254D-195E-4EC1-ADDF-D3770A02A3B3}','HKCU:\SOFTWARE\CLASSES\CLSID\{A44E254D-195E-4EC1-ADDF-D3770A02A3B3}')
foreach($k in $clsids){ if(Test-Path $k){ try { Remove-Item -Recurse -Force $k -ErrorAction Stop; $r += 'deleted clsid ' + $k } catch { $r += 'FAILED clsid ' + $k + ' :: ' + $_.Exception.Message } } }
$ov = 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Explorer\ShellIconOverlayIdentifiers'
Get-ChildItem $ov -ErrorAction SilentlyContinue | Where-Object { $_.PSChildName -like '*TaoWn*' -or $_.PSChildName -like '*Recovery*' } | ForEach-Object { try { Remove-Item -Recurse -Force $_.PSPath -ErrorAction Stop; $r += 'deleted overlay ' + $_.PSChildName } catch { $r += 'FAILED overlay ' + $_.PSChildName } }
try { Get-ChildItem 'HKLM:\SOFTWARE\Classes\CLSID' -ErrorAction SilentlyContinue | ForEach-Object { $p = $_.PSPath; $d = Get-ItemProperty -Path ($p + '\InprocServer32') -ErrorAction SilentlyContinue; if($d -and ($d.'(default)' -like '*WROberlay*')){ try { Remove-Item -Recurse -Force $p -ErrorAction Stop; $r += 'deleted clsid-ref ' + $_.PSChildName } catch { $r += 'FAILED clsid-ref ' + $_.PSChildName } } } } catch {}
try { Get-ChildItem 'HKLM:\SOFTWARE\WOW6432Node\Classes\CLSID' -ErrorAction SilentlyContinue | ForEach-Object { $p = $_.PSPath; $d = Get-ItemProperty -Path ($p + '\InprocServer32') -ErrorAction SilentlyContinue; if($d -and ($d.'(default)' -like '*WROberlay*')){ try { Remove-Item -Recurse -Force $p -ErrorAction Stop; $r += 'deleted wow-clsid-ref ' + $_.PSChildName } catch { $r += 'FAILED wow-clsid-ref ' + $_.PSChildName } } } } catch {}
try { Stop-Process -Name explorer -Force -ErrorAction Stop; Start-Sleep -Seconds 2; Start-Process explorer.exe; $r += 'explorer restarted (DLL unloaded)' } catch { $r += 'explorer restart skipped: ' + $_.Exception.Message }
$r += 'CLEANUP-DONE'
$r -join [Environment]::NewLine | Out-File '__EK_REPORT__' -Encoding ascii`

// cleanupScriptFor 返回已填充报告路径的清除脚本。
// 报告必须落在 exe 同目录（而不是编译机的绝对路径）——否则换台机器运行会因为
// 目录不存在而写不出报告，且会把开发机的目录结构泄漏进分发的 exe。
func cleanupScriptFor() string {
	return strings.ReplaceAll(cleanupScript, "__EK_REPORT__", filepath.Join(exeDir(), "_clean_report.txt"))
}

// avThreatFiles 扫描系统目录里是否还有 WROberlay* 残留（PUA:Win32/Softcnapp 家族）。
// 只读，不需要管理员权限；用于决定前端是否显示「深度清除」按钮。
func avThreatFiles() []string {
	var out []string
	for _, dir := range []string{`C:\Windows\System32`, `C:\Windows\SysWOW64`, `C:\Windows`} {
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			if strings.HasPrefix(strings.ToLower(e.Name()), "wroberlay") {
				out = append(out, filepath.Join(dir, e.Name()))
			}
		}
	}
	return out
}

func handleAVClean(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		files := avThreatFiles()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"threat":%v,"count":%d}`, len(files) > 0, len(files))
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	h, granted := beginTaskH("清除流氓软件", true)
	if !granted {
		http.Error(w, "有任务正在执行，请稍候", 409)
		return
	}
	go func() {
		defer h.Done()
		fin := auditStart(actUser, "av_clean", "system", "")
		defer func() { fin(resOK, "清除流程已执行") }()
		report := filepath.Join(exeDir(), "_clean_report.txt")
		_ = os.Remove(report)
		warn(scSys, "清除", "正在请求管理员权限，请在弹出的 UAC 窗口点「是」...")
		if isAdminFlag {
			ctxC, cancelC := cmdContext(5 * time.Minute)
			err := exec.CommandContext(ctxC, "powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", cleanupScriptFor()).Run()
			cancelC()
			if err != nil {
				fail(scSys, "清除", "执行失败：%v", err)
				return
			}
		} else {
			outer := fmt.Sprintf(
				`Start-Process powershell -Verb RunAs -WindowStyle Hidden -ArgumentList @('-NoProfile','-ExecutionPolicy','Bypass','-Command','%s')`, psEncode(cleanupScriptFor()))
			if err := exec.Command("powershell", "-NoProfile", "-EncodedCommand", psEncode(outer)).Start(); err != nil {
				fail(scSys, "清除", "启动提权进程失败：%v", err)
				return
			}
		}
		// 等待报告（最长 90s）
		for i := 0; i < 45; i++ {
			time.Sleep(2 * time.Second)
			if b, err := os.ReadFile(report); err == nil && strings.Contains(string(b), "CLEANUP-DONE") {
				for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
					ln = strings.TrimSpace(ln)
					if ln == "" || ln == "CLEANUP-DONE" {
						continue
					}
					// 报告内容来自外部脚本，必须走 %s —— 否则里面的 % 会被当成格式符
					if strings.HasPrefix(ln, "FAILED") || strings.HasPrefix(ln, "Remove-MpThreat: ") {
						warn(scSys, "清除", "%s", ln)
					} else {
						ok(scSys, "清除", "%s", ln)
					}
				}
				ok(scSys, "清除", "完成。建议重启电脑一次（若 DLL 被占用已改名 .quarantine，重启后手动删除），再验证前后端启动")
				return
			}
		}
		fail(scSys, "清除", "超时未确认（可能 UAC 未点「是」）。可重试；或手动在安全中心 → 保护历史记录 清除 PUA:Win32/Softcnapp")
	}()
	_, _ = w.Write([]byte(`{"started":true}`))
}
