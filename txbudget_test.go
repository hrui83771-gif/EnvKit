// txbudget_test.go —— v3.0 T1.5（恢复事务两层节制，修 G5/G6）的单测。
//
// 验收口径（plan-v3.0 §3.1 T1.5）：达 max_attempts 后拒绝再执行；
// 窗口预算超限整体熔断并留审计。这里把两条各拆成正反用例，
// 并钉住两个容易坏的设计细节：
//   · 上限拒绝不消耗预算（顺序反了会让"反正要被拒的动作"浪费额度）；
//   · 升级有冷却（没有冷却的话守护循环会把审计和通知刷爆，
//     那正是预算要防的"刷爆"换了个地方）。
package main

import (
	"strings"
	"testing"
	"time"
)

// txResetForTest 复位全局预算状态与配置，避免测试间互相污染。
func txResetForTest(t *testing.T, rec RecoveryConfig) {
	t.Helper()
	txBudgetReset()
	txMu.Lock()
	txRefusedAt = map[string]time.Time{}
	txMu.Unlock()
	cfgMu.Lock()
	old := cfg.Recovery
	cfg.Recovery = rec
	cfgMu.Unlock()
	t.Cleanup(func() {
		cfgMu.Lock()
		cfg.Recovery = old
		cfgMu.Unlock()
		txBudgetReset()
	})
}

// txRecorder 替换真实升级动作（审计+通知），返回记录切片。
func txRecorder(t *testing.T) *[]string {
	t.Helper()
	var got []string
	old := txOnRefuse
	txOnRefuse = func(kind, why string) { got = append(got, kind+" | "+why) }
	t.Cleanup(func() { txOnRefuse = old })
	return &got
}

// ---------- 全局动作预算 ----------

func TestTxBudgetTakeUnderLimit(t *testing.T) {
	txResetForTest(t, RecoveryConfig{MaxActions: 3, WindowSecs: 60})
	for i := 0; i < 3; i++ {
		if err := txBudgetTake("start_service"); err != nil {
			t.Fatalf("第 %d 次应放行，实际：%v", i+1, err)
		}
	}
	if n := txBudgetUsed(); n != 3 {
		t.Fatalf("窗口内应记 3 笔，实际 %d", n)
	}
}

func TestTxBudgetTakeOverLimit(t *testing.T) {
	txResetForTest(t, RecoveryConfig{MaxActions: 2, WindowSecs: 60})
	rec := txRecorder(t)
	if err := txBudgetTake("start_service"); err != nil {
		t.Fatalf("第 1 次应放行：%v", err)
	}
	if err := txBudgetTake("restart_service"); err != nil {
		t.Fatalf("第 2 次应放行：%v", err)
	}
	err := txBudgetTake("start_service")
	if err == nil {
		t.Fatalf("第 3 次必须被预算拒绝")
	}
	if !strings.Contains(err.Error(), "预算超限") {
		t.Fatalf("拒绝原因必须点明预算超限，实际：%v", err)
	}
	// 熔断必须升级（审计+通知由 txOnRefuse 承担，这里替换成了记录器）
	if len(*rec) != 1 {
		t.Fatalf("超限应触发一次升级，实际 %d 次：%v", len(*rec), *rec)
	}
	if !strings.Contains((*rec)[0], "start_service") {
		t.Fatalf("升级应带动作类型，实际：%v", (*rec)[0])
	}
	// 再拒一次：冷却期内不重复升级（否则守护循环会把审计刷爆）
	if err := txBudgetTake("start_service"); err == nil {
		t.Fatalf("熔断期内必须继续拒绝")
	}
	if len(*rec) != 1 {
		t.Fatalf("冷却期内不应重复升级，实际 %d 次", len(*rec))
	}
}

// 窗口滑过后额度自然恢复——预算治的是"单位窗口内刷爆"，
// 不是"永远不能再动"。
func TestTxBudgetWindowSlide(t *testing.T) {
	txResetForTest(t, RecoveryConfig{MaxActions: 2, WindowSecs: 1})
	if err := txBudgetTake("start_service"); err != nil {
		t.Fatalf("第 1 次应放行：%v", err)
	}
	if err := txBudgetTake("start_service"); err != nil {
		t.Fatalf("第 2 次应放行：%v", err)
	}
	if err := txBudgetTake("start_service"); err == nil {
		t.Fatalf("第 3 次应被拒")
	}
	time.Sleep(1200 * time.Millisecond)
	if err := txBudgetTake("start_service"); err != nil {
		t.Fatalf("窗口滑过后应放行，实际：%v", err)
	}
}

func TestTxBudgetReset(t *testing.T) {
	txResetForTest(t, RecoveryConfig{MaxActions: 1, WindowSecs: 60})
	if err := txBudgetTake("start_service"); err != nil {
		t.Fatalf("第 1 次应放行：%v", err)
	}
	if err := txBudgetTake("start_service"); err == nil {
		t.Fatalf("第 2 次应被拒")
	}
	txBudgetReset()
	if err := txBudgetTake("start_service"); err != nil {
		t.Fatalf("人工复位后应放行，实际：%v", err)
	}
}

// 零值配置 = 内置默认（不配也能用，且默认值可预期）。
func TestTxBudgetDefaults(t *testing.T) {
	txResetForTest(t, RecoveryConfig{})
	window, maxActions := txBudgetConf()
	if window != txWindowDefault || maxActions != txMaxActionsDefault {
		t.Fatalf("默认值不对：%v / %d", window, maxActions)
	}
	if n := txMaxAttempts(); n != txMaxAttemptsDefault {
		t.Fatalf("默认尝试上限应为 %d，实际 %d", txMaxAttemptsDefault, n)
	}
}

// ---------- per-target 尝试上限 ----------

func setRestarts(t *testing.T, target string, n int) {
	t.Helper()
	svcWith(target, func(r *svcRuntimeRec) { r.Restarts = n })
	t.Cleanup(func() { svcReset(target) })
}

func TestTxAttemptGateUnderLimit(t *testing.T) {
	txResetForTest(t, RecoveryConfig{MaxAttempts: 3})
	setRestarts(t, "web", 2)
	if err := txAttemptGate("web"); err != nil {
		t.Fatalf("连续失败 2 次（上限 3）应放行，实际：%v", err)
	}
}

func TestTxAttemptGateAtLimit(t *testing.T) {
	txResetForTest(t, RecoveryConfig{MaxAttempts: 3})
	rec := txRecorder(t)
	setRestarts(t, "web", 3)
	err := txAttemptGate("web")
	if err == nil {
		t.Fatalf("连续失败达上限必须拒绝")
	}
	if !strings.Contains(err.Error(), "尝试上限") {
		t.Fatalf("拒绝原因必须点明尝试上限，实际：%v", err)
	}
	// 升级要带退避建议（backoffSteps 有话要说）
	if !strings.Contains(err.Error(), "连续失败") {
		t.Fatalf("拒绝原因应带退避建议，实际：%v", err)
	}
	if len(*rec) != 1 {
		t.Fatalf("达上限应升级一次，实际 %d 次", len(*rec))
	}
}

// 复验通过即清零（svcRecordVerify 的 ok 分支）——上限治的是
// "连续失败"，不是"历史上一共失败过"。不清零的话，
// 一个曾经崩过 3 次、后来稳定运行一天的服务就永远不能再启动。
func TestTxAttemptGateResetByVerify(t *testing.T) {
	txResetForTest(t, RecoveryConfig{MaxAttempts: 3})
	setRestarts(t, "web", 5)
	if err := txAttemptGate("web"); err == nil {
		t.Fatalf("连续失败 5 次应被拒")
	}
	svcRecordVerify("web", "已就绪：端口 8080 在监听", "", true)
	if err := txAttemptGate("web"); err != nil {
		t.Fatalf("复验通过后应放行，实际：%v", err)
	}
}

// 上限拒绝不消耗预算：顺序反了会让"反正要被拒的动作"浪费额度，
// 预算本来是防刷爆的，先被自己人耗光就成了内伤。
func TestTxRecoveryGateAttemptsFirstNoBudgetConsumed(t *testing.T) {
	txResetForTest(t, RecoveryConfig{MaxActions: 2, WindowSecs: 60, MaxAttempts: 3})
	setRestarts(t, "web", 3)
	if err := txRecoveryGate("start_service", "web"); err == nil {
		t.Fatalf("达上限应整体拒绝")
	}
	if n := txBudgetUsed(); n != 0 {
		t.Fatalf("上限拒绝不应消耗预算，实际消耗 %d", n)
	}
	// 另一个没到上限的目标不受影响，且预算额度完好
	if err := txRecoveryGate("start_service", "backend"); err != nil {
		t.Fatalf("backend 未达上限应放行，实际：%v", err)
	}
	if n := txBudgetUsed(); n != 1 {
		t.Fatalf("放行后应记 1 笔，实际 %d", n)
	}
}
