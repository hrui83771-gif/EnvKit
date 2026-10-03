package main

// restore_drill_test.go —— v2.3 N3 的单测
//
// 测的是"静态检查能不能查出文件完整但内容缺失"——
// 现有 verifyBackup 的三项判据（校验和/建表/结尾）在这些场景下全绿。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeDump 造一份 dump 文本。
func writeDump(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "farm-20261003-100000.sql")
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	// verifyBackup 要求 .sha256  companion 文件
	sum := fileSHA256Hex(p)
	if err := os.WriteFile(p+".sha256", []byte(sum+"  "+filepath.Base(p)), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

const fullDumpBody = "-- MySQL dump 10.13\n" +
	"SET NAMES utf8mb4;\n" +
	"CREATE TABLE `farm_user` (\n" +
	"  `id` int NOT NULL AUTO_INCREMENT,\n" +
	"  `name` varchar(64) NOT NULL,\n" +
	"  PRIMARY KEY (`id`),\n" +
	"  KEY `idx_name` (`name`)\n" +
	") ENGINE=InnoDB;\n" +
	"CREATE TABLE `farm_log` (`id` int NOT NULL, PRIMARY KEY (`id`)) ENGINE=InnoDB;\n" +
	"DELIMITER ;;\n" +
	"CREATE DEFINER=`root`@`localhost` TRIGGER trg_ins BEFORE INSERT ON `farm_user`\n" +
	"FOR EACH ROW BEGIN END;;\n" +
	"DELIMITER ;\n" +
	"CREATE VIEW v_farm AS SELECT * FROM farm_user;\n" +
	"CREATE PROCEDURE p_farm() BEGIN SELECT 1; END;\n" +
	"-- Dump completed on 2026-10-03 10:00:00\n"

// ===== 静态检查 =====

func TestBackupStaticCheckFullDump(t *testing.T) {
	finds, gaps := backupStaticCheck(writeDump(t, fullDumpBody))
	joined := strings.Join(finds, " | ")
	for _, must := range []string{"表定义 2 个", "触发器", "视图", "存储过程", "字符集"} {
		if !strings.Contains(joined, must) {
			t.Errorf("完整 dump 应识别出 %q，实际：%s", must, joined)
		}
	}
	if len(gaps) != 0 {
		t.Errorf("完整 dump 不该有缺口，实际：%v", gaps)
	}
}

// 核心场景：文件完整、校验和一致、能导进去，但触发器被 --skip-triggers 丢掉了。
// 现有三项判据全查不出来。
func TestBackupStaticCheckDetectsMissingTriggers(t *testing.T) {
	body := "-- MySQL dump 10.13\nSET NAMES utf8mb4;\n" +
		"CREATE TABLE `t1` (`id` int, PRIMARY KEY (`id`)) ENGINE=InnoDB;\n" +
		"CREATE VIEW v1 AS SELECT 1;\n" +
		"CREATE PROCEDURE p1() BEGIN SELECT 1; END;\n" +
		"-- Dump completed on 2026-10-03 10:00:00\n"
	_, gaps := backupStaticCheck(writeDump(t, body))
	joined := strings.Join(gaps, " ")
	if !strings.Contains(joined, "触发器") {
		t.Errorf("缺触发器必须报出来（现有三项判据查不出）：%v", gaps)
	}
	// 应指出成因，否则用户不知道该改什么
	if !strings.Contains(joined, "skip-triggers") {
		t.Errorf("应指出可能的成因（备份时用了 --skip-triggers）：%v", gaps)
	}
}

func TestBackupStaticCheckMissingCharset(t *testing.T) {
	body := "-- MySQL dump 10.13\n" +
		"CREATE TABLE `t1` (`id` int, PRIMARY KEY (`id`)) ENGINE=InnoDB;\n" +
		"-- Dump completed on 2026-10-03 10:00:00\n"
	_, gaps := backupStaticCheck(writeDump(t, body))
	if !strings.Contains(strings.Join(gaps, " "), "字符集") {
		t.Errorf("缺字符集声明必须报出（还原后中文可能乱码）：%v", gaps)
	}
}

func TestBackupStaticCheckEmptyIsGap(t *testing.T) {
	_, gaps := backupStaticCheck(writeDump(t,
		"-- MySQL dump 10.13\n-- Dump completed\n"))
	if !strings.Contains(strings.Join(gaps, " "), "CREATE TABLE") {
		t.Errorf("一张表都没有必须报出来：%v", gaps)
	}
}

func TestBackupStaticCheckUnreadable(t *testing.T) {
	_, gaps := backupStaticCheck(filepath.Join(t.TempDir(), "nope.sql"))
	if len(gaps) == 0 {
		t.Error("读不到文件必须报缺口（不能静默返回空）")
	}
}

func TestCountSubstr(t *testing.T) {
	if countSubstr("aXbXc", "X") != 2 {
		t.Error("基本计数错误")
	}
	if countSubstr("abc", "") != 0 {
		t.Error("空子串应返回 0（否则会无限循环）")
	}
	if countSubstr("", "x") != 0 {
		t.Error("空输入应返回 0")
	}
	// 上限保护
	if countSubstr(strings.Repeat("x", 100)+strings.Repeat("y", drillMaxCount), "x") != 100 {
		t.Error("计数应正确")
	}
}

// ===== 与现有验证的衔接 =====

// 完整备份：静态检查通过，仍判"已复验通过"但要注明没做演练
func TestVerifyBackupStaticCleanPass(t *testing.T) {
	r := verifyBackupStatic(writeDump(t, fullDumpBody))
	if !r.Ok {
		t.Fatalf("完整备份应通过：%s / %s", r.Msg, strings.Join(r.Evidence, " | "))
	}
	if !strings.Contains(r.Msg, "未做真实还原演练") {
		t.Errorf("必须如实说明没做真实演练（不能让人以为验过可还原性）：%s", r.Msg)
	}
}

// 内容缺损：文件完整但触发器没了 → 降级为 restore_fail
func TestVerifyBackupStaticDegradesOnGap(t *testing.T) {
	body := "-- MySQL dump 10.13\nSET NAMES utf8mb4;\n" +
		"CREATE TABLE `t1` (`id` int, PRIMARY KEY (`id`)) ENGINE=InnoDB;\n" +
		"CREATE VIEW v1 AS SELECT 1;\n" +
		"CREATE PROCEDURE p1() BEGIN SELECT 1; END;\n" +
		"-- Dump completed on 2026-10-03 10:00:00\n"
	r := verifyBackupStatic(writeDump(t, body))
	if r.Ok {
		t.Fatal("内容缺损时不应判成功")
	}
	// 归类必须是 restore_fail 而不是 verify_fail：
	// "文件没坏"与"能还原"是两回事，混在一起用户会以为是文件损坏
	if r.ErrKind != errKindRestoreFail {
		t.Errorf("归类应为 %s，实际 %q", errKindRestoreFail, r.ErrKind)
	}
	if !strings.Contains(r.Msg, "这不等于文件损坏") {
		t.Errorf("必须说清与文件损坏的区别（处置方式不同）：%s", r.Msg)
	}
	joined := strings.Join(r.Evidence, " | ")
	if !strings.Contains(joined, "gap:") {
		t.Errorf("证据里要逐条列出缺口：%s", joined)
	}
}

// 基础验证没过时不硬做静态检查（结论会误导）
func TestVerifyBackupStaticKeepsOriginalFailure(t *testing.T) {
	// 产物不存在
	r := verifyBackupStatic(filepath.Join(t.TempDir(), "gone.sql"))
	if r.Ok {
		t.Fatal("产物不存在时不得判成功")
	}
	if r.ErrKind != errKindVerifyFail {
		t.Errorf("基础失败应保持原归类 %s，实际 %q", errKindVerifyFail, r.ErrKind)
	}
}

// 校验和不一致时也保持原判据（静态检查不该掩盖更严重的问题）
func TestVerifyBackupStaticKeepsChecksumFailure(t *testing.T) {
	p := writeDump(t, fullDumpBody)
	// 篡改产物但不改校验和
	if err := os.WriteFile(p, []byte(fullDumpBody+"\n-- 篡改\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r := verifyBackupStatic(p)
	if r.Ok {
		t.Fatal("校验和不一致时不得判成功")
	}
	if !strings.Contains(r.Msg, "校验和") && !strings.Contains(r.Msg, "校验") {
		t.Errorf("应报校验和问题，实际：%s", r.Msg)
	}
}
