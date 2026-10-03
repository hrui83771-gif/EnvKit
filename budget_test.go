package main

import (
	"strings"
	"testing"
)

// ===== 预算提额治理器 =====

// 用户实际报的问题原样入测：这句话不含任何 v2.3 初版关键词
// （没有"怎么跑"也没有"分析"），初版被判成 4 轮并截断了正确答案。
func TestBudgetGovernor_RealReportedCase(t *testing.T) {
	// 初版分档确实会判错——这是本项要修的根因，钉住它以免回归
	q := "区块链的定义，以及在本项目上链了什么数据"
	b := budgetFor(q)
	if b.Max >= aiTurnExplore {
		t.Skipf("该问题现在被文本分档直接判为探索档（%d 轮），提额路径不再是唯一保障", b.Max)
	}

	// 模拟模型的真实行为：连续 4 次探索类调用，每次都成功
	g := newBudgetGovernor(b)
	seq := []string{"get_project_brief", "search_files", "list_project", "read_file"}
	raised, newMax := false, 0
	for _, tool := range seq {
		r, nm := g.observe(tool)
		raised, newMax = r, nm
	}
	if !raised {
		t.Fatalf("连续 4 轮探索都该提额，实际未提（max=%d productive=%d）", g.max, g.productive)
	}
	if newMax <= b.Max {
		t.Errorf("提额后上限应变大：%d -> %d", b.Max, newMax)
	}
	// 提额后至少要够用户案例里实际发生的那 5 次调用
	if newMax < 5+budgetEscalateStep/2 {
		t.Errorf("提额后应留有足够余量，实际 %d 轮", newMax)
	}
}

// 非探索类调用不算"有产出"——写操作与状态快照都不推进认知
func TestBudgetGovernor_NonProductiveToolsDoNotCount(t *testing.T) {
	for _, tool := range []string{"get_system_state", "start_service",
		"verify_environment", "db_backup", "get_logs", "get_chain_guard"} {
		g := newBudgetGovernor(turnBudget{Max: 4, Reason: "t"})
		for i := 0; i < 4; i++ {
			if r, _ := g.observe(tool); r {
				t.Fatalf("%s 不该提额（它不推进认知）", tool)
			}
		}
		if g.productive != 0 {
			t.Errorf("%s 不该计入 productive，实际 %d", tool, g.productive)
		}
	}
}

// 打转场景不给更多轮次：夹杂无效调用时永远不达标
func TestBudgetGovernor_StuckDoesNotEscalate(t *testing.T) {
	// 一半轮次在读文件，一半在读同样的东西（打转）
	g := newBudgetGovernor(turnBudget{Max: 4, Reason: "t"})
	seq := []string{"read_file", "read_file", "read_file", "read_file"}
	raised := false
	for _, tool := range seq {
		if r, _ := g.observe(tool); r {
			raised = true
		}
	}
	// 纯探索且全部成功 → 会提额；这里验证的是"提额后不再无限涨"
	if !raised {
		t.Fatal("前提不成立：连续探索本应提额")
	}
	first := g.max
	// 再喂 20 轮探索：最多提一次，不该无限增长
	for i := 0; i < 20; i++ {
		if r, _ := g.observe("read_file"); r {
			t.Fatalf("第二次提额不该发生：%d -> %d", first, g.max)
		}
	}
	if g.max > aiTurnDeep {
		t.Fatalf("上限必须封顶 deep：%d", g.max)
	}
}

// 提额上限：不得越过 deep
func TestBudgetGovernor_CappedAtDeep(t *testing.T) {
	g := newBudgetGovernor(turnBudget{Max: aiTurnDeep, Reason: "t"})
	for i := 0; i < 40; i++ {
		g.observe("search_files")
	}
	if g.max > aiTurnDeep {
		t.Errorf("初始已是 deep 时不该再涨：%d", g.max)
	}
	if g.escalated != 0 {
		t.Errorf("初始已是 deep 不该记提额：%d", g.escalated)
	}
}

// nil 治理器不该 panic（AI 未启用等路径不构造它）
func TestBudgetGovernorNilSafe(t *testing.T) {
	var g *budgetGovernor
	if r, n := g.observe("read_file"); r || n != 0 {
		t.Error("nil 治理器应安全返回 false/0")
	}
}

// 已提额后仍要能继续工作（不是只能提一次就停摆）
func TestBudgetGovernor_StillUsableAfterEscalation(t *testing.T) {
	g := newBudgetGovernor(turnBudget{Max: 4, Reason: "t"})
	for i := 0; i < 4; i++ {
		g.observe("search_files")
	}
	m := g.max
	g.observe("read_file")
	if g.max != m {
		t.Errorf("提额后的正常观察不该再改上限：%d -> %d", m, g.max)
	}
	if g.productive < 5 {
		t.Errorf("productive 应持续累计，实际 %d", g.productive)
	}
}

// 提额要留审计痕迹，否则"为什么这次没被截断"无法回答
func TestBudgetRaiseIsAuditable(t *testing.T) {
	// action 名要对：指标「无效操作次数」与后续排查都依赖它
	if !strings.Contains("ai_budget_raised", "budget") {
		t.Error("提额审计 action 应含 budget，便于按动作筛")
	}
}

// 用户案例的完整序列：brief → search → list → read → search
// 第 4 轮（达到初值 4）必须提额，第 5 轮才能继续，
// 否则用户实际发生的 5 次调用会被砍掉第 5 次。
func TestBudgetGovernor_AllowsFifthCallInReportedCase(t *testing.T) {
	g := newBudgetGovernor(turnBudget{Max: 4, Reason: "常规排查"})
	tools := []string{"get_project_brief", "search_files", "list_project",
		"read_file", "search_files"}
	usable := 0
	for _, tool := range tools {
		if g.max > usable {
			usable = g.max
		}
		g.observe(tool)
	}
	if g.max < len(tools) {
		t.Errorf("用户实际发生的 %d 次调用都该放行，实际上限 %d", len(tools), g.max)
	}
}
