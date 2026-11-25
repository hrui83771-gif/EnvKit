// dbbrowse.go —— 数据浏览：库/表/行数据/自定义 SQL，走 Go MySQL 驱动。
// 默认只读；单格修改、写 SQL、删库是三条独立受控路径，删库还必须输入口令并先备份。
// 安全原则：服务端强制只读（白名单动词 + 单语句扫描 + 会话级 READ ONLY 兜底），不信任前端
package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 用户明确要求的删除库口令。只在服务端比较，不写进前端页面。
const dbDropPassword = "123456"

var dbSystemSchemas = map[string]bool{
	"mysql": true, "information_schema": true, "performance_schema": true, "sys": true,
}

func dbEnsureNotSystem(name string) error {
	if dbSystemSchemas[strings.ToLower(strings.TrimSpace(name))] {
		return fmt.Errorf("系统库 %s 不允许在这里删除", name)
	}
	return nil
}

// ---------- 基础：连接参数与标识符 ----------

// 反引号安全转义；拒绝可能引入注入面的字符
func quoteIdent(s string) (string, error) {
	if s == "" || len(s) > 64 || strings.ContainsAny(s, "`;\x00\r\n\\") {
		return "", fmt.Errorf("非法标识符：%q", s)
	}
	return "`" + s + "`", nil
}

func quoteStr(s string) (string, error) {
	if strings.ContainsAny(s, "\x00\r\n") {
		return "", fmt.Errorf("非法字符串")
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'", nil
}

// ---------- 只读 SQL 守卫：引号/注释感知扫描，找出顶层分号与顶层 LIMIT ----------

type sqlScan struct {
	semi     []int // 语句分隔分号（引号/注释之外）
	topLimit bool  // 顶层出现 LIMIT 关键词
}

func scanSQL(s string) sqlScan {
	var sc sqlScan
	n := len(s)
	for i := 0; i < n; i++ {
		c := s[i]
		switch {
		case c == '\'' || c == '"' || c == '`': // 字符串/标识符：跳到闭合
			q := c
			i++
			for ; i < n; i++ {
				if s[i] == '\\' && q != '`' {
					i++
				} else if s[i] == q {
					if q != '`' && i+1 < n && s[i+1] == q { // '' 转义
						i++
					} else {
						break
					}
				}
			}
		case c == '#' || (c == '-' && i+1 < n && s[i+1] == '-'): // 行注释
			for ; i < n && s[i] != '\n'; i++ {
			}
		case c == '/' && i+1 < n && s[i+1] == '*': // 块注释
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				i = n
			} else {
				i += end + 3
			}
		case c == ';':
			sc.semi = append(sc.semi, i)
		default:
			// 关键词识别：取一个词
			if isWordStart(c) {
				j := i
				for j < n && isWordChar(s[j]) {
					j++
				}
				w := strings.ToLower(s[i:j])
				if w == "limit" {
					sc.topLimit = true
				}
				i = j - 1
			}
		}
	}
	return sc
}

func isWordStart(c byte) bool { return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isWordChar(c byte) bool  { return isWordStart(c) || (c >= '0' && c <= '9') }

// sqlTopWords 取 SQL 顶层的首个/前几个关键字，跳过注释和字符串。写守卫靠它，
// 不能拿 strings.Fields 代替：DROP /*x*/ DATABASE 会被拆错。
func sqlTopWords(s string, max int) []string {
	out := []string{}
	n := len(s)
	for i := 0; i < n && (max <= 0 || len(out) < max); i++ {
		c := s[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			q := c
			i++
			for ; i < n; i++ {
				if s[i] == '\\' && q != '`' {
					i++
				} else if s[i] == q {
					if q != '`' && i+1 < n && s[i+1] == q {
						i++
					} else {
						break
					}
				}
			}
		case c == '#' || (c == '-' && i+1 < n && s[i+1] == '-'):
			for ; i < n && s[i] != '\n'; i++ {
			}
		case c == '/' && i+1 < n && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				i = n
			} else {
				i += end + 3
			}
		case isWordStart(c):
			j := i
			for j < n && isWordChar(s[j]) {
				j++
			}
			out = append(out, strings.ToLower(s[i:j]))
			i = j - 1
		}
	}
	return out
}

func dbOneStatement(q string) (string, error) {
	s := strings.TrimSpace(q)
	if s == "" {
		return "", fmt.Errorf("SQL 为空")
	}
	sc := scanSQL(s)
	if len(sc.semi) > 0 {
		if len(sc.semi) != 1 || strings.TrimSpace(s[sc.semi[0]+1:]) != "" {
			return "", fmt.Errorf("仅允许执行一条语句")
		}
		s = strings.TrimSpace(s[:sc.semi[0]])
	}
	if s == "" {
		return "", fmt.Errorf("SQL 为空")
	}
	return s, nil
}

func dbReadVerb(w string) bool {
	switch w {
	case "select", "show", "desc", "describe", "explain", "with", "table", "values":
		return true
	}
	return false
}

// dbWritableSQL 只放行明确的 DML/表级 DDL。DROP DATABASE 必须走带密码的专用入口，
// 否则自定义 SQL 就成了绕过口令的后门。
func dbWritableSQL(q string) (string, error) {
	s, err := dbOneStatement(q)
	if err != nil {
		return "", err
	}
	words := sqlTopWords(s, 3)
	if len(words) == 0 {
		return "", fmt.Errorf("SQL 为空")
	}
	second := ""
	if len(words) > 1 {
		second = words[1]
	}
	switch words[0] {
	case "insert", "replace", "update", "delete", "truncate", "rename":
		return s, nil
	case "create":
		if second != "table" && second != "view" && second != "index" && second != "unique" &&
			second != "procedure" && second != "function" && second != "trigger" && second != "event" {
			return "", fmt.Errorf("自定义 SQL 只允许建表/视图/索引/存储过程等表级对象")
		}
		return s, nil
	case "alter":
		if second != "table" && second != "view" {
			return "", fmt.Errorf("自定义 SQL 只允许修改表或视图")
		}
		return s, nil
	case "drop":
		if second == "database" || second == "schema" {
			return "", fmt.Errorf("删除数据库请使用「删除数据库」按钮并输入密码")
		}
		if second != "table" && second != "view" && second != "index" && second != "procedure" &&
			second != "function" && second != "trigger" && second != "event" && second != "temporary" {
			return "", fmt.Errorf("自定义 SQL 只允许删除表级对象")
		}
		return s, nil
	}
	return "", fmt.Errorf("写操作仅允许 INSERT/UPDATE/DELETE/REPLACE/TRUNCATE/CREATE/ALTER/DROP 表级对象")
}

// dbReadOnlySQL 校验并规整用户输入：只允许单条只读语句；无顶层 LIMIT 自动补 1000
func dbReadOnlySQL(q string) (string, error) {
	s := strings.TrimSpace(q)
	if s == "" {
		return "", fmt.Errorf("SQL 为空")
	}
	sc := scanSQL(s)
	if len(sc.semi) > 0 {
		// 允许唯一的结尾分号
		if len(sc.semi) != 1 || strings.TrimSpace(s[sc.semi[0]+1:]) != "" {
			return "", fmt.Errorf("仅允许执行一条语句")
		}
		s = strings.TrimSpace(s[:sc.semi[0]])
	}
	// 剥掉前导注释后看第一个词
	body := s
	for { // 剥掉前导注释，让首词判定看到真实动词
		body = strings.TrimSpace(body)
		if strings.HasPrefix(body, "/*") {
			end := strings.Index(body, "*/")
			if end < 0 {
				return "", fmt.Errorf("注释未闭合")
			}
			body = body[end+2:]
		} else if strings.HasPrefix(body, "--") || strings.HasPrefix(body, "#") {
			idx := strings.IndexByte(body, '\n')
			if idx < 0 {
				return "", fmt.Errorf("仅允许执行一条语句")
			}
			body = body[idx+1:]
		} else {
			break
		}
	}
	first := ""
	for _, w := range strings.FieldsFunc(body, func(r rune) bool { return !isWordChar(byte(r)) }) {
		if w != "" {
			first = strings.ToLower(w)
			break
		}
	}
	switch first {
	case "select", "show", "desc", "describe", "explain", "with", "table":
	default:
		return "", fmt.Errorf("仅允许只读查询（SELECT / SHOW / DESC / EXPLAIN / WITH）")
	}
	if !sc.topLimit && first != "show" && first != "desc" && first != "describe" {
		s += " LIMIT 1000"
	}
	return s, nil
}

// ---------- 执行 ----------

// readOnlyExec 在会话级 READ ONLY 下执行（就算守卫被绕过，写也会被服务端拒绝）
func readOnlyExec(db, sql string) (cols []string, rows [][]string, err error) {
	p, err := dbConnParams()
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := cmdContext(60 * time.Second)
	defer cancel()
	return mysqlReadOnlyQuery(ctx, p, db, sql)
}

// ---------- Handlers ----------

func handleDBList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	_, rows, err := readOnlyExec("", "SHOW DATABASES")
	if err != nil {
		http.Error(w, "查询数据库列表失败："+err.Error(), 400)
		return
	}
	dbs := make([]string, 0, len(rows))
	for _, rw := range rows {
		if len(rw) > 0 {
			dbs = append(dbs, rw[0])
		}
	}
	writeJSON(w, map[string]any{"databases": dbs, "current": strings.TrimSpace(cfgSnapshot().Projects.DBName)})
}

func handleDBTables(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	db := r.URL.Query().Get("db")
	dq, err := quoteIdent(db)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// 表清单
	dbs, err := quoteStr(db)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	_, rows, err := readOnlyExec("", "SELECT table_name FROM information_schema.tables WHERE table_schema="+dbs+" AND table_type='BASE TABLE' ORDER BY table_name")
	if err != nil {
		http.Error(w, "查询表列表失败："+err.Error(), 400)
		return
	}
	tables := make([]string, 0, len(rows))
	for _, rw := range rows {
		if len(rw) > 0 {
			tables = append(tables, rw[0])
		}
	}
	// 真实行数：拼一条 UNION ALL，一次进程拿全（用户确认用真实 COUNT，不用近似值）
	counts := map[string]any{}
	if len(tables) > 0 {
		parts := make([]string, 0, len(tables))
		for _, t := range tables {
			tq, e := quoteIdent(t)
			if e != nil {
				continue
			}
			parts = append(parts, "SELECT "+mustJSON(t)+" AS _t, COUNT(*) AS _c FROM "+dq+"."+tq)
		}
		if len(parts) > 0 {
			_, crows, e := readOnlyExec("", strings.Join(parts, " UNION ALL "))
			if e == nil {
				for _, rw := range crows {
					if len(rw) >= 2 {
						counts[rw[0]] = rw[1]
					}
				}
			}
		}
	}
	writeJSON(w, map[string]any{"tables": tables, "counts": counts})
}

func handleDBRows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	q := r.URL.Query()
	db, table := q.Get("db"), q.Get("table")
	dq, err := quoteIdent(db)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	tq, err := quoteIdent(table)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	size, _ := strconv.Atoi(q.Get("size"))
	if size <= 0 || size > 200 {
		size = 50
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	off := (page - 1) * size

	// 真实行数
	_, crows, err := readOnlyExec(db, "SELECT COUNT(*) FROM "+dq+"."+tq)
	if err != nil {
		http.Error(w, "统计行数失败："+err.Error(), 400)
		return
	}
	total := ""
	if len(crows) == 1 && len(crows[0]) == 1 {
		total = crows[0][0]
	}
	// 主键（稳定排序 + 单行编辑定位）；复合主键按 ordinal_position 全部取回
	pkNames := []string{}
	pkQuoted := []string{}
	dbs2, e1 := quoteStr(db)
	tbs2, e2 := quoteStr(table)
	if e1 == nil && e2 == nil {
		_, krows, err := readOnlyExec("", "SELECT column_name FROM information_schema.key_column_usage WHERE table_schema="+dbs2+" AND table_name="+tbs2+" AND constraint_name='PRIMARY' ORDER BY ordinal_position")
		if err == nil {
			for _, rw := range krows {
				if len(rw) == 0 || rw[0] == "" {
					continue
				}
				pkq, e := quoteIdent(rw[0])
				if e != nil {
					continue
				}
				pkNames = append(pkNames, rw[0])
				pkQuoted = append(pkQuoted, pkq)
			}
		}
	}
	order := ""
	if len(pkQuoted) > 0 {
		order = " ORDER BY " + strings.Join(pkQuoted, ", ")
	}
	cols, rows, err := readOnlyExec(db, fmt.Sprintf("SELECT * FROM %s.%s%s LIMIT %d OFFSET %d", dq, tq, order, size, off))
	if err != nil {
		http.Error(w, "查询数据失败："+err.Error(), 400)
		return
	}
	writeJSON(w, map[string]any{"cols": cols, "rows": rows, "total": total, "page": page, "size": size, "pk": pkNames})
}

type dbQueryReq struct {
	DB  string `json:"db"`
	SQL string `json:"sql"`
}

func handleDBQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body dbQueryReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.SQL) == "" {
		http.Error(w, "请提供 sql", 400)
		return
	}
	sqlText, err := dbReadOnlySQL(body.SQL)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	cols, rows, err := readOnlyExec(body.DB, sqlText)
	auditNow(actUser, "db_query", body.DB, runeSnippet(sqlText, 120), resOK, fmt.Sprintf("%d 行", len(rows)))
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"cols": cols, "rows": rows, "sql": sqlText})
}

type dbExecReq struct {
	DB      string `json:"db"`
	SQL     string `json:"sql"`
	Write   bool   `json:"write"`
	Confirm bool   `json:"confirm"`
}

func handleDBExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body dbExecReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.SQL) == "" {
		writeJSON(w, map[string]any{"error": "请提供 sql"})
		return
	}
	s, err := dbOneStatement(body.SQL)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	words := sqlTopWords(s, 2)
	first := ""
	if len(words) > 0 {
		first = words[0]
	}
	if dbReadVerb(first) {
		sqlText, err := dbReadOnlySQL(body.SQL)
		if err != nil {
			writeJSON(w, map[string]any{"error": err.Error()})
			return
		}
		cols, rows, err := readOnlyExec(body.DB, sqlText)
		auditNow(actUser, "db_query", body.DB, runeSnippet(sqlText, 120), map[bool]string{true: resOK, false: resFail}[err == nil], fmt.Sprintf("%d 行", len(rows)))
		if err != nil {
			writeJSON(w, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "kind": "read", "cols": cols, "rows": rows, "sql": sqlText})
		return
	}
	if !body.Write {
		writeJSON(w, map[string]any{"error": "写操作需要勾选「允许写操作」"})
		return
	}
	sqlText, err := dbWritableSQL(body.SQL)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	verdict := PolicyGate("db_exec_write", body.DB, actUser)
	if !verdict.Allowed() {
		policyDeny(w, "db_exec_write", body.DB, verdict.Reason)
		return
	}
	if verdict.NeedsConfirm() && !body.Confirm {
		needConfirm(w, "db_exec_write", body.DB, "将执行自定义写 SQL："+runeSnippet(sqlText, 120), true)
		return
	}
	p, err := dbConnParams()
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	affected, err := mysqlExecAffected(p, body.DB, sqlText)
	auditNow(actUser, "db_exec", body.DB, runeSnippet(sqlText, 120), map[bool]string{true: resOK, false: resFail}[err == nil], fmt.Sprintf("affected=%d", affected))
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "kind": "write", "affected": affected, "sql": sqlText})
}

type dbCellUpdateReq struct {
	DB     string      `json:"db"`
	Table  string      `json:"table"`
	Column string      `json:"column"`
	Value  *string     `json:"value"`
	PK     []dbKeyPart `json:"pk"`
}

type dbKeyPart struct {
	Column string  `json:"column"`
	Value  *string `json:"value"`
}

func buildCellUpdateSQL(db, table, column string, pk []dbKeyPart) (string, error) {
	dq, err := quoteIdent(db)
	if err != nil {
		return "", err
	}
	tq, err := quoteIdent(table)
	if err != nil {
		return "", err
	}
	cq, err := quoteIdent(column)
	if err != nil {
		return "", err
	}
	if len(pk) == 0 {
		return "", fmt.Errorf("没有主键，不能定位要修改的行")
	}
	where := make([]string, 0, len(pk))
	for _, k := range pk {
		if k.Value == nil {
			return "", fmt.Errorf("主键 %s 为空，不能定位要修改的行", k.Column)
		}
		kq, err := quoteIdent(k.Column)
		if err != nil {
			return "", err
		}
		if k.Column == column {
			return "", fmt.Errorf("主键列不允许直接编辑")
		}
		where = append(where, kq+"=?")
	}
	return fmt.Sprintf("UPDATE %s.%s SET %s=? WHERE %s LIMIT 1", dq, tq, cq, strings.Join(where, " AND ")), nil
}

func handleDBUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body dbCellUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]any{"error": "请求解析失败"})
		return
	}
	sqlText, err := buildCellUpdateSQL(body.DB, body.Table, body.Column, body.PK)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	p, err := dbConnParams()
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	args := []any{dbNullable(body.Value)}
	for _, k := range body.PK {
		args = append(args, *k.Value)
	}
	affected, err := mysqlExecArgs(p, body.DB, sqlText, args...)
	auditNow(actUser, "db_cell_update", body.DB+"."+body.Table+"."+body.Column, fmt.Sprintf("%v", body.Value), map[bool]string{true: resOK, false: resFail}[err == nil], fmt.Sprintf("affected=%d", affected))
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "affected": affected})
}

func dbNullable(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

type dbDropReq struct {
	DB       string `json:"db"`
	Password string `json:"password"`
	Confirm  bool   `json:"confirm"`
}

func handleDBDrop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body dbDropReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]any{"error": "请求解析失败"})
		return
	}
	db := strings.TrimSpace(body.DB)
	dq, err := quoteIdent(db)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	if err := dbEnsureNotSystem(db); err != nil {
		writeJSONStatus(w, 403, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Password), []byte(dbDropPassword)) != 1 {
		auditNow(actUser, "db_drop", db, "口令错误", resDenied, "")
		writeJSONStatus(w, 403, map[string]any{"ok": false, "error": "删除口令不正确"})
		return
	}
	if !body.Confirm {
		needConfirm(w, "db_drop", db, "将永久删除数据库 "+db+"，删除前会自动备份一份 SQL", true)
		return
	}
	dbq, err := quoteStr(db)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	_, rows, err := readOnlyExec("", "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME="+dbq)
	if err != nil {
		writeJSON(w, map[string]any{"error": "检查数据库是否存在失败：" + err.Error()})
		return
	}
	if len(rows) == 0 {
		writeJSONStatus(w, 404, map[string]any{"ok": false, "error": "数据库不存在：" + db})
		return
	}
	p, err := dbConnParams()
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	if err := os.MkdirAll(backupDir(), 0755); err != nil {
		writeJSON(w, map[string]any{"error": "创建备份目录失败：" + err.Error()})
		return
	}
	stem := strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(db)
	backup := filepath.Join(backupDir(), fmt.Sprintf("%s-drop-%s.sql", stem, time.Now().Format("20060102-150405")))
	size, sha, err := dumpDatabaseTo(p, db, backup)
	if err != nil {
		auditNow(actUser, "db_drop", db, "备份失败", resFail, err.Error())
		writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "删除前备份失败，已中止：" + err.Error()})
		return
	}
	if err := mysqlExec(p, "", "DROP DATABASE "+dq); err != nil {
		auditNow(actUser, "db_drop", db, "DROP 失败", resFail, err.Error())
		writeJSON(w, map[string]any{"ok": false, "error": "删除数据库失败：" + err.Error(), "backup": backup})
		return
	}
	auditNow(actUser, "db_drop", db, "已删除", resOK, backup)
	writeJSON(w, map[string]any{"ok": true, "db": db, "backup": backup, "size": size, "sha256": sha})
}

func runeSnippet(s string, n int) string {
	r := []rune(strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\t", " "))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
