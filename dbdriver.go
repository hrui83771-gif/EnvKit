// dbdriver.go —— MySQL 的 Go 驱动封装：连接、只读查询、建库/执行。
//
// 为什么不继续走 mysql.exe：
//   - 连接测试和只读浏览是高频路径，命令行输出要猜编码、猜转义、猜错误文本；
//   - 用户机器上服务在跑、但 PATH 里没有 mysql.exe 时，db_check 会误判成“MySQL 没装”；
//   - 驱动能直接拿到列名、NULL、错误类型和超时，不用解析 stdout。
//
// 备份/恢复仍保留 mysqldump/mysql CLI：它们要处理 DELIMITER、source、
// 大文件流式导入，驱动不适合替代，也不该为了统一而冒这个险。
package main

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

type mysqlConnParams struct {
	Host string
	Port int
	User string
	Pass string
	DB   string
}

func dbConnParams() (mysqlConnParams, error) {
	s := cfgSnapshot()
	p := s.Projects
	if strings.TrimSpace(p.MySQLHost) == "" || strings.TrimSpace(p.MySQLUser) == "" || p.MySQLPort <= 0 {
		return mysqlConnParams{}, fmt.Errorf("MySQL 连接未配置（请先填好主机、端口和用户名）")
	}
	return mysqlConnParams{
		Host: p.MySQLHost,
		Port: p.MySQLPort,
		User: p.MySQLUser,
		Pass: p.MySQLPass,
		DB:   p.DBName,
	}, nil
}

func (p mysqlConnParams) config() (*mysql.Config, error) {
	host := strings.TrimSpace(p.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	port := p.Port
	if port <= 0 {
		port = 3306
	}
	user := strings.TrimSpace(p.User)
	if user == "" {
		user = "root"
	}
	if p.DB != "" {
		if _, err := quoteIdent(p.DB); err != nil {
			return nil, err
		}
	}
	c := mysql.NewConfig()
	c.User = user
	c.Passwd = p.Pass
	c.Net = "tcp"
	c.Addr = net.JoinHostPort(host, strconv.Itoa(port))
	c.DBName = p.DB
	c.Params = map[string]string{"charset": "utf8mb4"}
	c.Timeout = 8 * time.Second
	c.ReadTimeout = 30 * time.Second
	c.WriteTimeout = 30 * time.Second
	c.MultiStatements = false
	return c, nil
}

func openMySQL(p mysqlConnParams) (*sql.DB, error) {
	c, err := p.config()
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("mysql", c.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(5 * time.Minute)
	return db, nil
}

// mysqlReadOnlyQuery 在会话级 READ ONLY 下执行查询。
// 用 Conn 而不是 DB.Query，是为了让 SET SESSION 和真正查询落在同一条连接上。
func mysqlReadOnlyQuery(ctx context.Context, p mysqlConnParams, dbName, query string) (cols []string, rows [][]string, err error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil, fmt.Errorf("SQL 为空")
	}
	p.DB = dbName
	db, err := openMySQL(p)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()

	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "SET SESSION TRANSACTION READ ONLY"); err != nil {
		return nil, nil, err
	}
	r, err := conn.QueryContext(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	defer r.Close()

	cols, err = r.Columns()
	if err != nil {
		return nil, nil, err
	}
	raw := make([]sql.RawBytes, len(cols))
	dest := make([]any, len(cols))
	for i := range raw {
		dest[i] = &raw[i]
	}
	for r.Next() {
		if err := r.Scan(dest...); err != nil {
			return nil, nil, err
		}
		row := make([]string, len(raw))
		for i, v := range raw {
			// mysql CLI 的 -B 模式把 NULL 打印成字符串 NULL；
			// 这里保持同样口径，避免 AI 侧把 NULL 和空串混为一谈。
			if v == nil {
				row[i] = "NULL"
			} else {
				row[i] = string(v)
			}
		}
		rows = append(rows, row)
	}
	if err := r.Err(); err != nil {
		return nil, nil, err
	}
	return cols, rows, nil
}

func readOnlyOne(query string) (string, error) {
	_, rows, err := readOnlyExec("", query)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 || len(rows[0]) == 0 {
		return "", nil
	}
	return rows[0][0], nil
}

func mysqlExec(p mysqlConnParams, dbName, statement string) error {
	_, err := mysqlExecAffected(p, dbName, statement)
	return err
}

// mysqlExecAffected 返回受影响行数；用于数据浏览的单行修改与自定义写 SQL。
func mysqlExecAffected(p mysqlConnParams, dbName, statement string) (int64, error) {
	return mysqlExecArgs(p, dbName, statement)
}

func mysqlExecArgs(p mysqlConnParams, dbName, statement string, args ...any) (int64, error) {
	p.DB = dbName
	db, err := openMySQL(p)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	ctx, cancel := cmdContext(60 * time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	res, err := conn.ExecContext(ctx, statement, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil // 某些语句不提供行数；执行本身已成功
	}
	return n, nil
}
