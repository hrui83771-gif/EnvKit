package main

// agent_loop_e2e_test.go —— Agent 闭环的端到端回归（确定性，不调模型）。
//
// ## 为什么需要这条
//
// mission / playbook / sediment / lesson 各有单测，但**没有一条把五段串起来**。
// 而实测断链全都发生在接缝上，每次都"每段都对、连起来不对"：
//   · Phase 2：注入只在「有已完成步骤」时发生 → 全新 pending 行动点「继续」时
//     模型反问"你想让我做什么"；
//   · Phase 2：存为预案没去重 → 连点两下得到两条一样的预案，删一条另一条还在。
// 这两条都不是任何单个模块的错，单个模块的测试永远照绿。
//
// ## 它走的是用户能感知的顺序
//
//   ① 告警进来     → 产出 pending 行动（此时还没有预案，不该凭空套一个）
//   ② 执行 + 复验   → done（结论来自 verify_result，不是模型自述）
//   ③ 存为预案     → 步骤来自 Plan、匹配键来自 Trigger；再存一次必须被拒
//   ④ 同类告警再来 → 自动命中 + 起草 Draft（Plan 仍为空，两个字段不许混）
//   ⑤ 执行 + 复验   → done，预案统计 Used/Succ +1（成功只认复验）
//   ⑥ 复验连败 3 次 → 预案转待复审，且下一次告警**不再推荐**它
//   ⑦ 失败沉淀     → R6 目标级连败经验出现，中文任务能召回
//
// ## 为什么注入步骤不算"测另一套代码"
//
// "决定调哪个工具"是模型的事，这里用夹具注入；而**判据**读的是落盘的步骤记录
// （ok 字段来自工具执行结果）与 verify 结论 —— 与模型是谁无关。红线 1 的落点
// 是 missionFinish，这里正是从外部把它的输入喂进去，验它落到哪个终态。

import (
	"path/filepath"
	"strings"
	"testing"
)

// withLoopEnv 闭环测试的全套隔离：行动 + 预案 + 轨迹 + 审计 + 经验否决表 + 成败计数。
//
// 少隔离任何一样，测试都会读写开发机上的真实文件：missions.json 被塞进
// 七八条假的"后端故障"，比测试失败更糟——它看起来像真数据。
func withLoopEnv(t *testing.T) func() {
	t.Helper()
	undoAI := withAIMissionEnv(t)
	undoLesson := withLessonEnv(t, nil)
	dir := t.TempDir()
	oldOut := outcomeFile
	outcomeFile = func() string { return filepath.Join(dir, "lesson-outcomes.json") }
	return func() {
		outcomeFile = oldOut
		undoLesson()
		undoAI()
	}
}

func mustMission(t *testing.T, id string) Mission {
	t.Helper()
	m, ok := missionGet(id)
	if !ok {
		t.Fatalf("行动 %s 丢了（missions.json 里找不到）", id)
	}
	return m
}

// sedimentEntries 取出某服务的沉淀审计（R6 的唯一输入）。
func sedimentEntries(t *testing.T, svc string) []AuditEntry {
	t.Helper()
	var out []AuditEntry
	for _, e := range auditQuery("all", "", 500) {
		if e.Action == msSinkAction && e.Target == svc {
			out = append(out, e)
		}
	}
	return out
}

// TestAgentLoopEndToEnd 告警 → 预案 → 执行 → 复验 → 沉淀 → 复用 → 降级。
func TestAgentLoopEndToEnd(t *testing.T) {
	defer withLoopEnv(t)()

	const svc = "backend"

	// ===== ① 告警进来：还没有预案，行动必须"赤裸"地等人批 =====
	m1, created := missionFromGuard(svc, "后端", errKindVerifyFail,
		"后端拉起后复验未过", "backend | 端口 3000 未监听")
	if !created {
		t.Fatalf("守护事件应产出新行动")
	}
	if m1.State != msPending {
		t.Fatalf("新行动应为 pending（等人批），实为 %s", m1.State)
	}
	if m1.Playbook != "" || len(m1.Draft) != 0 {
		t.Fatalf("库里没有预案时不该凭空套一个：playbook=%q draft=%d",
			m1.Playbook, len(m1.Draft))
	}

	// ===== ② 执行 + 复验 → done =====
	runTurn(t, m1.ID, outSuccess, func() {
		aiMissionStep("get_logs", svc, "查后端日志", true, "末行：EADDRINUSE :3000")
		aiMissionStep("restart_service", svc, "重启后端", true, "已启动 pid=12345")
		aiMissionVerify(OpResult{Ok: true, Verified: true, Msg: "端口 3000 正在监听"})
	})
	m1 = mustMission(t, m1.ID)
	if m1.State != msDone {
		t.Fatalf("复验通过应落 done，实为 %s（%+v）", m1.State, m1.Result)
	}
	if m1.Result == nil || !m1.Result.Verified {
		t.Fatalf("done 必须带已复验的结论，实为 %+v", m1.Result)
	}

	// 沉淀审计已经写出（R6 靠它，不是靠 missions.json）
	if es := sedimentEntries(t, svc); len(es) != 1 || es[0].Result != resOK {
		t.Fatalf("成功收口应写一条 resOK 沉淀审计，实为 %+v", es)
	}

	// ===== ③ 存为预案：步骤来自 Plan，匹配键来自 Trigger =====
	p, err := pbkFromMission(m1.ID, "后端端口被占：查日志 → 重启 → 复验")
	if err != nil {
		t.Fatalf("存为预案失败：%v", err)
	}
	if p.SourceMission != m1.ID {
		t.Fatalf("预案必须能追溯来源行动，实为 %q", p.SourceMission)
	}
	if len(p.Steps) != 2 || p.Steps[0].Tool != "get_logs" || p.Steps[1].Tool != "restart_service" {
		t.Fatalf("步骤应原样取自 Plan（2 步），实为 %+v", p.Steps)
	}
	if len(p.Matcher.Services) != 1 || p.Matcher.Services[0] != svc {
		t.Fatalf("匹配键应含服务 %s，实为 %+v", svc, p.Matcher)
	}
	if len(p.Matcher.Triggers) != 1 || p.Matcher.Triggers[0] != "guard" {
		t.Fatalf("匹配键应含来源 guard，实为 %+v", p.Matcher)
	}
	if _, err := pbkFromMission(m1.ID, ""); err == nil {
		t.Fatalf("同一行动第二次沉淀必须被拒（否则界面出现两条一模一样的预案）")
	}

	// ===== ④ 同类告警再来：自动命中 + 起草，但绝不自动执行 =====
	m2, created := missionFromGuard(svc, "后端", errKindVerifyFail,
		"后端又没起来", "backend | 端口 3000 未监听")
	if !created {
		t.Fatalf("上一条已终态，同类告警应产出新行动")
	}
	if m2.Playbook != p.ID {
		t.Fatalf("同类告警应命中预案 %s，实为 %q", p.ID, m2.Playbook)
	}
	if len(m2.Draft) != 2 {
		t.Fatalf("应起草 2 步，实为 %+v", m2.Draft)
	}
	// 最容易混的一处：Draft 是"没做的事"，Plan 是"已经发生的事"。
	// 混在一起会让断点续作回放不该回放的步骤、让报告型判据误以为已完成。
	if len(m2.Plan) != 0 {
		t.Fatalf("起草不得写进 Plan（那是已发生步骤），实为 %+v", m2.Plan)
	}

	// ===== ⑤ 执行 + 复验 → done，预案统计回写 =====
	runTurn(t, m2.ID, outSuccess, func() {
		ctx := aiMissionResumeContext()
		if !strings.Contains(ctx, p.Title) {
			t.Errorf("续作上下文应带上预案标题（否则模型不知道有套路可按）：%s", ctx)
		}
		if !strings.Contains(ctx, "不省确认") {
			t.Errorf("上下文必须写明预案不省确认（红线 2）：%s", ctx)
		}
		if !strings.Contains(ctx, "第一次执行") {
			t.Errorf("全新行动不该被描述成「此前中断」：%s", ctx)
		}
		aiMissionStep("get_logs", svc, "查后端日志", true, "末行：EADDRINUSE :3000")
		aiMissionStep("restart_service", svc, "重启后端", true, "已启动 pid=12346")
		aiMissionVerify(OpResult{Ok: true, Verified: true, Msg: "端口 3000 正在监听"})
	})
	m2 = mustMission(t, m2.ID)
	if m2.State != msDone {
		t.Fatalf("第二回合复验通过应落 done，实为 %s（%+v）", m2.State, m2.Result)
	}
	pb, ok := pbkGet(p.ID)
	if !ok {
		t.Fatalf("预案丢了")
	}
	if pb.Stats.Used != 1 || pb.Stats.Succ != 1 || pb.Stats.Fail != 0 || pb.Stats.Streak != 0 {
		t.Fatalf("预案统计应为 1 用 1 成 0 败 streak=0，实为 %+v", pb.Stats)
	}

	// ===== ⑥ 复验连败 → 待复审 → 不再推荐 =====
	for i := 0; i < pbkReviewStreak; i++ {
		mi, ok := missionFromGuard(svc, "后端", errKindVerifyFail,
			"后端反复崩溃", "backend | 端口 3000 未监听")
		if !ok {
			t.Fatalf("第 %d 次复验失败回合：应产出新行动", i+1)
		}
		runTurn(t, mi.ID, outSuccess, func() {
			aiMissionStep("restart_service", svc, "重启后端", true, "已重启")
			aiMissionVerify(OpResult{Ok: false, Verified: false, Msg: "端口 3000 仍未监听"})
		})
		if got := mustMission(t, mi.ID); got.State != msFailed {
			t.Fatalf("第 %d 次复验未过应落 failed，实为 %s", i+1, got.State)
		}
	}
	pb, _ = pbkGet(p.ID)
	if pb.Stats.Used != 1+pbkReviewStreak || pb.Stats.Fail != pbkReviewStreak {
		t.Fatalf("统计应为 %d 用 %d 败，实为 %+v",
			1+pbkReviewStreak, pbkReviewStreak, pb.Stats)
	}
	if pb.State != pbkReview {
		t.Fatalf("连续 %d 次复验未过应转待复审，实为 %s", pbkReviewStreak, pb.State)
	}
	// 待复审的预案不许再被推荐：宁可走完整 AI 诊断，也不要喂一个已证明不好用的套路
	m3, _ := missionFromGuard(svc, "后端", errKindVerifyFail,
		"后端再次崩溃", "backend | 端口 3000 未监听")
	if m3.Playbook != "" {
		t.Fatalf("待复审的预案不该再被套用，实为 %q", m3.Playbook)
	}

	// ===== ⑦ 沉淀成经验：3 次真实失败 → R6 连败，且中文任务能召回 =====
	es := sedimentEntries(t, svc)
	if len(es) != 1+1+pbkReviewStreak {
		t.Fatalf("每次终态都应有且只有一条沉淀审计，期望 %d 条，实为 %d",
			1+1+pbkReviewStreak, len(es))
	}
	l := lessonByID(lessonsFor(), lsID(lsRepeatFail, msSinkAction, svc))
	if l == nil {
		t.Fatalf("同一服务连续 %d 次失败应产出目标级经验，实际：%+v", pbkReviewStreak, lessonsFor())
	}
	if l.Hits != pbkReviewStreak {
		t.Fatalf("命中次数应只数「最后一次成功之后」的失败（%d），实为 %d",
			pbkReviewStreak, l.Hits)
	}
	if !strings.Contains(l.Title, "后端") {
		t.Fatalf("标题要用中文服务名（给人看），实为 %q", l.Title)
	}
	// 召回：用户说的是中文目标，不会说 mission_fix @ backend
	if s := lessonMatchScore(*l, "后端起不来，端口被占了"); s <= 0 {
		t.Fatalf("中文任务应能召回这条经验，实得分数 %d（title=%q）", s, l.Title)
	}
}
