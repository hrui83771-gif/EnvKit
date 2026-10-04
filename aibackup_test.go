package main

// aibackup_test.go —— v2.5：备份只读查询与意图路由的回归
//
// ## 这些测试锁的是 v2.4 评测抓到的真实缺陷
//
// 用户问「检查一下最近的备份文件，看看能不能用」，
// AI **一次工具都没调**就要执行 mysqldump。
//
// 危害不是"多做一次备份"，而是**覆盖问题现场**：
// 损坏的备份被新备份盖掉后，就再也查不出"当时到底坏在哪"。
//
// 缺陷有两层，**只修一层不够**：
//  1. ai_intent.go 预检把「含备份二字」一律判成创建
//  2. AI 根本没有"查看现有备份"的工具——**缺能力时模型会拿最像的动作凑**，
//     而那个动作恰好有副作用
//
// 所以测试分两组：路由（T01~T0n）与工具行为（T1n）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ===== 意图路由：检查类 vs 创建类 =====

// TestIntent_CheckBackupNotCreate 本组是回归的核心。
// 第一版只修路由没补工具时，这些会过但 AI 仍然答不对——
// 因为它没有工具可查，只能去创建。
func TestIntent_CheckBackupNotCreate(t *testing.T) {
	checkAsks := []string{
		"检查一下最近的备份文件，看看能不能用",
		"检查一下最近的备份文��，看看能不能用",
		"最近那份备份能真的还原吗？",
		"最近那份备份看起来完整吗？",
		"备份文件完整不完整，能不能还原",
		"看看有哪些备份",
		"备份在哪",
		"检查备份",
		"查看备份能用吗",
		"验证一下备份是否可用",
	}
	for _, q := range checkAsks {
		tool, args, isWrite := aiMatchIntent(q)
		if tool == "db_backup" {
			t.Errorf("「%s」是**检查**诉求，不该判成创建备份（isWrite=%v, args=%v）", q, isWrite, args)
		}
		if isWrite {
			t.Errorf("「%s」是只读检查，isWrite 应为 false，实际 true（工具 %s）", q, tool)
		}
	}
}

func TestIntent_CreateBackupStillCreate(t *testing.T) {
	// 修复不能把真正的创建诉求也拦掉——那是误伤，
	// 用户说"备份数据库"时不给做才是新的缺陷。
	createAsks := []string{
		"备份数据库",
		"帮我备份一下",
		"做一份备份",
		"导出数据库",
		"备份",
	}
	for _, q := range createAsks {
		tool, _, isWrite := aiMatchIntent(q)
		if tool != "db_backup" {
			t.Errorf("「%s」应判成创建备份，实际 %s", q, tool)
		}
		if !isWrite {
			t.Errorf("「%s」创建备份是写操作，isWrite 应为 true", q)
		}
	}
}

// TestIntent_CheckOtherThingsNotBroken 确认同类路由没被带坏。
// 日志里「检查数据库」→ db_check、「检查环境」→ get_system_state
// 本来就对，这次不能因为改了备份分支而退化。
func TestIntent_CheckOtherThingsNotBroken(t *testing.T) {
	cases := []struct{ ask, want string }{
		{"检查数据库", "db_check"},
		{"检查环境", "get_system_state"},
		{"查看日志", "get_logs"},
	}
	for _, c := range cases {
		if got, _, _ := aiMatchIntent(c.ask); got != c.want {
			t.Errorf("「%s」应路由到 %s，实际 %s", c.ask, c.want, got)
		}
	}
}

// ===== 工具行为 =====

// withBackupDir 把备份目录指向临时目录并塞入指定文件。
//
// 刻意用可替换点而不是改 backupDir 本身——
// backupDir 在 dbops.go 里是函数，测试里改它会与生产代码纠缠。
func withBackupDir(t *testing.T, files map[string]string) func() {
	t.Helper()
	old := backupDirFn
	dir := t.TempDir()
	backupDirFn = func() string { return dir }
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return func() { backupDirFn = old }
}

func TestListBackupsEmpty(t *testing.T) {
	defer withBackupDir(t, nil)()
	lst := listBackups("farm")
	if len(lst) != 0 {
		t.Fatalf("空目录应返回 0 条，实际 %d", len(lst))
	}
	// 空目录时的措辞很重要：必须明确告诉模型"不要用 db_backup 顶替"，
	// 否则它会二选一里挑那个能做的
	brief := backupsBrief("farm")
	if !strings.Contains(brief, "db_backup") {
		t.Errorf("空目录时应提示不要用 db_backup 顶替检查：%s", brief)
	}
}

func TestListBackupsDetectsTruncation(t *testing.T) {
	defer withBackupDir(t, map[string]string{
		"farm-20260101-120000.sql": "CREATE TABLE `a` (`id` int);\n-- Dump completed on 2026-01-01",
		"farm-20260102-120000.sql": "CREATE TABLE `a` (`id` int);\n", // 缺结束标记
	})()
	lst := listBackups("farm")
	if len(lst) != 2 {
		t.Fatalf("应有 2 份，实际 %d", len(lst))
	}
	// 新的在前：用户问"最近那一份"时第一个就是答案
	if !strings.Contains(lst[0].Name, "20260102") {
		t.Errorf("应按时间倒序（新的在前），实际第一个是 %s", lst[0].Name)
	}
	if !lst[0].Truncate {
		t.Error("缺 Dump completed 标记的应被标记为截断")
	}
	if lst[1].Truncate {
		t.Error("有 Dump completed 标记的不该报截断")
	}
}

func TestListBackupsDetectsMissingTriggers(t *testing.T) {
	defer withBackupDir(t, map[string]string{
		"farm-20260101-120000.sql": "-- MySQL dump\nSET NAMES utf8mb4;\n" +
			"CREATE TABLE `a` (`id` int);\n" +
			"CREATE VIEW `v` AS SELECT 1;\n" +
			"CREATE PROCEDURE `p`() BEGIN END;\n" +
			"-- Dump completed\n",
	})()
	lst := listBackups("farm")
	if len(lst) != 1 {
		t.Fatalf("应有 1 份，实际 %d", len(lst))
	}
	// 这份缺 CREATE TRIGGER，静态检查必须报出来
	if !strings.Contains(lst[0].Static, "触发器") {
		t.Errorf("缺触发器应被静态检查报出，实际：%s", lst[0].Static)
	}
}

func TestListBackupsNoSHAWhenMissing(t *testing.T) {
	defer withBackupDir(t, map[string]string{
		"farm-20260101-120000.sql": "-- Dump completed\n",
	})()
	lst := listBackups("farm")
	if len(lst) != 1 {
		t.Fatalf("应有 1 份，实际 %d", len(lst))
	}
	// 没有 .sha256 旁挂时不能报"校验通过"——那是把没检查说成检查了
	if lst[0].SHAOk {
		t.Error("没有 .sha256 旁挂文件时不得报告校验一致")
	}
}

// TestListBackupsBriefDoesNotClaimRestorable 关键：
// 静态检查**不能**证明"能还原"，brief 必须如实说这一点。
func TestListBackupsBriefDoesNotClaimRestorable(t *testing.T) {
	defer withBackupDir(t, map[string]string{
		"farm-20260101-120000.sql": "CREATE TABLE `a` (`id` int);\n-- Dump completed\n",
	})()
	brief := backupsBrief("farm")
	// 必须提到"演练"或"不能只靠静态检查判断可还原"
	if !strings.Contains(brief, "演练") {
		t.Errorf("必须说明真实可还原性需要演练实测：%s", brief)
	}
	// 不得声称校验和已验证（没有旁挂时）
	if strings.Contains(brief, "一致（仅旁挂比对") {
		t.Errorf("无旁挂文件时不得声称一致：%s", brief)
	}
}

// TestInjectedBackupExcluded 注入的测试产物（名字带 injected）
// 不该出现在给AI 的报告里——它不是真实备份。
func TestInjectedBackupExcluded(t *testing.T) {
	defer withBackupDir(t, map[string]string{
		"corrupt-injected.sql":     "-- junk\n",
		"truncated-injected.sql":   "-- junk\n",
		"farm-20260101-120000.sql": "-- real\n-- Dump completed\n",
	})()
	lst := listBackups("farm")
	for _, b := range lst {
		if strings.Contains(b.Name, "injected") {
			t.Errorf("注入的测试产物不该出现在报告里：%s", b.Name)
		}
	}
	if len(lst) != 1 {
		t.Errorf("过滤注入文件后应只剩 1 份真实备份，实际 %d", len(lst))
	}
}
