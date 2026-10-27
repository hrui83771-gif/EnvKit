package main

// trigger_test.go —— T5：guard 挂钩与定时产出。
//
// 验收 4 的一部分：guard 复验失败的告警自动变成待批行动候选。
// （guardOne 本体不进单测——它要完整服务拓扑；这里测挂钩函数本身，
// 另有源码契约测试钉住 guard.go 的两个调用点不被删掉。）

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestMissionFromGuard(t *testing.T) {
	defer withAIMissionEnv(t)()

	// 第一次：新建候选（pending，等人批）
	m1, created := missionFromGuard("backend", "后端", errKindVerifyFail,
		"后端恢复待确认", "已拉起但复验未通过：端口没监听")
	if !created {
		t.Fatalf("首次应新建")
	}
	if m1.State != msPending || m1.Trigger.Kind != "guard" {
		t.Fatalf("候选应为 pending/guard：%s / %s", m1.State, m1.Trigger.Kind)
	}
	if !strings.Contains(m1.Trigger.Detail, "backend") || !strings.Contains(m1.Trigger.Detail, "端口没监听") {
		t.Fatalf("detail 应带服务 ID 与原因：%q", m1.Trigger.Detail)
	}

	// 未收口前重复事件：去重（守护 60s 一轮，不去重会刷屏）
	m2, created2 := missionFromGuard("backend", "后端", errKindVerifyFail, "后端恢复失败", "又一次失败")
	if created2 {
		t.Fatalf("同一服务未收口时不该重复产出")
	}
	if m2.ID != m1.ID {
		t.Fatalf("去重应返回同一行动：%s vs %s", m2.ID, m1.ID)
	}
	// 不同服务互不影响
	if _, created := missionFromGuard("web", "前端", "", "前端恢复失败", "x"); !created {
		t.Fatalf("不同服务应各自产出")
	}

	// 用户取消（终态）后再次故障：重新产出（dismiss ≠ 永久静默）
	if _, err := missionUpdate(m1.ID, func(x *Mission) error { return missionGo(x, msCanceled) }); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, created := missionFromGuard("backend", "后端", errKindVerifyFail, "后端恢复失败", "再次故障"); !created {
		t.Fatalf("终态后应重新产出候选")
	}
}

// TestGuardHookContract 源码契约：guard.go 两个非成功分支必须挂着 missionFromGuard。
// （与 verify_record_test.go 同手法——这类"接线"最容易在重构中被无声删掉。）
func TestGuardHookContract(t *testing.T) {
	b, err := os.ReadFile("guard.go")
	if err != nil {
		t.Fatalf("读 guard.go: %v", err)
	}
	src := string(b)
	n := strings.Count(src, "missionFromGuard(")
	if n != 2 {
		t.Fatalf("guard.go 应有 2 处 missionFromGuard 调用（复验未过/恢复失败），实为 %d", n)
	}
	// 三态出口的结构不许被挪走
	for _, want := range []string{"已拉起但复验未完成", "自动恢复失败"} {
		if !strings.Contains(src, want) {
			t.Errorf("guard.go 缺少 %q 分支", want)
		}
	}
}

func TestMsSchedTick(t *testing.T) {
	loc := time.Local
	at := func(d int, h, m int) time.Time {
		return time.Date(2026, 10, d, h, m, 0, 0, loc)
	}
	on := true
	off := false

	cases := []struct {
		name string
		cfg  MissionConfig
		st   msSchedState
		now  time.Time
		want string
	}{
		{
			name: "总开关关 → 永不产",
			cfg:  MissionConfig{Enabled: &off, PatrolIntervalMin: 60, BriefingAt: "08:30"},
			st:   msSchedState{lastPatrol: at(26, 6, 0)},
			now:  at(26, 9, 0),
			want: "",
		},
		{
			name: "巡检间隔未到 → 不产",
			cfg:  MissionConfig{Enabled: &on, PatrolIntervalMin: 60, BriefingAt: "off"},
			st:   msSchedState{lastPatrol: at(26, 9, 0)},
			now:  at(26, 9, 30),
			want: "",
		},
		{
			name: "巡检间隔已到 → patrol",
			cfg:  MissionConfig{Enabled: &on, PatrolIntervalMin: 60, BriefingAt: "off"},
			st:   msSchedState{lastPatrol: at(26, 9, 0)},
			now:  at(26, 10, 1),
			want: "patrol",
		},
		{
			name: "刚启动（lastPatrol 零值）→ 不产",
			cfg:  MissionConfig{Enabled: &on, PatrolIntervalMin: 60, BriefingAt: "off"},
			st:   msSchedState{},
			now:  at(26, 9, 0),
			want: "",
		},
		{
			name: "间隔 0 按默认 60 分钟",
			cfg:  MissionConfig{Enabled: &on, PatrolIntervalMin: 0, BriefingAt: "off"},
			st:   msSchedState{lastPatrol: at(26, 9, 0)},
			now:  at(26, 9, 59),
			want: "",
		},
		{
			name: "晨报到点窗口内 → schedule",
			cfg:  MissionConfig{Enabled: &on, PatrolIntervalMin: 60, BriefingAt: "08:30"},
			st:   msSchedState{lastPatrol: at(26, 9, 0)},
			now:  at(26, 8, 35),
			want: "schedule",
		},
		{
			name: "晨报当天已产 → 跳过（巡检接管）",
			cfg:  MissionConfig{Enabled: &on, PatrolIntervalMin: 60, BriefingAt: "08:30"},
			st:   msSchedState{lastPatrol: at(26, 8, 34), briefingDay: "2026-10-26"},
			now:  at(26, 8, 35),
			want: "",
		},
		{
			name: "晨报错过窗口（晚上启动）→ 不补",
			cfg:  MissionConfig{Enabled: &on, PatrolIntervalMin: 60, BriefingAt: "08:30"},
			st:   msSchedState{lastPatrol: at(26, 19, 30)},
			now:  at(26, 20, 0),
			want: "",
		},
		{
			name: "晨报 off → 不产",
			cfg:  MissionConfig{Enabled: &on, PatrolIntervalMin: 60, BriefingAt: "off"},
			st:   msSchedState{lastPatrol: at(26, 8, 0)},
			now:  at(26, 8, 35),
			want: "",
		},
		{
			name: "晨报时刻非法 → 忽略（只按巡检走）",
			cfg:  MissionConfig{Enabled: &on, PatrolIntervalMin: 60, BriefingAt: "早上八点半"},
			st:   msSchedState{lastPatrol: at(26, 8, 30)},
			now:  at(26, 8, 35),
			want: "",
		},
		{
			name: "晨报与巡检同刻 → 晨报优先",
			cfg:  MissionConfig{Enabled: &on, PatrolIntervalMin: 60, BriefingAt: "08:30"},
			st:   msSchedState{lastPatrol: at(26, 7, 0)},
			now:  at(26, 8, 35),
			want: "schedule",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := c.st
			got := msSchedTick(c.cfg, &st, c.now)
			if got != c.want {
				t.Fatalf("want %q, got %q", c.want, got)
			}
			// 产过的状态必须被记账（防下一 tick 重复产）
			if got == "patrol" && !st.lastPatrol.Equal(c.now) {
				t.Errorf("产 patrol 后 lastPatrol 应更新为 now")
			}
			if got == "schedule" && st.briefingDay != c.now.Format("2006-01-02") {
				t.Errorf("产 schedule 后 briefingDay 应记为当天")
			}
		})
	}
}

func TestMsProduceDedup(t *testing.T) {
	defer withAIMissionEnv(t)()
	withAuditDir(t, nil)

	now := time.Now()
	msProduce("patrol", now)
	msProduce("patrol", now) // 未收口 → 去重
	list := missionList()
	if len(list) != 1 {
		t.Fatalf("同一 kind 未收口只应产 1 条，实为 %d", len(list))
	}
	if list[0].State != msPending || list[0].Trigger.Kind != "patrol" {
		t.Fatalf("产出应为 pending/patrol：%s / %s", list[0].State, list[0].Trigger.Kind)
	}
	// 收口后可再产
	if _, err := missionUpdate(list[0].ID, func(x *Mission) error { return missionGo(x, msCanceled) }); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	msProduce("patrol", now)
	if n := len(missionList()); n != 2 {
		t.Fatalf("收口后应能再产，实为 %d 条", n)
	}
	// 未知 kind 静默忽略
	msProduce("telepathy", now)
	if n := len(missionList()); n != 2 {
		t.Fatalf("未知 kind 不该产出，实为 %d 条", n)
	}
}
