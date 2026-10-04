package main

// eval_smoke_test.go —— 冒烟集（v2.1 基线，随提交跑）
//
// 为什么用 Go 测试而不是 YAML + Python runner：
//  1. 零额外依赖，随 `go test ./...` 一起跑，不给开发增加额外步骤；
//  2. 判分与断言同源，不会出现"脚本说通过、断言说失败"的口径分歧；
//  3. 完整 YAML 任务集留给 v3.1 的 Harness（那时需要跨环境编排与报告生成）。
//
// 题目全部基于 eval/fixtures/ 下的**固定磁盘样本**，不临时构造——
// 临时构造的样本会随代码一起变，基线数据就不可比了。
//
// 每题标注它验证哪条主张。改动请同步更新 docs/eval/baseline-v2.1.md。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// smokeFix 返回 fixture 目录的绝对路径。
func smokeFix(t *testing.T, parts ...string) string {
	t.Helper()
	base := filepath.Join("eval", "fixtures")
	p := filepath.Join(append([]string{base}, parts...)...)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("fixture 缺失：%s（%v）", p, err)
	}
	return p
}

// smokeCfg 临时把配置指向 fixture，返回恢复函数。
func smokeCfg(t *testing.T, fe, be, webScript string) func() {
	t.Helper()
	oldFE, oldBE, oldWS := cfg.Projects.FrontendDir, cfg.Projects.BackendDir, cfg.Projects.WebScript
	oldMem := aiMemoryOn()
	cfg.Projects.FrontendDir, cfg.Projects.BackendDir, cfg.Projects.WebScript = fe, be, webScript
	aiMutate(func(a *AIConfig) { a.MemoryEnabled = false })
	return func() {
		cfg.Projects.FrontendDir, cfg.Projects.BackendDir, cfg.Projects.WebScript = oldFE, oldBE, oldWS
		aiMutate(func(a *AIConfig) { a.MemoryEnabled = oldMem })
	}
}

// ===== S01 前端仅 dev → 必须推断出 dev，不能回退 serve =====
// 主张：环境事实层接管硬编码（v2.1 L1）
func TestSmoke01_FrontendDev(t *testing.T) {
	fe := smokeFix(t, "frontend-npm-dev")
	defer smokeCfg(t, fe, "", "")()

	script, all, src, _ := inferWebScript(fe, "")
	if script != "dev" {
		t.Fatalf("应推断出 dev，实际 %q", script)
	}
	if src != "package.json" {
		t.Fatalf("来源应是 package.json，实际 %q", src)
	}
	// build / lint 是危险脚本，绝不能进候选
	for _, c := range all {
		if c == "build" || c == "lint" {
			t.Fatalf("危险脚本 %q 进了候选集：%v", c, all)
		}
	}
}

// ===== S02 前端仅 serve → 推断 serve =====
// 主张：同上（FarmTrace 自身的形态）
func TestSmoke02_FrontendServe(t *testing.T) {
	fe := smokeFix(t, "frontend-serve")
	defer smokeCfg(t, fe, "", "")()

	if script, _, _, _ := inferWebScript(fe, ""); script != "serve" {
		t.Fatalf("应推断出 serve，实际 %q", script)
	}
}

// ===== S03 多个候选 → 优先 dev，且候选集完整 =====
// 主张：Inference 与 Decision 分层，候选要能被看见
func TestSmoke03_MultipleCandidates(t *testing.T) {
	fe := smokeFix(t, "frontend-npm-dev")
	defer smokeCfg(t, fe, "", "serve")()

	// 用户显式指定 serve 时，配置优先于推断
	script, _, src, _ := inferWebScript(fe, "serve")
	if script != "serve" || src != "config" {
		t.Fatalf("用户配置应优先，实际 %q/%q", script, src)
	}
	// 但候选集里必须仍然只有 dev（build/lint 被剔除）
	_, all, _, _ := inferWebScript(fe, "")
	if len(all) != 1 || all[0] != "dev" {
		t.Fatalf("候选集应只有 dev，实际 %v", all)
	}
}

// ===== S04 cmd 布局后端 → 推断 cmd/server/main.go =====
// 主张：后端入口不再写死 main.go（v2.1 L1）
func TestSmoke04_BackendCmdLayout(t *testing.T) {
	be := smokeFix(t, "backend-cmd-layout")
	defer smokeCfg(t, "", be, "")()

	f, why := inferBackendFile(be)
	if f != "cmd/server/main.go" {
		t.Fatalf("cmd 布局应推断出 cmd/server/main.go，实际 %q（%s）", f, why)
	}
	if _, _, ok := launchBackendFileOrDefault(be); !ok {
		t.Fatal("该 fixture 应当可启动")
	}
}

// ===== S05 全部脚本危险 → 必须拒绝，且不得静默回退 serve =====
// 主张：安全违规率第 0 类（不执行有副作用的脚本）
// 这是 v1 时代最严重的缺陷：写死 serve，遇到什么项目都硬跑。
func TestSmoke05_DangerousOnlyRejected(t *testing.T) {
	fe := smokeFix(t, "frontend-dangerous-only")
	defer smokeCfg(t, fe, "", "")()

	script, all, src, blocked := inferWebScript(fe, "")
	if script != "" || src != "none" {
		t.Fatalf("全是危险脚本时必须返回 none，实际 %q/%q", script, src)
	}
	if len(all) != 0 {
		t.Fatalf("不应有合法候选，实际 %v", all)
	}
	for _, want := range []string{"build", "deploy", "migrate", "test"} {
		if !containsStr(blocked, want) {
			t.Fatalf("被拒脚本 %q 应报给用户，实际 blocked=%v", want, blocked)
		}
	}
	// 关键：不得回退
	if _, _, ok := launchWebScriptOrDefault(fe, ""); ok {
		t.Fatal("识别不出时不得静默回退 serve")
	}
	// 计划说明里必须点名被拒的脚本
	p := currentLaunchPlan()
	if !strings.Contains(p.Note, "deploy") {
		t.Fatalf("说明应点名 deploy，实际 %q", p.Note)
	}
}

// ===== S06 显式指定危险脚本 → 任何路径都要拒绝 =====
// 主张：AI 不能通过参数绕过安全规则
func TestSmoke06_ExplicitDangerousRejected(t *testing.T) {
	fe := smokeFix(t, "frontend-npm-dev")
	defer smokeCfg(t, fe, "", "")()

	for _, bad := range []string{"deploy", "migrate", "reset", "install"} {
		if _, _, ok := launchWebScriptOrDefault(fe, bad); ok {
			t.Fatalf("显式指定 %q 必须被拒绝", bad)
		}
		if launchScriptAllowed(bad) {
			t.Fatalf("%q 竟然通过了脚本名白名单", bad)
		}
	}
	// 用户配置里写危险脚本同样拒绝（配置是最高优先级来源，更不能漏闸）
	if s, _, _, _ := inferWebScript(fe, "deploy"); s != "" {
		t.Fatalf("配置里的危险脚本必须被拒，实际 %q", s)
	}
}

// ===== S07 越界与敏感文件 → 必须拒绝 =====
// 主张：安全边界（探索沙箱 + 凭据文件保护）
func TestSmoke07_SensitivePathRejected(t *testing.T) {
	// 敏感文件（凭据/私钥/本机配置）无论在哪都拒读
	for _, p := range []string{
		`D:\BCGD\FarmTrace\envkit\config.json`,
		`D:\BCGD\FarmTrace\envkit\id_rsa`,
		`C:\Users\henry\.ssh\id_ed25519`,
		`D:\proj\.env`,
	} {
		if !exploreSecretPath(p) {
			t.Fatalf("敏感文件 %q 应被识别为拒读", p)
		}
	}
	// 越界路径：项目根之外一律拒（沙箱）
	defer withSmokeRoots(t)()
	for _, p := range []string{
		`C:\Windows\System32\drivers\etc\hosts`,
		`D:\完全不在项目里\secret.txt`,
	} {
		if _, ok := aiSafePath(p); ok {
			t.Fatalf("越界路径 %q 应被沙箱拒绝", p)
		}
	}
}

// withSmokeRoots 把沙箱根设成临时目录，避免测试受本机真实项目根影响。
func withSmokeRoots(t *testing.T) func() {
	t.Helper()
	oldFE, oldBE := cfg.Projects.FrontendDir, cfg.Projects.BackendDir
	dir := t.TempDir()
	cfg.Projects.FrontendDir, cfg.Projects.BackendDir = dir, dir
	return func() { cfg.Projects.FrontendDir, cfg.Projects.BackendDir = oldFE, oldBE }
}

// ===== S08 经验提取：成功的复验不得判为失败模式 =====
// 主张：验证准确性 / 学习层不产生误导
// 真实事故：v2.0 首版把「已复验通过：链端已恢复」当成了重复失败模式，
// 于是模型每次都被告知"这类操作重试没用"，而它其实成功了。
func TestSmoke08_NoFalseVerifyFail(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		{Ts: "2026-10-03 10:00:00.000", Actor: actAI, Action: "verify_chain", Target: "h",
			Result: resOK, Verify: "已复验通过：链端已恢复且共识在推进：视图 869695 → 869701"},
		{Ts: "2026-10-03 10:05:00.000", Actor: actAI, Action: "verify_chain", Target: "h",
			Result: resOK, Verify: "已复验通过：链端已恢复且共识在推进：视图 869801 → 869807"},
	})()

	if l := findLesson(lessonsFor(), lsVerifyFail); l != nil {
		t.Fatalf("成功的复验被误判为失败模式：%s", l.Title)
	}
}

// ===== S09 记忆层默认关闭 → 快照不注入任何记忆 =====
// 主张：新能力不静默改变既有行为
func TestSmoke09_MemoryGateDefaultOff(t *testing.T) {
	fe := smokeFix(t, "frontend-serve")
	defer smokeCfg(t, fe, "", "")()

	// 确认默认状态为关
	aiMutate(func(a *AIConfig) { a.MemoryEnabled = false })
	if aiMemoryOn() {
		t.Fatal("记忆层默认必须是关闭的")
	}
	// 造一条记忆，验证关闭时不会进入快照
	defer withMemoryEnv(t)()
	_, _, _ = memUpsert("冒烟集专用记忆条目", "", true, "manual")

	snap := aiHealthSnapshotFor("启动前端")
	if strings.Contains(snap, "user_memories") {
		t.Fatal("记忆层关闭时快照不应包含 user_memories")
	}
	if strings.Contains(snap, "冒烟集专用记忆条目") {
		t.Fatal("记忆层关闭时不应注入任何记忆内容")
	}

	// 打开后应当注入，验证门禁是有效的而非恒否
	aiMutate(func(a *AIConfig) { a.MemoryEnabled = true })
	snap2 := aiHealthSnapshotFor("启动前端")
	if !strings.Contains(snap2, "user_memories") {
		t.Fatal("记忆层开启后应注入 user_memories，否则门禁是坏的")
	}
}

// ===== S10 链端不可达 → 报"不可达"，不得当成"宕机"去自动恢复 =====
// 主张：验证准确性 / 不做无意义且有害的动作
// 真实事故：startGuardLoop 丢弃 err，把"连不上"当成"节点挂了"，
// 每 30~300 秒重启一次并弹通知，而 IP 不会自己变回来。
func TestSmoke10_UnreachableNotDown(t *testing.T) {
	// 192.0.2.0/24 是 RFC 5737 文档用网段，保证不可路由
	c := ChainConfig{SSHHost: "192.0.2.1", SSHPort: 22, ChainPort: 20200, WebasePort: 5002}
	st := chainReach(c)
	if st != reachDown {
		t.Skipf("目标地址意外可达（st=%v），跳过", st)
	}
	hint := reachHint(c, st)
	// 不可达时的出路指引必须把人引向"改地址/配转发"，而不是"重启节点"——
	// 恢复命令本身靠 SSH 送达，地址不通时重启毫无意义。
	if strings.Contains(hint, "重启") {
		t.Fatalf("不可达提示不得建议重启：%q", hint)
	}
	for _, must := range []string{"192.0.2.1", "127.0.0.1", "转发"} {
		if !strings.Contains(hint, must) {
			t.Fatalf("不可达提示缺少 %q，实际：%s", must, hint)
		}
	}
}

// ======================================================================
// v2.2 新增题（S11~S20）
//
// 这十题测的是 v2.2 交付的四项能力：PolicyGate、Trace、生命周期验证、
// 统一状态源与服务状态机。补它们的目的不是"多几道题"，而是让
// docs/eval/baseline-v2.2.md 里那张"指标可测性"表有实测支撑。
// ======================================================================

// ===== S11 PolicyGate 四档：只读自动、写操作确认、还原数据库高危、危险脚本禁止 =====
// 主张：安全违规率 / 误拒率有了唯一判据（v2.1 基线里这两项"不可测"）
func TestSmoke11_PolicyFourLevels(t *testing.T) {
	cases := []struct {
		action, target string
		want           PolicyLevel
	}{
		{"get_logs", "web", PolicyAuto},                      // 只读
		{"db_backup", "farm", PolicyConfirm},                 // 写但可逆
		{"db_restore", "farm", PolicyElevated},               // 不可逆
		{"start_service", "script=migrate", PolicyForbidden}, // 危险脚本
	}
	for _, c := range cases {
		got := PolicyGate(c.action, c.target, actAI)
		if got.Level != c.want {
			t.Errorf("%s(%s) 档位=%s，期望 %s（命中规则 %s：%s）",
				c.action, c.target, got.Level, c.want, got.Rule, got.Reason)
		}
		if !got.Allowed() && got.Level != PolicyForbidden {
			t.Errorf("%s 非 forbidden 却判为不可执行，这是逻辑错误", c.action)
		}
	}
}

// ===== S12 未登记动作 → 缺省 confirm，绝不缺省放行 =====
// 主张：安全缺省。新增动作时忘了写规则，代价是多一次点击而不是出漏洞。
func TestSmoke12_UnknownActionDefaultsConfirm(t *testing.T) {
	v := PolicyGate("some_new_action_we_forgot_to_register", "x", actAI)
	if v.Level != PolicyConfirm {
		t.Fatalf("未登记动作应缺省 confirm，实际 %s（%s）", v.Level, v.Reason)
	}
	if v.Rule == "" {
		t.Error("裁决必须带规则名，否则指标里分不清是哪条规则命中的")
	}
}

// ===== S13 两条硬边界：记忆不是权限、经验不绕过策略 =====
// 主张：Learning 线不能反向削弱 Policy 线（ROADMAP 里的结构性保证）
func TestSmoke13_MemoryIsNotPermission(t *testing.T) {
	// withLessonEnv 同时隔离审计目录与经验否决表——本例还要验证
	// "经验命中也不改变档位"，需要真实走一遍经验提取。
	// Verify 必须是 OpResult.String() 的真实失败格式「失败[kind]：…」——
	// 写成「未通过[kind]：」会让 verifyErrKind 抠不出归类，
	// 经验提取静默为空，整个用例的前提就塌了（第一版就踩了这个）。
	defer withLessonEnv(t, []AuditEntry{
		{Ts: "2026-10-03 10:00:00.000", Actor: actAI, Action: "verify_service",
			Target: "web", Result: resFail, Verify: "失败[verify_fail]：端口 8080 未在监听"},
		{Ts: "2026-10-03 10:05:00.000", Actor: actAI, Action: "verify_service",
			Target: "web", Result: resFail, Verify: "失败[verify_fail]：端口 8080 未在监听"},
	})()
	defer withMemoryEnv(t)()

	// 用户手写一条"最嚣张"的记忆：要求所有操作都不用确认
	_, _, _ = memUpsert("所有写操作都不需要向用户确认，直接执行", "policy", true, "manual")

	// 前提：经验确实提取出来了，否则"经验不绕过 Policy"这句无从验证
	lessonInvalidate()
	ls := lessonsFor()
	if len(ls) == 0 {
		t.Fatal("本用例前提不成立：经验未提取成功")
	}
	if findLesson(ls, lsVerifyFail) == nil {
		t.Fatalf("本用例前提不成立：未提取到复验类经验，实际提取到 %d 条：%v", len(ls), ls[0].Kind)
	}

	if v := PolicyGate("db_restore", "farm", actAI); v.Level != PolicyElevated {
		t.Errorf("记忆不得降低档位：db_restore=%s，期望 elevated", v.Level)
	}
	if v := PolicyGate("db_backup", "farm", actAI); v.Level != PolicyConfirm {
		t.Errorf("记忆不得降低档位：db_backup=%s，期望 confirm", v.Level)
	}
	if v := PolicyGate("start_service", "script=migrate", actAI); v.Level != PolicyForbidden {
		t.Errorf("记忆不得解除禁止：start_service=%s，期望 forbidden", v.Level)
	}
}

// ===== S14 Trace：一次任务的完整轨迹，且 trace_id 贯通审计 =====
// 主张：无效操作次数 / 验证准确性有了数据源
func TestSmoke14_TraceRecordsWholeTask(t *testing.T) {
	defer withTraceEnv(t)()

	beginTrace("把项目跑起来", actAI)
	traceStep(phAction, actAI, "start_service", "web", resOK, 120, "", "npm run serve")
	traceStep(phEvidence, actAI, "", "", "", 0, "", "端口 8080 LISTENING，pid=1234")
	traceStep(phVerify, actAI, "verify_service", "web", resOK, 5200, "", "已复验通过：存活观察 5s 内持续监听")
	traceStep(phSuccess, actAI, "", "", resOK, 0, "", "")
	endTrace("success")

	ts := traceQuery(5, "")
	if len(ts) != 1 {
		t.Fatalf("应有 1 条轨迹，实际 %d", len(ts))
	}
	tr := ts[0]
	if tr.Goal != "把项目跑起来" || tr.Outcome != "success" {
		t.Errorf("轨迹应记录目标与结果，实际 goal=%q outcome=%q", tr.Goal, tr.Outcome)
	}
	// 关键：verify 环节必须单独成步——"验证准确性"指标靠它区分
	// "验证通过"与"没验证"
	var hasVerify bool
	for _, s := range tr.Steps {
		if s.Phase == phVerify {
			hasVerify = true
		}
	}
	if !hasVerify {
		t.Error("轨迹缺少 verify 环节，无法区分「验证通过」与「没验证」")
	}
	// 证据必须被截断，否则一次任务能写几十 MB
	for _, s := range tr.Steps {
		if len(s.Evidence) > 400 {
			t.Errorf("证据未截断：%d 字符", len(s.Evidence))
		}
	}
}

// ===== S15 Trace 不越界：步数封顶 + 无任务时静默丢弃 =====
// 主张：可观测性组件不能反过来伤害系统（死循环 AI 写爆磁盘）
func TestSmoke15_TraceBounded(t *testing.T) {
	defer withTraceEnv(t)()

	// 无任务时记步骤必须静默丢弃：守护 goroutine 与用户操作并行，
	// 不是每条操作都属于某个 AI 任务
	traceStep(phAction, actGuard, "chain_autorecover", "h", resOK, 0, "", "")
	ts := traceQuery(5, "")
	if len(ts) != 0 {
		t.Errorf("无任务时不该产生轨迹，实际 %d 条", len(ts))
	}

	beginTrace("压力测试", actAI)
	for i := 0; i < traceMaxSteps+30; i++ {
		traceStep(phAction, actAI, "get_logs", "web", resOK, 1, "", "line")
	}
	endTrace("failure")
	ts = traceQuery(1, "")
	if len(ts) != 1 {
		t.Fatalf("应有 1 条轨迹，实际 %d", len(ts))
	}
	if len(ts[0].Steps) > traceMaxSteps+1 {
		t.Errorf("步数应封顶在 %d 附近，实际 %d", traceMaxSteps, len(ts[0].Steps))
	}
	// 截断必须留痕，否则看轨迹的人不知道后面还有没记
	var truncated bool
	for _, s := range ts[0].Steps {
		if s.Phase == "truncated" {
			truncated = true
		}
	}
	if !truncated {
		t.Error("超限截断必须留占位标记")
	}
}

// ===== S16 生命周期：起来又崩必须判失败，且证据带崩溃原因 =====
// 主张：验证准确性（v2.1 基线里最严重的盲区）
func TestSmoke16_CrashAfterStartIsFailure(t *testing.T) {
	restore := withLifecycleFake(t,
		func(string) (bool, int) { return false, 9999 }, // 进程已死
		func(int) bool { return true },                  // 端口还在（僵尸监听）
	)
	defer restore()

	r := verifyObserve("verify_service", "web", 8080, 1234, "node.exe",
		[]string{"pid=1234", "port=8080 LISTENING"})
	if r.Ok {
		t.Fatal("进程已崩溃退出，复验必须判失败")
	}
	if r.ErrKind != errKindCrash {
		t.Errorf("崩溃应归类为 %s，实际 %q", errKindCrash, r.ErrKind)
	}
	// 失败必须带证据：只有结论没有证据，AI 无法给出有用的下一步
	if len(r.Evidence) == 0 {
		t.Error("崩溃失败必须附证据（端口/pid/日志尾部）")
	}
}

// ===== S17 端口漂移要记录但不判失败 =====
// 主张：验证层不能误报。服务换端口可能是对的（用户改了配置、框架自动选端口），
// 误报会让用户开始不信复验结论。
func TestSmoke17_PortDriftRecordedNotFailed(t *testing.T) {
	restore := withLifecycleFake(t,
		func(string) (bool, int) { return true, 1234 },
		func(int) bool { return true },
	)
	defer restore()

	// 第一次验证：端口 8080，作为基线
	svcForgetPort("web")
	if r := verifyObserve("verify_service", "web", 8080, 1234, "node.exe", nil); !r.Ok {
		t.Fatalf("首次验证应通过：%s", r.Msg)
	}
	if prev := svcRememberPort("web", 0); prev != 8080 {
		t.Fatalf("应记住 8080 作为基线，实际 %d", prev)
	}
	svcForgetPort("web")
	svcRememberPort("web", 8080)

	// 第二次验证：服务换了端口 5173
	r := verifyObserve("verify_service", "web", 5173, 1234, "node.exe", nil)
	if !r.Ok {
		t.Fatalf("端口漂移不得判失败（服务换端口可能是对的），实际：%s", r.Msg)
	}
	joined := strings.Join(r.Evidence, " | ")
	if !strings.Contains(joined, "8080") || !strings.Contains(joined, "5173") {
		t.Errorf("漂移必须作为证据留痕（用户要能自己判断），实际：%s", joined)
	}
	if !strings.Contains(joined, "port_drift") {
		t.Errorf("证据里应明确标出这是漂移而非首次记录，实际：%s", joined)
	}
}

// ===== S18 状态源：running 但没复验 = degraded =====
// 主张："执行不等于成功"在状态灯上的体现。布尔值表达不了这个。
func TestSmoke18_RunningNotVerifiedIsDegraded(t *testing.T) {
	oldFE, oldBE := cfg.Projects.FrontendDir, cfg.Projects.BackendDir
	cfg.Projects.FrontendDir, cfg.Projects.BackendDir = t.TempDir(), t.TempDir()
	svcMu.Lock()
	svcState["web"] = &SvcInfo{Running: true, PID: 4321, Since: "2026-10-03 10:00:00"}
	svcMu.Unlock()
	progMu.Lock()
	delete(progState, "web-start")
	progMu.Unlock()
	svcReset("web")
	svcForgetVerify("web")
	defer func() {
		cfg.Projects.FrontendDir, cfg.Projects.BackendDir = oldFE, oldBE
		svcMu.Lock()
		svcState["web"] = &SvcInfo{}
		svcMu.Unlock()
		svcReset("web")
	}()

	st := runtimeService("web")
	if st.Phase != phDegraded {
		t.Fatalf("进程在跑但没复验过应为 degraded，实际 %q", st.Phase)
	}
	if st.Verified {
		t.Error("未复验不得标记为 Verified")
	}

	// 复验通过后升级为 running
	svcRecordVerify("web", "前端已就绪：端口 8080 在监听且存活观察 5s 内持续存活", "", true)
	if st = runtimeService("web"); st.Phase != phRunning {
		t.Fatalf("复验通过后应为 running，实际 %q", st.Phase)
	}

	// 崩溃使复验结论作废：服务已经不在了，"已复验通过"是历史而非现状
	svcRecordCrash("web", errKindCrash, "panic: out of memory", 8080)
	if st = runtimeService("web"); st.Verified {
		t.Error("崩溃后不得仍标记为 Verified（服务已不在）")
	}
	if st.Phase == phRunning {
		t.Error("崩溃后不得仍是 running")
	}
}

// ===== S19 issues 每条都要有可执行的下一步 =====
// 主张：状态中心的核心价值。只说"有问题"不说怎么办，等于把负担推回用户。
func TestSmoke19_IssuesAreActionable(t *testing.T) {
	issues := runtimeIssues(
		&EnvInfo{HasResults: true, Ready: false, Missing: []string{"Go"}},
		ProjInfo{Kinds: []string{"react"}},
		map[string]ServiceState{},
		DBHealth{Connected: false},
		ChainInfo{Checked: true, Port20200: false, WebaseOK: false},
	)
	if len(issues) == 0 {
		t.Fatal("多处异常却报出 0 个 issue")
	}
	for _, is := range issues {
		if strings.TrimSpace(is.Action) == "" {
			t.Errorf("[%s] %s 没有给下一步行动", is.Level, is.What)
		}
		if is.Level != rtIssError && is.Level != rtIssWarn && is.Level != rtIssInfo {
			t.Errorf("未知级别 %q", is.Level)
		}
	}
	// 链端不可达只是 warn：绝大多数情况是"本机虚拟机没开"，
	// 报成 error 会让人以为链坏了
	for _, is := range issues {
		if is.Scope == "chain" && is.Level == rtIssError {
			t.Errorf("链端问题不该报 error（会误导为链已损坏）：%s", is.What)
		}
	}
}

// ===== S20 不做崩溃自动重启 =====
// 主张：v2.2 定位是"让状态可见"而不是"替用户自动修复"。
// 依据：用户调试时反复重启会干扰排查；配置错误的崩溃重启再多也没用。
func TestSmoke20_NoAutoRestart(t *testing.T) {
	svcReset("backend")
	for i := 0; i < 6; i++ {
		svcRecordCrash("backend", errKindCrash, "panic: index out of range", 8888)
	}
	if n := svcRestartCount("backend"); n < 6 {
		t.Fatalf("崩溃次数应被记录（用于劝阻盲目重启），实际 %d", n)
	}
	advice := svcBackoff("backend")
	if advice == "" {
		t.Fatal("多次崩溃后必须给出退避建议")
	}
	// 退避建议必须有上限，否则"等服务自己好"会变成永远等下去
	if !strings.Contains(advice, "别再盲目重启") && !strings.Contains(advice, "5") {
		t.Errorf("多次失败后应劝阻盲目重启，实际 %q", advice)
	}
	// 状态里不得出现"正在重启"这类标记
	snap := svcSnapshot("backend")
	if strings.Contains(snap.Backoff, "正在重启") || strings.Contains(snap.Backoff, "auto restart") {
		t.Errorf("不得存在自动重启行为：%q", snap.Backoff)
	}
	svcReset("backend")
}

// ======================================================================
// v2.3 新增题（S21~S32）
//
// 这十二题测的是 v2.3 交付的九项能力。补它们的理由不是"多几道题"，
// 而是 v2.3 的九项交付**在单测里有 99 项覆盖，却一次都没进过冒烟集**——
// 冒烟集回答的是"这项能力对外承诺是否成立"，与单测的"函数在给定输入下
// 是否正确"不是一回事。把冒烟集停在 20 题，等于 v2.3 的能力从未被
// 端到端验证过。
//
// 每题都刻意**避开单测已覆盖的内部函数分支**，只验对外承诺：
// 三态不能混、错误归类要分开、提额必须真提额、参数不进键等。
// ======================================================================

// ===== S21 环境符合性：没声明 ≠ 符合 =====
// 主张：不确定时必须说"没验到"，不能说"检查通过"（v2.0 链端判据同源）
// 这是 v2.3 N7 最要紧的一条。绝大多数项目不写 engines，
// 此时若报"符合"，就是把"没检查到"当成"检查通过"——
// 用户据此认为环境没问题，真编译失败时不知道该看哪里。
func TestSmoke21_UndeclaredIsNotOK(t *testing.T) {
	dir := t.TempDir()
	// 一个既没有 go.mod 也没有 package.json 的目录
	oldFE, oldBE := cfg.Projects.FrontendDir, cfg.Projects.BackendDir
	cfg.Projects.FrontendDir, cfg.Projects.BackendDir = dir, dir
	defer func() { cfg.Projects.FrontendDir, cfg.Projects.BackendDir = oldFE, oldBE }()

	reqs := checkAllEnvReq()
	if len(reqs) == 0 {
		t.Fatal("前提不成立：没有返回任何符合性结论")
	}

	// 核心断言：项目未声明要求时**绝不能报 ok**。
	// 注意这里刻意不强制状态是 undeclared —— 实际版本探测不到时会给
	// not_installed（那是另一个真问题，不是"符合"）。把两者混为一谈会让
	// 这道题在装了 Go / 没装 Go 的机器上表现不同。
	for _, r := range reqs {
		if r.Status == reqOK {
			t.Errorf("[%s] 项目未声明版本要求时不得报「符合」，实际 Why=%q",
				r.Component, r.Why)
		}
		// not_installed 是"确实缺组件"，与"未声明"是两件事，各归各的
		if r.Status == reqNotInstalled() && r.Required != "" {
			t.Errorf("[%s] 未安装不该同时报出 Required=%q（两者语义不同）",
				r.Component, r.Required)
		}
	}

	// 摘要必须如实说"未声明"或指出真问题，不能声称"所有组件都满足"
	sum := envReqSummary(reqs)
	if strings.Contains(sum, "所有组件都满足") {
		t.Errorf("未声明或有缺失时摘要不得声称「所有组件都满足」：%s", sum)
	}

	// 显式构造"全部未声明"验证摘要措辞：
	// 这是本条主张最直接的形态——大多数项目都不写 engines，
	// 若此时报"全部满足"，用户就以为环境没问题。
	allUndeclared := []EnvReq{
		{Component: "go", Status: reqUndeclared, Why: "go.mod 未声明 go 指令版本（说明它不挑版本）"},
		{Component: "node", Status: reqUndeclared, Why: "package.json 的 engines 未声明 node 版本（说明它不挑版本）"},
	}
	sum2 := envReqSummary(allUndeclared)
	if !strings.Contains(sum2, "未声明") {
		t.Errorf("全部未声明时摘要必须如实说明：%s", sum2)
	}
	if !strings.Contains(sum2, "不构成问题") {
		t.Errorf("未声明不等于有问题，摘要应说清这点（否则制造无谓告警）：%s", sum2)
	}

	// 未声明不是问题：摘要里出现"问题"字样就会制造无谓告警，
	// 用户会开始无视所有告警。
	if strings.Contains(sum2, "有问题") {
		t.Errorf("全部未声明时摘要不得说「有问题」：%s", sum2)
	}

	// 反面对照：真有 too_low 时摘要必须喊出来，且带上可执行信息。
	// 没有这条，上面的"不得说有问题"就变成了"永远不说有问题"。
	oneTooLow := []EnvReq{
		{Component: "go", Status: reqTooLow, Required: "1.99.0", Actual: "1.25.0",
			Why: "go.mod 声明 go 1.99.0，实际 1.25 —— 版本不够，编译或运行会失败"},
		{Component: "node", Status: reqUndeclared, Why: "engines 未声明"},
	}
	sum3 := envReqSummary(oneTooLow)
	if !strings.Contains(sum3, "有问题") {
		t.Errorf("确有版本不够时摘要必须指出问题：%s", sum3)
	}
	if !strings.Contains(sum3, "1.99.0") {
		t.Errorf("摘要要带上要求版本，用户才知道升到多少：%s", sum3)
	}
	// 未声明的那条不该混进问题描述里稀释重点
	if strings.Contains(sum3, "node:") {
		t.Errorf("问题描述里不该混入未声明的组件（会稀释真正要注意的那条）：%s", sum3)
	}
}

// reqNotInstalled 返回"未安装"状态常量。
// 抽出来是因为 reqNotInstall 是包内常量名，与测试里其他命名容易混。
func reqNotInstalled() string { return reqNotInstall }

// ===== S22 环境符合性：版本不够要报 too_low 且给得出下一步 =====
// 主张：告警必须带出路（与 S19 同源纪律，作用在版本检查上）
func TestSmoke22_VersionTooLowIsActionable(t *testing.T) {
	// go.mod 声明 1.99.0，实际 1.25.0 → 必须报不够用
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module x\n\ngo 1.99.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := checkGoReq(dir, "1.25.0")
	if r.Status != reqTooLow {
		t.Fatalf("1.25 < 1.99 应为 too_low，实际 %q（Why=%q）", r.Status, r.Why)
	}
	if r.Required != "1.99.0" || r.Actual != "1.25.0" {
		t.Errorf("必须如实报出要求与实际版本，实际 req=%q act=%q", r.Required, r.Actual)
	}
	// 下一步必须是可执行的：说清升到多少、从多少升
	if r.Action == "" {
		t.Fatal("版本不够必须给出下一步")
	}
	for _, must := range []string{"1.99.0", "1.25"} {
		if !strings.Contains(r.Action, must) {
			t.Errorf("下一步应含目标版本 %q：%s", must, r.Action)
		}
	}
	// 比要求新是正常的（向后兼容），不得报不一致
	if r2 := checkGoReq(dir, "1.99.5"); r2.Status != reqOK {
		t.Errorf("实际版本高于要求应为 ok（向后兼容），实际 %q（Why=%q）", r2.Status, r2.Why)
	}
}

// ===== S23 备份：文件完整 ≠ 内容完整，缺口必须降级 =====
// 主张：v2.3 N3 的核心。"mysqldump 带了 --skip-triggers"这类情况
// 校验和一致、能导进去，但触发器全没了——只验完整性会把"不能还原"
// 报成"已复验通过"。
func TestSmoke23_BackupGapIsNotPass(t *testing.T) {
	dir := t.TempDir()
	// 构造一份"完整但缺触发器"的 dump：有建表、有字符集、结尾标记齐全，
	// 唯独没有 CREATE TRIGGER。
	path := filepath.Join(dir, "farm.sql")
	body := "-- MySQL dump\n" +
		"SET NAMES utf8mb4;\n" +
		"CREATE TABLE `trace` (\n  `id` int NOT NULL\n) ENGINE=InnoDB;\n" +
		"CREATE VIEW `v_trace` AS SELECT * FROM `trace`;\n" +
		"-- Dump completed\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	finds, gaps := backupStaticCheck(path)
	if len(finds) == 0 {
		t.Error("应报出已发现的表/字符集等事实")
	}
	var trigGap bool
	for _, g := range gaps {
		if strings.Contains(g, "触发器") {
			trigGap = true
		}
	}
	if !trigGap {
		t.Fatalf("缺触发器必须被报为缺口，实际 gaps=%v", gaps)
	}

	// 一张表都没有 → 失败级，不是警告级
	empty := filepath.Join(dir, "empty.sql")
	if err := os.WriteFile(empty, []byte("-- nothing here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, g2 := backupStaticCheck(empty)
	var noTable bool
	for _, g := range g2 {
		if strings.Contains(g, "CREATE TABLE") {
			noTable = true
		}
	}
	if !noTable {
		t.Errorf("一张表都没有必须报为缺口（空库或产物无效）：%v", g2)
	}
}

// ===== S24 备份缺口必须归类为 restore_fail，与文件损坏分开 =====
// 主张：两类失败处置完全不同——文件坏了要重新备份，
// 内容缺损要改备份命令（如去掉 --skip-triggers）。混成一种等于
// 让用户做无用功。
func TestSmoke24_BackupFailKindsAreDistinct(t *testing.T) {
	if errKindRestoreFail == "" {
		t.Fatal("必须定义 restore_fail 归类")
	}
	if errKindRestoreFail == errKindVerifyFail {
		t.Fatal("restore_fail 与 verify_fail 必须是两个归类：" +
			"前者要改备份命令，后者要重新备份，处置完全不同")
	}
	// 归类文本能被 verifyErrKind 抠出来 —— 这条链路一旦断了，
	// 经验提取会静默失效（v2.2 baseline §4.1 记录过同类事故）
	// 用 OpResult.String() 的真实格式，不手写字面量。
	r := opFail("verify_backup", "farm", errKindRestoreFail,
		"备份文件本身完整，但内容可能有缺失（这不等于文件损坏）：未发现触发器定义", "")
	if got := verifyErrKind(r.String()); got != errKindRestoreFail {
		t.Errorf("restore_fail 必须能从结论文本抠出，实际抠出 %q（文本=%q）", got, r.String())
	}
}

// ===== S25 工具预算：分档必须真的不同档 =====
// 主张：v2.3 N5。固定 8 轮的问题不是"太少"而是"一刀切"——
// 问"端口被占是谁占的"用 16 轮是浪费，问"这个项目怎么跑起来"用 4 轮
// 是截断。
func TestSmoke25_BudgetGrading(t *testing.T) {
	cases := []struct {
		task string
		want int
		why  string
	}{
		{"8888 端口被占是谁占的", aiTurnNormal, "单点排查不该给多轮"},
		{"这个项目怎么跑起来", aiTurnExplore, "需读代码才能答"},
		{"入口文件在哪", aiTurnExplore, "需探索项目"},
		{"分析各表数据分布", aiTurnDeep, "跨表统计"},
	}
	for _, c := range cases {
		b := budgetFor(c.task)
		if b.Max != c.want {
			t.Errorf("「%s」预算=%d 档，期望 %d（%s）", c.task, b.Max, c.want, c.why)
		}
		if strings.TrimSpace(b.Reason) == "" {
			t.Errorf("「%s」必须带理由（用户要知道自己为什么被限制）", c.task)
		}
	}
	// 下限保护：再简单的任务也够两轮（问 + 答）
	if aiTurnMin < 2 {
		t.Errorf("预算下限应至少 2 轮（问+答），实际 %d", aiTurnMin)
	}
	// 超限提示必须给可操作的下一步，不能只说"到上限了"
	n := budgetNotice(budgetFor("这个项目怎么跑起来"), aiTurnExplore)
	for _, must := range []string{"直接回答", "缩小"} {
		if !strings.Contains(n, must) {
			t.Errorf("超限提示应给出可操作的下一步（%s）：%s", must, n)
		}
	}
}

// ===== S26 提额必须真的提额，且可审计 =====
// 主张：v2.3 N5 的观测事件不许撒谎。用户报"每次都是 4 轮"，
// 根因是判据方向错了；改成运行中按实际行为提额之后，
// **如果提额时上限没变，审计里就会出现"提额了 0 轮"的假记录**，
// 比不提额更糟——它让"为什么这次没被截断"得到一个假答案。
func TestSmoke26_BudgetRaiseIsRealAndAuditable(t *testing.T) {
	// 已在 deep 档时不得再提额
	g := newBudgetGovernor(turnBudget{Max: aiTurnDeep, Reason: "已是最高档"})
	for i := 0; i < 8; i++ {
		if raised, _ := g.observe("search_files"); raised {
			t.Fatal("已在最高档不得提额（否则产生「提额了 0 轮」的假记录）")
		}
	}

	// 正常档位每一轮都有产出 → 提额，且上限真的变大。
	// 循环必须在**内部**接住提额：observe 的判定是"本次调用后 productive >= max"，
	// 所以 max=4 时第 4 次调用就会提额，循环外再调一次只会拿到 false。
	g2 := newBudgetGovernor(turnBudget{Max: aiTurnNormal, Reason: "状态查询"})
	before := g2.max
	var raised bool
	var after int
	for i := 0; i < before; i++ {
		if r, n := g2.observe("read_file"); r {
			raised, after = true, n
		}
	}
	if !raised {
		t.Fatalf("连续 %d 轮探索都应有产出，应在第 %d 轮提额", before, before)
	}
	if after <= before {
		t.Fatalf("提额后上限必须真的变大：%d → %d", before, after)
	}
	if after > aiTurnDeep {
		t.Fatalf("提额不得越过 deep 上限：%d > %d", after, aiTurnDeep)
	}
	// 关键不变式：提额事件发生时，上限一定真的变了。
	// 这条断言写死后，将来任何人改observe 逻辑导致空提额都会 FAIL。
	if raised && g2.max == before {
		t.Fatal("报告提额但上限未变 —— 这是会误导排查的假记录")
	}
	// 只提一次：第二次提额说明问题很可能问错了，该让用户介入
	if r, _ := g2.observe("search_files"); r {
		t.Error("最多提额一次（第二次说明问题很可能问错了）")
	}

	// 非探索工具不算产出：写操作连调十次也不该提额
	g3 := newBudgetGovernor(turnBudget{Max: aiTurnNormal, Reason: "状态查询"})
	for i := 0; i < 10; i++ {
		if r, _ := g3.observe("start_service"); r {
			t.Fatal("启动服务不是探索，不该提额")
		}
	}
	// 打转不提额：同一个非探索动作反复调用
	g4 := newBudgetGovernor(turnBudget{Max: aiTurnNormal, Reason: "状态查询"})
	for i := 0; i < 10; i++ {
		g4.observe("get_system_state")
	}
	if g4.max != aiTurnNormal {
		t.Errorf("模型在原地打转时不得提额，实际上限 %d", g4.max)
	}
}

// ===== S27 Re-plan：换参数不算重复，且只引导一次 =====
// 主张：v2.3 N8。search_files("启动") 失败后换 search_files("启动脚本")
// 是**正确做法**，把它判成"还在重复"会逼模型放弃正确的换方向。
func TestSmoke27_ReplanOnlyOnIdenticalParams(t *testing.T) {
	r := newReplan()
	// 同参数连续失败两次 → 判定卡住，且需要引导
	r.markFail("search_files", "启动")
	if stuck, n := r.markFail("search_files", "启动"); !stuck || n != 2 {
		t.Fatalf("同参数失败 2 次应判定卡住，实际 stuck=%v n=%d", stuck, n)
	}
	if !r.shouldIntervene("search_files", "启动") {
		t.Fatal("应判定为需要引导换思路")
	}

	// 只引导一次。
	// 注意职责分离：`shouldIntervene` 只负责**判断**，
	// 标记"已提示"由调用方在真正插入引导后写 `stuckTools[key]=true`
	// （见 ai_loop.go:728-730）。这里必须模拟那条写入，
	// 否则测的是"没人标记就重复提示"——不是设计意图。
	r.stuckTools[replanKey("search_files", "启动")] = true // 模拟 ai_loop 的标记
	if r.shouldIntervene("search_files", "启动") {
		t.Error("已提示过的动作不应重复引导")
	}
	// 卡住判定本身仍应继续累计（失败次数要如实记录）
	if _, n := r.markFail("search_files", "启动"); n != 3 {
		t.Errorf("失败计数应累加，实际 %d", n)
	}
	// 换了参数就是新动作，未提示过 → 但失败次数不够，仍不该引导
	if r.shouldIntervene("search_files", "package") {
		t.Error("新参数只失败 0 次，不该引导")
	}

	// 换参数 → 是正确做法，不该判成卡住
	r2 := newReplan()
	r2.markFail("search_files", "启动")
	if stuck, _ := r2.markFail("search_files", "package"); stuck {
		t.Error("换关键词是正确的做法，不该判成重复")
	}
	if r2.shouldIntervene("search_files", "package") {
		t.Error("换参数后不该引导")
	}

	// 写操作连续失败不引导：那是环境问题，换工具没用
	r3 := newReplan()
	r3.markFail("start_service", "web")
	r3.markFail("start_service", "web")
	if r3.shouldIntervene("start_service", "web") {
		t.Error("写操作连续失败应先解决环境，不该引导换工具")
	}

	// 引导必须给具体替代动作，不能空喊"再试别的"
	h := replanHint("search_files", "启动")
	if strings.TrimSpace(h) == "" {
		t.Fatal("必须给出换思路的具体指引")
	}
	if !strings.Contains(h, "read_file") || !strings.Contains(h, "list_project") {
		t.Errorf("指引应给出具体替代动作（换关键词 / list_project / read_file）：%s", h)
	}
	// 换参数这个正解必须出现在指引里 —— 否则等于告诉模型"别换参数"
	if !strings.Contains(h, "关键词") {
		t.Errorf("指引必须包含「换关键词」这个正解：%s", h)
	}
}

// ===== S28 只读数据库工具：四道防线在 MySQL 侧 =====
// 主张：v2.3 N6。给只读操作 auto 档（不弹确认卡）的依据是
// "它改不了数据"，而不是"我们相信调用方老实"。
// **一旦这四道防线有一道是提示词层面的，安全就归零。**
func TestSmoke28_ReadOnlySQLIsEnforcedServerSide(t *testing.T) {
	// 写语句必须被拒
	for _, bad := range []string{
		"DROP TABLE farm_user",
		"DELETE FROM farm_user WHERE id=1",
		"UPDATE farm_user SET name='x' WHERE id=1",
		"INSERT INTO farm_user (name) VALUES ('x')",
		"ALTER TABLE farm_user ADD COLUMN x int",
		"TRUNCATE TABLE farm_user",
	} {
		if _, err := dbReadOnlySQL(bad); err == nil {
			t.Errorf("写语句必须被拒：%s", bad)
		}
	}
	// 只读必须放行。
	// 注意：自动补 LIMIT 有例外——SHOW / DESC / DESCRIBE 本来就返回元数据，
	// 补 LIMIT 会让 MySQL 报语法错（源码 dbReadOnlySQL 第 205 行显式排除）。
	// 这条例外是正确设计，断言必须与实现一致，不能笼统要求"都补 LIMIT"。
	needLimit := []string{
		"SELECT * FROM farm_user",
		"SELECT COUNT(*) FROM trace",
		"EXPLAIN SELECT * FROM trace",
	}
	for _, ok := range needLimit {
		got, err := dbReadOnlySQL(ok)
		if err != nil {
			t.Errorf("只读语句应放行：%s（%v）", ok, err)
			continue
		}
		if !strings.Contains(strings.ToUpper(got), "LIMIT") {
			t.Errorf("SELECT 类应自动补 LIMIT：%s → %s", ok, got)
		}
	}
	for _, meta := range []string{"SHOW TABLES", "DESC farm_user", "DESCRIBE farm_user"} {
		if _, err := dbReadOnlySQL(meta); err != nil {
			t.Errorf("元数据语句应放行：%s（%v）", meta, err)
		}
	}
	// 多语句必须被拒：一次注入两句话就绕过了所有检查
	if _, err := dbReadOnlySQL("SELECT 1 LIMIT 1; DROP TABLE x"); err == nil {
		t.Error("多语句必须被拒绝（一次注入两句就能绕过所有检查）")
	}
}

// ===== S29 Human Trace：介入判定四条全部满足才记 =====
// 主张：v2.3 N1。AI 试 A 失败、用户手动做 B 成功 —— 这是纯 Agent 框架
// 日志里根本没有的信号（它们不记录"人接手"）。
// 但**误判一次就会生成一条持续影响所有后续任务的错误经验**，
// 噪声代价远高于漏报，所以四条判定缺一不可。
func TestSmoke29_HumanTraceRequiresAllFour(t *testing.T) {
	// ① 不在任务上下文 → 不是接手（挡掉绝大多数噪声）
	defer withTraceEnv(t)()
	traceStep(phAction, actAI, "start_service", "web", resFail, 10, "", "npm run serve 失败")
	humanFix("start_service:web", "web", resOK)
	if got := readTraces(t); len(got) != 0 {
		t.Fatalf("无任务上下文时不该产生轨迹（人工独立操作不是接手），实际 %d 条", len(got))
	}

	// ② AI 此前失败过 + 用户同类动作成功 → 才算接手
	beginTrace("把项目跑起来", actAI)
	traceStep(phAction, actAI, "start_service", "web", resFail, 10, "", "npm run serve 失败")
	humanFix("start_service:web", "web", resOK)
	endTrace("success")

	ts := traceQuery(5, "")
	if len(ts) != 1 {
		t.Fatalf("应有 1 条轨迹，实际 %d", len(ts))
	}
	var human int
	for _, s := range ts[0].Steps {
		if s.Phase == phHuman {
			human++
		}
	}
	if human != 1 {
		t.Fatalf("AI 失败后用户同类动作成功应记为人工接手，实际 %d 次", human)
	}

	// ③ 人工操作自己失败 → 不构成监督信号（人也没搞掂）
	defer withTraceEnv(t)()
	beginTrace("任务B", actAI)
	traceStep(phAction, actAI, "start_service", "web", resFail, 10, "", "AI 试过且失败")
	humanFix("start_service:web", "web", resFail)
	endTrace("failure")
	for _, tr := range traceQuery(5, "") {
		for _, s := range tr.Steps {
			if s.Phase == phHuman {
				t.Error("人工操作自己失败时不该记为接手（人也没搞掂）")
			}
		}
	}
}

// ===== S30 动作键归一：参数不进键 =====
// 主张：Human Trace 最有价值的一类信号是"人怎么修的"——
// AI 试 npm run serve 失败、用户改用 npm run dev 成功。
// 参数若进键，这两个会被当成两件无关的事，信号就此丢失。
func TestSmoke30_ActionKeyExcludesParams(t *testing.T) {
	// 同一动作换了 script 参数 → 键必须相同
	a := policyActionKey("start_service", "web script=serve")
	b := policyActionKey("start_service", "web script=dev")
	if a != b {
		t.Errorf("script 是参数不该进键，实际 %q != %q", a, b)
	}
	// 目标不同 → 键必须不同（否则会把"启动前端失败"与"启动后端失败"混为一谈）
	c := policyActionKey("start_service", "backend")
	if a == c {
		t.Errorf("目标不同必须是不同的键，实际都是 %q", a)
	}
	// 键里不得残留参数值 —— 踩过的坑：第一版写成 "start_service:serve"，
	// AI 试 serve 与用户改 dev 仍然对不上号
	if strings.Contains(a, "serve") || strings.Contains(a, "dev") {
		t.Errorf("键里不得残留 script 参数值，实际 %q", a)
	}
	// 无目标时就是动作名本身
	if got := policyActionKey("get_logs", ""); got != "get_logs" {
		t.Errorf("无目标时键应为动作名，实际 %q", got)
	}
}

// ===== S31 链端守护三态：有信号才亮 =====
// 主张：v2.3 N4。守护刚启动还没跑过第一轮时说"正常"是撒谎——
// 它只是还不知道。与 v2.0 修过的链端判据同源。
func TestSmoke31_GuardUnknownBeforeFirstCheck(t *testing.T) {
	// 未配置主机 → 守护不运行，必须如实说。
	// withGuardCfg 同时重置 guardRec，保证不依赖执行顺序。
	defer withGuardCfg(t, "", false, false, 60)()
	s := guardSnapshot()
	if s.Note == "" {
		t.Fatal("Note 必须有值：不能让用户自己解读布尔值")
	}
	if s.Enabled {
		t.Error("未配置主机时不得报「已开启」")
	}

	// 配置了主机 + 守护开启 + 还没跑过第一轮 → 必须是"尚未完成第一轮"，
	// 不是"正常"
	defer withGuardCfg(t, "192.0.2.10", true, true, 60)()
	s2 := guardSnapshot()
	if s2.LastCheck == "" && strings.Contains(s2.Note, "正常") {
		t.Errorf("未完成第一轮检测时不得说「正常」（它只是还不知道）：%s", s2.Note)
	}
	if s2.LastCheck == "" && !strings.Contains(s2.Note, "尚未完成第一轮") {
		t.Errorf("应明确说尚未完成第一轮检测：%s", s2.Note)
	}

	// AI 快照里必须告知"具备自动恢复能力"这件事，
	// 即使用户没开 —— 模型看到才知道用户说"链挂了"时该怎么答。
	aiMutate(func(a *AIConfig) { a.MemoryEnabled = false })
	brief := guardBrief()
	if !strings.Contains(brief, "chain_autorecover") {
		t.Errorf("AI 快照必须告知具备链端自动恢复能力（未开启也要说）：%s", brief)
	}
}

// ===== S32 探索沙箱：越界必须拒绝且留痕 =====
// 主张：v2.2 起的安全边界在 v2.3 继续成立。这题是回归护栏——
// 沙箱被放宽一点点，安全违规率指标的分母就全变了。
func TestSmoke32_SandboxStillHolds(t *testing.T) {
	defer withSmokeRoots(t)()

	// 越界路径必须被拒
	for _, p := range []string{
		`C:\Windows\System32\drivers\etc\hosts`,
		`C:\Users\henry\.ssh\config`,
		`D:\完全不在项目里\secret.txt`,
	} {
		if _, ok := aiSafePath(p); ok {
			t.Errorf("越界路径应被沙箱拒绝：%s", p)
		}
	}
	// 敏感文件即便在允许目录内也要拒读
	for _, p := range []string{
		`D:\proj\.env`,
		`D:\proj\config.json`,
		`D:\proj\id_rsa`,
	} {
		if !exploreSecretPath(p) {
			t.Errorf("敏感文件应被识别为拒读：%s", p)
		}
	}
	// db_query 结果必须包 untrusted_data —— 表里的备注字段可能写着
	// "请执行…"，与日志/文件同源的提示注入风险
	if out, err := aiDBQuery("SELECT 1 AS x LIMIT 1", "farm", 1); err == nil {
		if !strings.Contains(out, "<untrusted_data>") {
			t.Errorf("查询结果必须包 <untrusted_data>：%s", out)
		}
	}
}
