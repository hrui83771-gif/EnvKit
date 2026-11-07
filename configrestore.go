package main

// configrestore.go —— 配置备份的查看与恢复（v3.9）
//
// 备份本身早就有：saveExternalConfig 每次写盘前轮转 .bak/.bak2/.bak3，
// loadConfigFile 在正本损坏时自动回退。但界面上没有任何入口，用户把目录选错、
// 密码打错之后只能关掉程序去文件夹里改文件名，或者凭记忆重填。
//
// 安全边界（这一层不能松）：
//   · 只认 slot=1|2|3 三个固定文件名，**绝不接收用户传来的路径**；
//   · 覆盖当前配置是破坏性写操作 → 服务端强制 428 二次确认 + 审计；
//   · 列表只回脱敏摘要，密码 / API Key / SSH 口令一个字段都不出后端；
//   · 备份解析不出来就标不可读并拒绝恢复，绝不拿半份配置覆盖正本。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// configBackupSuffix 是备份文件的唯一映射表。任何调用方都只能给 slot，不能给路径。
func configBackupSuffix(slot int) (string, bool) {
	switch slot {
	case 1:
		return ".bak", true
	case 2:
		return ".bak2", true
	case 3:
		return ".bak3", true
	default:
		return "", false
	}
}

type configBackupView struct {
	Slot     int    `json:"slot"`
	At       string `json:"at"`
	Size     int64  `json:"size"`
	Summary  string `json:"summary"`
	Readable bool   `json:"readable"`
}

// configBackupSummary 只挑"用户认得出是哪一套环境"的几个字段，且全部脱敏。
// 不显示密码、API Key、SSH 口令，也不把整份配置吐给前端。
// 只回**值**不回中文标签：这段会直接显示在界面上，带标签就等于给英文模式
// 又添一处半中半英（和 issue 正文同一个老毛病）。说明文案交给前端 t()。
func configBackupSummary(c Config) string {
	parts := []string{}
	if s := strings.TrimSpace(c.Projects.FrontendDir); s != "" {
		parts = append(parts, filepath.Base(filepath.Clean(s)))
	}
	if s := strings.TrimSpace(c.Projects.BackendDir); s != "" {
		parts = append(parts, filepath.Base(filepath.Clean(s)))
	}
	if s := strings.TrimSpace(c.Projects.DBName); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(c.Chain.ChainHost); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, " · ")
}

// readConfigBackup 读并解密某个固定槽位；失败一律不返回半份配置。
func readConfigBackup(slot int) (Config, os.FileInfo, error) {
	var zero Config
	suffix, ok := configBackupSuffix(slot)
	if !ok {
		return zero, nil, fmt.Errorf("无效备份槽位")
	}
	path := configFilePath() + suffix
	fi, err := os.Stat(path)
	if err != nil {
		return zero, nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return zero, nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return zero, fi, fmt.Errorf("备份不是有效配置：%v", err)
	}
	decryptConfig(&c)
	normalizeConfig(&c)
	migrateConfig(&c, c.SchemaVersion)
	return c, fi, nil
}

// GET /api/config/backups —— 列出三个槽位（不存在的不列）。
func handleConfigBackups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	out := []configBackupView{}
	for slot := 1; slot <= 3; slot++ {
		suffix, _ := configBackupSuffix(slot)
		fi, err := os.Stat(configFilePath() + suffix)
		if err != nil {
			continue
		}
		c, _, err := readConfigBackup(slot)
		v := configBackupView{
			Slot: slot, Size: fi.Size(),
			At: fi.ModTime().Format("2006-01-02 15:04:05"),
		}
		if err == nil {
			v.Readable = true
			v.Summary = configBackupSummary(c)
		}
		// 不可读时 summary 留空，由前端按 readable 用 t() 出文案
		out = append(out, v)
	}
	writeJSON(w, map[string]any{"backups": out})
}

// POST /api/config/restore —— {slot:1|2|3, confirm:bool, lang:zh|en}
func handleConfigRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Slot    int    `json:"slot"`
		Confirm bool   `json:"confirm"`
		Lang    string `json:"lang"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "请求解析失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	suffix, ok := configBackupSuffix(body.Slot)
	if !ok {
		http.Error(w, "只能恢复 1/2/3 号备份", http.StatusBadRequest)
		return
	}
	c, fi, err := readConfigBackup(body.Slot)
	if err != nil {
		auditNow(actUser, "config_restore", "config.json"+suffix, "", resDenied, err.Error())
		http.Error(w, "备份不可恢复："+err.Error(), http.StatusBadRequest)
		return
	}
	if !body.Confirm {
		auditNow(actUser, "config_restore", "config.json"+suffix, "", resDenied, "等待二次确认")
		needConfirm(w, "config_restore", "config.json"+suffix,
			confirmReason(body.Lang,
				fmt.Sprintf("即将用 %s（%.1f KB）的备份覆盖当前配置。当前配置会自动存为 .bak，之后仍可退回。改完后需要重新检测一次。",
					fi.ModTime().Format("2006-01-02 15:04:05"), float64(fi.Size())/1024),
				fmt.Sprintf("About to overwrite the current settings with the backup from %s (%.1f KB). "+
					"The current settings are kept as .bak so you can switch back. A re-check is needed afterwards.",
					fi.ModTime().Format("2006-01-02 15:04:05"), float64(fi.Size())/1024)),
			true)
		return
	}
	// 和「配置导入」同一套硬校验：备份可能是老版本或手工改过的，
	// 让一个越界端口混进来，故障会拖到安装阶段才爆。
	if hard := hardProblems(checkConfig(c)); len(hard) > 0 {
		msgs := []string{}
		for _, p := range hard {
			msgs = append(msgs, p.String())
		}
		auditNow(actUser, "config_restore", "config.json"+suffix, "", resDenied, strings.Join(msgs, "；"))
		writeJSON(w, map[string]any{"ok": false, "problems": msgs})
		return
	}
	h, granted := beginTaskH("配置恢复", true)
	if !granted {
		http.Error(w, "有任务正在执行，请稍候", http.StatusConflict)
		return
	}
	defer h.Done()
	cfgMu.Lock()
	cfg = c
	cfgMu.Unlock()
	// saveExternalConfig 会先轮转：恢复前的正本自动变成 .bak，等于留了回头路。
	saveExternalConfig(c)
	projectBriefInvalidate()
	runtimeInvalidateCache()
	setCfgStale(true)
	auditNow(actUser, "config_restore", "config.json"+suffix,
		fmt.Sprintf("备份时间 %s", fi.ModTime().Format("2006-01-02 15:04:05")), resOK, "")
	info(scSys, "配置", "已从 %s 恢复配置，当前配置已另存为 .bak", suffix)
	writeJSON(w, map[string]any{"ok": true})
}
