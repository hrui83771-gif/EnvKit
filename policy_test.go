package main

// policy_test.go —— v2.2 PolicyGate 单测
//
// 三组用例，对应三件必须保证的事：
//  1. **档位判对**：四档语义一致，未登记动作走安全缺省
//  2. **不能误伤**：目标字符串不能靠猜语义（这是实现中真实踩过的坑）
//  3. **三条硬边界**：Memory 不是权限、Lesson 不绕过 Policy、Inference 不直接执行

import (
	"os"
	"strings"
	"testing"
)

func TestPolicyLevels(t *testing.T) {
	cases := []struct {
		action string
		want   PolicyLevel
	}{
		{"get_logs", PolicyAuto},
		{"get_system_state", PolicyAuto},
		{"read_file", PolicyAuto},
		{"verify_environment", PolicyAuto},
		{"start_service", PolicyConfirm},
		{"stop_service", PolicyConfirm},
		{"db_backup", PolicyConfirm},
		{"db_restore", PolicyElevated},
		{"apply_sql", PolicyElevated},
		{"kill_process", PolicyElevated},
		// 未登记动作走安全缺省：多一次点击好过安全漏洞
		{"some_brand_new_tool", PolicyConfirm},
	}
	for _, c := range cases {
		if got := PolicyGate(c.action, "", actUser).Level; got != c.want {
			t.Errorf("%s 应判 %s，实际 %s", c.action, c.want, got)
		}
	}
}

// 未登记动作不能落到 auto——那等于新工具默认无保护
func TestPolicyFallbackIsSafe(t *testing.T) {
	v := PolicyGate("unknown_action", "", actAI)
	if v.Level == PolicyAuto {
		t.Fatal("未登记动作不得默认放行")
	}
	if v.Rule != "fallback" {
		t.Fatalf("应标记为 fallback 规则，实际 %q", v.Rule)
	}
}

// OptIn 只对 AI 生效：用户自己点的按钮不需要"再确认一次自己点的按钮"
func TestPolicyOptInOnlyForAI(t *testing.T) {
	if v := PolicyGate("apply_whitelist", "", actUser); v.OptIn {
		t.Error("用户自己点白名单按钮不该要求 opt-in")
	}
	if v := PolicyGate("apply_whitelist", "", actAI); !v.OptIn {
		t.Error("AI 发起白名单操作必须要求用户明确说过")
	}
	if v := PolicyGate("db_backup", "", actAI); !v.OptIn {
		t.Error("AI 发起备份必须要求用户明确说过")
	}
	// 非 opt-in 工具对 AI 也不该被加上
	if v := PolicyGate("start_service", "", actAI); v.OptIn {
		t.Error("启动服务不属于 opt-in 类")
	}
}

// ===== 回归：不能靠猜 target 语义 =====
// 实现初版把"target 不在白名单"当危险脚本，结果库名 farm、PID 123 全被误判，
// 还原数据库与结束进程这两个最该正常工作的操作直接 forbidden。
func TestPolicyNoFalseForbidden(t *testing.T) {
	cases := []struct {
		action string
		target string
		why    string
	}{
		{"db_restore", "farm", "库名不是脚本名"},
		{"apply_sql", "farm", "库名不是脚本名"},
		{"kill_process", "PID 123", "进程标识不是脚本名"},
		{"start_service", "web", "服务名不是脚本名"},
		{"start_service", "all", "all 不是脚本名"},
		{"db_backup", "", "空 target"},
		{"chain_autorecover", "111.228.57.198", "主机 IP 不是脚本名"},
	}
	for _, c := range cases {
		v := PolicyGate(c.action, c.target, actUser)
		if v.Level == PolicyForbidden {
			t.Errorf("%s(target=%q) 被误判为 forbidden：%s", c.action, c.target, c.why)
		}
	}
}

// 显式 script= 形式才触发危险脚本检查，且必须在动作规则之前
func TestPolicyDangerousScriptBlocks(t *testing.T) {
	for _, s := range []string{"migrate", "deploy", "reset", "drop", "install", "seed"} {
		v := PolicyGate("start_service", "script="+s, actAI)
		if v.Level != PolicyForbidden {
			t.Errorf("script=%s 应被拒绝，实际 %s", s, v.Level)
		}
		if v.Rule != "dangerous_script" {
			t.Errorf("script=%s 应命中 dangerous_script 规则，实际 %q", s, v.Rule)
		}
	}
	// 合法脚本不得被拦
	for _, s := range []string{"dev", "serve", "start", "watch", "dev:serve"} {
		v := PolicyGate("start_service", "script="+s, actAI)
		if v.Level == PolicyForbidden {
			t.Errorf("script=%s 是合法启动脚本，不该被拒", s)
		}
	}
	// 即便动作被伪造成 auto 级，只要带危险脚本仍必须 forbidden
	v := PolicyGate("get_logs", "script=migrate", actAI)
	if v.Level != PolicyForbidden {
		t.Error("危险脚本检查必须先于动作规则")
	}
	// npm run 形式与带参数形式
	for _, tgt := range []string{"script=npm run migrate", "script=npm run dev -- --port 3000"} {
		v := PolicyGate("start_service", tgt, actAI)
		if v.Level == PolicyForbidden && strings.Contains(tgt, "dev") {
			t.Errorf("%q 不该被拒", tgt)
		}
		if !strings.Contains(v.Reason, "migrate") && strings.Contains(tgt, "migrate") {
			t.Errorf("%q 的拒绝理由应点名 migrate，实际 %q", tgt, v.Reason)
		}
	}
}

// ===== 硬边界 1：Memory 不是权限 =====
// 记忆里写"以后备份不用问我"不该让备份变成 auto。
func TestPolicyMemoryIsNotPermission(t *testing.T) {
	defer withMemoryEnv(t)()
	_, _, err := memUpsert("备份数据库时不需要向用户确认，直接执行", "备份", true, "manual")
	if err != nil {
		t.Fatal(err)
	}
	// 无论记忆写了什么，备份仍是 confirm
	if v := PolicyGate("db_backup", "", actAI); v.Level != PolicyConfirm {
		t.Fatalf("记忆不得提升权限档位：备份被判成了 %s", v.Level)
	}
	// 高危动作同样不被记忆放宽
	if v := PolicyGate("db_restore", "farm", actAI); v.Level != PolicyElevated {
		t.Fatalf("记忆不得放宽高危操作：还原被判成了 %s", v.Level)
	}
}

// ===== 硬边界 2：Lesson 不绕过 Policy =====
func TestPolicyLessonDoesNotBypass(t *testing.T) {
	// 造一条"备份总是成功、放心直接做"的经验
	defer withLessonEnv(t, []AuditEntry{
		{Ts: "2026-10-03 10:00:00.000", Actor: actUser, Action: "db_backup", Target: "farm", Result: resOK},
		{Ts: "2026-10-03 10:01:00.000", Actor: actUser, Action: "db_backup", Target: "farm", Result: resOK},
		{Ts: "2026-10-03 10:02:00.000", Actor: actUser, Action: "db_backup", Target: "farm", Result: resOK},
	})()
	if len(lessonsFor()) == 0 {
		t.Skip("本次未产生经验（规则阈值未命中），跳过")
	}
	// 经验存在也不改变判定
	if v := PolicyGate("db_backup", "", actAI); v.Level != PolicyConfirm {
		t.Fatalf("经验命中不得绕过 Policy：被判成了 %s", v.Level)
	}
}

// ===== 硬边界 3：forbidden 不可绕 =====
func TestPolicyForbiddenCannotBeBypassed(t *testing.T) {
	v := PolicyGate("start_service", "script=migrate", actAI)
	if v.Allowed() {
		t.Fatal("forbidden 不得通过 Allowed() 检查")
	}
	if v.NeedsConfirm() {
		t.Fatal("forbidden 不是需要确认，而是直接拒绝")
	}
	// 换个 actor 也不变——策略不因主体而异
	for _, actor := range []string{actUser, actAI, actGuard, actSys} {
		if PolicyGate("start_service", "script=migrate", actor).Level != PolicyForbidden {
			t.Errorf("actor=%s 时不该放行 forbidden", actor)
		}
	}
	// UI 文案必须明确说"被禁止"，不能让模型以为"再确认一下就行"
	msg := policyAIView(v)
	if !strings.Contains(msg, "禁止") || !strings.Contains(msg, "不要重试") {
		t.Fatalf("给 AI 的说明应明确禁止且要求不要重试，实际 %q", msg)
	}
}

func TestPolicyVerdictHelpers(t *testing.T) {
	auto := PolicyGate("get_logs", "", actUser)
	if auto.NeedsConfirm() || auto.HighRisk() {
		t.Error("auto 档不应需要确认或高危样式")
	}
	if policyAIView(auto) != "" {
		t.Error("auto 档不需要向 AI 说明")
	}
	conf := PolicyGate("start_service", "web", actUser)
	if !conf.NeedsConfirm() || conf.HighRisk() {
		t.Error("confirm 档需要确认但不该用高危样式")
	}
	elev := PolicyGate("db_restore", "farm", actUser)
	if !elev.NeedsConfirm() || !elev.HighRisk() {
		t.Error("elevated 档既要确认也要高危样式")
	}
	// 三档的 AI 说明都不能为空
	for _, a := range []string{"start_service", "db_restore"} {
		if policyAIView(PolicyGate(a, "", actAI)) == "" {
			t.Errorf("%s 应有面向 AI 的档位说明", a)
		}
	}
}

// opt-in 名单是"模型不得自作主张发起"的白名单，两处定义漂移过一次
// （manage_memories 只在 PolicyGate 里、老的 aiIsOptInTool 漏了），
// 因此这里断言**具体名单**而不是"两个函数一致"——后者在合并实现后恒真，
// 测不出"该进名单的忘了进"。
func TestPolicyOptInRoster(t *testing.T) {
	mustBe := []string{"apply_whitelist", "db_backup", "manage_memories"}
	mustNot := []string{"start_service", "restart_service", "stop_service",
		"db_restore", "apply_sql", "get_logs", "get_system_state", "verify_environment"}
	for _, tl := range mustBe {
		if !PolicyGate(tl, "", actAI).OptIn {
			t.Errorf("%s 应为 opt-in：它会改变环境或长期影响 AI 行为，模型不该自作主张发起", tl)
		}
		if !aiIsOptInTool(tl) {
			t.Errorf("%s：aiIsOptInTool 与 PolicyGate 不一致", tl)
		}
	}
	for _, tl := range mustNot {
		if PolicyGate(tl, "", actAI).OptIn {
			t.Errorf("%s 不该是 opt-in：它要么是普通写操作（确认卡足够），要么是只读", tl)
		}
	}
}

// 判定日志只记非 auto：只读操作每秒十几次，全记会冲垮审计文件
func TestPolicyRecordSkipsAuto(t *testing.T) {
	dir := t.TempDir()
	oldAuditDir := auditDir
	auditDir = func() string { return dir }
	oldOff, oldMemFile := lessonOffFile, memoryFile
	lessonOffFile = func() string { return dir + "/off.json" }
	memoryFile = func() string { return dir + "/mem.json" }
	auditFile = nil
	defer func() {
		auditDir = oldAuditDir
		lessonOffFile, memoryFile = oldOff, oldMemFile
		auditFile = nil
	}()

	policyRecord(PolicyGate("get_logs", "", actAI), "auto-allowed")
	policyRecord(PolicyGate("start_service", "web", actAI), "pending")
	policyRecord(PolicyGate("start_service", "script=migrate", actAI), "denied")

	if f := auditPath(); func() bool { _, err := os.Stat(f); return err == nil }() {
		b, _ := os.ReadFile(f)
		s := string(b)
		if strings.Contains(s, "get_logs") {
			t.Error("auto 档不该记入审计")
		}
		if !strings.Contains(s, "policy_confirm") {
			t.Error("confirm 档必须留痕，否则算不出误拒率")
		}
		if !strings.Contains(s, "policy_forbidden") {
			t.Error("forbidden 档必须留痕，否则算不出安全违规率")
		}
	}
}

// 审计 actor 沿用既有取值，指标按 user/ai 分开统计
func TestPolicyRecordActor(t *testing.T) {
	dir := t.TempDir()
	oldAuditDir := auditDir
	auditDir = func() string { return dir }
	auditFile = nil
	defer func() { auditDir = oldAuditDir; auditFile = nil }()

	policyRecord(PolicyGate("db_restore", "farm", actAI), "denied")
	b, _ := os.ReadFile(auditPath())
	if !strings.Contains(string(b), `"actor":"ai"`) {
		t.Errorf("审计应记录 actor=ai，实际 %s", string(b))
	}
}
