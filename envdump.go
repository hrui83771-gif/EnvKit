// envdump.go —— 环境层数据库转储（Apply 前置备份 / 快照共用）
//
// 只做固定参数的 mysqldump，不接受调用方拼命令行；连接参数来自契约解析后的
// mysqlConnParams，密码只从本机配置取，不写进日志、不回传前端。
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// dumpDatabaseTo 把 dbName 转储到 dest（.sql）。dest 的 .sha256 会一并写出。
// 失败时清掉半截文件，绝不让调用方拿到“看起来有备份”的 0 字节产物。
func dumpDatabaseTo(p mysqlConnParams, dbName, dest string) (size int64, sha string, err error) {
	if _, err := quoteIdent(dbName); err != nil {
		return 0, "", err
	}
	exe := mysqlDumpExe()
	if exe == "" {
		return 0, "", fmt.Errorf("未找到 mysqldump（需 MySQL 组件或系统 PATH）")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return 0, "", fmt.Errorf("创建备份目录失败：%w", err)
	}
	args := []string{
		"-h", p.Host, "-P", fmt.Sprint(p.Port), "-u", p.User,
		"--default-character-set=utf8mb4", "--single-transaction", "--routines", "--triggers", dbName,
	}
	ctx, cancel := cmdContext(10 * time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Env = append(toolEnv(mysqlEnv()), "MYSQL_PWD="+p.Pass)
	out, createErr := os.Create(dest)
	if createErr != nil {
		return 0, "", fmt.Errorf("创建转储文件失败：%w", createErr)
	}
	defer out.Close()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		out.Close()
		_ = os.Remove(dest)
		_ = os.Remove(dest + ".sha256")
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = "mysqldump 无输出退出（可能是安全软件拦截）"
		}
		return 0, "", fmt.Errorf("%v：%s", err, firstLines(msg, 400))
	}
	out.Close()
	fi, statErr := os.Stat(dest)
	if statErr != nil {
		_ = os.Remove(dest + ".sha256")
		return 0, "", fmt.Errorf("转储文件未生成：%w", statErr)
	}
	if fi.Size() == 0 {
		_ = os.Remove(dest)
		_ = os.Remove(dest + ".sha256")
		return 0, "", fmt.Errorf("转储文件为空")
	}
	writeChecksum(dest)
	return fi.Size(), fileSHA256Hex(dest), nil
}
