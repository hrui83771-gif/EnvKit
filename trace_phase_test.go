package main

// trace_phase_test.go —— v2.3 N1 的单测
//
// 核心是 Human Trace 的判定纪律：宁可漏报也不能误报。
// 一次误判会生成一条持续影响后续所有任务的经验，代价远大于漏掉一条。
// 所以下面大量用例在验证"不该记的坚决不记"。

import (
	"strings"
	"testing"
)

// tracePhaseOf 找出轨迹里某个环节的步数。
func tracePhaseOf(tr *Trace, phase string) int {
	n := 0
	for _, s := range tr.Steps {
		if s.Phase == phase {
			n++
		}
	}
	return n
}

// withTraceAndAudit 同时隔离轨迹与审计目录。
//
// N1 的用例要通过 auditNow 触发人工介入（那是唯一的真实入口），
// 而 auditWrite 落 exe 同级的真实审计文件——不隔离会污染用户目录，
// 且与其它测试互相干扰（Windows 上还可能因句柄占用导致 TempDir 清理失败）。
func withTraceAndAudit(t *testing.T) func() {
	t.Helper()
	restoreTrace := withTraceEnv(t)
	restoreAudit := withAuditEnv(t)
	return func() {
		restoreAudit()
		restoreTrace()
	}
}

// ===== plan 环节 =====

func TestTracePhase_PlanRecorded(t *testing.T) {
	defer withTraceAndAudit(t)()
	beginTrace("把服务起起来", actAI)
	tracePlanFromTool("start_service", map[string]any{"service": "web"})
	endTrace("success") // 轨迹只在 endTrace 时落盘，漏掉会让下面查不到

	ts := traceQuery(1, "")
	if len(ts) != 1 || tracePhaseOf(&ts[0], phPlan) == 0 {
		t.Fatal("选型应记入 plan 环节")
	}
}

func TestTracePhase_PlanBasisDistinguishesInferred(t *testing.T) {
	defer withTraceEnv(t)()
	// 用真实 fixture：frontend-npm-dev 只有 dev 脚本，推断结果必然是 dev。
	fe := smokeFix(t, "frontend-npm-dev")
	defer smokeCfg(t, fe, "", "")()
	// 前置检查放最前：推断不出 dev 就没有"一致"这个概念，直接跳过
	if lp := currentLaunchPlan(); lp.WebScript != "dev" || lp.WebSource != "package.json" {
		t.Skipf("前置不成立：推断=%q source=%q，跳过", lp.WebScript, lp.WebSource)
	}

	// ① 传的脚本与推断一致 → 依据应说明来自 package.json
	beginTrace("起服务", actAI)
	tracePlanFromTool("start_service", map[string]any{"service": "web", "script": "dev"})
	endTrace("success")
	ts := traceQuery(1, "")
	if len(ts) != 1 {
		t.Fatalf("应有 1 条轨迹，实际 %d", len(ts))
	}
	if basis := planBasisIn(&ts[0]); !strings.Contains(basis, "package.json") {
		t.Errorf("与推断一致时依据应标明来自 package.json，实际：%q", basis)
	}

	// ② 传的脚本与推断不同 → 依据必须显式指出"这是调用方指定的"，
	//    否则日后无法判断这个决定是谁做的
	beginTrace("起服务", actAI)
	tracePlanFromTool("start_service", map[string]any{"service": "web", "script": "serve"})
	endTrace("success")
	ts = traceQuery(1, "")
	if len(ts) != 1 {
		t.Fatalf("应有 1 条轨迹，实际 %d", len(ts))
	}
	if basis := planBasisIn(&ts[0]); !strings.Contains(basis, "调用方指定") {
		t.Errorf("与推断不一致时必须显式标注（否则日后无法归因），实际：%q", basis)
	}
}

// planBasisIn 取轨迹里的决策依据文本。
func planBasisIn(tr *Trace) string {
	for _, s := range tr.Steps {
		if s.Phase == phPlan && s.Action == "plan_basis" {
			return s.Evidence
		}
	}
	return ""
}

// ===== policy 环节 =====

func TestTracePhase_PolicyRecordsRuleName(t *testing.T) {
	defer withTraceAndAudit(t)()
	beginTrace("备份", actAI)
	tracePolicyDenied(PolicyGate("start_service", "script=migrate", actAI))
	endTrace("failed")

	ts := traceQuery(1, "")
	if tracePhaseOf(&ts[0], phPolicy) == 0 {
		t.Fatal("拒绝裁决应进 policy 环节")
	}
	joined := ""
	for _, s := range ts[0].Steps {
		if s.Phase == phPolicy {
			joined += s.Evidence
		}
	}
	// 归因需要 rule 名，不只是 level：level 说"不行"，rule 说"哪条规矩不行"
	if !strings.Contains(joined, "rule=") || !strings.Contains(joined, "forbidden") {
		t.Errorf("policy 步必须带档位与规则名，实际：%q", joined)
	}
}

func TestTracePhase_PolicySkipsAuto(t *testing.T) {
	defer withTraceAndAudit(t)()
	beginTrace("看日志", actAI)
	tracePolicy(PolicyGate("get_logs", "web", actAI), "allowed")
	endTrace("success")

	ts := traceQuery(1, "")
	if len(ts) != 1 {
		t.Fatalf("应有 1 条轨迹，实际 %d", len(ts))
	}
	if got := tracePhaseOf(&ts[0], phPolicy); got != 0 {
		t.Errorf("auto 档不应进轨迹（只读每秒十几次，全记会冲垮轨迹），实际 %d 步", got)
	}
}

// ===== policyTargetOf 回归 =====
//
// 修掉一个真实缺陷：原实现 script= 命中就 return，把 service=web 直接吞掉。
// 后果是 AI 侧 target 只有脚本名、用户侧只有服务名，Human Trace 永远匹配不上——
// 而两边单独看都"正常"，没有报错、没有日志，只有监督信号静默失效。
func TestPolicyTargetKeepsServiceAndScript(t *testing.T) {
	got := policyTargetOf("start_service", map[string]any{
		"service": "web", "script": "serve",
	})
	if !strings.Contains(got, "web") || !strings.Contains(got, "serve") {
		t.Errorf("service 与 script 必须同时出现在 target 里（否则 Human Trace 对不上号），实际：%q", got)
	}
	// 只有服务时就是服务名
	if got := policyTargetOf("start_service", map[string]any{"service": "web"}); got != "web" {
		t.Errorf("仅 service 时 target 应为 web，实际 %q", got)
	}
	// 两者归一后必须相等（这是 Human Trace 能工作的前提）
	if policyActionKey("start_service", got) !=
		policyActionKey("start_service", policyTargetOf("start_service", map[string]any{
			"service": "web", "script": "serve"})) {
		t.Error("带不带 script 参数，归一后的动作键必须相同")
	}
}

// ===== human 环节：四条判定 =====

// AI 试过并失败 → 用户手动做成功 → 必须记
func TestHumanTrace_Detected(t *testing.T) {
	defer withTraceAndAudit(t)()
	beginTrace("起前端", actAI)
	traceStep(phAction, actAI, "start_service", "web", resFail, 100, "spawn_fail", "端口被占")
	// 用户点界面上的按钮重试同一个动作
	auditNow(actUser, "start_service", "web", "", resOK, "")
	endTrace("success")

	ts := traceQuery(1, "")
	if len(ts) != 1 {
		t.Fatalf("应有 1 条轨迹，实际 %d", len(ts))
	}
	if tracePhaseOf(&ts[0], phHuman) == 0 {
		t.Fatal("AI 试失败后用户手动做成功，应记为人工介入")
	}
}

// 判定一：不在任何任务上下文里 → 不记（挡掉绝大多数噪声）
func TestHumanTrace_RequiresTraceContext(t *testing.T) {
	defer withTraceAndAudit(t)()

	// 没有 beginTrace
	auditNow(actUser, "start_service", "web", "", resOK, "")

	if ts := traceQuery(1, ""); len(ts) != 0 {
		t.Fatalf("无任务上下文时不该产生轨迹，实际 %d 条", len(ts))
	}
}

// 判定二：AI 从没试过这个动作 → 不记
//
// 用户做自己本来就要做的事（他没等 AI 试过）不属于"接手"。
// 这是最容易误判的场景：用户自己开任务、自己点启动，成功了，
// 但那不是"监督 AI"，是"AI 根本还没参与"。
func TestHumanTrace_RequiresAIFailedFirst(t *testing.T) {
	defer withTraceAndAudit(t)()
	beginTrace("我自己来", actAI)
	auditNow(actUser, "start_service", "web", "", resOK, "")
	endTrace("success")

	ts := traceQuery(1, "")
	if len(ts) == 0 {
		t.Fatal("前置动作本身会产生轨迹（plan 步）")
	}
	if got := tracePhaseOf(&ts[0], phHuman); got != 0 {
		t.Errorf("AI 未尝试过时不应记人工介入，实际 %d 步", got)
	}
}

// 判定三：人工操作自己失败了 → 不构成监督信号
func TestHumanTrace_RequiresUserSuccess(t *testing.T) {
	defer withTraceAndAudit(t)()
	beginTrace("起前端", actAI)
	traceStep(phAction, actAI, "start_service", "web", resFail, 100, "", "")
	auditNow(actUser, "start_service", "web", "", resFail, "还是失败")
	endTrace("failed")

	ts := traceQuery(1, "")
	if got := tracePhaseOf(&ts[0], phHuman); got != 0 {
		t.Errorf("人也没搞掂时不构成监督信号，实际 %d 步", got)
	}
}

// 判定四：动作不同类 → 不记
func TestHumanTrace_RequiresSameAction(t *testing.T) {
	defer withTraceAndAudit(t)()
	beginTrace("起前端", actAI)
	traceStep(phAction, actAI, "start_service", "web", resFail, 100, "", "")
	// 用户做的是完全不同的事
	auditNow(actUser, "db_backup", "farm", "", resOK, "")
	endTrace("success")

	ts := traceQuery(1, "")
	if got := tracePhaseOf(&ts[0], phHuman); got != 0 {
		t.Errorf("动作不同类时不记，实际 %d 步", got)
	}
}

// 关键：换脚本参数仍算同类
//
// AI 试 npm run serve 失败，用户改用 npm run dev 成功——
// 这正是 Human Trace 最有价值的一类信号（"人怎么修的"），
// 但参数不同会让朴素的键比较把它当成两个无关动作。
func TestHumanTrace_ScriptParamStillSameAction(t *testing.T) {
	defer withTraceAndAudit(t)()
	beginTrace("前端起不来", actAI)
	// 真实形态（policyTargetOf 修好后）：两边都带 service=web，脚本不同。
	// 修之前 AI 侧是 "script=serve"（service 被吞掉）、用户侧是 "web"，
	// 归一后永远对不上——这个 bug 就是被本用例抓出来的。
	traceStep(phAction, actAI, "start_service", "web script=serve", resFail, 100, "", "")
	auditNow(actUser, "start_service", "web script=dev", "", resOK, "")
	endTrace("success")

	ts := traceQuery(1, "")
	if len(ts) != 1 {
		t.Fatalf("应有 1 条轨迹，实际 %d", len(ts))
	}
	if got := tracePhaseOf(&ts[0], phHuman); got == 0 {
		t.Errorf("换启动脚本参数仍属同类动作，必须记（这是最有价值的一类监督信号），实际 %d 步", got)
	}
}

func TestPolicyActionKeyNormalize(t *testing.T) {
	cases := [][3]string{
		{"start_service", "web", "start_service:web"},
		// script= 是参数不是对象，必须被剥离
		{"start_service", "script=serve", "start_service"},
		{"start_service", "script=dev", "start_service"},
		{"db_backup", "farm", "db_backup:farm"},
		{"get_logs", "", "get_logs"},
	}
	for _, c := range cases {
		if got := policyActionKey(c[0], c[1]); got != c[2] {
			t.Errorf("policyActionKey(%q,%q)=%q，期望 %q", c[0], c[1], got, c[2])
		}
	}
	// 换 script 参数后两边必须相等，否则"AI 试过什么"与"人做了什么"永远对不上号
	if policyActionKey("start_service", "script=serve") != policyActionKey("start_service", "script=dev") {
		t.Error("不同 script 参数应归一到同一个动作键（参数不进键）")
	}
	// 但对象不同仍要能区分
	if policyActionKey("start_service", "web") == policyActionKey("start_service", "backend") {
		t.Error("不同目标不应归一到同一个键")
	}
}

// guard 与 AI 的操作不算人工介入
func TestHumanTrace_GuardIsNotHuman(t *testing.T) {
	defer withTraceAndAudit(t)()
	beginTrace("链端", actAI)
	traceStep(phAction, actAI, "verify_chain", "h", resFail, 100, "", "")
	auditNow(actGuard, "chain_autorecover", "h", "", resOK, "")
	endTrace("success")

	ts := traceQuery(1, "")
	if got := tracePhaseOf(&ts[0], phHuman); got != 0 {
		t.Errorf("守护自动恢复不是人工介入，实际 %d 步", got)
	}
}

// ===== 摘要视图 =====

func TestTraceSummaryReportsReality(t *testing.T) {
	defer withTraceAndAudit(t)()
	beginTrace("起前端", actAI)
	tracePlan("选择 start_service @ web", "来自 package.json")
	traceStep(phAction, actAI, "start_service", "web", resFail, 100, "spawn_fail", "")
	auditNow(actUser, "start_service", "web", "", resOK, "")
	traceStep(phVerify, actAI, "verify_service", "web", resOK, 5200, "", "已复验通过")
	endTrace("success")

	ts := traceQuery(1, "")
	if len(ts) != 1 {
		t.Fatalf("应有 1 条轨迹，实际 %d", len(ts))
	}
	s := traceSummary(&ts[0])
	if s == "" {
		t.Fatal("摘要不应为空")
	}
	for _, must := range []string{"start_service", "人工接手", "已复验通过"} {
		if !strings.Contains(s, must) {
			t.Errorf("摘要缺少 %q，实际：%s", must, s)
		}
	}
}

func TestTraceSummaryFlagsUnverified(t *testing.T) {
	defer withTraceAndAudit(t)()
	beginTrace("起前端", actAI)
	traceStep(phAction, actAI, "start_service", "web", resOK, 100, "", "")
	endTrace("success")

	ts := traceQuery(1, "")
	s := traceSummary(&ts[0])
	// 没复验过就要明说，不能因为"动作都成功了"就显示成完成
	if !strings.Contains(s, "未取得复验结论") {
		t.Errorf("未复验的轨迹必须显式标注，实际：%s", s)
	}
}
