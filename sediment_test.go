package main

// sediment_test.go —— Phase 3 验收 1：一场处置收口后相关经验自动出现。
//
// 全部直接喂 AuditEntry：R6 的输入就是审计，绕过审计等于在测另一套代码。
// missionSediment 单独验它写出的条目长什么样（格式即协议，改了 R6 会静默失效）。
//
// 这里最要紧的两条反向断言：
//   - **用户取消不算失败**。取消三次不该得出「这个服务修不好」。
//   - **成功会打断连败**。否则一条经验一旦生成就永远在说反话。

import (
	"strings"
	"testing"
)

// sfe 构造一条沉淀审计条目，模拟 missionSediment 的输出格式。
func sfe(ts, svc, result, state string) AuditEntry {
	return AuditEntry{
		Ts: ts, Actor: actSys, Action: msSinkAction, Target: svc,
		Result: result, Detail: "[" + state + "] goal=修复 " + svc,
	}
}

func lessonByID(ls []Lesson, id string) *Lesson {
	for i := range ls {
		if ls[i].ID == id {
			return &ls[i]
		}
	}
	return nil
}

// 连续 3 次失败 → 目标级 repeat_fail
func TestSedimentRepeatAcrossMissions(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		sfe("2026-10-03 10:00:00.000", "backend", resFail, msFailed),
		sfe("2026-10-03 11:00:00.000", "backend", resFail, msFailed),
		sfe("2026-10-03 12:00:00.000", "backend", resFail, msFailed),
	})()

	l := lessonByID(lessonsFor(), lsID(lsRepeatFail, msSinkAction, "backend"))
	if l == nil {
		t.Fatalf("同一服务连续 3 次失败应产出目标级经验，实际：%+v", lessonsFor())
	}
	if l.Hits != 3 {
		t.Errorf("命中次数应为 3，实际 %d", l.Hits)
	}
	// Title 要给人看：必须出现中文服务名，否则界面上是「backend 上的处置」
	if !strings.Contains(l.Title, "后端") {
		t.Errorf("标题应使用中文服务名（给人看的），实际：%s", l.Title)
	}
	if !strings.Contains(l.Title, "别") && !strings.Contains(l.Title, "不要") {
		t.Errorf("标题必须劝阻重试，实际：%s", l.Title)
	}
}

// 中间成功过一次 → 连败清零，不能拿历史总失败数下结论
func TestSedimentSuccessResetsStreak(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		sfe("2026-10-03 09:00:00.000", "backend", resFail, msFailed),
		sfe("2026-10-03 09:10:00.000", "backend", resFail, msFailed),
		sfe("2026-10-03 09:20:00.000", "backend", resOK, msDone), // 修好了
		sfe("2026-10-03 10:00:00.000", "backend", resFail, msFailed),
		sfe("2026-10-03 11:00:00.000", "backend", resFail, msFailed),
	})()

	if l := lessonByID(lessonsFor(), lsID(lsRepeatFail, msSinkAction, "backend")); l != nil {
		t.Fatalf("成功后应重新计数，实际仍生成连败经验：%+v", *l)
	}
}

// 用户取消不是失败——这是本文件最重要的一条反向断言
func TestSedimentCanceledIsNotFailure(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		sfe("2026-10-03 10:00:00.000", "backend", resUnverified, msCanceled),
		sfe("2026-10-03 11:00:00.000", "backend", resUnverified, msCanceled),
		sfe("2026-10-03 12:00:00.000", "backend", resUnverified, msCanceled),
	})()

	if l := lessonByID(lessonsFor(), lsID(lsRepeatFail, msSinkAction, "backend")); l != nil {
		t.Fatalf("用户取消 3 次不等于失败 3 次，不该生成「修不好」经验：%+v", *l)
	}
}

// 用户取消后自己动手做成 → user_fix（R2 抓不到的一类：动作名对不上）
func TestSedimentUserFixAfterCancel(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		sfe("2026-10-03 10:00:00.000", "backend", resUnverified, msCanceled),
		{Ts: "2026-10-03 10:12:00.000", Actor: actUser, Action: "config_save",
			Target: "backend", Result: resOK},
	})()

	l := lessonByID(lessonsFor(), lsID(lsUserFix, msSinkAction, "backend"))
	if l == nil {
		t.Fatalf("取消后 12 分钟内用户自己修好，应产出 user_fix，实际：%+v", lessonsFor())
	}
	if l.Precheck != "ask_user_first" {
		t.Errorf("这类经验的结论是「先问用户」，实际 precheck=%q", l.Precheck)
	}
	if len(l.Evidence) != 2 {
		t.Errorf("证据应同时含「取消」与「用户成功」两条，实际 %d 条", len(l.Evidence))
	}
}

// 窗口外不算：用户两小时后恰好重启了服务，不是"接手这次处置"
func TestSedimentUserFixOutOfWindow(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		sfe("2026-10-03 10:00:00.000", "backend", resUnverified, msCanceled),
		{Ts: "2026-10-03 12:30:00.000", Actor: actUser, Action: "start_service",
			Target: "backend", Result: resOK},
	})()

	if l := lessonByID(lessonsFor(), lsID(lsUserFix, msSinkAction, "backend")); l != nil {
		t.Fatalf("超出 30 分钟窗口不该算用户接手，实际生成：%+v", *l)
	}
}

// 超期不是"用户放弃"：没人管和主动取消是两件事
func TestSedimentExpiredIsNotUserFix(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		sfe("2026-10-03 10:00:00.000", "backend", resUnverified, msExpired),
		{Ts: "2026-10-03 10:10:00.000", Actor: actUser, Action: "start_service",
			Target: "backend", Result: resOK},
	})()

	if l := lessonByID(lessonsFor(), lsID(lsUserFix, msSinkAction, "backend")); l != nil {
		t.Fatalf("超期不是用户取消，不该生成 user_fix：%+v", *l)
	}
}

// 别的服务上的成功不能算"修好了这个服务"
func TestSedimentUserFixNeedsSameService(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		sfe("2026-10-03 10:00:00.000", "backend", resUnverified, msCanceled),
		{Ts: "2026-10-03 10:05:00.000", Actor: actUser, Action: "start_service",
			Target: "web", Result: resOK},
	})()

	if l := lessonByID(lessonsFor(), lsID(lsUserFix, msSinkAction, "backend")); l != nil {
		t.Fatalf("web 上的成功不能证明 backend 被修好：%+v", *l)
	}
}

// ===== missionSediment 写出的条目本身 =====

func TestMissionSedimentWritesNormalizedAudit(t *testing.T) {
	defer withLessonEnv(t, nil)()

	missionSediment(Mission{
		ID: "m-1", Goal: "修复后端启动失败", State: msFailed,
		Trigger: Trigger{Kind: "guard", Detail: "backend | 复验未通过", ErrKind: errKindVerifyFail},
		Result:  &OpResult{Verified: false},
	})

	var got *AuditEntry
	for _, e := range auditQuery("all", "", 50) {
		if e.Action == msSinkAction {
			c := e
			got = &c
		}
	}
	if got == nil {
		t.Fatal("终态行动必须写一条沉淀审计，否则 R6 永远没有输入")
	}
	if got.Target != "backend" {
		t.Errorf("target 应是服务 ID（聚合键），实际 %q", got.Target)
	}
	if got.Result != resFail {
		t.Errorf("复验未过应记 fail，实际 %q", got.Result)
	}
	if !strings.HasPrefix(got.Detail, "[failed]") {
		t.Errorf("detail 首段必须是 [state]，R6c 靠它区分取消与超期，实际 %q", got.Detail)
	}
	if !strings.Contains(got.Detail, "err="+errKindVerifyFail) {
		t.Errorf("带症状类别时必须落进 detail，实际 %q", got.Detail)
	}
}

// 用户取消记 unverified，不是 fail
func TestMissionSedimentCanceledIsUnverified(t *testing.T) {
	defer withLessonEnv(t, nil)()

	missionSediment(Mission{
		ID: "m-2", Goal: "修复后端", State: msCanceled,
		Trigger: Trigger{Kind: "guard", Detail: "backend | 复验未通过"},
	})

	for _, e := range auditQuery("all", "", 50) {
		if e.Action != msSinkAction {
			continue
		}
		if e.Result != resUnverified {
			t.Fatalf("取消应记 unverified（未证明），实际 %q", e.Result)
		}
		return
	}
	t.Fatal("取消的终态也该留痕")
}

// 没有服务维度的行动不写沉淀：没有聚合键就会把互不相干的行动算成同一个目标
func TestMissionSedimentSkipsWithoutService(t *testing.T) {
	defer withLessonEnv(t, nil)()

	missionSediment(Mission{
		ID: "m-3", Goal: "晨报 10-27", State: msDone,
		Trigger: Trigger{Kind: "schedule", Detail: "定时产出 08:30"},
		Report:  "brf-2026-10-27.md",
		Result:  &OpResult{Verified: true},
	})

	for _, e := range auditQuery("all", "", 50) {
		if e.Action == msSinkAction {
			t.Fatalf("无服务维度的行动不该写沉淀条目（会污染目标级聚合）：%+v", e)
		}
	}
}

// ===== 相关性：中文任务要能召回目标级经验 =====

func TestMissionLessonMatchByChineseService(t *testing.T) {
	l := Lesson{
		Kind:    lsRepeatFail,
		Trigger: msSinkAction + " @ backend",
	}
	// 动作名是英文、kind 没有关键词——只靠 target 的中文称呼
	if s := lessonMatchScore(l, "后端起不来，帮我看一下"); s <= 0 {
		t.Fatalf("中文任务应能通过服务中文名召回，实际得分 %d", s)
	}
	if s := lessonMatchScore(l, "帮我看看前端的打包命令"); s != 0 {
		t.Fatalf("无关服务不该命中，实际得分 %d", s)
	}
}

// ===== Phase 3.3：预案/行动的失败证据回流到经验置信度 =====

// TestMissionSedimentFeedsLessonOutcome 证明「回流」这条链真的通了。
//
// 这条用例守的是一个很容易自我欺骗的地方：设计文档要求
// 「预案执行失败的证据回流到关联 Lesson 的 Fail 计数」，但代码里
// **没有一行是专门做这件事的** —— 它靠 missionSediment 写审计、
// 审计里的 lessonIDFor 认出经验、再 recordOutcome 三步串起来。
// 任何一环断了（比如 R1 抢先占了同名 ID、或者 ID 里服务名换了写法），
// 表现都只是"置信度涨得慢"，肉眼看不出来。
func TestMissionSedimentFeedsLessonOutcome(t *testing.T) {
	defer withScopeEnv(t)()
	defer withLessonEnv(t, []AuditEntry{
		sfe("2026-10-03 10:00:00.000", "backend", resFail, msFailed),
		sfe("2026-10-03 11:00:00.000", "backend", resFail, msFailed),
		sfe("2026-10-03 12:00:00.000", "backend", resFail, msFailed),
	})()

	id := lsID(lsRepeatFail, msSinkAction, "backend")
	if lessonByID(lessonsFor(), id) == nil {
		t.Fatalf("前置不成立：3 次失败后应有经验 %s", id)
	}
	if st := outcomeOf(id); st.Fail != 0 {
		t.Fatalf("前置不成立：经验刚建立时不该有计数，实际 %+v", st)
	}

	// 第 4 次失败：走真实收口路径写审计
	missionSediment(Mission{
		ID: "m-4", Goal: "修复后端", State: msFailed,
		Trigger: Trigger{Kind: "guard", Detail: "backend | 复验未通过"},
		Result:  &OpResult{Verified: false},
	})

	if st := outcomeOf(id); st.Fail != 1 {
		t.Fatalf("第 4 次失败应回流到经验的 Fail 计数（证明回流链是通的），实际 %+v", st)
	}
}
