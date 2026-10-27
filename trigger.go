package main

// trigger.go —— 触发框架（Phase 1 T5）：guard 降级挂钩 + 定时产出。
//
// 与 guard.go 的分工（设计 2.5，防重复建设最重要的一条）：
// guard 是**确定性快路径**（死了就重启，已验证可靠），继续原样跑，一行不动；
// 这里只接 guard **接不住**的事件——「拉起了但复验没过」「恢复失败」
//（guard.go 三态出口的两个非成功分支）才是需要诊断判断的，
// 升级为 pending 行动候选，**等人批**（红线 2）。
//
// 定时产出（巡检/晨报）只读不写（红线 3：巡检类默认开）：
// 产出的是 pending 行动，执行仍走用户点「继续」→ AI 对话通道——
// Phase 1 **不做脱离浏览器的后台执行器**（设计 2.4），
// 所以调度器只负责"到点把行动准备好"，不负责跑。

import (
	"strings"
	"time"
)

// ===== guard 挂钩 =====

// missionFromGuard guard 降级事件 → pending 行动候选（Phase 1 验收 4）。
//
// 返回 (行动, 是否新建)。去重规则：同一服务已有**未收口**的 guard 行动时
// 不再新建——守护循环每 60s 一轮，不加去重会刷出一屏同样的候选。
// 用户取消（终态）后再次故障会重新产出：明确.dismiss 不等于"永远别再报"。
//
// Phase 2：产出后立刻尝试匹配预案（pbkApplyToMission）。匹配失败不是错误 ——
// 没有预案就走完整 AI 诊断，这正是设计 3.2 的"无预案 → 走现在的对话能力"。
func missionFromGuard(svcID, svcLabel, errKind, title, detail string) (Mission, bool) {
	for _, m := range missionList() {
		if missionTerminal(m.State) {
			continue
		}
		if m.Trigger.Kind == "guard" && strings.Contains(m.Trigger.Detail, svcID) {
			return m, false
		}
	}
	m, err := missionNew(title, Trigger{
		Kind:    "guard",
		Detail:  svcID + " | " + truncateRunes(strings.TrimSpace(detail), 300),
		ErrKind: errKind,
	})
	if err != nil {
		warn(scSys, "行动", "guard 候选产出失败（%s）：%v", svcID, err)
		return Mission{}, false
	}
	auditNow(actGuard, "mission_guard_candidate", m.ID, svcID, resUnverified, firstLines(title, 80))
	info(scSys, "行动", "守护事件升级为待批行动 %s：%s", m.ID, title)
	// 先套预案再通知：SSE 推出去的列表应当已经带上「建议执行预案 X」，
	// 否则用户会先看到一条光秃秃的行动，2 秒后才变成有建议的样子。
	if p, ok := pbkApplyToMission(m.ID, svcID, errKind); ok {
		// 桌宠/气泡只说一句：这是"建议"而不是"已经做了"，用词不能滑
		notifyUser("检测到"+svcLabel+"异常", "建议执行预案「"+p.Title+"」（"+itoa(len(p.Steps))+" 步，需你确认）")
	}
	notifyMissions()
	return m, true
}

// ===== 定时产出（巡检/晨报）=====

// msSchedState 调度器内存状态（进程生命周期内有效）。
// 重启归零由两层兜底：巡检启动先等一个间隔；产出前按未收口行动去重。
type msSchedState struct {
	lastPatrol  time.Time
	briefingDay string // 已产出晨报的日期 "2006-01-02"
}

var msSched msSchedState

// msSchedTick 判断此刻该产出什么（纯函数，表驱动测试的落点）。
// 返回 trigger kind（patrol / schedule），"" = 不产。
//
// 规则：
//   - 总开关 off → 永不产；
//   - 晨报：到点后 2 小时窗口内产一次（当天已产跳过）；错过窗口不补——
//     20:00 启动时补一份"早报"是惊吓不是惊喜；
//   - 巡检：距上次 ≥ interval 分钟才产；lastPatrol 为零（刚启动）不产，
//     启动后先等一个完整间隔。
func msSchedTick(c MissionConfig, st *msSchedState, now time.Time) string {
	if !c.on() {
		return ""
	}
	// 晨报优先（特定时刻的产出不该被巡检挤掉）
	day := now.Format("2006-01-02")
	if bt := strings.TrimSpace(c.BriefingAt); bt != "" && bt != "off" && st.briefingDay != day {
		if t, err := time.ParseInLocation("15:04", bt, now.Location()); err == nil {
			btime := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
			if d := now.Sub(btime); d >= 0 && d < 2*time.Hour {
				st.briefingDay = day
				return "schedule"
			}
		}
	}
	iv := c.PatrolIntervalMin
	if iv <= 0 {
		iv = 60 // 0 = 默认 60（applyConfigDefaults 也会填，这里兜底测试/零值）
	}
	if !st.lastPatrol.IsZero() && now.Sub(st.lastPatrol) >= time.Duration(iv)*time.Minute {
		st.lastPatrol = now
		return "patrol"
	}
	return ""
}

// msProduce 按 kind 产出一条 pending 行动（去重 + 审计 + 推送）。
func msProduce(kind string, now time.Time) {
	title := ""
	switch kind {
	case "patrol":
		title = "定时巡检"
	case "schedule":
		title = "晨报 " + now.Format("01-02")
	default:
		return
	}
	// 去重：上一份还没被处理（非终态）就不产新的，避免列表堆积
	for _, m := range missionList() {
		if !missionTerminal(m.State) && m.Trigger.Kind == kind {
			return
		}
	}
	m, err := missionNew(title, Trigger{Kind: kind, Detail: "定时产出 " + now.Format("15:04")})
	if err != nil {
		warn(scSys, "行动", "定时产出失败（%s）：%v", kind, err)
		return
	}
	auditNow(actSys, "mission_"+kind, m.ID, "", resOK, title)
	info(scSys, "行动", "已产出 %s 行动 %s（待你继续）", kind, m.ID)
	notifyMissions()
	// 晨报不走 AI 回合：报告由系统按事实聚合，生成完直接收口（设计 3.2、
	// Phase 2 验收 4「内容全部来自系统记录」）。巡检仍留给 AI 现场诊断。
	if kind == "schedule" {
		briefingAttach(m.ID, now)
	}
}

// startMissionScheduler 后台调度循环（main.go 启动链调用）。
// 每分钟检查一次；配置在每 tick 现读 cfg——配置页改开关下分钟即生效。
func startMissionScheduler() {
	msSched = msSchedState{lastPatrol: time.Now()} // 启动后先等一个完整间隔
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for range t.C {
			now := time.Now()
			if kind := msSchedTick(cfg.Mission, &msSched, now); kind != "" {
				msProduce(kind, now)
			}
		}
	}()
}
