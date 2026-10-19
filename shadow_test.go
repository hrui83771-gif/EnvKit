package main

// shadow_test.go —— 影子模式与晋升规则的单测（v3.0 T2.8，plan §4.4）
//
// 覆盖四类事实：
//  ① 结论纯函数（计划不可用 / 前置检查不过 / 可晋升）逐条可证；
//  ② 影子流落盘后可数出"连续通过"——跨天不断档；
//  ③ 晋升"缺一不可"：只有通过率没有签名 → 不可晋升；只有签名没有
//     通过率 → 不可晋升（除非显式 grandfathered）；
//  ④ 守护循环里的影子事务**没有 Execute 调用点**（读源码，带正对照）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ---------- 辅助 ----------

// withShadowDir 把影子流与审计流都指向临时目录，返回该目录。
// 两个都要指：影子事务同时写 shadow-*.jsonl 与 audit-*.jsonl，
// 只指一个的话另一个会写进用户真实目录（Windows 上 TempDir 还会
// 因为句柄没关删不掉——审计句柄必须收尾时关）。
func withShadowDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	oldAudit, oldShadow := auditDir, shadowDir
	auditDir = func() string { return dir }
	shadowDir = func() string { return dir }
	auditMu.Lock()
	auditFile, auditFileAt = nil, ""
	auditMu.Unlock()
	t.Cleanup(func() {
		closeAuditForTest()
		auditDir, shadowDir = oldAudit, oldShadow
	})
	return dir
}

// writeShadowFile 直接造一天的影子流文件（模拟历史记录）。
func writeShadowFile(t *testing.T, dir, day string, recs []ShadowRecord) {
	t.Helper()
	var b strings.Builder
	for _, r := range recs {
		r.Ts = day + " 12:00:00.000"
		j, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		b.Write(j)
		b.WriteString("\n")
	}
	p := filepath.Join(dir, "shadow-"+day+".jsonl")
	if err := os.WriteFile(p, []byte(b.String()), 0600); err != nil {
		t.Fatalf("写 %s：%v", p, err)
	}
}

func shadowRec(strategy string, pass bool) ShadowRecord {
	return ShadowRecord{Service: "chain", Strategy: strategy, Pass: pass}
}

// funcBody 从 Go 源码里截出某个函数（从 func 名到下一个顶层 func 之前）。
// 粗截即可：判据要求 `\.Execute\(` 的调用形态，注释里的
// "Execute 前停止"没有点括号，不会误判。
func funcBody(t *testing.T, file, header string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("读不到 %s：%v", file, err)
	}
	s := string(b)
	i := strings.Index(s, header)
	if i < 0 {
		t.Fatalf("%s 里找不到 %s", file, header)
	}
	rest := s[i:]
	if j := strings.Index(rest[1:], "\nfunc "); j >= 0 {
		rest = rest[:j+1]
	}
	return rest
}

// ---------- ① 结论纯函数 ----------

// TestShadowConclude 三种结局必须分开，且"未执行"要写进结论文字——
// 读记录的人最该知道的就是"这条记录里没有任何动作被执行"。
func TestShadowConclude(t *testing.T) {
	// 计划不可用 → 不过，且原因可查
	c, pass, blockers := shadowConclude(Plan{}, fmt.Errorf("未配置目录或启动脚本"), nil)
	if pass {
		t.Error("计划不可用不该算通过")
	}
	if len(blockers) != 1 || !strings.Contains(blockers[0], "未配置目录或启动脚本") {
		t.Errorf("blockers 应带计划失败原因，实际 %v", blockers)
	}
	if !strings.Contains(c, "未进入执行") {
		t.Errorf("结论应说明未进入执行，实际 %q", c)
	}

	// 前置检查不过 → 不过，逐项列出
	c, pass, blockers = shadowConclude(
		Plan{Command: "cd /x && start.sh"}, nil, []string{"断路器已熔断", "预算已耗尽"})
	if pass {
		t.Error("前置检查不过不该算通过")
	}
	if len(blockers) != 2 {
		t.Errorf("blockers 应含两项，实际 %v", blockers)
	}
	if !strings.Contains(c, "断路器已熔断") || !strings.Contains(c, "预算已耗尽") {
		t.Errorf("结论应逐项列原因，实际 %q", c)
	}

	// 计划可生成且前置可过 → 通过，但必须写明"未执行任何动作"
	plan := Plan{Service: "chain", Action: "restart", Command: "cd /fisco && start.sh"}
	c, pass, blockers = shadowConclude(plan, nil, nil)
	if !pass {
		t.Error("计划可生成且前置可过应算通过")
	}
	if len(blockers) != 0 {
		t.Errorf("通过时 blockers 应为空，实际 %v", blockers)
	}
	if !strings.Contains(c, "未执行任何动作") || !strings.Contains(c, plan.Command) {
		t.Errorf("结论应含命令与未执行声明，实际 %q", c)
	}
}

// TestShadowRecordOfFields 记录必须带齐"若执行会做什么 / 预期复验 /
// 策略触发条件并排"——晋升评审就看这三样。
func TestShadowRecordOfFields(t *testing.T) {
	svc := Service{ID: "chain", Label: "链节点", Adapter: AdapterSSHChain, RiskTier: RiskLow}
	strat, _ := strategyByName("restart_chain")
	obs := Observation{Service: "chain", Healthy: false, ErrKind: "not_listening",
		Evidence: []string{"port=20200 未 LISTENING"}, At: time.Now()}
	plan := Plan{Service: "chain", Action: "restart", Command: "cd /fisco && start.sh", RiskTier: RiskLow}

	rec := shadowRecordOf(svc, strat, obs, plan, nil, []string{"group_lock=已持有"}, nil)
	if rec.WouldCommand != plan.Command || rec.WouldAction != "restart" {
		t.Errorf("应记录若执行的命令，实际 %q / %q", rec.WouldAction, rec.WouldCommand)
	}
	if rec.StoppedBefore != "execute" {
		t.Errorf("StoppedBefore 应恒为 execute，实际 %q", rec.StoppedBefore)
	}
	if rec.Trigger != "not_listening" || rec.StrategyTrigger != strat.Trigger {
		t.Error("探针失败态与策略触发条件应并排记录（供人比对，不做机器匹配）")
	}
	if rec.ExpectedVerify.Target != "chain" || len(rec.ExpectedVerify.Evidence) == 0 {
		t.Errorf("预期复验规范应来自策略 verify_spec，实际 %+v", rec.ExpectedVerify)
	}
	if rec.RiskTier != RiskLow {
		t.Errorf("风险分级应来自策略，实际 %q", rec.RiskTier)
	}

	// 计划失败时 WouldCommand 必须为空——不能留一条"看起来能执行"的命令
	rec2 := shadowRecordOf(svc, strat, obs, Plan{}, fmt.Errorf("未配置启动脚本"), nil, nil)
	if rec2.WouldCommand != "" {
		t.Errorf("计划失败时不该有命令，实际 %q", rec2.WouldCommand)
	}
	if rec2.Pass {
		t.Error("计划失败不该算通过")
	}
}

// TestShadowAuditDetail 一条 Detail 说清三件事：若执行会做什么、
// 预期复验规范、评估结论。缺一件，翻审计的人就得去翻 JSONL。
func TestShadowAuditDetail(t *testing.T) {
	rec := ShadowRecord{
		Label: "链节点", WouldAction: "restart", WouldCommand: "cd /fisco && start.sh",
		ExpectedVerify: ShadowVerify{Target: "chain", Evidence: []string{"port", "block_height"}, WaitSecs: 60},
		Conclusion:     "计划可生成且前置检查可过；已在 Execute 前停止，未执行任何动作",
		Pass:           true,
	}
	d := shadowAuditDetail(rec)
	for _, want := range []string{"若执行会做什么", "cd /fisco && start.sh", "预期复验", "block_height", "结论"} {
		if !strings.Contains(d, want) {
			t.Errorf("审计详情应含 %q，实际 %q", want, d)
		}
	}
}

// ---------- ② 影子流：落盘、跨天、连续计数 ----------

// TestShadowEmitAndConsecutivePasses 连续计数：一次不过即清零；
// 且跨天不能断档（只读当天会让跨午夜的连续归零，方向是"永远晋升不了"）。
func TestShadowEmitAndConsecutivePasses(t *testing.T) {
	dir := withShadowDir(t)

	// 昨天：通过、通过、不过
	writeShadowFile(t, dir, "20261018", []ShadowRecord{
		shadowRec("restart_backend", true),
		shadowRec("restart_backend", true),
		shadowRec("restart_backend", false),
	})
	// 今天：通过、通过（另一策略的不算）
	recs := []ShadowRecord{
		shadowRec("restart_backend", true),
		shadowRec("db_restart", false),
		shadowRec("restart_backend", true),
	}
	writeShadowFile(t, dir, time.Now().Format("20060102"), recs)

	if got := shadowPassesOf("restart_backend"); got != 2 {
		t.Errorf("连续通过应数尾部 2 次（昨天的不过已清零），实际 %d", got)
	}
	if got := shadowPassesOf("db_restart"); got != 0 {
		t.Errorf("db_restart 最后一次是不过，应清零，实际 %d", got)
	}
	if got := shadowPassesOf("不存在策略"); got != 0 {
		t.Errorf("没有记录的策略应为 0，实际 %d", got)
	}
}

// TestShadowEmitWritesJSONL shadowEmit 真落盘，且字段齐全。
func TestShadowEmitWritesJSONL(t *testing.T) {
	dir := withShadowDir(t)
	shadowEmit(ShadowRecord{Service: "chain", Strategy: "restart_chain",
		Trigger: "not_listening", Pass: true, StoppedBefore: "execute"})

	b, err := os.ReadFile(filepath.Join(dir, "shadow-"+time.Now().Format("20060102")+".jsonl"))
	if err != nil {
		t.Fatalf("影子流应已落盘：%v", err)
	}
	var rec ShadowRecord
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(b))), &rec); err != nil {
		t.Fatalf("影子流应是合法 JSONL：%v", err)
	}
	if rec.Strategy != "restart_chain" || !rec.Pass || rec.StoppedBefore != "execute" {
		t.Errorf("落盘内容不符：%+v", rec)
	}
}

// TestShadowDaysAndCleanup 日期清单 + 超期清理。
func TestShadowDaysAndCleanup(t *testing.T) {
	dir := withShadowDir(t)
	writeShadowFile(t, dir, "20261018", []ShadowRecord{shadowRec("restart_chain", true)})
	writeShadowFile(t, dir, time.Now().Format("20060102"), []ShadowRecord{shadowRec("restart_chain", true)})
	// 非影子文件不该被收进来
	if err := os.WriteFile(filepath.Join(dir, "audit-20261018.jsonl"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	days := shadowDays()
	if len(days) != 2 || days[0] < days[1] {
		t.Errorf("shadowDays 应倒序返回两天，实际 %v", days)
	}

	// 超期文件清理：造一个 40 天前的
	old := time.Now().AddDate(0, 0, -40).Format("20060102")
	writeShadowFile(t, dir, old, []ShadowRecord{shadowRec("restart_chain", true)})
	cleanupOldShadows()
	if _, err := os.Stat(filepath.Join(dir, "shadow-"+old+".jsonl")); !os.IsNotExist(err) {
		t.Error("40 天前的影子文件应被清理")
	}
	if _, err := os.Stat(filepath.Join(dir, "audit-20261018.jsonl")); err != nil {
		t.Error("清理不能碰审计文件")
	}
}

// ---------- ③ 晋升规则：缺一不可 ----------

// TestPromotionReadinessNeedsBoth plan §4.4 的两个条件缺一不可：
// 只凭通过率（没有签名）不可晋升；只凭签名（没有通过率）也不可
// ——后者正是"把签名当成证据"的那个洞。唯一逃逸口是显式 grandfathered。
func TestPromotionReadinessNeedsBoth(t *testing.T) {
	dir := withShadowDir(t)
	oldNeed := shadowPassNeed
	shadowPassNeed = 3
	t.Cleanup(func() { shadowPassNeed = oldNeed })

	// 垫 3 次通过（今天的文件）
	writeShadowFile(t, dir, time.Now().Format("20060102"), []ShadowRecord{
		shadowRec("wait_slow_start", true),
		shadowRec("wait_slow_start", true),
		shadowRec("wait_slow_start", true),
	})

	// 只有通过率、没有签名 → ready，但不可自动、不可晋升完成
	r := promotionReadiness("wait_slow_start")
	if r.Passes != 3 || r.Need != 3 {
		t.Fatalf("通过数应 3/3，实际 %d/%d", r.Passes, r.Need)
	}
	if r.Eligible {
		t.Error("只有通过率没有人工审批，不该算晋升达标")
	}
	if r.Route != RouteReady {
		t.Errorf("路线应为 ready（证据齐备待签字），实际 %q", r.Route)
	}
	if len(r.Blockers) == 0 || !strings.Contains(r.Blockers[0], "人工审批") {
		t.Errorf("blockers 应点名缺审批，实际 %v", r.Blockers)
	}
	if r.AutoExecutable {
		t.Error("没签名的策略不该可自动执行")
	}

	// 只有签名、没有通过率 → 不可晋升（除非 grandfathered）
	// 用 refresh_backup：它没有任何影子记录（上面 3 次通过记的是
	// wait_slow_start），所以这里测的是纯粹的"有签名、零证据"。
	s2, _ := strategyByName("refresh_backup")
	s2.AutoApproved, s2.ApprovedBy, s2.ApprovedAt = true, "reviewer", "2026-10-19"
	if !strategyAutoExecutable(s2) {
		t.Fatal("带审批三字段的 low 档应可自动执行（strategyAutoExecutable 口径）")
	}
	if shadowEvidenceOK(s2) {
		t.Error("有签名但零影子证据且未标 grandfathered，守护循环应拒绝执行")
	}

	// grandfathered：显式逃逸口，豁免影子证据要求
	s3 := s2
	s3.Grandfathered = true
	if !shadowEvidenceOK(s3) {
		t.Error("grandfathered 策略应豁免影子证据要求（它是显式的人工作证）")
	}
}

// TestPromotionReadinessBothMet 两个条件都满足 → 可晋升。
// 用 restart_chain 的 grandfathered 形态验证 Eligible 路径。
func TestPromotionReadinessBothMet(t *testing.T) {
	withShadowDir(t)
	r := promotionReadiness("restart_chain")
	if !r.Eligible {
		t.Errorf("restart_chain（grandfathered + 审批三字段）应晋升达标，blockers=%v", r.Blockers)
	}
	if r.Route != RouteGrandfathered {
		t.Errorf("路线应为 grandfathered，实际 %q", r.Route)
	}
	if !r.AutoExecutable {
		t.Error("restart_chain 应在 v3.0.1 基线行为下可自动执行")
	}

	// 未注册策略：不明着报"未注册"，就会让调用方以为是没有样本
	r2 := promotionReadiness("ghost_strategy")
	if r2.Eligible || len(r2.Blockers) == 0 {
		t.Error("未注册策略应有明确 blocker 且不可晋升")
	}
	if !strings.Contains(r2.Blockers[0], "未注册") {
		t.Errorf("blocker 应说明未注册，实际 %v", r2.Blockers)
	}
}

// TestGrandfatheredRequiresApproval grandfathered 是逃逸口不是免审批：
// 标了它就必须带审批三字段，否则"基线既有"会沦为免批金牌。
func TestGrandfatheredRequiresApproval(t *testing.T) {
	base, _ := strategyByName("restart_backend")
	bad := base
	bad.Grandfathered = true
	msgs := strategyValidate(bad)
	if len(msgs) == 0 {
		t.Error("grandfathered 却缺审批三字段，应报错")
	}
	ok := bad
	ok.Grandfathered = true
	ok.AutoApproved, ok.ApprovedBy, ok.ApprovedAt = true, "评审人", "2026-10-19"
	for _, m := range strategyValidate(ok) {
		if strings.Contains(m, "grandfathered") {
			t.Errorf("带齐审批字段后不该再报 grandfathered 错：%s", m)
		}
	}
	// 内置表必须干净（restart_chain 带 grandfathered + 三字段）
	for _, m := range validateStrategies(strategyTable()) {
		t.Errorf("内置策略表应全部合法：%s", m)
	}
}

// ---------- ④ 影子事务不执行 ----------

// TestGuardShadowTxNeverExecutes "在 Execute 前停止"不是注释里的承诺，
// 是读源码核对的事实。带正对照：判据必须能逮到真实的调用形态，
// 逮不到的断言比没有断言更坏。
func TestGuardShadowTxNeverExecutes(t *testing.T) {
	body := funcBody(t, "guard.go", "func guardShadowTx(")
	exec := regexp.MustCompile(`\.Execute\(`)
	if exec.MatchString(body) {
		t.Error("guardShadowTx 出现 .Execute( 调用——影子事务必须停在 Execute 前")
	}
	if !strings.Contains(body, ".Plan(") {
		t.Error("guardShadowTx 应调 .Plan( 走完 Observe→Plan")
	}
	// 正对照：这条判据本身要能命中
	if !exec.MatchString("rec.Execute(plan)") {
		t.Error("正对照失败：判据本身失效，逮不到 .Execute( 调用")
	}
	// StoppedBefore 必须写死 execute——它写在本文件的 shadowRecordOf 里
	// （影子记录的组装处），这里读源码核对
	if !strings.Contains(funcBody(t, "shadow.go", "func shadowRecordOf("), `"execute"`) {
		t.Error("影子记录应标记 stopped_before=execute")
	}
}

// TestShadowPrechecksDoNotConsume 影子事务的前置检查只读不消费：
// 评估前后预算用量必须一致——"只看不试"把额度试爆是暗账。
func TestShadowPrechecksDoNotConsume(t *testing.T) {
	// 隔离预算状态
	txBudgetReset()
	strat := RecoveryStrategy{
		Name: "fake_all_prechecks",
		Precheck: []string{PrecheckGroupLock, PrecheckBudget, PrecheckBreaker,
			PrecheckDeps, PrecheckAttended},
	}
	svc := Service{ID: "chain", Adapter: AdapterSSHChain, Guard: GuardSpec{AuditTarget: "chain-host"}}

	before := txBudgetUsed()
	lines, bad := shadowPrechecks(svc, strat)
	if txBudgetUsed() != before {
		t.Error("影子前置检查消耗了预算——评估和动作必须分开记账")
	}
	if len(bad) != 0 {
		t.Errorf("全新环境下五项前置检查都该过，实际不过：%v", bad)
	}
	joined := strings.Join(lines, "|")
	for _, want := range []string{"group_lock=已持有", "budget=余量", "breaker=未熔断", "deps=", "attended="} {
		if !strings.Contains(joined, want) {
			t.Errorf("前置检查结论应含 %q，实际 %q", want, joined)
		}
	}

	// 未知前置检查项：不静默当成通过（判定输入里出现不认识的东西必须显式）
	strat2 := RecoveryStrategy{Name: "fake_unknown", Precheck: []string{"who_knows"}}
	_, bad2 := shadowPrechecks(svc, strat2)
	if len(bad2) != 1 || !strings.Contains(bad2[0], "who_knows") {
		t.Errorf("未知前置检查项应显式不过，实际 %v", bad2)
	}
}

// TestTxBudgetRemainReadOnly 余量只读：不占额度，且不会出现负数。
func TestTxBudgetRemain(t *testing.T) {
	txBudgetReset()
	if got := txBudgetRemain(); got != txMaxActionsDefault {
		t.Errorf("清零后余量应为默认上限 %d，实际 %d", txMaxActionsDefault, got)
	}
	if err := txBudgetTake("unit_test"); err != nil {
		t.Fatalf("第 1 次 take 应放行：%v", err)
	}
	if got := txBudgetRemain(); got != txMaxActionsDefault-1 {
		t.Errorf("take 1 次后余量应减 1，实际 %d", got)
	}
	if txBudgetRemain() < 0 {
		t.Error("余量不该为负")
	}
	txBudgetReset()
}

// TestGuardShadowTxIntegration 守护循环里的影子事务：Observe→Plan
// 走完，审计（actor=shadow）与影子流各留一条，守护状态卡记 shadow。
func TestGuardShadowTxIntegration(t *testing.T) {
	withShadowDir(t)
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })
	cfg = Config{Projects: Projects{FrontendDir: "web"}}

	svc := Service{
		ID: "web", Label: "前端服务", Adapter: AdapterLocal, RiskTier: RiskLow,
		Recovery: "restart_backend",
		Guard:    GuardSpec{AuditTarget: "web-target"},
		Probe:    ProbeSpec{Kind: ProbePort, Timeout: 45},
	}
	strat, _ := strategyByName("restart_backend")
	obs := Observation{Service: "web", Healthy: false, ErrKind: "not_listening",
		Evidence: []string{"进程已退出"}, At: time.Now()}

	guardShadowTx(svc, strat, obs, false)

	// ① 影子流：一条，通过，停在 execute 前
	recs := shadowReadAll()
	if len(recs) != 1 {
		t.Fatalf("应写 1 条影子记录，实际 %d 条", len(recs))
	}
	if !recs[0].Pass || recs[0].StoppedBefore != "execute" {
		t.Errorf("影子记录应通过且停在 execute 前：%+v", recs[0])
	}
	if recs[0].WouldCommand == "" || !strings.Contains(recs[0].WouldCommand, "npm run") {
		t.Errorf("若执行的命令应来自 LocalProc.Plan，实际 %q", recs[0].WouldCommand)
	}

	// ② 审计：actor=shadow，action=shadow_eval，target 走 GuardSpec
	es := auditQuery("", actShadow, 50)
	if len(es) != 1 {
		t.Fatalf("应有 1 条 actor=shadow 审计，实际 %d 条", len(es))
	}
	e := es[0]
	if e.Action != "shadow_eval" || e.Target != "web-target" || e.Result != resOK {
		t.Errorf("影子审计字段不符：%+v", e)
	}
	if !strings.Contains(e.Detail, "若执行会做什么") || !strings.Contains(e.Detail, "预期复验") {
		t.Errorf("审计详情应含若执行与预期复验，实际 %q", e.Detail)
	}

	// ③ 守护状态卡：LastAction=shadow 且单独计数（不与恢复混计）
	st := guardSnapshot()
	if st.LastAction != "shadow" || st.ShadowCount != 1 {
		t.Errorf("守护状态应记 shadow 且计数 1，实际 action=%q count=%d", st.LastAction, st.ShadowCount)
	}
	if st.RecoverCount != 0 {
		t.Error("影子评估不该计入恢复次数")
	}
}

// TestAuditShadowActorAlias 影子主体必须能按别名筛出来——
// 晋升评审的第一手材料就是这些记录，筛不到等于没有。
func TestAuditShadowActorAlias(t *testing.T) {
	for _, k := range []string{"shadow", "影子"} {
		if aiAuditActorAlias[k] != actShadow {
			t.Errorf("别名 %q 应映射到 shadow，实际 %q", k, aiAuditActorAlias[k])
		}
	}
	// 图例要先有审计文件才会输出（没有记录时走的是另一句提示），
	// 所以这里垫一份再取文本。
	setupAuditFiles(t)
	txt, _ := aiAuditText("", "", 10)
	if !strings.Contains(txt, "shadow=影子评估") {
		t.Error("图例应说明 shadow 主体（模型转述时不会造出页面上不存在的词）")
	}
}
