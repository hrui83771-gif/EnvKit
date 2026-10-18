// breaker_test.go —— v3.0 T1.9 崩溃循环断路器（plan §3.2.3）的单测。
//
// 验收口径（plan §3.1 T1.9）：连续失败达限进熔断、熔断期拒绝自动重启、
// 熔断事件进审计+通知。这里把三态机（closed → open → half-open →
// closed/open）的每条边都钉住，并盯住三个设计细节：
//
//	· 达限熔断发生在"崩溃当时"（svcRecordCrash），不等某个 gate 调用来发现
//	  ——否则"没被 gate 看到的崩溃"永远不进状态机；
//	· half-open 必须把 Restarts 回退到限值-1，否则 txAttemptGate 会让
//	  "冷静期自然解除"成为死代码（纸面出口）；
//	· 复验通过同时清计数与清状态——只清一边，两个机制就说辞打架。
package main

import (
	"strings"
	"testing"
	"time"
)

// brkResetForTest 复位断路器相关全局态（目标记录 + 配置 + 通知记录器）。
// target 用测试专属键，避免与别的用例抢 "web"/"backend"。
func brkResetForTest(t *testing.T, target string, rec RecoveryConfig) *[]string {
	t.Helper()
	svcReset(target)
	cfgMu.Lock()
	old := cfg.Recovery
	cfg.Recovery = rec
	cfgMu.Unlock()
	var fired []string
	oldNotify := svcBreakerNotify
	svcBreakerNotify = func(title, why string) { fired = append(fired, title+" | "+why) }
	t.Cleanup(func() {
		svcBreakerNotify = oldNotify
		cfgMu.Lock()
		cfg.Recovery = old
		cfgMu.Unlock()
		svcReset(target)
	})
	return &fired
}

// brkCrash 制造 n 次崩溃。
//
// **这里替每个用过的 target 登记 cleanup**：svcRuntimeRec 是全局注册表，
// 漏一个 key 就会污染 TestSvcAllRecordsSnapshot 那种"一次快照全部服务"的断言
// （实测踩过：差一个 brk-gate，快照从 2 变 3）。收拾的责任放进 helper，
// 而不是指望每个用例记得写 defer。
func brkCrash(t *testing.T, target string, n int) {
	t.Helper()
	t.Cleanup(func() { svcReset(target) })
	for i := 0; i < n; i++ {
		svcRecordCrash(target, errKindCrash, "panic: 测试崩溃", 8080)
	}
}

// ---------- 达限进熔断 ----------

func TestBreakerTripsAtLimit(t *testing.T) {
	const target = "brk-trip"
	fired := brkResetForTest(t, target, RecoveryConfig{BreakerAfter: 3, BreakerCooldownSecs: 300})
	withAuditDir(t, nil)

	brkCrash(t, target, 2)
	if err := svcBreakerGate(target, "restart_service"); err != nil {
		t.Fatalf("未达限应放行，实际：%v", err)
	}
	brkCrash(t, target, 1) // 第 3 次：达限，崩溃当时即熔断
	tripped, _, restarts := svcBreakerStatus(target)
	if !tripped {
		t.Fatal("连续失败达限后应进入熔断态")
	}
	if restarts != 3 {
		t.Errorf("Restarts 应=3，实际 %d", restarts)
	}
	// 熔断是升级事件：通知必须响
	if len(*fired) != 1 {
		t.Fatalf("熔断应触发一次通知，实际 %d 次：%v", len(*fired), *fired)
	}
	// 熔断事件必须进审计（"为什么重启被拒了"要能追溯）
	var found bool
	for _, e := range auditQuery("", "", 0) {
		if e.Action == "breaker_tripped" && e.Target == target {
			found = true
		}
	}
	if !found {
		t.Error("熔断迁移未进审计")
	}
}

// ---------- 熔断期拒绝自动重启 ----------

func TestBreakerRefusesDuringCooldown(t *testing.T) {
	const target = "brk-refuse"
	brkResetForTest(t, target, RecoveryConfig{BreakerAfter: 2, BreakerCooldownSecs: 300})

	brkCrash(t, target, 2)
	for i := 0; i < 3; i++ {
		err := svcBreakerGate(target, "restart_service")
		if err == nil {
			t.Fatalf("第 %d 次：熔断中必须拒绝自动重启", i+1)
		}
		if !strings.Contains(err.Error(), "熔断") {
			t.Errorf("拒绝原因必须点明熔断态，实际：%v", err)
		}
	}
}

// ---------- 冷静期过后放行一次探针（half-open） ----------

func TestBreakerHalfOpenAfterCooldown(t *testing.T) {
	const target = "brk-half"
	brkResetForTest(t, target, RecoveryConfig{BreakerAfter: 3, BreakerCooldownSecs: 1})

	brkCrash(t, target, 3)
	if err := svcBreakerGate(target, "restart_service"); err == nil {
		t.Fatal("刚熔断应拒绝")
	}
	// 把熔断起点拨到 2 秒前（冷静期 1 秒已过）
	svcWith(target, func(r *svcRuntimeRec) { r.BreakerTrippedAt = time.Now().Add(-2 * time.Second) })
	if err := svcBreakerGate(target, "restart_service"); err != nil {
		t.Fatalf("冷静期过后应放行一次探针，实际：%v", err)
	}
	// 探针放行的代价：Restarts 回退到限值-1，否则 txAttemptGate
	// 会让"冷静期自然解除"成为死代码。
	if n := svcRestartCount(target); n != 2 {
		t.Errorf("half-open 应把 Restarts 回退到 2，实际 %d", n)
	}
	tripped, _, _ := svcBreakerStatus(target)
	if tripped {
		t.Error("half-open 后不应仍处于熔断态")
	}
}

// ---------- 探针又崩 → 立即再熔断，冷静期重新起算 ----------

func TestBreakerReTripsOnProbeFailure(t *testing.T) {
	const target = "brk-retrip"
	brkResetForTest(t, target, RecoveryConfig{BreakerAfter: 3, BreakerCooldownSecs: 300})

	brkCrash(t, target, 3)
	svcWith(target, func(r *svcRuntimeRec) { r.BreakerTrippedAt = time.Now().Add(-10 * time.Minute) })
	if err := svcBreakerGate(target, "restart_service"); err != nil {
		t.Fatalf("冷静期过后应放行探针：%v", err)
	}
	brkCrash(t, target, 1) // 探针失败
	tripped, _, _ := svcBreakerStatus(target)
	if !tripped {
		t.Fatal("探针失败必须立即再熔断")
	}
	if err := svcBreakerGate(target, "restart_service"); err == nil {
		t.Error("再熔断后必须继续拒绝")
	}
}

// ---------- 复验通过解除熔断（计数与状态同时清） ----------

func TestBreakerRecoversOnVerifyOK(t *testing.T) {
	const target = "brk-recover"
	brkResetForTest(t, target, RecoveryConfig{BreakerAfter: 2, BreakerCooldownSecs: 300})

	brkCrash(t, target, 2)
	if tripped, _, _ := svcBreakerStatus(target); !tripped {
		t.Fatal("应先熔断")
	}
	svcRecordVerify(target, "端口+HTTP 均正常", "", true)
	if n := svcRestartCount(target); n != 0 {
		t.Errorf("复验通过应清计数，实际 %d", n)
	}
	if tripped, _, _ := svcBreakerStatus(target); tripped {
		t.Error("复验通过应解除熔断")
	}
	if err := svcBreakerGate(target, "restart_service"); err != nil {
		t.Errorf("解除后应放行，实际：%v", err)
	}
}

// ---------- 人工复位（连计数一起清，否则 gate 立刻再熔断 = 空操作） ----------

func TestBreakerManualReset(t *testing.T) {
	const target = "brk-reset"
	fired := brkResetForTest(t, target, RecoveryConfig{BreakerAfter: 2, BreakerCooldownSecs: 300})
	withAuditDir(t, nil)

	brkCrash(t, target, 2)
	svcBreakerReset(target)
	if err := svcBreakerGate(target, "restart_service"); err != nil {
		t.Fatalf("人工复位后应放行，实际：%v", err)
	}
	// 复位必须连 Restarts 一起清：只清状态不清计数，gate 会因
	// "Restarts 仍达限"立刻再熔断，复位就成了空操作（实测踩过）。
	if n := svcRestartCount(target); n != 0 {
		t.Errorf("人工复位应连计数一起清，实际 %d", n)
	}
	var found bool
	for _, e := range auditQuery("", "", 0) {
		if e.Action == "breaker_reset" && e.Target == target {
			found = true
		}
	}
	if !found {
		t.Error("人工复位未进审计")
	}
	if len(*fired) < 2 {
		t.Errorf("熔断+复位应有两次通知，实际 %d：%v", len(*fired), *fired)
	}
}

// ---------- 集成：AI restart_service 与守护共用的 txRecoveryGate 被熔断拦住 ----------

func TestTxRecoveryGateBlockedByBreaker(t *testing.T) {
	const target = "brk-gate"
	txResetForTest(t, RecoveryConfig{MaxActions: 10, WindowSecs: 600, MaxAttempts: 3, BreakerAfter: 2, BreakerCooldownSecs: 300})
	svcReset(target)

	brkCrash(t, target, 2)
	err := txRecoveryGate("restart_service", target)
	if err == nil {
		t.Fatal("熔断中 txRecoveryGate 必须拒绝（AI 与守护同一道门）")
	}
	if !strings.Contains(err.Error(), "熔断") {
		t.Errorf("拒绝原因必须点明熔断，实际：%v", err)
	}
}
