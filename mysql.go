// mysql.go —— MySQL 能力封装：驱动做连接、只读查询与建库；CLI 保留给 SQL 导入与备份/恢复。
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------- 程序配置：MySQL 建库 / 生效 SQL ----------

// mysqlClient 依次尝试：EnvKit 组件目录 / PATH → Windows 服务 ImagePath →
// 常见安装目录。只靠前两者会漏掉"用户自己装的 MySQL"：本机 MySQL80 服务在跑、
// 客户端在 C:\Program Files\MySQL\...，但两个位置都不在 PATH，AI 的 db_check
// 就会报"未找到 mysql 客户端"，模型据此得出"MySQL 完全没有安装"的错结论。
func mysqlClient() string {
	if exe := findExe("mysql.exe", "mysql"); exe != "" {
		return exe
	}
	return mysqlClientFromInstall()
}

// mysqlClientFromInstall 从服务注册表与常见目录里找回 mysql.exe。
func mysqlClientFromInstall() string {
	if p := mysqlServiceExe(); p != "" {
		cli := filepath.Join(filepath.Dir(p), "mysql.exe")
		if hasFile(cli) {
			return cli
		}
	}
	for _, root := range []string{
		`C:\Program Files\MySQL`,
		`C:\Program Files (x86)\MySQL`,
		`D:\Program Files\MySQL`,
		`D:\MySQL`,
	} {
		matches, _ := filepath.Glob(filepath.Join(root, "MySQL Server *", "bin", "mysql.exe"))
		if len(matches) > 0 {
			// 多版本并存时取路径里版本号最大的那个；Glob 结果有序，倒序取第一个。
			return matches[len(matches)-1]
		}
	}
	return ""
}

var mysqlSvcPathRe = regexp.MustCompile(`(?i)"([^"]*mysqld\.exe)"`)
var mysqlSvcBareRe = regexp.MustCompile(`(?i)([A-Z]:\\[^\r\n"]*?mysqld\.exe)`)

// mysqlServiceExe 从服务 ImagePath 里取 mysqld.exe 的真实路径。
// ImagePath 形如 "C:\Program Files\...\mysqld.exe" --defaults-file=...
func mysqlServiceExe() string {
	name := strings.TrimSpace(cfg.MySQL.ServiceName)
	if name == "" {
		name = "MySQL80"
	}
	out, err := exec.Command("reg", "query",
		`HKLM\SYSTEM\CurrentControlSet\Services\`+name, "/v", "ImagePath").Output()
	if err != nil {
		return ""
	}
	if p := parseMysqlServiceExe(string(out)); p != "" && hasFile(p) {
		return p
	}
	return ""
}

// parseMysqlServiceExe 从 reg query 的输出里抽 mysqld.exe 路径（供单测直接验证）。
func parseMysqlServiceExe(out string) string {
	if m := mysqlSvcPathRe.FindStringSubmatch(out); len(m) >= 2 {
		return strings.TrimSpace(m[1])
	}
	if m := mysqlSvcBareRe.FindStringSubmatch(out); len(m) >= 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// 用 MYSQL_PWD 传密码，避免命令行警告污染输出（也更安全）
func mysqlEnv() map[string]string {
	return map[string]string{"MYSQL_PWD": cfg.Projects.MySQLPass}
}

// 只取 stdout（stderr 单独丢弃），用于查询类
func runMysqlOut(args ...string) (string, error) {
	exe := mysqlClient()
	if exe == "" {
		return "", fmt.Errorf("未找到 mysql 客户端")
	}
	var (
		out, msg string
		lastErr  error
	)
	// 本机 mysql 子进程偶发"零输出秒退"（杀软行为拦截），重试几次可自愈（见 retry.go）
	// 次数与退避来自设置页（默认 3 次 / 2 秒）。
	attempts, backoff := subprocRetry()
	_, err := runRetry(retryPolicy{
		Attempts: attempts,
		Backoff:  backoff,
		Scope:    scConfig,
		Tag:      "查询",
		Label:    "mysql",
	}, func(wrap bool) (bool, error) {
		ctx, cancel := cmdContext(60 * time.Second)
		defer cancel()
		cmd := buildCmd(ctx, wrap, exe, args...)
		cmd.Env = append(toolEnv(mysqlEnv()), "MYSQL_PWD="+cfg.Projects.MySQLPass)
		var eb, ob bytes.Buffer
		cmd.Stderr = &eb
		cmd.Stdout = &ob
		err := cmd.Run()
		out, msg = ob.String(), strings.TrimSpace(eb.String()+" "+ob.String())
		if err != nil {
			lastErr = fmt.Errorf("%v（exe=%s）", err, exe)
			// hasOut=true 表示拿到了真实报错 → 不再重试，把原始信息原样带回
			return msg != "", fmt.Errorf("%s", msg)
		}
		return true, nil
	})
	if err == nil {
		return out, nil
	}
	if msg != "" {
		return msg, lastErr
	}
	return "", fmt.Errorf("%v；子进程没有任何输出就退出（可能是安全软件拦截，也可能是 mysql 客户端本身起动失败）", lastErr)
}

func createDatabase() error {
	return createDatabaseNamed(cfg.Projects.DBName)
}

func createDatabaseNamed(db string) error {
	if db == "" {
		return fmt.Errorf("数据库名未配置")
	}
	ident, err := quoteIdent(db)
	if err != nil {
		return err
	}
	p, err := dbConnParams()
	if err != nil {
		return err
	}
	sql := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;", ident)
	info(scConfig, "建库", "%s", sql)
	if err := mysqlExec(p, "", sql); err != nil {
		return fmt.Errorf("建库失败：%s", firstLines(err.Error(), 300))
	}
	return nil
}

// ---------- SQL 执行记录：按内容哈希防误重跑 ----------
var sqlHistMu sync.Mutex

type sqlRec struct {
	Path string `json:"path"`
	Time string `json:"time"`
}

func sqlHistoryFile() string { return filepath.Join(exeDir(), "sql_history.json") }

func loadSQLHistory() map[string]sqlRec {
	m := map[string]sqlRec{}
	if b, err := os.ReadFile(sqlHistoryFile()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func saveSQLHistory(m map[string]sqlRec) {
	if b, err := json.MarshalIndent(m, "", "  "); err == nil {
		_ = os.WriteFile(sqlHistoryFile(), b, 0644)
	}
}

func fileSHA256Hex(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func applySQLFile(path, db string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("SQL 文件不存在: %s", path)
	}
	exe := mysqlClient()
	if exe == "" {
		return fmt.Errorf("未找到 mysql 客户端，请先安装 MySQL")
	}
	p := cfg.Projects
	dbName := db
	if dbName == "" {
		dbName = p.DBName
	}
	// 重复执行检查：同内容文件执行过就醒目提示（仍允许继续，由用户确认）
	if hash := fileSHA256Hex(path); hash != "" {
		sqlHistMu.Lock()
		hist := loadSQLHistory()
		prev, done := hist[hash]
		sqlHistMu.Unlock()
		if done {
			warn(scConfig, "SQL", "注意：该 SQL 文件（内容一致）已于 %s 执行过（%s），重复执行可能重复建表/覆盖数据", prev.Time, prev.Path)
		}
	}
	info(scConfig, "SQL", "%s（库 %s）", path, dbName)
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	args := []string{"-h", p.MySQLHost, "-P", strconv.Itoa(p.MySQLPort), "-u", p.MySQLUser}
	if dbName != "" {
		args = append(args, dbName)
	}
	ctxSQL, cancelSQL := cmdContext(10 * time.Minute)
	defer cancelSQL()
	cmd := exec.CommandContext(ctxSQL, exe, args...)
	cmd.Env = append(toolEnv(nil), "MYSQL_PWD="+p.MySQLPass)
	cmd.Stdin = f
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v：%s", err, firstLines(string(out), 800))
	}
	if hash := fileSHA256Hex(path); hash != "" {
		sqlHistMu.Lock()
		hist := loadSQLHistory()
		hist[hash] = sqlRec{Path: path, Time: time.Now().Format("2006-01-02 15:04:05")}
		saveSQLHistory(hist)
		sqlHistMu.Unlock()
	}
	ok(scConfig, "SQL", "已生效")
	return nil
}

// ---------- 数据库连接检测 ----------
func dbTest() (DBHealth, error) {
	p := cfgSnapshot().Projects
	if strings.TrimSpace(p.MySQLHost) == "" || strings.TrimSpace(p.MySQLUser) == "" || p.MySQLPort <= 0 {
		return DBHealth{}, fmt.Errorf("MySQL 连接未配置（请先填好主机、端口和用户名）")
	}
	version, err := readOnlyOne("SELECT VERSION();")
	if err != nil {
		return DBHealth{}, fmt.Errorf("连接失败：%s", firstLines(strings.TrimSpace(err.Error()), 300))
	}
	version = strings.TrimSpace(version)
	if version == "" || version == "NULL" {
		return DBHealth{}, fmt.Errorf("连接成功，但没有拿到 MySQL 版本号")
	}
	h := DBHealth{Connected: true, Version: firstLine(version), Msg: "连接成功"}
	if p.DBName != "" {
		q := fmt.Sprintf("SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='%s';",
			strings.ReplaceAll(p.DBName, "'", "''"))
		n, e := readOnlyOne(q)
		h.DBExists = e == nil && strings.TrimSpace(n) == "1"
	}
	return h, nil
}
