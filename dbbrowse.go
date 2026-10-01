// dbbrowse.go —— 数据浏览（只读）：库/表/行数据/自定义 SQL，走 mysql.exe CLI，零驱动依赖
// 安全原则：服务端强制只读（白名单动词 + 单语句扫描 + 会话级 READ ONLY 兜底），不信任前端
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// ---------- 基础：连接参数与标识符 ----------

func dbBrowseBase() ([]string, error) {
	p := cfg.Projects
	if p.MySQLHost == "" || p.MySQLUser == "" || p.MySQLPort == 0 {
		return nil, fmt.Errorf("MySQL 连接未配置（请在上方填好连接信息）")
	}
	return []string{
		"-h", p.MySQLHost,
		"-P", strconv.Itoa(p.MySQLPort),
		"-u", p.MySQLUser,
		"--default-character-set=utf8mb4",
	}, nil
}

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

// ---------- TSV 解析（--batch 模式：\t \n \r \0 \b \Z \\ 被转义，逐物理行=逐逻辑行） ----------

func splitTSV(line string) []string {
	cells := []string{}
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == '\\' && i+1 < len(line) {
			i++
			switch line[i] {
			case 't':
				cur.WriteByte('\t')
			case 'n':
				cur.WriteByte('\n')
			case 'r':
				cur.WriteByte('\r')
			case '0':
				cur.WriteByte(0)
			case 'b':
				cur.WriteByte(8)
			case 'Z':
				cur.WriteByte(26)
			case '\\':
				cur.WriteByte('\\')
			default:
				cur.WriteByte('\\')
				cur.WriteByte(line[i])
			}
		} else if c == '\t' {
			cells = append(cells, cur.String())
			cur.Reset()
		} else {
			cur.WriteByte(c)
		}
	}
	return append(cells, cur.String())
}

func parseTSV(out string) (cols []string, rows [][]string) {
	for i, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSuffix(ln, "\r")
		if ln == "" && i == strings.Count(out, "\n") {
			continue
		}
		if cols == nil {
			cols = splitTSV(ln)
		} else {
			rows = append(rows, splitTSV(ln))
		}
	}
	if cols == nil {
		cols = []string{}
	}
	return cols, rows
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
	base, err := dbBrowseBase()
	if err != nil {
		return nil, nil, err
	}
	args := append(base, "-B", "-e", "SET SESSION TRANSACTION READ ONLY; "+sql)
	if db != "" {
		dq, e := quoteIdent(db)
		if e != nil {
			return nil, nil, e
		}
		args = append(args, "-D", strings.Trim(dq, "`"))
	}
	out, err := runMysqlOut(args...)
	if err != nil {
		// runMysqlOut 出错时真实报错文本在 out 里（return msg, lastErr 的约定），拼进错误
		if s := strings.TrimSpace(out); s != "" {
			return nil, nil, fmt.Errorf("%s", s)
		}
		return nil, nil, err
	}
	cols, rows = parseTSV(out)
	return cols, rows, nil
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
	writeJSON(w, map[string]any{"databases": dbs})
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
	// 主键（稳定排序）
	pk := ""
	dbs2, e1 := quoteStr(db)
	tbs2, e2 := quoteStr(table)
	if e1 == nil && e2 == nil {
		_, krows, err := readOnlyExec("", "SELECT column_name FROM information_schema.key_column_usage WHERE table_schema="+dbs2+" AND table_name="+tbs2+" AND constraint_name='PRIMARY' ORDER BY ordinal_position")
		if err == nil && len(krows) > 0 && len(krows[0]) > 0 {
			pkq, e := quoteIdent(krows[0][0])
			if e == nil {
				pk = pkq
			}
		}
	}
	order := ""
	if pk != "" {
		order = " ORDER BY " + pk
	}
	cols, rows, err := readOnlyExec(db, fmt.Sprintf("SELECT * FROM %s.%s%s LIMIT %d OFFSET %d", dq, tq, order, size, off))
	if err != nil {
		http.Error(w, "查询数据失败："+err.Error(), 400)
		return
	}
	writeJSON(w, map[string]any{"cols": cols, "rows": rows, "total": total, "page": page, "size": size})
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
