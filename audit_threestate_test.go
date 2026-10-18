// audit_threestate_test.go —— v3.0 T1.6（修 G11）的单测：
// ① auditVerify 三态落值（Ok&&!Verified 与 fail 可区分）；
// ② 审计写失败可见（warn + 升级提示，不阻断业务，冷却防刷屏）。
//
// 为什么钉这条：旧实现把 Ok&&!Verified 压成 resFail——"执行了但没证明
// 恢复"在审计里与"失败"同形。而这两件事的下一步完全相反：前者该继续
// 等/继续验，后者该回滚或升级。判据和人读的是同一份审计，
// 审计撒谎，两边一起瞎。
package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAuditVerifyThreeStates 三态各落各的值，互不合并。
func TestAuditVerifyThreeStates(t *testing.T) {
	withAuditDir(t, nil)
	auditVerify(actAI, "verify_service", "web",
		opVerified("verify_service", "web", "已就绪：端口 8080 在监听"))
	auditVerify(actAI, "verify_service", "web",
		opOK("verify_service", "web", "进程已派生，但 25 秒内未观察到端口监听"))
	auditVerify(actAI, "verify_service", "web",
		opFail("verify_service", "web", errKindSpawnFail, "进程已退出，启动未成功"))

	es := auditTail(10)
	if len(es) != 3 {
		t.Fatalf("应有 3 条复验审计，实际 %d：%+v", len(es), es)
	}
	want := []string{resOK, resUnverified, resFail} // 写入顺序即文件顺序
	for i, w := range want {
		if es[i].Result != w {
			t.Fatalf("第 %d 条结果应为 %q，实际 %q（detail=%q）", i+1, w, es[i].Result, es[i].Detail)
		}
	}
	// 未证明恢复那一条的 Detail 必须自己说清楚"没证明"——
	// 只靠 Result 值不够，读 Detail 的人（AI/人）看到的是文本。
	if !strings.Contains(es[1].Detail, "未复验") {
		t.Fatalf("unverified 条目的 Detail 应带未复验措辞，实际：%q", es[1].Detail)
	}
}

// TestStatsCountsUnverified 新值要流到看板：分桶与合计都要有它。
// 看板是"复验通过率"的分母来源——unverified 进分母不进"失败"，
// 否则"还没验到"会被画成失败率，看板就在教人误读。
func TestStatsCountsUnverified(t *testing.T) {
	withAuditDir(t, nil)
	auditVerify(actAI, "verify_service", "web",
		opVerified("verify_service", "web", "已就绪"))
	auditVerify(actAI, "verify_service", "web",
		opOK("verify_service", "web", "进程已派生，未观察到端口监听"))

	b := statsRange(1)
	if b.Totals.Unverified != 1 {
		t.Fatalf("合计应记 1 条 unverified，实际 %d", b.Totals.Unverified)
	}
	if b.Totals.OK != 1 {
		t.Fatalf("合计应记 1 条 ok，实际 %d", b.Totals.OK)
	}
	if b.Totals.Fail != 0 {
		t.Fatalf("未证明恢复不算失败，实际 fail=%d", b.Totals.Fail)
	}
}

// TestAuditWriteFailVisible 审计目录不可写时：不阻断 + warn + 升级 + 冷却。
// 旧实现 OpenFile 失败只 warn（每次调用都 warn，日志刷爆）、WriteString
// 错误被完全丢弃——"审计静默丢失"本身成了不可观测事故。
func TestAuditWriteFailVisible(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "missing-subdir") // 父目录不存在，OpenFile 必失败
	oldDir := auditDir
	auditDir = func() string { return bad }
	auditMu.Lock()
	auditFile, auditFileAt = nil, ""
	auditMu.Unlock()
	t.Cleanup(func() {
		closeAuditForTest()
		auditDir = oldDir
	})

	var got []string
	oldEsc := auditFailEscalate
	auditFailEscalate = func(why string) { got = append(got, why) }
	t.Cleanup(func() { auditFailEscalate = oldEsc })
	auditFailMu.Lock()
	auditFailLast = time.Time{} // 清零冷却，避免被其它测试的最近时刻压制
	auditFailMu.Unlock()

	// 不阻断：写不上审计，业务调用照常返回（审计是观察者不是执行者）
	auditNow(actUser, "config_save", "config", "", resOK, "测试保存")

	if len(got) != 1 {
		t.Fatalf("审计写失败必须升级一次，实际 %d 次：%v", len(got), got)
	}
	if !strings.Contains(got[0], "无法写入") {
		t.Fatalf("升级原因必须点明写失败，实际：%v", got[0])
	}
	// warn 也要落到日志（hub 历史里找得到）——只升级不留痕等于换地方静默
	hub.mu.Lock()
	var warned bool
	for _, m := range hub.history {
		if strings.Contains(m.Line, "审计文件无法写入") {
			warned = true
			break
		}
	}
	hub.mu.Unlock()
	if !warned {
		t.Fatalf("审计写失败必须 warn 到日志")
	}
	// 冷却期内第二次不升级：审计写失败时每条操作都提醒一遍就是刷爆，
	// 那正是预算类机制要防的模式，不能在自己身上重演。
	auditNow(actUser, "config_save", "config", "", resOK, "测试保存 2")
	if len(got) != 1 {
		t.Fatalf("冷却期内不应重复升级，实际 %d 次", len(got))
	}
}
