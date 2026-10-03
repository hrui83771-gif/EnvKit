package main

// lesson_test.go —— M1 经验提取的单测
//
// 这些用例盯的是三条底线：
//  1. **该提取的能提取出来**（五条规则各自生效）；
//  2. **不该提取的不提取**（阈值保守，宁可少也不要噪声污染上下文）；
//  3. **不碰用户真实文件**（auditDir / lessonOffFile 全部替换到临时目录）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withLessonEnv 把审计目录与否决表都指到临时目录，并预置审计内容。
// 返回恢复函数。auditDir / lessonOffFile 都是包级变量，测试必须替换。
func withLessonEnv(t *testing.T, entries []AuditEntry) func() {
	t.Helper()
	dir := t.TempDir()
	oldAudit, oldOff := auditDir, lessonOffFile
	auditDir = func() string { return dir }
	lessonOffFile = func() string { return filepath.Join(dir, "lessons-off.json") }

	// 按天分文件写入，模拟真实布局
	byDay := map[string][]AuditEntry{}
	for _, e := range entries {
		day := "20261003"
		if len(e.Ts) >= 10 {
			day = strings.ReplaceAll(e.Ts[:10], "-", "")
		}
		byDay[day] = append(byDay[day], e)
	}
	for day, es := range byDay {
		var sb strings.Builder
		for _, e := range es {
			sb.WriteString(auditJSONLine(t, e))
			sb.WriteString("\n")
		}
		if err := os.WriteFile(filepath.Join(dir, "audit-"+day+".jsonl"), []byte(sb.String()), 0600); err != nil {
			t.Fatalf("写入审计文件失败：%v", err)
		}
	}
	lessonInvalidate()
	return func() {
		auditDir, lessonOffFile = oldAudit, oldOff
		lessonInvalidate()
	}
}

func auditJSONLine(t *testing.T, e AuditEntry) string {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("序列化审计条目失败：%v", err)
	}
	return string(b)
}

func ev(ts, actor, action, target, result string) AuditEntry {
	return AuditEntry{Ts: ts, Actor: actor, Action: action, Target: target, Result: result}
}

func findLesson(ls []Lesson, kind string) *Lesson {
	for i := range ls {
		if ls[i].Kind == kind {
			return &ls[i]
		}
	}
	return nil
}

// R1：同一件事反复失败
func TestLessonRepeatFail(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		ev("2026-10-03 10:00:00.000", actAI, "start_service", "web", resFail),
		ev("2026-10-03 10:01:00.000", actAI, "start_service", "web", resFail),
		ev("2026-10-03 10:02:00.000", actAI, "start_service", "web", resFail),
	})()

	ls := lessonsFor()
	l := findLesson(ls, lsRepeatFail)
	if l == nil {
		t.Fatalf("3 次连续失败应产出 repeat_fail 经验，实际：%+v", ls)
	}
	if l.Hits != 3 {
		t.Fatalf("命中次数应为 3，实际 %d", l.Hits)
	}
	if !strings.Contains(l.Title, "别再原样重试") {
		t.Fatalf("经验文案必须劝阻原样重试，实际：%s", l.Title)
	}
	if l.Precheck == "" {
		t.Fatal("启动类失败必须带前置检查建议（M2 靠它）")
	}
}

// 阈值：2 次不算"反复"——宁可少提取，也不要噪声
func TestLessonRepeatBelowThreshold(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		ev("2026-10-03 10:00:00.000", actAI, "start_service", "web", resFail),
		ev("2026-10-03 10:01:00.000", actAI, "start_service", "web", resFail),
	})()

	if l := findLesson(lessonsFor(), lsRepeatFail); l != nil {
		t.Fatalf("2 次失败未达阈值，不该产出经验：%s", l.Title)
	}
}

// R2：AI 试错 → 用户手动修好（信息量最大的一条规则）
func TestLessonUserFix(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		ev("2026-10-03 10:00:00.000", actAI, "db_backup", "farm", resFail),
		ev("2026-10-03 10:00:30.000", actUser, "db_backup", "farm", resOK),
	})()

	l := findLesson(lessonsFor(), lsUserFix)
	if l == nil {
		t.Fatal("AI 失败后用户手动成功应产出 user_fix 经验")
	}
	if l.Precheck != "ask_user_first" {
		t.Fatalf("这类经验应建议先问用户，实际 precheck=%q", l.Precheck)
	}
	if len(l.Evidence) != 2 {
		t.Fatalf("证据应含失败与成功两条，实际 %d 条", len(l.Evidence))
	}
}

// 反例：AI 失败后**AI 自己**成功了，不该记成"用户修好的"
func TestLessonUserFixNotWhenAISucceeds(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		ev("2026-10-03 10:00:00.000", actAI, "db_backup", "farm", resFail),
		ev("2026-10-03 10:00:30.000", actAI, "db_backup", "farm", resOK),
	})()

	if l := findLesson(lessonsFor(), lsUserFix); l != nil {
		t.Fatalf("AI 自己重试成功不算 user_fix，实际：%s", l.Title)
	}
}

// 反例：间隔太久（不是同一轮排查）不该归因成"你修好的"
func TestLessonUserFixOutsideWindow(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		ev("2026-10-03 10:00:00.000", actAI, "db_backup", "farm", resFail),
		ev("2026-10-03 18:00:00.000", actUser, "db_backup", "farm", resOK),
	})()

	if l := findLesson(lessonsFor(), lsUserFix); l != nil {
		t.Fatalf("相隔 8 小时不该判定为同一次排查的修复：%s", l.Title)
	}
}

// R3：反复被安全策略拒绝（能纠正 AI 自身的行为模式）
// 数据必须按真实格式造：target 存工具名，路径在 detail 里（见 aiExploreDenied）。
func TestLessonDenied(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		{Ts: "2026-10-03 10:00:00.000", Actor: actAI, Action: "explore_denied", Target: "read_file",
			Result: resFail, Detail: "(该文件因安全策略不可读)：D:\\x\\config.json"},
		{Ts: "2026-10-03 10:05:00.000", Actor: actAI, Action: "explore_denied", Target: "read_file",
			Result: resFail, Detail: "(该文件因安全策略不可读)：D:\\x\\config.json"},
	})()

	l := findLesson(lessonsFor(), lsDenied)
	if l == nil {
		t.Fatal("同一路径被拒 2 次应产出 denied 经验")
	}
	if !strings.Contains(l.Title, "别再试") {
		t.Fatalf("文案应劝阻重试，实际：%s", l.Title)
	}
}

// R4：慢路径
func TestLessonSlow(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		{Ts: "2026-10-03 10:00:00.000", Actor: actUser, Action: "db_backup", Target: "farm", Result: resOK, DurMs: 1000},
		{Ts: "2026-10-03 10:01:00.000", Actor: actUser, Action: "db_backup", Target: "farm", Result: resOK, DurMs: 1100},
		{Ts: "2026-10-03 10:02:00.000", Actor: actUser, Action: "db_backup", Target: "farm", Result: resOK, DurMs: 1050},
		{Ts: "2026-10-03 10:03:00.000", Actor: actUser, Action: "db_backup", Target: "farm", Result: resOK, DurMs: 90000},
		{Ts: "2026-10-03 10:04:00.000", Actor: actUser, Action: "db_backup", Target: "farm", Result: resOK, DurMs: 85000},
	})()

	l := findLesson(lessonsFor(), lsSlow)
	if l == nil {
		t.Fatal("出现远超中位数的慢路径应产出 slow 经验")
	}
	if !strings.Contains(l.Title, "慢路径") {
		t.Fatalf("文案应点明慢路径，实际：%s", l.Title)
	}
}

// R5：复验反复同一归类
func TestLessonVerifyFail(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		{Ts: "2026-10-03 10:00:00.000", Actor: actAI, Action: "verify_chain", Target: "h", Result: resFail, Verify: "失败[verify_fail]：节点端口 20200 未在监听"},
		{Ts: "2026-10-03 10:05:00.000", Actor: actAI, Action: "verify_chain", Target: "h", Result: resFail, Verify: "失败[verify_fail]：节点端口 20200 未在监听"},
	})()

	l := findLesson(lessonsFor(), lsVerifyFail)
	if l == nil {
		t.Fatal("同一 err_kind 复验失败 2 次应产出 verify_fail 经验")
	}
	if !strings.Contains(l.Title, "换思路") {
		t.Fatalf("文案应劝阻重复同一操作，实际：%s", l.Title)
	}
}

// 回归：成功的复验绝不���产出 verify_fail 经验。
// 第一版 R5 不看 result 字段，直接去 verify 文本里找方括号/冒号抠错误码，
// 结果把「已复验通过：链端已恢复且共识在推进」也当成了失败模式——
// 于是模型每次都被告知"这类操作重试没用"，而它其实成功了。
// 判断成败有现成的 result 字段，不该去解析中文文本。
func TestLessonVerifySuccessNotMisread(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		{Ts: "2026-10-03 10:00:00.000", Actor: actAI, Action: "verify_chain", Target: "h", Result: resOK,
			Verify: "已复验通过：链端已恢复且共识在推进：视图 869695 → 869701"},
		{Ts: "2026-10-03 10:05:00.000", Actor: actAI, Action: "verify_chain", Target: "h", Result: resOK,
			Verify: "已复验通过：链端已恢复且共识在推进：视图 869801 → 869807"},
		{Ts: "2026-10-03 10:06:00.000", Actor: actAI, Action: "verify_service", Target: "web", Result: resOK,
			Verify: "已复验通过：前端已就绪：进程存活且端口 8080 在监听"},
		{Ts: "2026-10-03 10:07:00.000", Actor: actAI, Action: "verify_service", Target: "web", Result: resOK,
			Verify: "已复验通过：前端已就绪：进程存活且端口 8080 在监听"},
	})()

	if l := findLesson(lessonsFor(), lsVerifyFail); l != nil {
		t.Fatalf("成功的复验不该产出失败经验，实际：%s", l.Title)
	}
}

// 回归：R3 必须从 detail 取路径，target 存的是工具名。
// 第一版直接用 target，生成的是「别再试 read_file」这种零信息量的经验。
func TestLessonDeniedUsesDetailPath(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		{Ts: "2026-10-03 10:00:00.000", Actor: actAI, Action: "explore_denied", Target: "read_file",
			Result: resFail, Detail: "拒绝读取：路径不在已配置的项目目录内（D:\\BCGD\\FarmTrace）"},
		{Ts: "2026-10-03 10:05:00.000", Actor: actAI, Action: "explore_denied", Target: "read_file",
			Result: resFail, Detail: "拒绝读取：路径不在已配置的项目目录内（D:\\BCGD\\FarmTrace）"},
	})()

	l := findLesson(lessonsFor(), lsDenied)
	if l == nil {
		t.Fatal("同一路径被拒 2 次应产出 denied 经验")
	}
	if !strings.Contains(l.Title, "D:\\BCGD\\FarmTrace") {
		t.Fatalf("经验应指向具体路径而不是工具名，实际：%s", l.Title)
	}
	if strings.Contains(l.Title, "read_file") {
		t.Fatalf("经验文案不该拿工具名充数，实际：%s", l.Title)
	}
}

// 详情里取不到路径就不产出经验——宁缺毋滥
func TestLessonDeniedNoPathNoLesson(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		{Ts: "2026-10-03 10:00:00.000", Actor: actAI, Action: "explore_denied", Target: "read_file",
			Result: resFail, Detail: "该文件因安全策略不可读"},
		{Ts: "2026-10-03 10:05:00.000", Actor: actAI, Action: "explore_denied", Target: "read_file",
			Result: resFail, Detail: "该文件因安全策略不可读"},
	})()

	if l := findLesson(lessonsFor(), lsDenied); l != nil {
		t.Fatalf("取不到路径时不该产出经验，实际：%s", l.Title)
	}
}

// 用户可否决经验（正文不可编辑，只能否决——否则等于给伪造证据开口子）
func TestLessonToggle(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		ev("2026-10-03 10:00:00.000", actAI, "start_service", "web", resFail),
		ev("2026-10-03 10:01:00.000", actAI, "start_service", "web", resFail),
		ev("2026-10-03 10:02:00.000", actAI, "start_service", "web", resFail),
	})()

	l := findLesson(lessonsFor(), lsRepeatFail)
	if l == nil || !l.Enabled {
		t.Fatal("新提取的经验默认应启用")
	}
	if enabled, ok := lessonToggle(l.ID); !ok || enabled {
		t.Fatalf("第一次 toggle 应变为禁用，实际 enabled=%v ok=%v", enabled, ok)
	}
	lessonInvalidate()
	l2 := findLesson(lessonsFor(), lsRepeatFail)
	if l2 == nil || l2.Enabled {
		t.Fatal("否决后刷新应显示为禁用")
	}
	if enabled, ok := lessonToggle(l2.ID); !ok || !enabled {
		t.Fatalf("第二次 toggle 应恢复启用，实际 enabled=%v ok=%v", enabled, ok)
	}
}

func TestLessonToggleUnknownID(t *testing.T) {
	defer withLessonEnv(t, nil)
	if _, ok := lessonToggle("repeat_fail|nonexistent|"); ok {
		t.Fatal("不存在的经验 id 不该被写入否决表")
	}
}

// 注入形态：只给结论，不带证据（证据进上下文就是浪费 token）
func TestLessonBriefNoEvidence(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		ev("2026-10-03 10:00:00.000", actAI, "start_service", "web", resFail),
		ev("2026-10-03 10:01:00.000", actAI, "start_service", "web", resFail),
		ev("2026-10-03 10:02:00.000", actAI, "start_service", "web", resFail),
	})()

	b := lessonBrief(lessonsForTask("", 5))
	if b == "" {
		t.Fatal("应有可注入的经验文本")
	}
	if strings.Contains(b, "2026-10-03") {
		t.Fatalf("注入文本不该带审计时间戳（那是证据，留在详情里）：%s", b)
	}
}

// 全量注入会炸上下文，必须按任务相关性收敛
func TestLessonsForTaskLimitsCount(t *testing.T) {
	var es []AuditEntry
	for i := 0; i < 8; i++ {
		es = append(es,
			ev("2026-10-03 10:0"+string(rune('0'+i))+":00.000", actAI, "start_service", "web", resFail),
			ev("2026-10-03 11:0"+string(rune('0'+i))+":00.000", actAI, "db_backup", "farm", resFail),
			ev("2026-10-03 12:0"+string(rune('0'+i))+":00.000", actAI, "verify_chain", "h", resFail),
		)
	}
	defer withLessonEnv(t, es)

	if got := lessonsForTask("随便什么任务", 3); len(got) > 3 {
		t.Fatalf("应最多返回 3 条，实际 %d", len(got))
	}
}

// 空环境不能崩，且返回空数组而不是 null
func TestLessonsEmpty(t *testing.T) {
	defer withLessonEnv(t, nil)
	ls := lessonsFor()
	if ls == nil {
		t.Fatal("无数据时必须返回空数组，前端会直接遍历")
	}
	if len(ls) != 0 {
		t.Fatalf("无审计时不该产出经验，实际 %d 条", len(ls))
	}
	if s := lessonBrief(ls); s != "" {
		t.Fatalf("无经验时注入文本应为空，实际 %q", s)
	}
}

func TestVerifyErrKind(t *testing.T) {
	cases := map[string]string{
		"失败[verify_fail]：节点端口 20200 未在监听": "verify_fail",
		"失败[spawn_fail]：进程已退出":            "spawn_fail",
		// 成功结论里没有「失败[kind]」锚点，必须取不到错误码
		"已复验通过：链端已恢复且共识在推进：视图 869695 → 869701": "",
		"未复验：节点端口 20200 未在监听（可能仍在编译）":          "",
		"没有归类信息": "",
	}
	for in, want := range cases {
		if got := verifyErrKind(in); got != want {
			t.Fatalf("verifyErrKind(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestPrecheckFor(t *testing.T) {
	if precheckFor("start_service") == "" {
		t.Fatal("启动类动作必须有前置检查建议")
	}
	if got := precheckFor("apply_whitelist"); got != "" {
		t.Fatalf("无关动作不该硬塞预检建议，实际 %q", got)
	}
}
