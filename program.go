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

	"envkit/internal/platform"
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
	th, granted := beginTaskHG("数据库连接检测", []string{grpDB}, false)
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
	h, granted := beginTaskHG("建库并执行 SQL", []string{grpDB}, true)
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
	hh, granted := beginTaskHG("建库", []string{grpDB}, true)
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
	h, granted := beginTaskHG("执行 SQL", []string{grpDB}, true)
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
		// v3.3：npm 不能靠 findExe——PATH 上第一个 npm.cmd 可能是个
		// node_modules 已被删掉、每次必失败的残壳。这里逐个校验真身再选。
		cand, broken := resolveNpm(true)
		if cand.Exe == "" {
			setProg("web-install", false, false, "未找到可用的 npm")
			fail(scConfig, "前端", "未找到可用的 npm，请先安装 Node.js")
			if hint := npmBrokenHint(broken); hint != "" {
				fail(scConfig, "前端", "%s", hint)
			}
			fin(resFail, "未找到可用的 npm")
			return
		}
		if len(broken) > 0 {
			warn(scConfig, "前端", "%s", npmBrokenHint(broken))
		}
		info(scConfig, "前端", "npm install @ %s", dir)
		nm, nargs := npmInvocation(cand, "install", "--registry", cfg.NpmRegistry)
		res, err := execStreamedDetail(scConfig, "", "前端", dir, npmEnvExtra(), nm, nargs...)
		if err != nil {
			why := explainExit(res, err)
			setProg("web-install", false, false, why)
			fail(scConfig, "前端", "npm install 失败：%s", why)
			if hint := npmBrokenHint(broken); hint != "" {
				fail(scConfig, "前端", "%s", hint)
			}
			fin(resFail, why)
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
	kind := backendProjectKind(dir)
	taskLabel := "后端依赖整理"
	if kind == "maven" {
		taskLabel = "后端 Maven 依赖解析"
	}
	h, granted := beginTaskH(taskLabel, true)
	if !granted {
		http.Error(w, "有任务正在执行，请稍候", 409)
		return
	}
	go func() {
		defer h.Done()
		if dir == "" {
			setProg("backend-tidy", false, false, "未指定后端目录")
			fail(scConfig, "后端", "未指定后端目录")
			return
		}
		// Maven 项目没有 go.mod，跑 go mod tidy 只会得到"找不到 go.mod"；
		// 依赖整理改为 dependency:resolve（等价于把 pom 里的依赖全部拉齐）。
		if kind == "maven" {
			fin := auditStart(actUser, "backend_tidy", dir, "mvn dependency:resolve")
			setProg("backend-tidy", true, false, "mvn dependency:resolve 中...")
			javaPath := findExe("java.exe", "java")
			if javaPath == "" {
				if jh := strings.TrimSpace(os.Getenv("JAVA_HOME")); jh != "" {
					cand := filepath.Join(jh, "bin", "java.exe")
					if hasFile(cand) {
						javaPath = cand
					}
				}
			}
			if javaPath == "" {
				setProg("backend-tidy", false, false, "未找到 JDK")
				fail(scConfig, "后端", "未找到 JDK，请先安装 JDK")
				fin(resFail, "未找到 JDK")
				return
			}
			mvnPath := resolveMavenCmd(dir)
			if mvnPath == "" {
				setProg("backend-tidy", false, false, "未找到 Maven")
				fail(scConfig, "后端", "未找到 Maven，请先安装 Maven")
				fin(resFail, "未找到 Maven")
				return
			}
			info(scConfig, "后端", "mvn dependency:resolve @ %s", dir)
			if err := execStreamed(scConfig, "", "后端", dir, mavenStartEnv(dir, javaPath), mvnPath, "-q", "dependency:resolve"); err != nil {
				setProg("backend-tidy", false, false, err.Error())
				fail(scConfig, "后端", "mvn dependency:resolve 失败：%v", err)
				fin(resFail, err.Error())
			} else {
				setProg("backend-tidy", false, true, "成功")
				ok(scConfig, "后端", "Maven 依赖解析完成")
				fin(resOK, dir)
			}
			return
		}
		fin := auditStart(actUser, "backend_tidy", dir, "go mod tidy")
		setProg("backend-tidy", true, false, "go mod tidy 中...")
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
	res := webStartTask(dir, script, actUser)
	if !res.Ok {
		http.Error(w, res.String(), 409)
		return
	}
	if res.SkipVerify {
		writeJSON(w, map[string]any{
			"started": true, "verified": false, "external": true,
			"message": res.Msg,
		})
		return
	}
	// v2.5：与后端端点同一个修复——点「启动」后当场复验。
	// 理由见 handleBackendStart 的注释：只反映部分调用路径的复验记录
	// 会让 ever_verified变成假判据，比不复验更糟。
	verified := verifyService("web", verifyWaitWeb())
	if !verified.Ok {
		writeJSON(w, map[string]any{
			"started": true, "verified": false,
			"message":  res.Msg + "；" + verified.Msg,
			"err_kind": verified.ErrKind,
		})
		return
	}
	writeJSON(w, map[string]any{
		"started": true, "verified": verified.Verified,
		"message": res.Msg + "；" + verified.Msg,
	})
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
//
// v3.0 T1.2：持 **web 资源组**锁——与启动/停止/重启前端互斥，
// 但与后端任务并行（旧全局锁会把两者无谓地串起来）。
func webStartTask(dir, script, actor string) OpResult {
	if actor == "" {
		actor = actUser
	}
	if dir == "" {
		dir = cfg.Projects.FrontendDir
	}
	h, granted := beginTaskHG("启动前端", []string{grpWeb}, true)
	if !granted {
		return opFail("start_service", "web", errKindBusy, busyReason("web"))
	}
	defer h.Done()
	// v3.0 T1.4：看门狗超时时真正终止前台命令（web 启动没有前台命令，
	// fgKill 会返回 false，terminate 据此说"未注册可终止的子进程"而不是撒谎）；
	// 超时后先核对实际状态再记审计。
	h.SetKill(func() { fgKill(scStart + "/web") })
	h.TimeoutFact = func() string { return startTimeoutFact("web") }
	res := opStartWeb(dir, script, actor)
	h.SetResult(res)
	return res
}

// webStartTaskLocked 启动前端执行体（调用方**已持 web 组锁**时用）。
// restart_service(all) 这类"停+起必须一气呵成"的动作先自己拿锁，
// 再走这个变体——否则内层 webStartTask 抢同一把锁只会拿到 errKindBusy。
func webStartTaskLocked(dir, script, actor string) OpResult {
	if dir == "" {
		dir = cfg.Projects.FrontendDir
	}
	return opStartWeb(dir, script, actor)
}

// svcStartSkip 启动幂等前置（v3.0 T1.3，修 G3）。
//
// 旧实现不查状态直接派生，两个后果：
//  1. 重复 start 产生孤儿进程（端口被旧的占着，新的起不来或起在错端口）；
//  2. 旧监控 goroutine 退出时 delete(activeProcs, key)（exec.go）会把
//     新进程的条目删掉，之后 stopByKey 找不到进程——"停不掉"。
//
// 规则（plan-v3.0 §2.4 幂等控制）：
//
//	· Running && Verified → 返回"已在运行且复验通过"（ok+verified，不派生）；
//	· Running && !Verified → 不重复派生，交回调用方走 Verify
//	  （"已执行但未证明恢复"是未知，不是成功——R3 三态不合并）；
//	· 未 Running → 不跳过，正常派生。
//
// 第二档返回 opOK 而非 opVerified 是刻意的：调用方（按钮/AI）拿到它
// 都会继续复验，复验不过就该 stop/restart，而不是再派生一个。
func svcStartSkip(target string) (OpResult, bool) {
	svcMu.Lock()
	s := svcState[target]
	var running bool
	var pid int
	if s != nil {
		running, pid = s.Running, s.PID
	}
	svcMu.Unlock()
	if !running {
		// EnvKit 没在管，但端口观察器可能看到外部实例。这里不派生新进程：
		// 用户已经在别的终端跑起来时，重复启动只会撞端口/产生孤儿。
		if o, ok := observeSnapshot(target); ok {
			switch o.State {
			case "external":
				r := opOK("start_service", target,
					fmt.Sprintf("检测到%s外部实例（PID %d，端口 %d）：EnvKit 未托管，未重复启动；可直接使用，停止请到原终端",
						svcLabel(target), o.PID, o.Port),
					fmt.Sprintf("pid=%d port=%d source=external", o.PID, o.Port),
					"hint=如需由 EnvKit 管理，请先停止外部实例再启动")
				r.SkipVerify = true
				return r, true
			case "unknown":
				return opFail("start_service", target, errKindPortConflict,
					fmt.Sprintf("%s候选端口 %d 有监听，但无法确认是本项目；未启动，避免与未知进程抢端口",
						svcLabel(target), o.Port),
					fmt.Sprintf("pid=%d port=%d owner=%s source=unknown", o.PID, o.Port, nonEmptyStr(o.OwnerName, "未知")),
					"hint=到「端口诊断」核对命令行/路径后再决定"), true
			case "foreign":
				// 只有项目画像/package.json/config 声明出来的端口（>=75）才允许阻断。
				// scan_ports=60 是诊断清单，用户可能把后端 8080 放进去排查；拿它拦前端
				// 会把正在运行的后端误判成“前端端口冲突”。
				if o.PortConfidence >= 75 {
					return opFail("start_service", target, errKindPortConflict,
						fmt.Sprintf("%s启动前置失败：端口 %d 被 %s（PID %d）占着，且不像本项目",
							svcLabel(target), o.Port, nonEmptyStr(o.OwnerName, "未知进程"), o.PID),
						fmt.Sprintf("port=%d pid=%d owner=%s source=foreign", o.Port, o.PID, nonEmptyStr(o.OwnerName, "未知")),
						"hint="+lifecycleHint(errKindPortConflict)), true
				}
			}
		}
		return OpResult{}, false
	}
	if svcSnapshot(target).Verified {
		return opVerified("start_service", target,
			svcLabel(target)+"已在运行且复验通过，未重复启动",
			fmt.Sprintf("pid=%d", pid),
			"hint=需要重启请用 stop/restart，重复 start 不会派生新进程"), true
	}
	return opOK("start_service", target,
		fmt.Sprintf("%s进程已在运行（PID %d），未重复启动；服务是否可用以复验结论为准", svcLabel(target), pid),
		fmt.Sprintf("pid=%d", pid),
		"hint=复验未通过时可用 restart_service 停旧起新，不要反复 start"), true
}

// progSetSkip 把"幂等跳过"落成正确的三态，而不是非真即假。
//
// 语义边界：跳过 = 这一轮**没有执行任何启动动作**，结论只能来自既往复验。
//
//	已复验通过 → ok    （真的在跑，且验过）
//	未复验     → warn  （在跑，但这一轮没有新鲜证据；**不是失败**）
//	!Ok        → fail  （理论到不了：命中跳过说明 Running 为真）
//
// 为什么必须单独一个函数：setProg(key, running, ok2, msg) 只有两态，
// 表达不了"既没在启动、也没失败"这一档，只能压成 fail。
func progSetSkip(key string, r OpResult) {
	st := progWarn
	switch {
	case !r.Ok:
		st = progFail
	case r.SkipVerify:
		// 外部实例：启动动作被有意跳过，但它确实在跑。
		// 不能落 progWarn（界面会说"已启动 · 未通过复验"，像是 EnvKit 启动失败）。
		st = progExternal
	case r.Verified:
		st = progOK
	}
	msg := r.Msg
	if msg == "" {
		msg = "已在运行，未重复启动"
	}
	setProgStatus(key, false, st, msg)
}

// startTimeoutFact 启动任务被看门狗超时终止后，强制核对实际状态（v3.0 T1.4，修 G4）。
//
// 旧实现超时只释放锁、不核对："以为超时其实成功了"只能靠下一次复验偶然发现。
// 核对只用客观事实：进程在不在（svcAlive）、端口有没有在听、日志尾是什么。
// 结论只允许两种——"已退出"或"仍在运行未复验"，不存在"可能起来了"这第三种：
// 端口在听也只能说"未复验"，因为没过存活观察窗就不算恢复（R3）。
func startTimeoutFact(target string) string {
	label := svcLabel(target)
	alive, pid := svcAlive(target)
	note := ""
	if tail := svcLogTail(target, 6); tail != "" {
		note = "；日志尾：" + tail
	}
	if !alive {
		return label + "已退出（超时核对：进程不存在，本次启动未成功）" + note
	}
	if port := firstListeningPort(svcPortsFn(target)); port > 0 {
		return fmt.Sprintf("%s仍在运行未复验（超时核对：PID %d 在，端口 %d 在监听，但未经存活观察窗确认，不能算已恢复）",
			label, pid, port)
	}
	return fmt.Sprintf("%s仍在运行未复验（超时核对：PID %d 在，候选端口均未监听）%s", label, pid, note)
}

// opStartWeb 前端启动执行体（任务协程内运行），返回结构化结果。
// script 传空表示"由服务端推断"（v2.1）——这是默认路径，也是 AI 与按钮共用的路径。
func opStartWeb(dir, script, actor string) OpResult {
	fin := auditStart(actor, "start_service", "web", script)
	setProg("web-start", true, false, "启动中...")
	// v3.0 T1.3 幂等前置（修 G3）：已在运行就不重复派生（见 svcStartSkip）。
	if res, hit := svcStartSkip("web"); hit {
		// 幂等跳过是**正常结局**，不是失败。
		//
		// 老实现写的是 setProg(key, false, res.Verified, msg)：res.Verified=false 时
		// setProg 的 switch 落到 progFail —— "服务明明在跑"被记成"启动失败"。
		// 触发条件很常见：上一次启动慢（首次 vite 编译超窗口）→ 复验未通过 →
		// 用户再点一次启动 → 命中幂等跳过 → 状态被写成 fail。
		// 随后 handleWebStart 里的 verifyService 其实通过了，也救不回来
		// （endStartFlag 见 Running=false 会直接 return）。
		// 于是界面卡片是绿的、这份状态是红的，两个读数打架。
		progSetSkip("web-start", res)
		fin(rrRes(res), "启动前置："+res.Msg)
		return res
	}
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
	// v3.3：与「安装依赖」走同一个解析器（见 exec.go resolveNpm）。
	// 旧实现两处都用 findExe("npm.cmd")：PATH 上第一个是残壳时，
	// 安装必失败、启动则"看似能跑"——因为 npm.cmd 壳仍能被 cmd 解析起来，
	// 真正失败发生在 node 里，且只在深一层才报错。
	cand, broken := resolveNpm(true)
	if cand.Exe == "" {
		setProg("web-start", false, false, "未找到可用的 npm")
		failS(scStart, "web", "前端", "未找到可用的 npm")
		if hint := npmBrokenHint(broken); hint != "" {
			failS(scStart, "web", "前端", "%s", hint)
		}
		fin(resFail, "未找到可用的 npm")
		return opFail("start_service", "web", errKindMissingDep,
			"未找到可用的 npm：请在「环境安装」重新安装 Node.js", "need=npm",
			"broken="+strings.Join(broken, ","))
	}
	if len(broken) > 0 {
		warnS(scStart, "web", "前端", "%s", npmBrokenHint(broken))
	}
	infoS(scStart, "web", "前端", "npm run %s（%s） @ %s", script, scriptSrc, dir)
	nm, nargs := npmInvocation(cand, "run", script)
	if err := execBackground(scStart, "web", "web-start", "前端", dir, npmEnvExtra(), nm, nargs...); err != nil {
		setProg("web-start", false, false, err.Error())
		failS(scStart, "web", "前端", "启动失败：%v", err)
		fin(resFail, err.Error())
		return opFail("start_service", "web", errKindSpawnFail,
			"npm 进程派生失败："+firstLines(err.Error(), 200),
			"dir="+dir, "script="+script, "source="+scriptSrc,
			"hint=先看「启动」日志里 npm 自己打印的报错；确无任何输出时才考虑安全软件拦截")
	}
	okS(scStart, "web", "前端", "已启动（npm run %s）", script)
	fin(resOK, script+"（"+scriptSrc+"）")
	// 诚实措辞：此刻只证明"进程已派生且未立即退出"，尚未做端口/HTTP 可用性复验（见 v2.0 P2 验证层）
	//
	// v3.6.2 注意：这里**故意不复位** "web-start" 标志。
	// 它表示的是"这一轮启动还没结束"（含随后的可用性复验等待），
	// 复位点在 verifyService —— 那是所有启动路径（按钮 / AI / 守护）的共同收口。
	// 早期版本在成功路径就复位、失败路径不复位，两边都错；
	// 另一版只在进程退出时复位，于是只要进程活着，界面永远停在「启动中」。
	return opOK("start_service", "web", "npm run "+script+" 进程已派生",
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
	res := backendStartTask(dir, actUser)
	if !res.Ok {
		http.Error(w, res.String(), 409)
		return
	}
	if res.SkipVerify {
		writeJSON(w, map[string]any{
			"started": true, "verified": false, "external": true,
			"message": res.Msg,
		})
		return
	}
	// v2.5：用户点「启动」后**当场复验**，不只报"进程已派生"。
	//
	// ## 为什么必须在这里补（评测逼出来的）
	//
	// 原来只有 AI 工具路径走 aiStartVerify（会调 verifyService 并写
	// svcRecordVerify），而**用户点按钮这条路径完全不复验**。
	// 后果不只是"用户不知道有没有起来"，还有一个更隐蔽的问题：
	//
	//   svcRecordVerify 从没被调用 → ever_verified 永远是 false
	//   → 快照里那句「服务从未成功运行过」是**错的**
	//   → AI 拿到一个假判据，据此做出错误诊断
	//
	// 实测踩到：沙箱评测里服务被杀掉后，AI 读到 ever_verified=false，
	// 于是判成"启动失败"而不是"被杀后需要重启"，方向从第一步就错。
	//
	// **一个只反映部分调用路径的记录比没有记录更危险**——
	// 它让判据看起来是客观事实，实际取决于谁触发的。
	//
	// 同步等待而不是异步：这是用户点按钮，本来就该等结果。
	// 45s 是上限，正常服务 1~2s 内就绪。
	verified := verifyService("backend", verifyWaitBackend())
	if !verified.Ok {
		// 复验失败不回滚启动——进程可能还在启动中，
		// 但必须如实说"派生成功但复验没通过"，不能报"已启动"。
		writeJSON(w, map[string]any{
			"started": true, "verified": false,
			"message":  res.Msg + "；" + verified.Msg,
			"err_kind": verified.ErrKind,
		})
		return
	}
	writeJSON(w, map[string]any{
		"started": true, "verified": verified.Verified,
		"message": res.Msg + "；" + verified.Msg,
	})
}

// backendStartTask 构建并启动后端（供按钮与 AI 工具共用）；同步等待完成（含 go build）。
// 返回 OpResult：go build 失败 / go run 派生失败都会显式返回失败，不再被吞掉。
// v3.0 T1.2：持 **backend 资源组**锁（同 webStartTask 的理由）。
func backendStartTask(dir, actor string) OpResult {
	if actor == "" {
		actor = actUser
	}
	if dir == "" {
		dir = cfg.Projects.BackendDir
	}
	h, granted := beginTaskHG("构建并启动后端", []string{grpBackend}, true)
	if !granted {
		return opFail("start_service", "backend", errKindBusy, busyReason("backend"))
	}
	defer h.Done()
	// v3.0 T1.4：go build 是这条路径上唯一的前台命令（execStreamed
	// 以 "start/backend" 注册）。旧实现不注册 SetKill——看门狗超时只放锁，
	// go build 挂死后锁放了、编译还在跑，之后谁都拿不到 backend 组锁。
	h.SetKill(func() { fgKill(scStart + "/backend") })
	h.TimeoutFact = func() string { return startTimeoutFact("backend") }
	res := opStartBackend(dir, actor)
	h.SetResult(res)
	return res
}

// backendStartTaskLocked 后端启动执行体（调用方已持 backend 组锁时用）。
func backendStartTaskLocked(dir, actor string) OpResult {
	if dir == "" {
		dir = cfg.Projects.BackendDir
	}
	return opStartBackend(dir, actor)
}

// opStartBackend 后端构建 + 启动执行体（任务协程内运行）。
// 按 backendProjectKind 分派：Maven 项目走 mvn，其余走 go。
// 幂等跳过与目录校验两种路径对两种后端完全一致，所以放在分派之前。
func opStartBackend(dir, actor string) OpResult {
	kind := backendProjectKind(dir)
	label := "go build + go run"
	if kind == "maven" {
		label = "mvn spring-boot:run"
	}
	fin := auditStart(actor, "start_service", "backend", label)
	setProg("backend-start", true, false, "构建并启动中...")
	// v3.0 T1.3 幂等前置（修 G3）：已在运行就不重复派生。
	// 后端连 go build 一起跳过——"改代码后想重新编译"用 restart_service，
	// 它先停旧再起新，不存在半新半旧的中间态。
	if res, hit := svcStartSkip("backend"); hit {
		progSetSkip("backend-start", res)
		fin(rrRes(res), "启动前置："+res.Msg)
		return res
	}
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
	if kind == "maven" {
		return opStartBackendMaven(dir, actor, fin)
	}
	return opStartBackendGo(dir, actor, fin)
}

// probeJavaVer 独立成变量只为测试注入；生产恒为 javaVerOf。
var probeJavaVer = javaVerOf

// javaForProject 选一个能跑当前 Maven 项目的 JDK。
//
// 真实故障（2026-11-23）：JAVA_HOME 指着 JDK8，项目要求 Java 17，PATH 里没有
// java；检测阶段提示“会失败”，启动阶段仍然拿 JAVA_HOME 去跑，最后 Maven 只报
// UnsupportedClassVersionError，用户看不出是 JDK 选错。这里在启动前做一次
// “当前 JDK 不够 → 在常见安装位置找兼容 JDK”的选择，返回实际要用的 java.exe。
//
// 只在“已经找到 java、但版本低于项目要求”时扫描安装目录；连 java 都找不到时
// 仍走原来的 missing_dep，不在用户没装 JDK 的机器上乱翻盘。
func javaForProject(dir string) (string, string) {
	cur := currentJavaPath()
	if cur == "" {
		return "", ""
	}
	curVer := probeJavaVer(cur)
	req := mavenRequiredJava(dir)
	if req == "" || curVer == "" || !javaVerLess(curVer, req) {
		return cur, curVer
	}

	bestPath, bestVer := cur, curVer
	for _, p := range javaInstallCandidates() {
		if p == "" || strings.EqualFold(filepath.Clean(p), filepath.Clean(cur)) {
			continue
		}
		v := probeJavaVer(p)
		if v == "" || javaVerLess(v, req) {
			continue
		}
		if bestVer == curVer || javaVerLess(bestVer, v) {
			bestPath, bestVer = p, v
		}
	}
	return bestPath, bestVer
}

func currentJavaPath() string {
	if p := findExe("java.exe", "java"); p != "" {
		return p
	}
	if jh := strings.TrimSpace(os.Getenv("JAVA_HOME")); jh != "" {
		cand := filepath.Join(jh, "bin", "java.exe")
		if hasFile(cand) {
			return cand
		}
	}
	return ""
}

// javaInstallCandidates 只扫描常见 JDK 安装根的一到两层，不递归全盘。
// 覆盖 Oracle/OpenJDK、Adoptium、Microsoft、Corretto、Zulu、BellSoft，
// EnvKit 自己装的 tools 目录，以及用户级 .jdks / Adoptium 目录。
func javaInstallCandidates() []string {
	roots := []string{}
	addRoot := func(p string) {
		if strings.TrimSpace(p) != "" {
			roots = append(roots, p)
		}
	}
	for _, c := range cfg.Components {
		if strings.EqualFold(c.Name, "JDK") || strings.EqualFold(c.Name, "Java") || strings.EqualFold(c.Name, "Java Runtime") {
			addRoot(componentBinDir(c))
		}
	}
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		addRoot(filepath.Join(pf, "Java"))
		addRoot(filepath.Join(pf, "Eclipse Adoptium"))
		addRoot(filepath.Join(pf, "Microsoft"))
		addRoot(filepath.Join(pf, "Amazon Corretto"))
		addRoot(filepath.Join(pf, "Zulu"))
		addRoot(filepath.Join(pf, "BellSoft"))
	}
	if pf := os.Getenv("ProgramFiles(x86)"); pf != "" {
		addRoot(filepath.Join(pf, "Java"))
	}
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		addRoot(filepath.Join(la, "Programs", "Eclipse Adoptium"))
	}
	if up := os.Getenv("USERPROFILE"); up != "" {
		addRoot(filepath.Join(up, ".jdks"))
	}
	addRoot(cfg.InstallDir)

	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = filepath.Clean(p)
		key := strings.ToLower(p)
		if seen[key] || !hasFile(p) {
			return
		}
		seen[key] = true
		out = append(out, p)
	}
	for _, root := range roots {
		patterns := []string{
			filepath.Join(root, "bin", "java.exe"),
			filepath.Join(root, "*", "bin", "java.exe"),
			filepath.Join(root, "*", "*", "bin", "java.exe"),
		}
		for _, pat := range patterns {
			ms, _ := filepath.Glob(pat)
			for _, m := range ms {
				add(m)
			}
			if len(out) >= 40 {
				break
			}
		}
		if len(out) >= 40 {
			break
		}
	}
	return out
}

// javaVerOf 探测一个 java.exe 的版本。java -version 打在 stderr，
// runVersionCmd 用 CombinedOutput 拿得到（见 envreq.go 同款判据）。
func javaVerOf(javaPath string) string {
	if javaPath == "" || !hasFile(javaPath) {
		return ""
	}
	return extractJavaVersion(runVersionCmd(javaPath, "-version"))
}

// javaHomeOf 从 java.exe 全路径推 JDK home：解析符号链接后取 <home>\bin\java.exe
// 的上两级。javapath（Oracle 的 PATH 转发目录）是符号链接，解出真身后同样成立。
func javaHomeOf(javaPath string) string {
	if javaPath == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(javaPath); err == nil && r != "" {
		javaPath = r
	}
	bin := filepath.Dir(javaPath)
	if !strings.EqualFold(filepath.Base(bin), "bin") {
		return "" // 不是 <home>\bin\java.exe 形态，宁可不切也不猜
	}
	return filepath.Dir(bin)
}

// mavenRequiredJava 读后端目录 pom.xml 声明的 java.version。
// 与符合性检查同一口径（pomJavaDeclared），画像/检查/启动读出同一个值。
func mavenRequiredJava(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "pom.xml"))
	if err != nil {
		return ""
	}
	return pomJavaDeclared(string(b))
}

// mavenStartEnv 决定 Maven 启动要不要改写 JAVA_HOME（只作用于本次启动的子进程）。
//
// Maven 用 JAVA_HOME 选 JDK，而 java -version 探测走 PATH——两者指向不同
// JDK 时会出现"检测说 17 够用、一跑编译却报 release 17 不支持"的分裂。
// 只有"JAVA_HOME 的版本 < 项目要求 且 PATH 的 java 满足要求"才切：
// 能不切就不切，切就只切子进程环境，用户的系统变量一个字节不动。
func mavenStartEnv(dir, javaPath string) map[string]string {
	req := mavenRequiredJava(dir)
	if req == "" || javaPath == "" {
		return nil
	}
	jh := strings.TrimSpace(os.Getenv("JAVA_HOME"))
	if jh == "" {
		return nil
	}
	jhVer := probeJavaVer(filepath.Join(jh, "bin", "java.exe"))
	if jhVer == "" {
		// JAVA_HOME 里探测不到 java：不乱动，让 Maven 自己把错报出来
		return nil
	}
	pathVer := probeJavaVer(javaPath)
	if pathVer == "" || !javaVerLess(jhVer, req) || javaVerLess(pathVer, req) {
		return nil
	}
	home := javaHomeOf(javaPath)
	if home == "" {
		return nil
	}
	return map[string]string{"JAVA_HOME": home}
}

// opStartBackendMaven Maven 后端执行体：JDK 前置 → mvn 编译快速失败 → spring-boot:run。
//
// 与 go 路径同构：build 阶段 streamed（编译错误当场报，不拖进后台日志），
// run 阶段 background（进程树进 Win Job Object，stop/restart 能整棵杀）。
// 端口由复验层探（内置候选表含 8080，Spring Boot 默认端口直接命中）。
func opStartBackendMaven(dir, actor string, fin func(result, detail string)) OpResult {
	// JDK 前置：优先当前 PATH / JAVA_HOME；版本不够时在常见安装位置找一个
	// 能满足 pom.xml 要求的 JDK，只作用于本次启动的 JAVA_HOME。
	javaPath, javaVer := javaForProject(dir)
	if javaPath == "" {
		setProg("backend-start", false, false, "未找到 JDK")
		failS(scStart, "backend", "后端", "未找到 JDK（java）")
		fin(resFail, "未找到 JDK")
		return opFail("start_service", "backend", errKindMissingDep,
			"未找到 JDK：Maven 项目需要 JDK 才能编译运行，请安装 JDK 并配置 PATH 或 JAVA_HOME",
			"dir="+dir, "need=java")
	}
	if req := mavenRequiredJava(dir); req != "" && javaVer != "" && javaVerLess(javaVer, req) {
		setProg("backend-start", false, false, "JDK 版本不足")
		failS(scStart, "backend", "后端", "项目要求 Java %s，当前只找到 %s", req, javaVer)
		fin(resFail, "JDK 版本不足")
		return opFail("start_service", "backend", errKindMissingDep,
			fmt.Sprintf("项目要求 Java %s，当前只找到 JDK %s；请安装 Java %s 或在 JAVA_HOME 指向兼容 JDK", req, javaVer, req),
			"dir="+dir, "need=java "+req, "found=java "+javaVer)
	}
	if cur := currentJavaPath(); cur != "" && !strings.EqualFold(filepath.Clean(cur), filepath.Clean(javaPath)) {
		infoS(scStart, "backend", "后端", "项目要求 Java %s，本次使用兼容 JDK：%s", mavenRequiredJava(dir), javaPath)
	}
	// Maven：项目自带 mvnw.cmd 优先（不依赖全局安装，CI/换机都不炸），
	// 其次 PATH/组件 bin，最后 mvnw 发行版缓存——"没装 Maven"的机器上
	// 常有一套历史上 wrapper 下载的完整发行版（同一口径：检测也是这么判的）。
	mvnPath := resolveMavenCmd(dir)
	switch {
	case mvnPath == "":
		setProg("backend-start", false, false, "未找到 Maven")
		failS(scStart, "backend", "后端", "未找到 Maven（mvn）")
		fin(resFail, "未找到 Maven")
		return opFail("start_service", "backend", errKindMissingDep,
			"未找到 Maven：请安装 Maven 并加入 PATH，或在后端目录提供 mvnw.cmd",
			"dir="+dir, "need=maven")
	case mvnPath == filepath.Join(dir, "mvnw.cmd"):
		infoS(scStart, "backend", "后端", "使用项目自带 Maven 包装器 mvnw.cmd")
	case strings.Contains(strings.ToLower(mvnPath), filepath.Join(".m2", "wrapper", "dists")):
		infoS(scStart, "backend", "后端", "使用 mvnw 发行版缓存：%s", mvnPath)
	}
	// JAVA_HOME 错配修正：Maven 用 JAVA_HOME 里的 JDK 编译运行，而 java -version
	// 探测走 PATH——JAVA_HOME 指着 JDK8、项目要求 17 时，直接跑必然
	// "release version 17 not supported"。此时若 PATH 的 java 满足要求，
	// 就只对本次启动把 JAVA_HOME 切到 PATH java 的安装目录（不动用户环境变量）。
	envExtra := mavenStartEnv(dir, javaPath)
	infoS(scStart, "backend", "后端", "Maven 编译 @ %s", dir)
	// -q 只压 info 日志，编译错误照常输出；首次跑会拉依赖，耗时与 npm install 同级。
	if err := execStreamed(scStart, "backend", "后端", dir, envExtra, mvnPath, "-q", "compile"); err != nil {
		setProg("backend-start", false, false, err.Error())
		failS(scStart, "backend", "后端", "Maven 编译失败：%v", err)
		fin(resFail, err.Error())
		return opFail("start_service", "backend", errKindBuildFail,
			"Maven 编译失败："+firstLines(err.Error(), 300),
			"dir="+dir, "hint=查看「程序启动」日志中的编译错误；首次运行需下载依赖，失败也可能是网络问题")
	}
	infoS(scStart, "backend", "后端", "mvn spring-boot:run")
	if err := execBackground(scStart, "backend", "backend-start", "后端", dir, envExtra, mvnPath, "spring-boot:run"); err != nil {
		setProg("backend-start", false, false, err.Error())
		failS(scStart, "backend", "后端", "mvn spring-boot:run 失败：%v", err)
		fin(resFail, err.Error())
		return opFail("start_service", "backend", errKindSpawnFail,
			"Maven 启动进程派生失败："+firstLines(err.Error(), 200),
			"dir="+dir, "hint=先看「启动」日志里 Maven 自己打印的报错；确无任何输出时才考虑安全软件拦截")
	}
	okS(scStart, "backend", "后端", "已启动（mvn spring-boot:run）")
	fin(resOK, "backend")
	return opOK("start_service", "backend", "后端进程已派生（Maven 编译通过）", "dir="+dir)
}

func opStartBackendGo(dir, actor string, fin func(result, detail string)) OpResult {
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
			"dir="+dir, "entry="+entry, "hint=先看「启动」日志里 go 自己打印的报错；确无任何输出时才考虑安全软件拦截")
	}
	okS(scStart, "backend", "后端", "已启动（go run main.go）")
	fin(resOK, "backend")
	// 与前端同理：此处只证明"构建通过且进程已派生"，端口/HTTP 可用性复验见 v2.0 P2。
	// "backend-start" 标志同理由 verifyService 复位（详见 opStartWeb 同处注释）。
	return opOK("start_service", "backend", "后端进程已派生（go build 通过）", "dir="+dir)
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
	// 停止权限边界：只停 EnvKit 自己派生的 activeProcs。
	// 外部实例（source=external）不归我们管，接口也不能假装"已停止"。
	var external []string
	var problems []string
	noteExternal := func(key string) {
		if managedServiceRunning(key) {
			return
		}
		if o, ok := observeSnapshot(key); ok && (o.State == "external" || o.State == "unknown") {
			external = append(external, fmt.Sprintf("%s(PID %d, 端口 %d)", svcLabel(key), o.PID, o.Port))
		}
	}
	stopAndWait := func(key string, managed bool) {
		stopByKey(scStart, key, key+"-start")
		if managed {
			svcReset(key)
		}
		// 与 AI 工具同一判据：父进程退出不等于端口释放；npm 的 Vite 子进程
		// 可能变成孤儿继续占着 5173，必须等端口真空出来再报"已停止"。
		if why := restartWaitPorts(key); why != "" {
			problems = append(problems, fmt.Sprintf("%s：%s", svcLabel(key), why))
		}
	}
	switch body.Key {
	case "web":
		managed := managedServiceRunning("web")
		noteExternal("web")
		stopAndWait("web", managed)
		if len(problems) > 0 {
			auditNow(actUser, "stop_service", "web", "", resFail, strings.Join(problems, "；"))
		} else {
			auditNow(actUser, "stop_service", "web", "", resOK, "")
		}
	case "backend":
		managed := managedServiceRunning("backend")
		noteExternal("backend")
		stopAndWait("backend", managed)
		if len(problems) > 0 {
			auditNow(actUser, "stop_service", "backend", "", resFail, strings.Join(problems, "；"))
		} else {
			auditNow(actUser, "stop_service", "backend", "", resOK, "")
		}
	default:
		managedWeb := managedServiceRunning("web")
		managedBackend := managedServiceRunning("backend")
		noteExternal("web")
		noteExternal("backend")
		stopAndWait("web", managedWeb)
		stopAndWait("backend", managedBackend)
		if len(problems) > 0 {
			auditNow(actUser, "stop_service", "all", "", resFail, strings.Join(problems, "；"))
		} else {
			auditNow(actUser, "stop_service", "all", "", resOK, "")
		}
	}
	kickPortObserver()
	msg := ""
	if len(problems) > 0 {
		msg = "未完全停止：" + strings.Join(problems, "；")
	}
	if len(external) > 0 {
		if msg != "" {
			msg += "；"
		}
		msg += "EnvKit 只停止了由自己启动的服务；以下外部实例未受管，未停止：" + strings.Join(external, "、")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":       len(problems) == 0,
		"external": external,
		"problems": problems,
		"msg":      msg,
	})
}

func managedServiceRunning(target string) bool {
	svcMu.Lock()
	defer svcMu.Unlock()
	return svcState[target] != nil && svcState[target].Running
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
			// 只退出助手、服务继续跑：把 Job 句柄复制进仍在运行的子进程，
			// 防止 EnvKit 退出关闭最后一个句柄后 KILL_ON_JOB_CLOSE 强杀服务树。
			platform.KeepJobAlive(func() int {
				procMu.Lock()
				defer procMu.Unlock()
				for _, c := range activeProcs {
					if c.Process != nil {
						return c.Process.Pid
					}
				}
				return 0
			})
		}
		os.Exit(0)
	}()
}
