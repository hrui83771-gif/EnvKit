// program.go —— 程序配置/启动接口：建库、装依赖、起停前后端、状态查询
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ---------- SQL 风险扫描 ----------
// 执行 SQL 文件是一张单程票：MySQL 的 DDL 会隐式提交，执行到一半失败就停在"改了一半"，
// 且没有任何记录告诉你改到哪。这里至少做到"执行前把高危语句摆到用户面前"。

type sqlRisk struct {
	Line int    `json:"line"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

var (
	reDropDB    = regexp.MustCompile(`(?i)\bdrop\s+(database|schema)\b`)
	reDropObj   = regexp.MustCompile(`(?i)\bdrop\s+(table|view|index|trigger|procedure|function)\b`)
	reTruncate  = regexp.MustCompile(`(?i)\btruncate\b`)
	reAlterDrop = regexp.MustCompile(`(?i)\balter\s+table\b.*\bdrop\b`)
	reDelete    = regexp.MustCompile(`(?i)\bdelete\s+from\b`)
	reUpdateSet = regexp.MustCompile(`(?i)\bupdate\b.*\bset\b`)
	reRename    = regexp.MustCompile(`(?i)\brename\s+table\b`)
)

// scanRiskySQL 扫描 SQL 文件中的高危语句，返回最多 max 条（含行号）。
// 会跳过注释行与块注释内容，避免把文档里的示例语句误报成风险。
func scanRiskySQL(path string, max int) ([]sqlRisk, error) {
	if max <= 0 {
		max = 20
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var risks []sqlRisk
	inBlock := false
	lineNo := 0
	for sc.Scan() {
		lineNo++
		raw := sc.Text()
		code, still := stripSQLComment(raw, inBlock)
		inBlock = still
		s := strings.ToLower(strings.TrimSpace(code))
		if s == "" {
			continue
		}
		hasWhere := strings.Contains(s, " where ")
		var kind string
		switch {
		case reDropDB.MatchString(s):
			kind = "删库"
		case reDropObj.MatchString(s):
			kind = "删表/删对象"
		case reTruncate.MatchString(s):
			kind = "清空表"
		case reAlterDrop.MatchString(s):
			kind = "删字段/约束"
		case reRename.MatchString(s):
			kind = "改表名"
		case reDelete.MatchString(s) && !hasWhere:
			kind = "无 WHERE 的 DELETE"
		case reUpdateSet.MatchString(s) && !hasWhere:
			kind = "无 WHERE 的 UPDATE"
		}
		if kind == "" {
			continue
		}
		risks = append(risks, sqlRisk{Line: lineNo, Kind: kind, Text: truncateStr(strings.TrimSpace(code), 120)})
		if len(risks) >= max {
			break
		}
	}
	return risks, sc.Err()
}

// stripSQLComment 去掉行注释与块注释，返回本行有效代码与"是否仍在块注释中"。
func stripSQLComment(line string, inBlock bool) (string, bool) {
	var b strings.Builder
	i := 0
	for i < len(line) {
		if inBlock {
			if strings.HasPrefix(line[i:], "*/") {
				inBlock = false
				i += 2
			} else {
				i++
			}
			continue
		}
		if strings.HasPrefix(line[i:], "/*") {
			inBlock = true
			i += 2
			continue
		}
		if strings.HasPrefix(line[i:], "--") {
			break
		}
		if strings.HasPrefix(line[i:], "#") {
			break
		}
		b.WriteByte(line[i])
		i++
	}
	return b.String(), inBlock
}

// sqlRiskSummary 把风险列表压成一段人能读的文字（弹窗与日志共用）。
func sqlRiskSummary(risks []sqlRisk) string {
	return joinRiskKinds(risks, riskKindEN, "、")
}

// sqlRiskSummaryEN 英文版风险摘要（英文界面下的确认弹窗用）。
func sqlRiskSummaryEN(risks []sqlRisk) string {
	return joinRiskKinds(risks, riskKindEN, ", ")
}

// riskKindEN 中文风险类型 → 英文。
func riskKindEN(k string) string {
	switch k {
	case "删库":
		return "DROP DATABASE"
	case "删表/删对象":
		return "DROP TABLE/OBJECT"
	case "清空表":
		return "TRUNCATE"
	case "删字段/约束":
		return "ALTER ... DROP"
	case "改表名":
		return "RENAME TABLE"
	case "无 WHERE 的 DELETE":
		return "DELETE without WHERE"
	case "无 WHERE 的 UPDATE":
		return "UPDATE without WHERE"
	}
	return k
}

func joinRiskKinds(risks []sqlRisk, name func(string) string, sep string) string {
	if len(risks) == 0 {
		return ""
	}
	kinds := map[string]int{}
	order := []string{}
	for _, r := range risks {
		if _, seen := kinds[r.Kind]; !seen {
			order = append(order, r.Kind)
		}
		kinds[r.Kind]++
	}
	parts := make([]string, 0, len(order))
	for _, k := range order {
		parts = append(parts, fmt.Sprintf("%s ×%d", name(k), kinds[k]))
	}
	return strings.Join(parts, sep)
}

func dirHasNodeModules(dir string) bool {
	if dir == "" {
		return false
	}
	fi, err := os.Stat(filepath.Join(dir, "node_modules"))
	return err == nil && fi.IsDir()
}

func progOk(key string) bool {
	progMu.Lock()
	defer progMu.Unlock()
	st, ok2 := progState[key]
	return ok2 && st.Ok
}

// ---------- 程序配置 / 启动 处理入口 ----------
func handleDBTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	// 只读任务：不阻塞其它只读操作（如日志查看、诊断报告）
	th, granted := beginTaskH("数据库连接检测", false)
	if !granted {
		http.Error(w, "有任务正在执行，请稍候", 409)
		return
	}
	go func() {
		defer th.Done()
		fin := auditStart(actUser, "db_check", cfg.Projects.DBName, "")
		setProg("db-test", true, false, "检测中...")
		info(scConfig, "连接", "测试 %s:%d（用户 %s）", cfg.Projects.MySQLHost, cfg.Projects.MySQLPort, cfg.Projects.MySQLUser)
		h, err := dbTest()
		h.At = time.Now().Format("15:04:05")
		if err != nil {
			h.Msg = err.Error()
			dbMu.Lock()
			dbHealth = h
			dbMu.Unlock()
			setProg("db-test", false, false, err.Error())
			fail(scConfig, "连接", "%v", err)
			fin(resFail, err.Error())
			return
		}
		fin(resOK, h.Msg)
		dbMu.Lock()
		dbHealth = h
		dbMu.Unlock()
		if h.DBExists {
			ok(scConfig, "连接", "MySQL %s；数据库 %s 已存在", h.Version, cfg.Projects.DBName)
		} else {
			warn(scConfig, "连接", "MySQL %s；数据库 %s 不存在（可点“建库并执行 SQL”）", h.Version, cfg.Projects.DBName)
		}
		setProg("db-test", false, true, "成功")
	}()
	_, _ = w.Write([]byte(`{"started":true}`))
}

// 一步：建库 + 建表
func handleInitDB(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		DB  string `json:"db"`
		Sql string `json:"sql"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	// 记住所选 SQL 文件
	if body.Sql != "" {
		cfgMu.Lock()
		cfg.Projects.SQLFile = body.Sql
		c := cfg
		cfgMu.Unlock()
		saveExternalConfig(c)
	}
	h, granted := beginTaskH("建库并执行 SQL", true)
	if !granted {
		http.Error(w, "有任务正在执行，请稍候", 409)
		return
	}
	go func() {
		defer h.Done()
		db := body.DB
		if db == "" {
			db = cfg.Projects.DBName
		}
		fin := auditStart(actUser, "init_db", db, body.Sql)
		setProg("init-db", true, false, "建库中...")
		if err := createDatabaseNamed(db); err != nil {
			setProg("init-db", false, false, err.Error())
			fail(scConfig, "初始化", "建库失败：%v", err)
			fin(resFail, err.Error())
			return
		}
		ok(scConfig, "初始化", "数据库 %s 就绪", db)
		if body.Sql != "" {
			setProg("init-db", true, false, "执行 SQL 中...")
			if err := applySQLFile(body.Sql, db); err != nil {
				setProg("init-db", false, false, err.Error())
				fail(scConfig, "初始化", "执行 SQL 失败：%v", err)
				fin(resFail, err.Error())
				return
			}
		}
		fin(resOK, db)
		dbMu.Lock()
		dbHealth = DBHealth{Connected: true, DBExists: true, Msg: "已建库", At: time.Now().Format("15:04:05")}
		dbMu.Unlock()
		setProg("init-db", false, true, "成功")
		ok(scConfig, "初始化", "建库建表完成")
	}()
	_, _ = w.Write([]byte(`{"started":true}`))
}

func handleMySQLCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	hh, granted := beginTaskH("建库", true)
	if !granted {
		http.Error(w, "有任务正在执行，请稍候", 409)
		return
	}
	go func() {
		defer hh.Done()
		fin := auditStart(actUser, "create_db", cfg.Projects.DBName, "")
		setProg("mysql-create", true, false, "建库中...")
		if err := createDatabase(); err != nil {
			setProg("mysql-create", false, false, err.Error())
			fail(scConfig, "建库", "%v", err)
			fin(resFail, err.Error())
		} else {
			setProg("mysql-create", false, true, "成功")
			fin(resOK, cfg.Projects.DBName)
		}
	}()
	_, _ = w.Write([]byte(`{"started":true}`))
}

func handleApplySQL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Path       string `json:"path"`
		DB         string `json:"db"`
		Confirm    bool   `json:"confirm"`
		SkipBackup bool   `json:"skip_backup"`
		Lang       string `json:"lang"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Path == "" {
		http.Error(w, "请提供 sql 文件路径", 400)
		return
	}
	applySQLFlow(w, body.Path, body.DB, body.Confirm, body.SkipBackup, body.Lang, actUser, nil)
}

// applySQLFlow —— apply-sql 与 apply-upload（桌宠拖入 SQL，v1.9.6）共用的执行流：
// ① 高危语句扫描 → ② 未确认回 428（风险摘要交给前端弹窗）→ ③ 强制备份 → ④ 执行。
// cleanup：临时文件等执行后清理。confirm=false 时走不到后台协程，由本函数 defer 清理；
// confirm=true 时执行在后台协程，cleanup 挂在协程 defer 上（文件在执行完之前不能删）。
func applySQLFlow(w http.ResponseWriter, path, db string, confirm, skipBackup bool, lang, actor string, cleanup func()) {
	if db == "" {
		db = cfg.Projects.DBName
	}
	if cleanup != nil && !confirm {
		defer cleanup()
	}
	// ① 执行前扫描高危语句，把风险摆到用户面前（DROP/TRUNCATE/无 WHERE 的 DML…）
	risks, scanErr := scanRiskySQL(path, 20)
	if scanErr != nil {
		http.Error(w, "读取 SQL 文件失败："+scanErr.Error(), 400)
		return
	}
	// ② 未确认 → 返回 428 让前端弹窗（含风险摘要与影响对象）
	if !confirm {
		zh := fmt.Sprintf("即将对库 %s 执行 %s", db, filepath.Base(path))
		en := fmt.Sprintf("About to run %s against database %s", filepath.Base(path), db)
		if len(risks) > 0 {
			zh += fmt.Sprintf("。检测到 %d 处高危语句（%s），其中第 %d 行：%s",
				len(risks), sqlRiskSummary(risks), risks[0].Line, risks[0].Text)
			en += fmt.Sprintf(". Detected %d risky statement(s) (%s), e.g. line %d: %s",
				len(risks), sqlRiskSummaryEN(risks), risks[0].Line, risks[0].Text)
		} else {
			zh += "。未检测到高危语句，但该操作不可撤销，执行前会自动备份一次。"
			en += ". No risky statements detected, but this cannot be undone — a backup will be taken first."
		}
		auditNow(actor, "apply_sql", db, path, resDenied, "等待二次确认")
		needConfirm(w, "apply_sql", db, confirmReason(lang, zh, en), len(risks) > 0)
		return
	}
	h, granted := beginTaskH("执行 SQL", true)
	if !granted {
		if cleanup != nil {
			cleanup()
		}
		http.Error(w, "有任务正在执行，请稍候", 409)
		return
	}
	go func() {
		defer h.Done()
		if cleanup != nil {
			defer cleanup()
		}
		fin := auditStart(actor, "apply_sql", db, path)
		setProg("apply-sql", true, false, "执行中...")
		if len(risks) > 0 {
			warn(scConfig, "SQL", "该文件含 %d 处高危语句（%s），已确认执行", len(risks), sqlRiskSummary(risks))
		}
		// ③ 变更前强制备份：备份失败即中止，绝不带着"没有退路"的状态改库
		if !skipBackup {
			info(scConfig, "SQL", "执行前自动备份库 %s ...", db)
			bp := doBackup(actUser, nil)
			if bp == "" {
				setProg("apply-sql", false, false, "自动备份失败，已取消执行")
				fail(scConfig, "SQL", "自动备份失败，已取消执行（数据库未被修改）。请确认 mysqldump 可用后重试")
				fin(resFail, "自动备份失败，已取消")
				return
			}
			ok(scConfig, "SQL", "已备份：%s", bp)
		} else {
			warn(scConfig, "SQL", "已跳过自动备份（用户选择），本次变更没有回滚点")
		}
		if err := applySQLFile(path, db); err != nil {
			setProg("apply-sql", false, false, err.Error())
			fail(scConfig, "SQL", "执行失败（部分语句可能已生效，请用上面的备份恢复）：%v", err)
			fin(resFail, err.Error())
			return
		}
		setProg("apply-sql", false, true, "成功")
		ok(scConfig, "SQL", "执行完成：%s -> 库 %s", filepath.Base(path), db)
		fin(resOK, db)
	}()
	_, _ = w.Write([]byte(`{"started":true}`))
}

// handleApplyUpload —— 桌宠拖入 .sql 的上传通道（v1.9.6）：文件来源从"本机路径"变成
// "浏览器上传的内容"，落到系统临时目录后走与 apply-sql 完全相同的确认/备份/风险扫描流，执行完即删临时文件。
func handleApplyUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 24<<20) // 内容走 base64，24MB 上限约对应 18MB 的 SQL
	var body struct {
		Name    string `json:"name"`
		Content string `json:"content"` // base64（可带 data: 前缀）
		DB      string `json:"db"`
		Confirm bool   `json:"confirm"`
		Lang    string `json:"lang"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "请求体过大或格式错误（SQL 文件上限约 18MB）", 400)
		return
	}
	if !strings.HasSuffix(strings.ToLower(body.Name), ".sql") {
		http.Error(w, "只接受 .sql 文件", 400)
		return
	}
	b64 := body.Content
	if i := strings.Index(b64, ","); i >= 0 && strings.HasPrefix(b64, "data:") {
		b64 = b64[i+1:]
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) == 0 {
		http.Error(w, "文件内容解码失败或为空", 400)
		return
	}
	tmp, err := os.CreateTemp("", "ek-upload-*.sql")
	if err != nil {
		http.Error(w, "创建临时文件失败："+err.Error(), 500)
		return
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		http.Error(w, "写入临时文件失败："+err.Error(), 500)
		return
	}
	tmp.Close()
	applySQLFlow(w, tmpPath, body.DB, body.Confirm, false, body.Lang, actUser, func() { os.Remove(tmpPath) })
}

func handleWebInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Dir string `json:"dir"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	dir := body.Dir
	if dir == "" {
		dir = cfg.Projects.FrontendDir
	}
	h, granted := beginTaskH("前端 npm install", true)
	if !granted {
		http.Error(w, "有任务正在执行，请稍候", 409)
		return
	}
	go func() {
		defer h.Done()
		fin := auditStart(actUser, "web_install", dir, "npm install")
		setProg("web-install", true, false, "npm install 中...")
		if dir == "" {
			setProg("web-install", false, false, "未指定前端目录")
			fail(scConfig, "前端", "未指定前端目录")
			fin(resFail, "未指定前端目录")
			return
		}
		exe := findExe("npm.cmd", "npm")
		if exe == "" {
			setProg("web-install", false, false, "未找到 npm")
			fail(scConfig, "前端", "未找到 npm，请先安装 Node.js")
			fin(resFail, "未找到 npm")
			return
		}
		info(scConfig, "前端", "npm install @ %s", dir)
		if err := execStreamed(scConfig, "", "前端", dir, npmEnvExtra(), exe, "install", "--registry", cfg.NpmRegistry); err != nil {
			setProg("web-install", false, false, err.Error())
			fail(scConfig, "前端", "npm install 失败：%v", err)
			fin(resFail, err.Error())
		} else {
			setProg("web-install", false, true, "成功")
			ok(scConfig, "前端", "npm install 完成")
			fin(resOK, dir)
		}
	}()
	_, _ = w.Write([]byte(`{"started":true}`))
}

func handleBackendTidy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Dir string `json:"dir"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	dir := body.Dir
	if dir == "" {
		dir = cfg.Projects.BackendDir
	}
	h, granted := beginTaskH("后端 go mod tidy", true)
	if !granted {
		http.Error(w, "有任务正在执行，请稍候", 409)
		return
	}
	go func() {
		defer h.Done()
		fin := auditStart(actUser, "backend_tidy", dir, "go mod tidy")
		setProg("backend-tidy", true, false, "go mod tidy 中...")
		if dir == "" {
			setProg("backend-tidy", false, false, "未指定后端目录")
			fail(scConfig, "后端", "未指定后端目录")
			fin(resFail, "未指定后端目录")
			return
		}
		exe := findExe("go.exe", "go")
		if exe == "" {
			setProg("backend-tidy", false, false, "未找到 go")
			fail(scConfig, "后端", "未找到 go，请先安装 Go")
			fin(resFail, "未找到 go")
			return
		}
		info(scConfig, "后端", "go mod tidy @ %s", dir)
		if err := execStreamed(scConfig, "", "后端", dir, goEnvExtra(), exe, "mod", "tidy"); err != nil {
			setProg("backend-tidy", false, false, err.Error())
			fail(scConfig, "后端", "go mod tidy 失败：%v", err)
			fin(resFail, err.Error())
		} else {
			setProg("backend-tidy", false, true, "成功")
			ok(scConfig, "后端", "go mod tidy 完成")
			fin(resOK, dir)
		}
	}()
	_, _ = w.Write([]byte(`{"started":true}`))
}

func handleWebStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Dir    string `json:"dir"`
		Script string `json:"script"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	dir := body.Dir
	if dir == "" {
		dir = cfg.Projects.FrontendDir
	}
	script := strings.TrimSpace(body.Script) // 留空 = 服务端按 package.json 推断（v2.1）
	// 注意：不在这里塞默认值 "serve"。旧版无条件回退到 serve，
	// 导致"项目没有 serve 脚本"时得到一句莫名的 npm 报错。
	// 兜底逻辑收敛到 launchWebScriptOrDefault 一处，且只在读不到 package.json 时才生效。
	if script != "" && !validScriptName(script) {
		http.Error(w, "脚本名含非法字符（只允许字母、数字、- _ : . /）", 400)
		return
	}
	if r := webStartTask(dir, script, actUser); !r.Ok {
		http.Error(w, r.String(), 409)
		return
	}
	_, _ = w.Write([]byte(`{"started":true}`))
}

// validScriptName 校验 npm 脚本名：防注入兜底（exec 走 argv 不经 shell，此为纵深防御）。
func validScriptName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == ':' || r == '.' || r == '/':
		default:
			return false
		}
	}
	return true
}

// webStartTask 启动前端（供按钮与 AI 工具共用）；同步等待派生完成。
// actor 用于审计区分操作主体（user / ai）。
//
// 返回 OpResult 而非 error：老实现里失败只写日志、外层一律 return nil，
// 调用方（尤其 AI 工具）因此永远以为成功进而向用户谎报"已启动"。
func webStartTask(dir, script, actor string) OpResult {
	if actor == "" {
		actor = actUser
	}
	if dir == "" {
		dir = cfg.Projects.FrontendDir
	}
	h, granted := beginTaskH("启动前端", true)
	if !granted {
		return opFail("start_service", "web", errKindBusy, "有任务正在执行，请稍候")
	}
	done := make(chan struct{})
	var res OpResult
	go func() {
		defer h.Done()
		defer close(done)
		res = opStartWeb(dir, script, actor)
		h.SetResult(res)
	}()
	<-done
	return res
}

// opStartWeb 前端启动执行体（任务协程内运行），返回结构化结果。
// script 传空表示"由服务端推断"（v2.1）——这是默认路径，也是 AI 与按钮共用的路径。
func opStartWeb(dir, script, actor string) OpResult {
	fin := auditStart(actor, "start_service", "web", script)
	setProg("web-start", true, false, "启动中...")
	if dir == "" {
		setProg("web-start", false, false, "未指定前端目录")
		failS(scStart, "web", "前端", "未指定前端目录")
		fin(resFail, "未指定前端目录")
		return opFail("start_service", "web", errKindBadConfig,
			"未指定前端目录：请先在「程序配置」里选择前端目录", "hint=config.frontend_dir")
	}
	// 目录存在是"能不能跑"的第一道事实：不存在时不要派进程，否则只会得到一句莫名的 npm 错误
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		setProg("web-start", false, false, "前端目录不可用")
		failS(scStart, "web", "前端", "前端目录不可用：%s", dir)
		fin(resFail, "前端目录不可用")
		return opFail("start_service", "web", errKindBadConfig, "前端目录不可用："+dir, "dir="+dir)
	}
	// 启动脚本：显式指定 > 用户配置 > package.json 推断。识别不出就明确失败，
	// 绝不静默回退到 serve —— 那正是"换个项目就启动不了"的根源（v2.1）。
	script, scriptSrc, ok := launchWebScriptOrDefault(dir, script)
	if !ok {
		setProg("web-start", false, false, "未识别启动脚本")
		failS(scStart, "web", "前端", "未识别可用的前端启动脚本")
		fin(resFail, "未识别可用的前端启动脚本")
		plan := currentLaunchPlan()
		msg := "未识别可用的前端启动脚本"
		if len(plan.Blocked) > 0 {
			msg += "：package.json 里的脚本（" + strings.Join(plan.Blocked, "、") + "）带副作用或破坏性语义，已按安全规则拒绝"
		} else {
			msg += "：未在 package.json 中找到 dev / serve / start 之类的脚本"
		}
		return opFail("start_service", "web", errKindBadConfig, msg,
			"dir="+dir, "hint=在「程序配置」的前端启动脚本字段手动填写",
			"candidates="+strings.Join(plan.WebAll, ","))
	}
	exe := findExe("npm.cmd", "npm")
	if exe == "" {
		setProg("web-start", false, false, "未找到 npm")
		failS(scStart, "web", "前端", "未找到 npm")
		fin(resFail, "未找到 npm")
		return opFail("start_service", "web", errKindMissingDep,
			"未找到 npm：请先在「环境安装」安装 Node.js", "need=npm")
	}
	infoS(scStart, "web", "前端", "npm run %s（%s） @ %s", script, scriptSrc, dir)
	if err := execBackground(scStart, "web", "web-start", "前端", dir, npmEnvExtra(), exe, "run", script); err != nil {
		setProg("web-start", false, false, err.Error())
		failS(scStart, "web", "前端", "启动失败：%v", err)
		fin(resFail, err.Error())
		return opFail("start_service", "web", errKindSpawnFail,
			"npm 进程派生失败："+firstLines(err.Error(), 200),
			"dir="+dir, "script="+script, "source="+scriptSrc,
			"hint=零输出秒退通常是杀毒软件拦截，可用 apply_whitelist 加入 Defender 白名单")
	}
	okS(scStart, "web", "前端", "已启动（npm run %s）", script)
	fin(resOK, script+"（"+scriptSrc+"）")
	// 诚实措辞：此刻只证明"进程已派生且未立即退出"，尚未做端口/HTTP 可用性复验（见 v2.0 P2 验证层）
	return opOK("start_service", "web", "npm run "+script+" 进程已派生（尚未做服务可用性复验）",
		"dir="+dir, "script="+script)
}

func handleBackendStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Dir string `json:"dir"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	dir := body.Dir
	if dir == "" {
		dir = cfg.Projects.BackendDir
	}
	if r := backendStartTask(dir, actUser); !r.Ok {
		http.Error(w, r.String(), 409)
		return
	}
	_, _ = w.Write([]byte(`{"started":true}`))
}

// backendStartTask 构建并启动后端（供按钮与 AI 工具共用）；同步等待完成（含 go build）。
// 返回 OpResult：go build 失败 / go run 派生失败都会显式返回失败，不再被吞掉。
func backendStartTask(dir, actor string) OpResult {
	if actor == "" {
		actor = actUser
	}
	if dir == "" {
		dir = cfg.Projects.BackendDir
	}
	h, granted := beginTaskH("构建并启动后端", true)
	if !granted {
		return opFail("start_service", "backend", errKindBusy, "有任务正在执行，请稍候")
	}
	done := make(chan struct{})
	var res OpResult
	go func() {
		defer h.Done()
		defer close(done)
		res = opStartBackend(dir, actor)
		h.SetResult(res)
	}()
	<-done
	return res
}

// opStartBackend 后端构建 + 启动执行体（任务协程内运行）。
func opStartBackend(dir, actor string) OpResult {
	fin := auditStart(actor, "start_service", "backend", "go build + go run")
	setProg("backend-start", true, false, "构建并启动中...")
	if dir == "" {
		setProg("backend-start", false, false, "未指定后端目录")
		failS(scStart, "backend", "后端", "未指定后端目录")
		fin(resFail, "未指定后端目录")
		return opFail("start_service", "backend", errKindBadConfig,
			"未指定后端目录：请先在「程序配置」里选择后端目录", "hint=config.backend_dir")
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		setProg("backend-start", false, false, "后端目录不可用")
		failS(scStart, "backend", "后端", "后端目录不可用：%s", dir)
		fin(resFail, "后端目录不可用")
		return opFail("start_service", "backend", errKindBadConfig, "后端目录不可用："+dir, "dir="+dir)
	}
	exe := findExe("go.exe", "go")
	if exe == "" {
		setProg("backend-start", false, false, "未找到 go")
		failS(scStart, "backend", "后端", "未找到 go")
		fin(resFail, "未找到 go")
		return opFail("start_service", "backend", errKindMissingDep,
			"未找到 go：请先在「环境安装」安装 Go", "need=go")
	}
	infoS(scStart, "backend", "后端", "go build @ %s", dir)
	if err := execStreamed(scStart, "backend", "后端", dir, goEnvExtra(), exe, "build", "./..."); err != nil {
		setProg("backend-start", false, false, err.Error())
		failS(scStart, "backend", "后端", "go build 失败：%v", err)
		fin(resFail, err.Error())
		return opFail("start_service", "backend", errKindBuildFail,
			"go build 失败："+firstLines(err.Error(), 300),
			"dir="+dir, "hint=查看「程序启动」日志中的编译错误")
	}
	// 入口不再写死 main.go（v2.1）：cmd/ 布局的项目会因此直接失败。
	// 推断不出时报错——盲跑 `go run main.go` 只会得到一句莫名的编译错误。
	entry, why, ok := launchBackendFileOrDefault(dir)
	if !ok {
		setProg("backend-start", false, false, "未识别后端入口")
		failS(scStart, "backend", "后端", "未识别后端入口：%s", why)
		fin(resFail, "未识别后端入口")
		return opFail("start_service", "backend", errKindBadConfig,
			"未识别后端入口："+why,
			"dir="+dir, "hint=在「程序配置」的后端入口字段手动填写（相对后端目录，如 main.go 或 cmd/server/main.go）")
	}
	infoS(scStart, "backend", "后端", "go run %s", entry)
	if err := execBackground(scStart, "backend", "backend-start", "后端", dir, goEnvExtra(), exe, "run", entry); err != nil {
		setProg("backend-start", false, false, err.Error())
		failS(scStart, "backend", "后端", "go run 失败：%v", err)
		fin(resFail, err.Error())
		return opFail("start_service", "backend", errKindSpawnFail,
			"go run 进程派生失败："+firstLines(err.Error(), 200),
			"dir="+dir, "entry="+entry, "hint=零输出秒退通常是杀毒软件拦截，可用 apply_whitelist 加入 Defender 白名单")
	}
	okS(scStart, "backend", "后端", "已启动（go run main.go）")
	fin(resOK, "backend")
	// 与前端同理：此处只证明"构建通过且进程已派生"，端口/HTTP 可用性复验见 v2.0 P2
	return opOK("start_service", "backend", "后端进程已派生（go build 通过，尚未做服务可用性复验）", "dir="+dir)
}

func handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	// 停服务时清掉端口记录：否则重启后会拿上次的端口做漂移比对，
	// 把"正常重启后端口一致"误报成"没变化"或反向误报成"漂移"。
	switch body.Key {
	case "web":
		stopByKey(scStart, "web", "web-start")
		svcForgetPort("web")
		svcForgetVerify("web")
		auditNow(actUser, "stop_service", "web", "", resOK, "")
	case "backend":
		stopByKey(scStart, "backend", "backend-start")
		svcForgetPort("backend")
		svcForgetVerify("backend")
		auditNow(actUser, "stop_service", "backend", "", resOK, "")
	default:
		stopByKey(scStart, "web", "web-start")
		stopByKey(scStart, "backend", "backend-start")
		svcForgetPort("web")
		svcForgetPort("backend")
		svcForgetVerify("web")
		svcForgetVerify("backend")
		auditNow(actUser, "stop_service", "all", "", resOK, "")
	}
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// handleProgramExit 网页端「退出程序」。stop_services=false：只退出 EnvKit，
// 前端/后端服务继续在后台运行（Windows 上需先把 Job 句柄复制进子进程，否则
// KILL_ON_JOB_CLOSE 会在进程退出瞬间强杀整棵子进程树，见 keepJobAlive）；
// true：先停子进程再退出，与 Ctrl+C 关窗同一套清理路径。
// 响应先写回并延迟 500ms 再退出，避免浏览器侧 fetch 直接断连报错。
func handleProgramExit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		StopServices bool `json:"stop_services"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	mode := "assistant_only"
	if body.StopServices {
		mode = "with_services"
	}
	auditNow(actUser, "exit_program", mode, "", resOK, "")
	_, _ = w.Write([]byte(`{"ok":true}`))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		time.Sleep(500 * time.Millisecond)
		if body.StopServices {
			shutdownChildren()
		} else {
			keepJobAlive()
		}
		os.Exit(0)
	}()
}
