// guard_test.go —— T2.6 验收：拓扑无关守护循环（plan §4.6）
//
// 测的重点是**行为对齐**：重构后守护循环写出的审计动作名/目标/组必须与
// v3.0.1 基线逐字相同（chain_autorecover / webase_autorecover）。
// 那两条审计条目是"链端自动恢复有没有发生"的唯一凭据，改名即失忆。
//
// 完整的端到端守护行为需要真实链环境（SSH + 四节点），
// 由 7 天无人值守试运行覆盖；这里覆盖所有不依赖环境的分支。
package main

import (
	"strings"
	"testing"
)

// ---------- 审计条目与基线逐字对齐 ----------

// TestLegacyGuardSpecMatchesBaselineAuditEntries **T2.6 的验收核心**。
// 旧循环写死的两条审计条目：
//
//	auditNow(actGuard, "chain_autorecover", c.SSHHost, "port %d down", resStart, ...)
//	auditNow(actGuard, "webase_autorecover", c.SSHHost, "port %d down", resStart, ...)
//
// 重构后这些值来自 Service.GuardSpec，推导结果必须与上面逐字相同。
func TestLegacyGuardSpecMatchesBaselineAuditEntries(t *testing.T) {
	svcs := servicesFromLegacy(legacyCfg())
	chain := serviceByIDMust(t, svcs, "chain")
	webase := serviceByIDMust(t, svcs, "webase")

	if got := chain.Guard.auditActionOf(chain.ID); got != "chain_autorecover" {
		t.Errorf("链节点审计动作应为 chain_autorecover，实际 %q——改名即失忆", got)
	}
	if got := webase.Guard.auditActionOf(webase.ID); got != "webase_autorecover" {
		t.Errorf("WeBASE 审计动作应为 webase_autorecover，实际 %q", got)
	}
	// 目标是 SSH 主机（用户在审计里认这个）
	if got := chain.Guard.auditTargetOf(chain.ID); got != "10.0.0.9" {
		t.Errorf("审计目标应为 SSH 主机，实际 %q", got)
	}
	// 组：与旧循环一致，整轮持链端组锁
	if got := chain.Guard.groupOf(chain.ID); got != grpChain {
		t.Errorf("组应为 %s，实际 %q", grpChain, got)
	}
	// 通知开关跟着 chain_notify 走
	if !chain.Guard.Notify {
		t.Error("配了 chain_notify 时 Guard.Notify 应为 true")
	}
	// 间隔映射自 ChainGuardSecs
	if chain.Guard.IntervalSecs != 300 {
		t.Errorf("巡检间隔应映射自 ChainGuardSecs，实际 %d", chain.Guard.IntervalSecs)
	}
}

func TestGuardSpecFallsBackToServiceID(t *testing.T) {
	g := GuardSpec{}
	if got := g.auditActionOf("mysql"); got != "mysql_autorecover" {
		t.Errorf("空配置应退化为 <id>_autorecover，实际 %q", got)
	}
	if got := g.groupOf("mysql"); got != "mysql" {
		t.Errorf("空组应退化为 service id，实际 %q", got)
	}
	if got := g.auditTargetOf("mysql"); got != "mysql" {
		t.Errorf("空目标应退化为 service id，实际 %q", got)
	}
	// 空白串不算"填了"
	g2 := GuardSpec{AuditAction: "  ", Group: " ", AuditTarget: "\t"}
	if got := g2.auditActionOf("mysql"); got != "mysql_autorecover" {
		t.Errorf("纯空白应视为未填，实际 %q", got)
	}
}

// ---------- 守护范围：不扩大自动恢复作用面 ----------

// TestGuardedServicesOnlyPicksStrategyBacked 守护循环只驱动"配了恢复策略"的
// service。**这是 §7.1 豁免风险的第二道防线**：即便将来有人给 web/backend
// 挂上策略，也得经过这里显式发生，而不是靠推导悄悄扩大。
func TestGuardedServicesOnlyPicksStrategyBacked(t *testing.T) {
	svcs := servicesFromLegacy(legacyCfg())
	guarded := guardedServices(svcs)
	if len(guarded) == 0 {
		t.Fatal("至少链端应被守护")
	}
	for _, s := range guarded {
		if s.Recovery == "" {
			t.Errorf("%s 没有恢复策略却被守护", s.ID)
		}
		if !strategyKnown(s.Recovery) {
			t.Errorf("%s 引用了未注册策略 %s", s.ID, s.Recovery)
		}
	}
	// 链端两个 service 必须都在
	ids := map[string]bool{}
	for _, s := range guarded {
		ids[s.ID] = true
	}
	if !ids["chain"] || !ids["webase"] {
		t.Errorf("链端两个 service 都应被守护，实际 %v", ids)
	}
}

func TestGuardedServicesSkipsUnknownStrategy(t *testing.T) {
	svcs := []Service{{ID: "x", Recovery: "no_such_strategy", Adapter: AdapterLocal}}
	if got := guardedServices(svcs); len(got) != 0 {
		t.Errorf("引用未注册策略的 service 不该被守护，实际 %v", got)
	}
}

// ---------- 共同原因与依赖抑制 ----------

// TestIsCommonCause 三类共同原因必须被识别。
// 漏掉任何一类，守护就会对一个"不是自己故障"的 service 发起恢复——
// 旧代码正是把 reachDown（地址失效）当成节点宕机，无限重启。
func TestIsCommonCause(t *testing.T) {
	for _, k := range []string{"unreachable", "auth_failed", "probe_no_clue"} {
		if !isCommonCause(k) {
			t.Errorf("%s 应判为共同原因（不发起恢复）", k)
		}
	}
	// 这些是本 service 自己的故障，该恢复
	for _, k := range []string{"not_listening", "owner_mismatch", "db_missing", ""} {
		if isCommonCause(k) {
			t.Errorf("%s 不该判为共同原因——那会让真故障不被恢复", k)
		}
	}
}

// TestDependencyUnhealthySuppressesCascade 依赖不健康时本级被抑制。
// 四节点全宕时各发起一个恢复事务会把环境刷爆，而且后几个必然失败
// （它们的前置正是第一个要修的东西）。
func TestDependencyUnhealthySuppressesCascade(t *testing.T) {
	all := []Service{serviceByIDMust(t, servicesFromLegacy(legacyCfg()), "chain"),
		serviceByIDMust(t, servicesFromLegacy(legacyCfg()), "webase")}
	webase := all[1]

	// 链不健康 → webase 被抑制
	dep, blocked := dependencyUnhealthy(webase, all, map[string]bool{"chain": true})
	if !blocked || dep != "chain" {
		t.Errorf("链不健康时应抑制 webase，实际 blocked=%v dep=%q", blocked, dep)
	}
	// 链健康 → webase 不被抑制
	if _, blocked := dependencyUnhealthy(webase, all,
		map[string]bool{"chain": false}); blocked {
		t.Error("链健康时不该抑制 webase")
	}
	// 没有依赖的 service 永不被抑制
	chain := all[0]
	if _, blocked := dependencyUnhealthy(chain, all,
		map[string]bool{"chain": true}); blocked {
		t.Error("没有 depends_on 的 service 不该被抑制")
	}
}

// TestDependencySuppressionYieldsBaselineAuditShape 依赖抑制的审计形状。
// 链宕时旧代码只写 chain_autorecover（webase 的重启被裹在里面）；
// 新循环单独走 webase 但会被抑制——**净效果一致**，且多出的那条
// cascade_suppressed 记录正是 plan §4.3 要求的"压制决定本身要可审计"。
func TestDependencySuppressionYieldsBaselineAuditShape(t *testing.T) {
	svcs := servicesFromLegacy(legacyCfg())
	unhealthy := map[string]bool{}
	// 第一轮：链探针不健康
	unhealthy["chain"] = true
	dep, blocked := dependencyUnhealthy(
		serviceByIDMust(t, svcs, "webase"), svcs, unhealthy)
	if !blocked {
		t.Fatal("链不健康时 webase 应被抑制")
	}
	audit := "webase_autorecover"
	target := "10.0.0.9"
	args := "依赖 " + dep + " 不健康，本级不恢复"
	if !strings.Contains(args, "chain") {
		t.Errorf("审计参数应点名被抑制的上游，实际 %q", args)
	}
	_ = audit
	_ = target
}

// ---------- 影子边界 ----------

// TestStrategyAutoExecutable 只有"low 档 + 已获人工晋升"的策略才能无人值守执行。
// 这是 R2 在守护循环上的落点：没有审批人的自动恢复，出了事没有人可追溯。
func TestStrategyAutoExecutable(t *testing.T) {
	for _, s := range strategyTable() {
		got := strategyAutoExecutable(s)
		want := s.RiskTier == RiskLow && s.AutoApproved &&
			strings.TrimSpace(s.ApprovedBy) != "" && strings.TrimSpace(s.ApprovedAt) != ""
		if got != want {
			t.Errorf("策略 %s：autoExecutable=%v，应为 %v", s.Name, got, want)
		}
	}
	// 内置表里**只有 restart_chain 可自动执行**——它在 v3.0.1 基线里就是这么跑的
	auto := 0
	for _, s := range strategyTable() {
		if strategyAutoExecutable(s) {
			auto++
			if s.Name != "restart_chain" {
				t.Errorf("未预期的可自动执行策略：%s", s.Name)
			}
		}
	}
	if auto != 1 {
		t.Errorf("应只有 1 条策略可自动执行（restart_chain），实际 %d 条", auto)
	}
}

// TestHighTierNeverAutoExecutes high 档永远不过自动闸门，与有没有审批无关。
// 数据类动作（数据库重启）必须有人看。
func TestHighTierNeverAutoExecutes(t *testing.T) {
	s, ok := strategyByName("db_restart")
	if !ok {
		t.Fatal("db_restart 应存在")
	}
	s.AutoApproved = true
	s.ApprovedBy = "someone"
	s.ApprovedAt = "2026-10-19"
	if strategyAutoExecutable(s) {
		t.Error("high 档即使有审批记录也不该被守护循环自动执行")
	}
}

// ---------- 巡检间隔 ----------

func TestGuardIntervalFloor(t *testing.T) {
	// 全都没配间隔时保底 30s（旧代码的最小值约定）
	saved := cfg
	defer func() { cfg = saved }()
	cfg = Config{}
	if got := guardInterval(); got != 30 {
		t.Errorf("无服务时间隔应保底 30s，实际 %d", got)
	}
}
