// mysql.go —— MySQL 客户端封装：查询、建库、SQL 文件执行（哈希防重跑）、连接检测
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
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------- 程序配置：MySQL 建库 / 生效 SQL ----------
func mysqlClient() string { return findExe("mysql.exe", "mysql") }

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
	_, err := runRetry(retryPolicy{
		Attempts: 3,
		Backoff:  2 * time.Second,
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
	return "", fmt.Errorf("%v；子进程无任何输出，疑似被杀毒软件拦截（可点顶部「杀软白名单」，或把 EnvKit 目录加入杀软排除项）", lastErr)
}

func createDatabase() error {
	return createDatabaseNamed(cfg.Projects.DBName)
}

func createDatabaseNamed(db string) error {
	exe := mysqlClient()
	if exe == "" {
		return fmt.Errorf("未找到 mysql 客户端，请先安装 MySQL")
	}
	if db == "" {
		return fmt.Errorf("数据库名未配置")
	}
	p := cfg.Projects
	sql := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;", db)
	info(scConfig, "建库", "%s", sql)
	return execStreamed(scConfig, "", "建库", "", mysqlEnv(), exe,
		"-h", p.MySQLHost, "-P", strconv.Itoa(p.MySQLPort), "-u", p.MySQLUser, "-e", sql)
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
	if mysqlClient() == "" {
		return DBHealth{}, fmt.Errorf("未找到 mysql 客户端，请先安装 MySQL")
	}
	p := cfg.Projects
	base := []string{"-h", p.MySQLHost, "-P", strconv.Itoa(p.MySQLPort), "-u", p.MySQLUser, "-N", "-B"}
	out, err := runMysqlOut(append(append([]string{}, base...), "-e", "SELECT VERSION();")...)
	if err != nil {
		// 不要把 err 吞掉：可能是 spawn 失败（路径/权限），也可能是 mysql 自身报错
		detail := firstLines(strings.TrimSpace(out), 300)
		if detail == "" {
			detail = err.Error()
		}
		return DBHealth{}, fmt.Errorf("连接失败：%s", detail)
	}
	h := DBHealth{Connected: true, Version: firstLine(strings.TrimSpace(out)), Msg: "连接成功"}
	if p.DBName != "" {
		q := fmt.Sprintf("SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='%s';", p.DBName)
		o2, e2 := runMysqlOut(append(append([]string{}, base...), "-e", q)...)
		h.DBExists = e2 == nil && strings.TrimSpace(o2) == "1"
	}
	return h, nil
}
