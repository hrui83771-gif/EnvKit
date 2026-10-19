// strategy_test.go —— T2.1 验收：恢复策略元数据六字段齐备性（plan §4.5）
//
// 测的重点不是"表里有几条"，而是**缺字段时会不会被抓住**。
// 散在代码里的恢复逻辑恰恰是缺件时最不容易被发现的形式——
// 所以这里逐字段各造一个缺件的策略，断言它必须报错。
package main

import (
	"strings"
	"testing"
)

func goodStrategy() RecoveryStrategy {
	return RecoveryStrategy{
		Name:        "test_restart",
		Trigger:     "probe.kind=port 连续 2 轮未 LISTENING",
		Precheck:    []string{PrecheckGroupLock, PrecheckBudget, PrecheckBreaker},
		RiskTier:    RiskLow,
		MaxAttempts: 3,
		VerifySpec:  VerifySpec{Target: "backend", Evidence: []string{"pid", "port"}},
		Compensate:  "记录原 pid；失败保持停止并升级人工",
	}
}

// TestBuiltinStrategiesAreComplete 内置表必须全部自洽。
// 这条是"表本身没错"的底线——表里任何一条缺字段，
// 将来照它执行的自动恢复就是一个没有定义的自动恢复。
func TestBuiltinStrategiesAreComplete(t *testing.T) {
	bad := validateStrategies(strategyTable())
	if len(bad) != 0 {
		t.Fatalf("内置策略表有 %d 处不齐备：%v", len(bad), bad)
	}
	if len(strategyTable()) == 0 {
		t.Fatal("内置策略表不能为空")
	}
}

func TestBuiltinStrategyNamesUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range strategyTable() {
		if seen[s.Name] {
			t.Errorf("策略名重复：%s", s.Name)
		}
		seen[s.Name] = true
	}
}

// TestStrategyValidateRejectsMissingFields 六字段逐个缺一遍，每个都必须报错。
// 用表驱动，缺哪个字段、期望报错里出现哪个关键字，一目了然。
func TestStrategyValidateRejectsMissingFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*RecoveryStrategy)
		want   string
	}{
		{"缺 trigger", func(s *RecoveryStrategy) { s.Trigger = "" }, "trigger"},
		{"trigger 为空白", func(s *RecoveryStrategy) { s.Trigger = "   " }, "trigger"},
		{"缺 precheck", func(s *RecoveryStrategy) { s.Precheck = nil }, "precheck"},
		{"precheck 含未知项", func(s *RecoveryStrategy) { s.Precheck = []string{"magic"} }, "magic"},
		{"非法 risk_tier", func(s *RecoveryStrategy) { s.RiskTier = "medium" }, "risk_tier"},
		{"max_attempts 为 0", func(s *RecoveryStrategy) { s.MaxAttempts = 0 }, "max_attempts"},
		{"缺 verify_spec.evidence", func(s *RecoveryStrategy) { s.VerifySpec.Evidence = nil }, "verify_spec"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := goodStrategy()
			c.mutate(&s)
			bad := strategyValidate(s)
			if len(bad) == 0 {
				t.Fatalf("%s 必须报错", c.name)
			}
			if !strings.Contains(bad[0], c.want) {
				t.Errorf("报错应指向 %s，实际：%s", c.want, bad[0])
			}
		})
	}
}

// TestLowTierRequiresCompensate plan §4.5：**无可回滚则必须 high**。
// low 档（可自动）却不写回滚定义 = 能自动做又撤不回来，不允许。
func TestLowTierRequiresCompensate(t *testing.T) {
	s := goodStrategy()
	s.Compensate = ""
	bad := strategyValidate(s)
	if len(bad) == 0 {
		t.Fatal("low 档缺 compensate 必须报错")
	}
	if !strings.Contains(bad[0], "compensate") {
		t.Errorf("报错应指向 compensate，实际：%s", bad[0])
	}
	// 同一个策略升到 high 档就不该再因缺 compensate 报错——
	// 但会因为缺有人值守前置检查而报错（见下一条），所以这里只验 compensate 不再出现
	s.RiskTier = RiskHigh
	s.Precheck = append(s.Precheck, PrecheckAttended)
	for _, m := range strategyValidate(s) {
		if strings.Contains(m, "compensate") {
			t.Errorf("升到 high 档后不应再因 compensate 报错：%s", m)
		}
	}
}

// TestHighTierRequiresAttendedPrecheck high 档必须有人值守前置检查。
// 数据类动作自动跑，是这条规则要防的唯一一件事。
func TestHighTierRequiresAttendedPrecheck(t *testing.T) {
	s := goodStrategy()
	s.RiskTier = RiskHigh
	s.Precheck = []string{PrecheckGroupLock} // 刻意不含 attended
	bad := strategyValidate(s)
	if len(bad) == 0 {
		t.Fatal("high 档缺有人值守前置检查必须报错")
	}
	if !strings.Contains(bad[0], PrecheckAttended) {
		t.Errorf("报错应指向 %s，实际：%s", PrecheckAttended, bad[0])
	}
}

// TestAutoApprovedRequiresApproverAndTime plan §4.4：
// 只凭"影子连续通过"就放行 = 把通过率当成了审批人。
func TestAutoApprovedRequiresApproverAndTime(t *testing.T) {
	s := goodStrategy()

	s.AutoApproved = true
	bad := strategyValidate(s)
	if len(bad) == 0 {
		t.Fatal("标了 auto_approved 却缺审批人/时间必须报错")
	}
	if !strings.Contains(bad[0], "approved_by") {
		t.Errorf("报错应点名 approved_by，实际：%s", bad[0])
	}

	s.ApprovedBy = "张三"
	bad = strategyValidate(s)
	if len(bad) == 0 {
		t.Fatal("只有审批人没有时间仍须报错")
	}

	s.ApprovedAt = "2026-10-19"
	if bad = strategyValidate(s); len(bad) != 0 {
		t.Errorf("三字段齐备后不该再报错：%v", bad)
	}
}

// TestStrategyKnownAndByName 查表：找不到就是找不到，不静默给第一条。
func TestStrategyKnownAndByName(t *testing.T) {
	if !strategyKnown("restart_backend") {
		t.Error("内置策略应可查到")
	}
	if strategyKnown("") {
		t.Error("空名不应算已注册")
	}
	if strategyKnown("no_such") {
		t.Error("未注册名不应算已注册")
	}
	if _, ok := strategyByName("no_such"); ok {
		t.Error("未注册名不该返回 ok")
	}
	s, ok := strategyByName("db_restart")
	if !ok {
		t.Fatal("db_restart 应存在")
	}
	if s.RiskTier != RiskHigh {
		t.Errorf("db_restart 应为 %s 档，实际 %s", RiskHigh, s.RiskTier)
	}
}

// TestNoStrategyBorrowsAnotherDomainsCriteria plan §4.5「不用他类的判据冒充」。
//
// 正确的断言之辨：同一 target 下**允许**多条策略共用证据清单——
// wait_slow_start 与 restart_backend 验的都是"后端在服务"，
// 差别在触发条件与动作（等 vs 重启），不在复验。
// 真正要禁的是**跨 target 共用证据**：拿链端的 block_height 去验备份，
// 或用备份的 file/sha256 去验服务重启，那就是一类恢复在用他类的判据冒充。
func TestNoStrategyBorrowsAnotherDomainsCriteria(t *testing.T) {
	byEvidence := map[string]string{} // evidence 清单 → 首次见到的 target
	for _, s := range strategyTable() {
		key := strings.Join(s.VerifySpec.Evidence, ",")
		if key == "" {
			t.Errorf("策略 %s 的 verify_spec.evidence 为空", s.Name)
			continue
		}
		for _, e := range s.VerifySpec.Evidence {
			if strings.TrimSpace(e) == "" {
				t.Errorf("策略 %s 的 evidence 含空字段名", s.Name)
			}
		}
		if prevTarget, seen := byEvidence[key]; seen && prevTarget != s.VerifySpec.Target {
			t.Errorf("策略 %s（target=%s）与某条 target=%s 的策略共用同一份证据清单「%s」——"+
				"一类恢复在用他类的判据冒充", s.Name, s.VerifySpec.Target, prevTarget, key)
		}
		byEvidence[key] = s.VerifySpec.Target
	}
}

// TestEveryStrategyTargetIsKnownDomain 复验目标必须是已知域。
// 目标乱填会让复验结论记到一个没人看的地方，等于没复验。
func TestEveryStrategyTargetIsKnownDomain(t *testing.T) {
	known := map[string]bool{"web": true, "backend": true, "db": true,
		"chain": true, "webase": true}
	for _, s := range strategyTable() {
		if !known[s.VerifySpec.Target] {
			t.Errorf("策略 %s 的复验目标「%s」不是已知域（%v）",
				s.Name, s.VerifySpec.Target, known)
		}
	}
}

// TestKnownPrecheck 前置检查项白名单。
func TestKnownPrecheck(t *testing.T) {
	for _, p := range []string{PrecheckGroupLock, PrecheckBudget, PrecheckBreaker,
		PrecheckDeps, PrecheckAttended} {
		if !knownPrecheck(p) {
			t.Errorf("%s 应是合法前置检查项", p)
		}
	}
	if knownPrecheck("whatever") {
		t.Error("未知项不该通过")
	}
	if knownPrecheck("") {
		t.Error("空项不该通过")
	}
}
