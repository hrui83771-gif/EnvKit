package main

// backup_inventory_test.go —— 备份静态检查「把没找到当丢了」的误报回归
//
// 真实事故（2026-11-06，blood 库）：4 张 BASE TABLE、information_schema.TRIGGERS
// 0 行、ROUTINES 0 行；备份文件 sha256 复算一致、末尾 Dump completed 齐全，
// 却因为 dump 里没有 CREATE TRIGGER / VIEW / PROCEDURE 被判 errKindRestoreFail，
// AI 只能如实转述成 verified=failed。
//
// 判据必须有**独立于被检文件**的 ground truth：源库当时有没有这些对象。
// 这里用 sidecar 明确给出清单，不依赖本机 MySQL，也不让"装置凑不出前提"假绿。
//
// 同样重要的是反例：源库真有、dump 里没有时仍必须判 restore_fail ——
// 修误报不等于把校验器废掉。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 与事故同构：4 张表、字符集声明、完整结尾，三类对象一个都没有。
const plainDumpBody = "-- MySQL dump 10.13\n" +
	"SET NAMES utf8mb4;\n" +
	"CREATE TABLE `blood_relationship` (`id` int, PRIMARY KEY (`id`)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;\n" +
	"CREATE TABLE `bloodline_attachments` (`id` int) ENGINE=InnoDB;\n" +
	"CREATE TABLE `bloodline_history` (`id` int) ENGINE=InnoDB;\n" +
	"CREATE TABLE `users` (`id` int) ENGINE=InnoDB;\n" +
	"-- Dump completed on 2026-11-06 20:37:49\n"

func writeInventory(t *testing.T, backup string, inv dbObjectInventory) {
	t.Helper()
	b, err := json.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inventoryPath(backup), append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

// ===== 正面：源库本来就没有，不该报缺失，也不该判失败 =====

func TestVerifyBackupStaticSourceHasNoObjects(t *testing.T) {
	p := writeDump(t, plainDumpBody)
	writeInventory(t, p, dbObjectInventory{
		Schema: "blood", Triggers: 0, Views: 0, Routines: 0, Verified: true,
	})
	r := verifyBackupStatic(p)
	if !r.Ok {
		t.Fatalf("源库本来就没有三类对象时，一次完整备份不该判失败：%s / %s",
			r.Msg, strings.Join(r.Evidence, " | "))
	}
	if !r.Verified {
		t.Error("文件校验通过 + 源库清单已核实无对象，应算已复验通过（静态层面）")
	}
	if r.ErrKind != "" {
		t.Errorf("不该有失败归类，实际 %q", r.ErrKind)
	}
	ev := strings.Join(r.Evidence, " | ")
	for _, must := range []string{
		"源库无触发器（非缺失）", "源库无视图（非缺失）", "源库无存储过程/函数（非缺失）",
		"表定义 4 个", "含字符集声明",
	} {
		if !strings.Contains(ev, must) {
			t.Errorf("证据里应写明 %q，实际：%s", must, ev)
		}
	}
	if strings.Contains(ev, "gap:") {
		t.Errorf("不该出现任何 gap 证据：%s", ev)
	}
	if strings.Contains(r.Msg, "内容可能有缺失") {
		t.Errorf("结论不该再说内容缺失：%s", r.Msg)
	}
	if !strings.Contains(r.Msg, "未做真实还原演练") {
		t.Errorf("静态检查永远不能证明能还原，结论必须写明：%s", r.Msg)
	}
}

// ===== 反例一：源库真有、dump 丢了 → 仍必须 restore_fail =====

func TestVerifyBackupStaticRealLossStillFails(t *testing.T) {
	p := writeDump(t, plainDumpBody)
	writeInventory(t, p, dbObjectInventory{
		Schema: "blood", Triggers: 3, Views: 0, Routines: 0, Verified: true,
	})
	r := verifyBackupStatic(p)
	if r.Ok {
		t.Fatal("源库有 3 个触发器但备份里一个都没有，必须判失败（别把校验器废掉）")
	}
	if r.ErrKind != errKindRestoreFail {
		t.Errorf("归类应为 %s，实际 %q", errKindRestoreFail, r.ErrKind)
	}
	ev := strings.Join(r.Evidence, " | ")
	if !strings.Contains(ev, "源库有 3 个触发器") {
		t.Errorf("缺口要写清源库有多少，实际：%s", ev)
	}
	// 源库为 0 的两项不能跟着被报成缺口
	if strings.Contains(ev, "源库有 0 个视图") || strings.Contains(ev, "源库有 0 个存储过程") {
		t.Errorf("源库为 0 的项不该报缺口：%s", ev)
	}
}

// ===== 反例二：视图/存储过程真丢了也要各自报出来 =====

func TestVerifyBackupStaticReportsEveryRealGap(t *testing.T) {
	p := writeDump(t, plainDumpBody)
	writeInventory(t, p, dbObjectInventory{
		Schema: "blood", Triggers: 1, Views: 2, Routines: 4, Verified: true,
	})
	r := verifyBackupStatic(p)
	if r.Ok {
		t.Fatal("三类对象全丢却判成功，说明判据空转")
	}
	ev := strings.Join(r.Evidence, " | ")
	for _, must := range []string{"源库有 1 个触发器", "源库有 2 个视图", "源库有 4 个存储过程/函数"} {
		if !strings.Contains(ev, must) {
			t.Errorf("缺口应逐项列出 %q，实际：%s", must, ev)
		}
	}
}

// ===== unknown ≠ 0：查不到源库时既不能说齐全，也不能判失败 =====

func TestVerifyBackupStaticUnknownBaselineIsNotFailure(t *testing.T) {
	old := liveObjectInventory
	liveObjectInventory = func(string) dbObjectInventory {
		return dbObjectInventory{Verified: false} // 库不可达 / 没权限
	}
	defer func() { liveObjectInventory = old }()

	p := writeDump(t, plainDumpBody) // 故意不写 sidecar
	r := verifyBackupStatic(p)
	if !r.Ok {
		t.Fatalf("查不到源库清单不等于备份失败：%s", r.Msg)
	}
	if r.Verified {
		t.Error("清单没核实，不许说「已复验通过」")
	}
	if r.ErrKind != "" {
		t.Errorf("不该有失败归类，实际 %q", r.ErrKind)
	}
	ev := strings.Join(r.Evidence, " | ")
	if !strings.Contains(ev, "未核实") || !strings.Contains(ev, "unverified:") {
		t.Errorf("证据里要显式标出未核实，实际：%s", ev)
	}
	if !strings.Contains(r.Msg, "未做真实还原演练") {
		t.Errorf("结论要写明未证明能还原：%s", r.Msg)
	}
}

func TestDBObjectInventoryOfEmptySchemaIsUnknown(t *testing.T) {
	if dbObjectInventoryOf("").Verified {
		t.Error("库名为空时绝不能返回一份「已核实为 0 个」的清单")
	}
}

// ===== sidecar 自身 =====

func TestObjectInventoryRoundTrip(t *testing.T) {
	p := writeDump(t, plainDumpBody)
	want := dbObjectInventory{Schema: "blood", Triggers: 2, Views: 1, Routines: 3,
		At: "2026-11-06T20:37:49+08:00", Verified: true}
	writeInventory(t, p, want)
	got, ok := readObjectInventory(inventoryPath(p))
	if !ok {
		t.Fatal("写进去的清单应能读回来")
	}
	if got != want {
		t.Errorf("清单往返不一致：want=%+v got=%+v", want, got)
	}
	// 破损的 sidecar 不能当成「源库 0 个」
	if err := os.WriteFile(inventoryPath(p), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readObjectInventory(inventoryPath(p)); ok {
		t.Error("破损清单必须读失败（fail closed），不能退回一个全 0 的假事实")
	}
	_, _, unknown := backupStaticCheckEx(p)
	if !unknown {
		t.Error("破损清单应导致 unknown=true")
	}
}

func TestBackupStaticCheckExFlagsUnknown(t *testing.T) {
	p := writeDump(t, plainDumpBody)
	if _, _, unknown := backupStaticCheckEx(p); !unknown {
		t.Error("没有清单时必须标 unknown")
	}
	writeInventory(t, p, dbObjectInventory{Schema: "blood", Verified: true})
	if _, _, unknown := backupStaticCheckEx(p); unknown {
		t.Error("有已核实清单时不该再标 unknown")
	}
	_, gaps, _ := backupStaticCheckEx(p)
	if len(gaps) != 0 {
		t.Errorf("源库 0 个对象且清单已核实，不该有缺口：%v", gaps)
	}
}

func TestPruneBackupsRemovesInventorySidecar(t *testing.T) {
	dir := t.TempDir()
	for _, stamp := range []string{"20261106-200000", "20261106-200001", "20261106-200002"} {
		base := filepath.Join(dir, "blood-"+stamp+".sql")
		if err := os.WriteFile(base, []byte(plainDumpBody), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(inventoryPath(base), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if n := pruneBackups(dir, "blood", 1); n != 2 {
		t.Fatalf("应清理 2 份旧备份，实际 %d", n)
	}
	left, err := filepath.Glob(filepath.Join(dir, "*.objects.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 {
		t.Errorf("旧备份的清单不该留孤儿文件，剩 %d 份：%v", len(left), left)
	}
	if !strings.HasSuffix(left[0], "20261106-200002.sql.objects.json") {
		t.Errorf("留下的应是最新那份，实际 %s", left[0])
	}
}
