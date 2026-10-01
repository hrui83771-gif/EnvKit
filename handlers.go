// handlers.go —— 通用接口：配置读写/导入导出、检测与安装状态、SSE 事件、健康与提权
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// 配置导出：?sanitize=1 时去除所有密码（用于分享/换机迁移）
func handleConfigExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	cfgMu.Lock()
	data, _ := json.MarshalIndent(cfg, "", "  ")
	cfgMu.Unlock()
	sanitized := r.URL.Query().Get("sanitize") == "1"
	if sanitized {
		var c Config
		if json.Unmarshal(data, &c) == nil {
			c.MySQL.Password = ""
			c.Projects.MySQLPass = ""
			c.Chain.SSHPassword = ""
			if c.AI != nil {
				c.AI.APIKey = ""
			}
			data, _ = json.MarshalIndent(c, "", "  ")
		}
	}
	auditNow(actUser, "config_export", "config.json", fmt.Sprintf("sanitize=%v", sanitized), resOK, "")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=envkit-config.json")
	_, _ = w.Write(data)
}

// 配置导入：整体恢复；空密码字段保留本机已有密码（兼容脱敏导出的文件）
func handleConfigImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var in Config
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "配置解析失败: "+err.Error(), 400)
		return
	}
	// 导入的文件可能来自别的机器/手工编辑，同样要过硬校验：
	// 否则一个端口越界的配置会让导入"成功"，直到安装阶段才炸。
	normalizeConfig(&in)
	if hard := hardProblems(checkConfig(in)); len(hard) > 0 {
		var msgs []string
		for _, p := range hard {
			msgs = append(msgs, p.String())
		}
		auditNow(actUser, "config_import", "config.json", "", resDenied, strings.Join(msgs, "；"))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "problems": hard})
		return
	}
	// 版本推进：导入文件可能是老版本结构，迁移到当前版本再入库
	steps := migrateConfig(&in, in.SchemaVersion)
	for _, s := range steps {
		info(scSys, "配置", "导入迁移：%s", s)
	}
	applyConfigDefaults()
	cfgMu.Lock()
	if in.MySQL.Password == "" {
		in.MySQL.Password = cfg.MySQL.Password
	}
	if in.Projects.MySQLPass == "" {
		in.Projects.MySQLPass = cfg.Projects.MySQLPass
	}
	if in.Chain.SSHPassword == "" {
		in.Chain.SSHPassword = cfg.Chain.SSHPassword
	}
	cfg = in
	cfgMu.Unlock()
	saveExternalConfig(in)
	auditNow(actUser, "config_import", "config.json", fmt.Sprintf("schema v%d", in.SchemaVersion), resOK, "")
	info(scSys, "配置", "已从导入文件恢复配置")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method == http.MethodGet {
		cfgMu.Lock()
		_ = json.NewEncoder(w).Encode(cfg)
		cfgMu.Unlock()
		return
	}
	if r.Method == http.MethodPost {
		var c Config
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			http.Error(w, "配置解析失败: "+err.Error(), 400)
			return
		}
		// 页面表单只提交"界面上有的字段"：缺失的整段必须沿用原值。
		// （曾经的坑：直接 cfg = c，导致在「程序配置」里动一下输入框触发自动保存，
		//  就把 AI 配置连同 API Key 一起抹掉了。）
		cfgMu.Lock()
		c = mergeFormConfig(c, cfg)
		cfgMu.Unlock()
		normalizeConfig(&c)
		// 硬错误直接拒绝：让用户在输入框旁边就看到原因，而不是等到"安装失败/连不上库"再倒查。
		if hard := hardProblems(checkConfig(c)); len(hard) > 0 {
			var msgs []string
			for _, p := range hard {
				msgs = append(msgs, p.String())
			}
			auditNow(actUser, "config_save", "config.json", "", resDenied, strings.Join(msgs, "；"))
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "problems": hard})
			return
		}
		cfgMu.Lock()
		cfg = c
		cfgMu.Unlock()
		saveExternalConfig(c)
		auditNow(actUser, "config_save", "config.json", "", resOK, fmt.Sprintf("schema v%d", c.SchemaVersion))
		_, _ = w.Write([]byte(`{"ok":true}`))
		return
	}
	http.Error(w, "method not allowed", 405)
}

func handleCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	// 只读任务：可与其它只读任务并发，但有写任务在跑时拒绝（避免检测结果读到半安装状态）
	h, granted := beginTaskH("环境检测", false)
	if !granted {
		http.Error(w, "有任务正在执行，请稍候", 409)
		return
	}
	go func() {
		defer h.Done()
		fin := auditStart(actUser, "env_check", "components", "")
		runCheck()
		fin(resOK, "")
	}()
	_, _ = w.Write([]byte(`{"started":true}`))
}

func handleInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	h, granted := beginTaskH("环境安装", true)
	if !granted {
		http.Error(w, "有任务正在执行，请稍候", 409)
		return
	}
	go func() {
		defer h.Done()
		fin := auditStart(actUser, "env_install", cfg.InstallDir, "")
		runInstall()
		fin(resOK, "")
	}()
	_, _ = w.Write([]byte(`{"started":true}`))
}

func handleState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	resultsMu.Lock()
	rs := make([]CheckResult, 0, len(results))
	for _, v := range results {
		rs = append(rs, v)
	}
	resultsMu.Unlock()
	phaseMu.Lock()
	ph := phase
	phaseMu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"phase": ph, "results": rs})
}

func handleProgState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	progMu.Lock()
	cp := map[string]ProgStatus{}
	for k, v := range progState {
		cp[k] = v
	}
	progMu.Unlock()
	_ = json.NewEncoder(w).Encode(cp)
}

func handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok2 := w.(http.Flusher)
	if !ok2 {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	ch := hub.Subscribe()
	defer hub.Unsubscribe(ch)
	hub.mu.Lock()
	hist := append([]LogMsg{}, hub.history...)
	hub.mu.Unlock()
	for _, m := range hist {
		b, _ := json.Marshal(m)
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case m := <-ch:
			b, _ := json.Marshal(m)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}

func setPhase(p string) {
	phaseMu.Lock()
	phase = p
	phaseMu.Unlock()
}

func setProg(key string, running, ok2 bool, msg string) {
	progMu.Lock()
	progState[key] = ProgStatus{Running: running, Ok: ok2, Msg: msg, At: time.Now().Format("15:04:05")}
	progMu.Unlock()
}

// ---------- 全局健康 / 权限 ----------
func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	resultsMu.Lock()
	n := len(results)
	allOK := n > 0
	for _, v := range results {
		if !v.Installed {
			allOK = false
		}
	}
	resultsMu.Unlock()

	dbMu.Lock()
	dbh := dbHealth
	dbMu.Unlock()

	svcMu.Lock()
	web := *svcState["web"]
	be := *svcState["backend"]
	svcMu.Unlock()

	chainMu.Lock()
	ch := chainInfo
	chainMu.Unlock()
	ch.WebaseURL = webaseBaseURL()

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"admin": isAdminFlag,
		"busy":  currentTask(),
		"chain": ch,
		"env":   map[string]interface{}{"hasResults": n > 0, "ready": allOK},
		"db":    dbh,
		"deps": map[string]bool{
			"frontend": dirHasNodeModules(cfg.Projects.FrontendDir),
			"backend":  progOk("backend-tidy"),
		},
		"services": map[string]SvcInfo{"web": web, "backend": be},
	})
}

func handleElevate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	ps := fmt.Sprintf("Start-Process -FilePath '%s' -Verb RunAs", strings.ReplaceAll(exe, "'", "''"))
	if err := exec.Command("powershell.exe", "-NoProfile", "-Command", ps).Start(); err != nil {
		http.Error(w, "提权失败："+err.Error(), 500)
		return
	}
	info(scSys, "权限", "已请求以管理员身份重新启动")
	_, _ = w.Write([]byte(`{"ok":true}`))
	go func() { time.Sleep(700 * time.Millisecond); os.Exit(0) }()
}
