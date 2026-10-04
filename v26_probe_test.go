package main

// v26_probe_test.go —— v2.6：修 v2.5 评测暴露的两个真缺陷（实证→ 回归）
//
// ## 这组测试锁的是 v2.6 抓到的两个产品缺陷
//
// v2.5 允许写模式跑完 12 次，Verification Rate = 0.667（8/12），
// 4 个 FAIL **全部集中在备份类场景**，工具序列清一色 `['list_backups']`。
// 判分器给的FAIL 理由是「未做任何客观复验——它只能相信自己说的话」。
//
// 但那个理由**本身有问题**：`list_backups` 确实在读盘
// （backupStaticCheck 数 CREATE TABLE / 触发器 / 视图）。
// 而那四次的 AI 回答里出现了这样的句子：
//   「校验和：与旁挂文件一致 —— 但要注意，这**只是旁挂比对，没有重算文件内容**」
//   「静态检查只能查出「内容缺损」，**不等于「能还原」**」
//
// **它在准确描述自己拿到的是弱证据。** 查下去发现问题出在更上游：
//
// 缺陷 1（假绿）：`verifyShaSidecar` 只校验旁挂文件的十六进制形态，
//
//	与文件内容无关，却输出「校验和旁挂：一致」。
//	实测：内容被篡改 + 旁挂写 64 个 '0' → 报"一致"，真实摘要对不上。
//
// 缺陷 2（假警报）：截断检测读**前 4MB** 找 "dump completed"，
//
//	而该标记在文件**末尾**。实测：5MB 完整备份被报成"疑似截断"。
//
// **两个都是"把没检查说成检查了"** —— 正是本项目从 v2.0 起就在防的那类错。
// 危害方向相反但同样坏：假绿让模型以为文件完好，假警报让模型劝用户重做备份。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ===== 缺陷 1 的回归：旁挂「一致」必须真的复算过内容 =====

// withBackupDirRaw 与 aibackup_test.go 的 withBackupDir 同义，
// 这里独立实现是为了本文件能自洽阅读；两者行为必须一致。
func withBackupDirRaw(t *testing.T, files map[string]string) (string, func()) {
	t.Helper()
	old := backupDirFn
	dir := t.TempDir()
	backupDirFn = func() string { return dir }
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, func() { backupDirFn = old }
}

// 内容被篡改 + 旁挂形态合法 → 必须报「不一致」，绝不能报「一致」。
//
// 这正是 tools/fault_inject.py 的 inject_corrupt_backup 造的形态：
// 正文完整，.sha256 写 64 个 '0'。
func TestShaSidecarRecomputesContentNotJustShape(t *testing.T) {
	body := "-- MySQL dump 10.13\n" +
		"CREATE TABLE `t0` (`id` int);\n" +
		"INSERT INTO `t0` VALUES (1);\n" +
		"-- Dump completed on 2026-01-01\n"
	dir, done := withBackupDirRaw(t, map[string]string{
		"farm-20260101-000000-injected-corrupt.sql": body,
	})
	defer done()

	if err := os.WriteFile(
		filepath.Join(dir, "farm-20260101-000000-injected-corrupt.sql.sha256"),
		[]byte(strings.Repeat("0", 64)), 0o644); err != nil {
		t.Fatal(err)
	}

	// 先确认样本本身成立：真实摘要确实不等于 64 个 0。
	real, err := fileSHA256(filepath.Join(dir,
		"farm-20260101-000000-injected-corrupt.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Trim(real, "0") == "" {
		t.Skipf("测试样本摘要恰好全为 0，换个 body 再试：%s", real)
	}

	lst := listBackups("farm")
	if len(lst) != 1 {
		t.Fatalf("应有 1 条，实际 %d", len(lst))
	}
	if lst[0].SHAOk {
		t.Fatalf("内容与旁挂对不上（真实摘要 %s…），SHAOk 不得为 true。\n"+
			"这说明 verifyShaSidecar 又退回成只校验旁挂形态了。", real[:16])
	}
	if !strings.Contains(lst[0].SHAState, "不一致") {
		t.Errorf("状态文字应说明不一致，实际：%q", lst[0].SHAState)
	}
	// brief 里也必须能看到这条
	brief := backupsBrief("farm")
	if strings.Contains(brief, "校验和：一致") {
		t.Errorf("brief 不得输出与事实相反的「一致」：%s", brief)
	}
}

// 旁挂确实对得上时必须报「一致」——不能为了修假绿把真绿也弄没了。
func TestShaSidecarTrueWhenActuallyConsistent(t *testing.T) {
	body := "-- MySQL dump\nCREATE TABLE `a` (`id` int);\n-- Dump completed\n"
	dir, done := withBackupDirRaw(t, map[string]string{
		"farm-20260101-000000.sql": body,
	})
	defer done()

	real, err := fileSHA256(filepath.Join(dir, "farm-20260101-000000.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "farm-20260101-000000.sql.sha256"),
		[]byte(real), 0o644); err != nil {
		t.Fatal(err)
	}

	lst := listBackups("farm")
	if len(lst) != 1 {
		t.Fatalf("应有 1 条，实际 %d", len(lst))
	}
	if !lst[0].SHAOk {
		t.Errorf("旁挂与内容确实一致时必须报一致，实际状态：%q", lst[0].SHAState)
	}
	if !strings.Contains(lst[0].SHAState, "复算") {
		t.Errorf("状态应说明做了复算，实际：%q", lst[0].SHAState)
	}
}

// 四种状态必须分开，不能压成布尔。
//
// 「未复算」与「一致」的证据强度完全不同，
// 混起来就是把没检查说成检查了。
func TestShaSidecarStatesAreDistinct(t *testing.T) {
	body := "-- Dump completed\n"
	dir, done := withBackupDirRaw(t, map[string]string{
		"farm-20260101-000000.sql": body,
	})
	defer done()

	// ① 缺失
	lst := listBackups("farm")
	if len(lst) != 1 {
		t.Fatalf("应有 1 条，实际 %d", len(lst))
	}
	if lst[0].SHAOk || !strings.Contains(lst[0].SHAState, "缺失") {
		t.Errorf("无旁挂时应报缺失且不为 true，实际 ok=%v state=%q",
			lst[0].SHAOk, lst[0].SHAState)
	}

	// ② 旁挂格式异常
	if err := os.WriteFile(
		filepath.Join(dir, "farm-20260101-000000.sql.sha256"),
		[]byte("not-a-hash"), 0o644); err != nil {
		t.Fatal(err)
	}
	lst = listBackups("farm")
	if lst[0].SHAOk {
		t.Errorf("旁挂不是摘要时不得报一致，实际：%q", lst[0].SHAState)
	}

	// ③ 超大文件 → 未复算，且**绝不能是 true**
	// 用常量边界直接构造，避免真造 64MB 文件（慢且占磁盘）。
	p := filepath.Join(dir, "farm-20260101-000000.sql")
	big, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	ok, state := verifyShaSidecar(p, big.Size()+shaRecalcMaxBytes+1)
	if ok {
		t.Errorf("超限未复算时不得返回 true，实际 state=%q", state)
	}
	if !strings.Contains(state, "未复算") {
		t.Errorf("超限必须明说未复算，实际：%q", state)
	}
}

// ===== 缺陷 2 的回归：大文件不得被误报「疑似截断」 =====

// 一份 >4MB、末尾带完整标记的备份，必须判定为**未截断**。
//
// 旧实现读前 4MB 找 "dump completed"，于是每一份超过 4MB 的
// 好备份都被报成损坏。真实项目的 dump 动辄几十上百 MB ——
// 这等于让工具天天对完好的备份拉警报。
func TestLargeCompleteBackupNotFlaggedTruncated(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("-- MySQL dump 10.13\n")
	sb.WriteString("SET NAMES utf8mb4;\n")
	chunk := strings.Repeat("x", 4096)
	for sb.Len() < 5*1024*1024 {
		sb.WriteString(chunk)
	}
	sb.WriteString("\n-- Dump completed on 2026-01-01\n")
	body := sb.String()

	_, done := withBackupDirRaw(t, map[string]string{
		"farm-20260101-000000.sql": body,
	})
	defer done()

	lst := listBackups("farm")
	if len(lst) != 1 {
		t.Fatalf("应有 1 条，实际 %d", len(lst))
	}
	if lst[0].Truncate {
		t.Errorf("一份 %.1f MB、末尾带 'Dump completed' 的完整备份被误报为截断。\n"+
			"原因：截断检测读的是文件头部，而结束标记在末尾。",
			float64(lst[0].SizeBytes)/1024/1024)
	}
}

// 真正的截断仍必须被报出来——修误报不能把真报一起修掉。
func TestTruncatedLargeBackupStillFlagged(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("-- MySQL dump 10.13\n")
	chunk := strings.Repeat("x", 4096)
	for sb.Len() < 5*1024*1024 {
		sb.WriteString(chunk)
	}
	// 没有 Dump completed：真的被截断了
	body := sb.String()

	_, done := withBackupDirRaw(t, map[string]string{
		"farm-20260101-000000.sql": body,
	})
	defer done()

	lst := listBackups("farm")
	if len(lst) != 1 {
		t.Fatalf("应有 1 条，实际 %d", len(lst))
	}
	if !lst[0].Truncate {
		t.Errorf("末尾确实缺结束标记，必须报截断（%.1f MB）",
			float64(lst[0].SizeBytes)/1024/1024)
	}
}

// ===== 边界措辞必须留在输出里 =====

// brief 必须同时说清两件事：文件没被改动 ≠ 能还原。
//
// 这句是 v2.4 的核心设计：AI 很容易把"文件完整"直接说成"备份能用"。
func TestBriefStillStatesRestoreBoundary(t *testing.T) {
	body := "-- Dump\nCREATE TABLE `a` (`id` int);\n-- Dump completed\n"
	dir, done := withBackupDirRaw(t, map[string]string{
		"farm-20260101-000000.sql": body,
	})
	defer done()
	real, _ := fileSHA256(filepath.Join(dir, "farm-20260101-000000.sql"))
	_ = os.WriteFile(filepath.Join(dir, "farm-20260101-000000.sql.sha256"),
		[]byte(real), 0o644)

	brief := backupsBrief("farm")
	if !strings.Contains(brief, "演练") {
		t.Errorf("必须说明真实可还原性需要演练实测：%s", brief)
	}
	// 强校验之后也不能把"内容一致"说成"能还原"。
	// 断言卡整句的关键片段："不证明"与"能还原"必须同时出现。
	if !strings.Contains(brief, "不证明") || !strings.Contains(brief, "能还原") {
		t.Errorf("必须明说校验和不等于能还原（应含「不证明…能还原」）：%s", brief)
	}
	// 不得再出现旧的弱检查措辞
	if strings.Contains(brief, "仅旁挂比对") {
		t.Errorf("不应再出现「仅旁挂比对」（现在真的复算了）：%s", brief)
	}
}
