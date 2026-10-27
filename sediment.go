package main

// sediment.go —— Agent 闭环 Phase 3：Mission → Lesson 自动沉淀。
//
// 设计文档：docs/DESIGN-agent-loop.md §4.1。
//
// ## 为什么不新建一个「Mission 经验库」
//
// lesson.go 开头第二条设计决策是「经验 = 审计的一个视图，不是一份新存储」。
// 直接读 missions.json 造经验会推翻它，并且立刻产生两个事实源：用户否掉一条
// 经验、审计被 30 天清理、missions 被淘汰，三者时序一乱就会出现
// 「经验还在、依据没了」——而那种经验是没法被质证的。
//
// 所以这里的做法是：**终态收口时写一条规范化审计**（action=mission_fix），
// 归入对审计的统一提取（R6 规则），和其他经验共用同一套 Scope/Confidence/Stale。
// 代价是 Mission 经验也随审计过期；这与 missions.json 的保留期一致，语义也对：
// 半年前的处置结论，环境早变了。
//
// ## 只学审计学不到的
//
// R1（反复失败）/ R2（AI 失败→用户修好）/ R5（复验同归类）已经在从审计提取，
// Mission 维度**不再重复造**，否则同一个事实会生成两条措辞不同的经验，
// 互相抢提示词里的名额。这里只补审计根本看不见的一件事：**整轮目标打没打成**。
// 审计看到的是一个个工具调用；一场行动连败三次，在审计里只是十几个普通条目，
// 没有任何一条说「这个目标没达成」。
//
// ## 三类终态只有两类算失败
//
// canceled / expired 记 resUnverified 而不是 resFail。用户主动取消不是「修不好」，
// 超期也不是。把它们记进失败聚合，迟早会生成一条「这个服务修不好」的假经验，
// 而真相只是用户那天没空。resUnverified（未证明）既保住了事实，
// 又不会进任何失败聚合（R1/R5 都只认 resFail）。

import (
	"fmt"
	"strings"
	"time"
)

const (
	// msSinkAction 沉淀用的规范动作名。
	// 刻意用英文固定串：lessonMatchScore 拿 Trigger 的第一段做包含匹配，
	// 中文动作名在中文任务里反而更容易误命中。
	msSinkAction = "mission_fix"

	// msSinkFixWindow 「用户放弃→自己动手」的对齐窗口，与 userFixWindow 同口径。
	msSinkFixWindow = 30 * time.Minute
)

// missionSediment 终态收口时写一条规范化审计（R6 提取的唯一输入）。
//
// 只在**有服务维度**时写：晨报/巡检/手动行动没有 svcID，按目标聚合会把
// 互不相干的行动算成同一个「目标」，那比不沉淀更糟。
func missionSediment(m Mission) {
	if !missionTerminal(m.State) {
		return
	}
	svc := missionSvcID(m.Trigger.Detail)
	if svc == "" {
		return
	}
	verified := m.Result != nil && m.Result.Verified
	result := resUnverified
	switch {
	case m.State == msDone && verified:
		result = resOK
	case m.State == msFailed:
		result = resFail
	case m.State == msDone && !verified:
		// done 却没复验通过：红线 1 的兜底分支，按失败记。
		result = resFail
	}
	// detail 的首段固定是 [state]：R6c 要靠它区分「用户放弃」与「超期」，
	// 而这两者在审计里都是 resUnverified，没有别的字段能分开。
	detail := "[" + m.State + "]"
	if m.Trigger.ErrKind != "" {
		detail += " err=" + m.Trigger.ErrKind
	}
	if m.PauseWhy != "" {
		detail += " why=" + truncateRunes(strings.TrimSpace(m.PauseWhy), 60)
	}
	detail += " goal=" + truncateRunes(strings.TrimSpace(m.Goal), 80)
	// params 放预案 ID：R6 现在不用它，但「这次失败时用的是哪套预案」
	// 是复盘第一个会问的问题，缺失了就只能靠猜。
	auditNow(actSys, msSinkAction, svc, m.Playbook, result, detail)
}

// ===== R6 提取 =====

// extractMissionLessons 从审计里提取 Mission 维度的经验（R6）。
//
// 输入必须是**时间正序**的审计（extractLessons 已经把倒序翻过了）。
func extractMissionLessons(asc []AuditEntry) []Lesson {
	bySvc := map[string][]AuditEntry{}
	var order []string // 保持首次出现顺序，保证结果与 map 遍历顺序无关
	for _, e := range asc {
		if e.Action != msSinkAction {
			continue
		}
		svc := strings.TrimSpace(e.Target)
		if svc == "" {
			continue
		}
		if _, seen := bySvc[svc]; !seen {
			order = append(order, svc)
		}
		bySvc[svc] = append(bySvc[svc], e)
	}
	var out []Lesson
	for _, svc := range order {
		if l, ok := msRepeatLesson(svc, bySvc[svc]); ok {
			out = append(out, l)
		}
		if l, ok := msUserFixLesson(svc, bySvc[svc], asc); ok {
			out = append(out, l)
		}
	}
	return out
}

// msRepeatLesson 目标级连败：同一服务上「从最后一次成功之后」连续失败达阈值。
//
// **为什么必须从最后一次成功往后数**：按历史总失败数算的话，一条经验会
// 永久生效——哪怕问题后来修好了，模型还是被告知「这个服务修不好」。
// 连续失败才是「当前还没解决」的正确表达。
//
// canceled / expired 不打断连续性（它们不是成功），但也不计入失败数——
// 用户取消三次不等于失败三次。
func msRepeatLesson(svc string, es []AuditEntry) (Lesson, bool) {
	lastOK := -1
	for i, e := range es {
		if e.Result == resOK {
			lastOK = i
		}
	}
	var fails []string
	var last string
	for _, e := range es[lastOK+1:] {
		if e.Result != resFail {
			continue
		}
		fails = append(fails, evLine(e))
		last = e.Ts
	}
	if len(fails) < lsRepeatMin {
		return Lesson{}, false
	}
	return Lesson{
		ID:   lsID(lsRepeatFail, msSinkAction, svc),
		Kind: lsRepeatFail,
		Title: fmt.Sprintf("「%s」上的处置已连续 %d 次没做成——先查根因（上次停在哪一步、卡在什么症状），"+
			"不要原样重来一遍同样的做法", svcLabelCN(svc), len(fails)),
		Trigger:  msSinkAction + " @ " + svc,
		Precheck: "",
		Evidence: lastN(fails, lsMaxEvid),
		Hits:     len(fails),
		LastHit:  last,
	}, true
}

// msUserFixLesson 用户放弃 AI 的处置后，自己动手做成了。
//
// 这是 R2 抓不到的一类：R2 要求「同一个 action 先失败后成功」，而用户点取消
// 之后往往是换了个动作（AI 试 restart_service，用户直接去改配置）。
// action 对不上，R2 就永远沉默——而这类事件的信息量恰恰最大
// （它说明 AI 的判断方向就是错的，不只是手法不对）。
//
// 按服务而不是按动作对齐，是这里唯一能用的确定性键。
func msUserFixLesson(svc string, es []AuditEntry, asc []AuditEntry) (Lesson, bool) {
	var at string
	var ev []string
	for _, e := range es {
		// 只看「用户主动放弃」：超期（expired）不是放弃，是没人管，
		// 拿它去凑「用户接手」会把沉默当成决策。
		if !strings.HasPrefix(e.Detail, "["+msCanceled+"]") {
			continue
		}
		at = e.Ts
		ev = []string{evLine(e)}
	}
	if at == "" {
		return Lesson{}, false
	}
	for _, u := range asc {
		if u.Actor != actUser || u.Result != resOK {
			continue
		}
		if !msSameSvc(u.Target, svc) {
			continue
		}
		if !withinFixWindow(at, u.Ts) {
			continue
		}
		return Lesson{
			ID:   lsID(lsUserFix, msSinkAction, svc),
			Kind: lsUserFix,
			Title: fmt.Sprintf("你上次取消了 AI 在「%s」上的处置，随后自己动手做成了（%s）——"+
				"下次同类问题先问你想怎么做，别让它重走那条被否掉的路",
				svcLabelCN(svc), u.Ts),
			Trigger:  msSinkAction + " @ " + svc,
			Precheck: "ask_user_first",
			Evidence: append(ev, evLine(u)),
			Hits:     1,
			LastHit:  u.Ts,
		}, true
	}
	return Lesson{}, false
}

// msSameSvc 判断一条审计的 target 是否就是这个服务。
//
// 用户手动操作记的 target 是服务 ID（见 handleServiceStart 的约定），
// 但也可能是服务的中文标签。两种都认，别的都不认——模糊匹配会让
// 「用户备份了数据库」被当成「用户修好了后端」。
func msSameSvc(target, svc string) bool {
	t := strings.TrimSpace(target)
	if t == "" {
		return false
	}
	if strings.EqualFold(t, svc) {
		return true
	}
	return t == svcLabelCN(svc)
}

// ===== 展示用的小工具 =====

// svcLabelCN 服务 ID → 界面上的中文称呼。
//
// 存在的理由有两个，都不能省：
//  1. 经验的 Title 要给人看，「backend 上的处置」不如「后端」；
//  2. lessonMatchScore 要能用中文任务召回它——用户说的是「后端起不来」，
//     不会说「backend 起不来」。没有这层映射，R6 的经验永远进不了提示词。
//
// 只认已知的五个服务。未知 ID 原样返回：宁可显示英文，也不要瞎翻。
func svcLabelCN(svc string) string {
	switch strings.ToLower(strings.TrimSpace(svc)) {
	case "backend":
		return "后端"
	case "web", "frontend":
		return "前端"
	case "mysql", "db":
		return "数据库"
	case "chain":
		return "链端"
	case "webase":
		return "WeBASE"
	}
	return svc
}
