package main

// mission_test.go —— Phase 1 验收 5：状态机转移表 100% 表驱动覆盖，
// 外加落盘往返、启动降级、红线 1（终态只认复验）的规则测试。

import (
	"path/filepath"
	"testing"
	"time"
)

// withMissionEnv 把 missionFile 重定向到 TempDir（同 withMemoryEnv 手法）。
func withMissionEnv(t *testing.T) func() {
	t.Helper()
	dir := t.TempDir()
	old := missionFile
	missionFile = func() string { return filepath.Join(dir, "missions.json") }
	// Phase 2：guard/inbound 产出后会自动去匹配预案，必须把预案文件也隔离，
	// 否则测试会读到开发机上真实的 playbooks.json（结果随本机数据漂移）。
	oldPbk := playbookFile
	playbookFile = func() string { return filepath.Join(dir, "playbooks.json") }
	// 气泡出口换成空实现：这些测试会走到"检测到异常，建议执行预案"，
	// 不换的话跑一次单测就弹几十个真实通知。
	oldNotify := notifyFn
	notifyFn = func(string, string) {}
	return func() {
		missionFile = old
		playbookFile = oldPbk
		notifyFn = oldNotify
	}
}

// allMissionStates 状态机的全集。加状态必须同步这里 + missionTransitions。
func allMissionStates() []string {
	return []string{
		msPending, msAwaitingConfirm, msRunning, msPaused,
		msDone, msFailed, msCanceled, msExpired,
	}
}

// TestMissionTransitionTable 转移表逐格验证（8×8 = 64 格，无一遗漏）。
//
// 期望值在测试里**独立写一份**，不读 missionTransitions——否则就是
// 用实现测实现，改坏了表也测不出来。
func TestMissionTransitionTable(t *testing.T) {
	want := map[string]map[string]bool{
		msPending:         {msRunning: true, msAwaitingConfirm: true, msPaused: true, msFailed: true, msCanceled: true, msExpired: true},
		msAwaitingConfirm: {msRunning: true, msPaused: true, msFailed: true, msCanceled: true, msExpired: true},
		msRunning:         {msAwaitingConfirm: true, msPaused: true, msDone: true, msFailed: true, msCanceled: true, msExpired: true},
		msPaused:          {msRunning: true, msAwaitingConfirm: true, msFailed: true, msCanceled: true, msExpired: true},
		msDone:            {},
		msFailed:          {},
		msCanceled:        {},
		msExpired:         {},
	}
	// 与实现的表对照（实现多写/少写一格都要炸）
	for _, from := range allMissionStates() {
		impl := map[string]bool{}
		for _, to := range missionTransitions[from] {
			impl[to] = true
		}
		if len(impl) != len(want[from]) {
			t.Fatalf("转移表 %s：实现 %d 格，期望 %d 格", from, len(impl), len(want[from]))
		}
		for to := range want[from] {
			if !impl[to] {
				t.Errorf("转移表 %s：实现缺少 %s→%s", from, from, to)
			}
		}
	}
	// 行为验证：允许的转移真的改状态，不允许的报错且不改状态
	for _, from := range allMissionStates() {
		for _, to := range allMissionStates() {
			m := &Mission{ID: "x", State: from}
			err := missionGo(m, to)
			allowed := want[from][to] || from == to // 自转移是合法 no-op
			if allowed && err != nil {
				t.Errorf("%s→%s 应允许，报错：%v", from, to, err)
			}
			if !allowed && err == nil {
				t.Errorf("%s→%s 应拒绝，却成功了", from, to)
			}
			if !allowed && m.State != from {
				t.Errorf("%s→%s 被拒后状态被改动：%s", from, to, m.State)
			}
			if allowed && m.State != to {
				t.Errorf("%s→%s 成功后状态应为 %s，实为 %s", from, to, to, m.State)
			}
		}
	}
}

// TestMissionFinishOnlyVerify 红线 1：没有复验证据的终态一律 failed。
func TestMissionFinishOnlyVerify(t *testing.T) {
	cases := []struct {
		name     string
		from     string
		verified bool
		want     string
	}{
		{"复验通过才配 done", msRunning, true, msDone},
		{"复验未过 → failed", msRunning, false, msFailed},
		{"pending 直接收口（未跑过）→ failed", msPending, false, msFailed},
		{"复验没判过不许 done（pending→done 非法）", msPending, true, msDone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &Mission{ID: "x", State: c.from}
			err := missionFinish(m, c.verified, "复验结论")
			if c.want == msDone && c.from != msRunning {
				// 未运行过的行动不许直接 done：走非法转移路径
				if err == nil {
					t.Fatalf("%s→done 应被状态机拒绝", c.from)
				}
				return
			}
			if err != nil {
				t.Fatalf("missionFinish 报错：%v", err)
			}
			if m.State != c.want {
				t.Fatalf("状态应为 %s，实为 %s", c.want, m.State)
			}
			if m.Result == nil || m.Result.Verified != c.verified || m.Result.Ok != c.verified {
				t.Fatalf("Result 判据不符： %+v", m.Result)
			}
		})
	}
	// failed 的 Result 也必须留下（审计要看为什么失败）
	m := &Mission{ID: "x", State: msRunning}
	_ = missionFinish(m, false, "端口没起来")
	if m.Result == nil || m.Result.Msg == "" || m.Result.Verified {
		t.Fatalf("failed 行动必须带非空、未验证的 Result，实为 %+v", m.Result)
	}
}

// TestMissionFinishReportTypeFromPending 报告型行动（晨报）可以在 pending 直接
// 收口 done，但前提是**真的挂上了报告文件**。
//
// 这条例外最容易退化成后门：如果把判据写成"调用方说它是报告型"，那么任何
// 可执行行动都能借这个 flag 逃过转移表。所以这里用两条反向断言钉住边界。
func TestMissionFinishReportTypeFromPending(t *testing.T) {
	m := &Mission{ID: "brf", State: msPending, Report: "briefing-2026-10-27.md"}
	if err := missionFinish(m, true, "晨报已生成"); err != nil {
		t.Fatalf("报告型行动应从 pending 直接收口：%v", err)
	}
	if m.State != msDone || m.Result == nil || !m.Result.Verified {
		t.Fatalf("报告型行动收口不符：state=%s result=%+v", m.State, m.Result)
	}

	// 反向 1：没挂报告 → 不许借道
	x := &Mission{ID: "no-rep", State: msPending}
	if err := missionFinish(x, true, "声称完成"); err == nil {
		t.Fatal("没有报告的 pending 行动不许直接 done（红线 1）")
	}
	// 反向 2：挂了报告但复验没过 → 依旧 failed，不能靠报告洗白
	y := &Mission{ID: "rep-fail", State: msPending, Report: "r.md"}
	if err := missionFinish(y, false, "复验未过"); err != nil {
		t.Fatalf("报告型行动落 failed 不该报错：%v", err)
	}
	if y.State != msFailed {
		t.Fatalf("复验没过应为 failed，实为 %s", y.State)
	}
}

// TestMissionPersistence 落盘往返：创建 → 重启视角重读 → 一致。
func TestMissionPersistence(t *testing.T) {
	defer withMissionEnv(t)()

	m, err := missionNew("检查磁盘趋势", Trigger{Kind: "manual"})
	if err != nil {
		t.Fatalf("missionNew: %v", err)
	}
	if m.State != msPending || m.ID == "" || m.Goal != "检查磁盘趋势" {
		t.Fatalf("新建行动字段不符： %+v", m)
	}
	if len(m.ID) < 10 || m.ID[:2] != "m-" {
		t.Fatalf("ID 格式应为 m-<时间>-<后缀>，实为 %s", m.ID)
	}
	if !m.ExpiresAt.After(m.CreatedAt) {
		t.Fatalf("ExpiresAt 应晚于 CreatedAt")
	}

	got, ok := missionGet(m.ID)
	if !ok || got.Goal != m.Goal || got.State != msPending {
		t.Fatalf("missionGet 往返失败： %+v", got)
	}
	if list := missionList(); len(list) != 1 || list[0].ID != m.ID {
		t.Fatalf("missionList 应含 1 条，实为 %d", len(list))
	}

	// 变更落盘
	upd, err := missionUpdate(m.ID, func(x *Mission) error {
		return missionGo(x, msRunning)
	})
	if err != nil || upd.State != msRunning {
		t.Fatalf("missionUpdate 失败：%v %+v", err, upd)
	}
	if got, _ := missionGet(m.ID); got.State != msRunning {
		t.Fatalf("更新未持久化，重读为 %s", got.State)
	}
	// 不存在的 ID 要报错
	if _, err := missionUpdate("m-nope", func(x *Mission) error { return nil }); err == nil {
		t.Fatalf("不存在的 ID 应报错")
	}
	// 空目标拒绝
	if _, err := missionNew("   ", Trigger{Kind: "manual"}); err == nil {
		t.Fatalf("空目标应报错")
	}
}

// TestMissionStartupSweep 启动降级（验收 1 的核心）。
func TestMissionStartupSweep(t *testing.T) {
	defer withMissionEnv(t)()

	now := time.Now()
	mk := func(id, state string, updated time.Time) Mission {
		return Mission{
			ID: id, Goal: "g", State: state,
			CreatedAt: updated.Add(-time.Hour), UpdatedAt: updated,
			ExpiresAt: now.Add(24 * time.Hour),
		}
	}
	seed := []Mission{
		mk("m-run", msRunning, now),                                // 重启 → 必须 paused
		mk("m-await", msAwaitingConfirm, now.Add(-31*time.Minute)), // 超时 → paused
		mk("m-fresh-await", msAwaitingConfirm, now),                // 没超时 → 不动
		mk("m-expire", msPending, now.Add(-40*24*time.Hour)),       // 有旧 UpdatedAt 但 ExpiresAt 未过 → 不动
		mk("m-dead", msPending, now),                               // ExpiresAt 过期 → expired
		{
			ID: "m-old-done", Goal: "g", State: msDone,
			CreatedAt: now.Add(-60 * 24 * time.Hour),
			UpdatedAt: now.Add(-60 * 24 * time.Hour), // 终态过保留期 → 删
			ExpiresAt: now.Add(-30 * 24 * time.Hour),
		},
		mk("m-done", msDone, now), // 终态未过保留期 → 留
	}
	seed[4].ExpiresAt = now.Add(-time.Hour) // 只有 m-dead 过期；m-expire 靠旧 UpdatedAt 不算过期
	if err := saveMissions(seed); err != nil {
		t.Fatalf("saveMissions: %v", err)
	}

	missionStartupSweep()

	got := map[string]Mission{}
	for _, m := range loadMissions() {
		got[m.ID] = m
	}
	if m := got["m-run"]; m.State != msPaused || m.PauseWhy == "" {
		t.Errorf("running 重启后应降级 paused 且带原因，实为 %s / %q", m.State, m.PauseWhy)
	}
	if m := got["m-await"]; m.State != msPaused {
		t.Errorf("awaiting_confirm 超时应降级 paused，实为 %s", m.State)
	}
	if m := got["m-fresh-await"]; m.State != msAwaitingConfirm {
		t.Errorf("未超时的 awaiting_confirm 不该动，实为 %s", m.State)
	}
	if m := got["m-expire"]; m.State != msPending {
		t.Errorf("未过 ExpiresAt 的不该过期，实为 %s", m.State)
	}
	if m := got["m-dead"]; m.State != msExpired {
		t.Errorf("过 ExpiresAt 应 expired，实为 %s", m.State)
	}
	if _, ok := got["m-old-done"]; ok {
		t.Errorf("过保留期的终态行动应被删除")
	}
	if m := got["m-done"]; m.State != msDone {
		t.Errorf("保留期内的终态行动应保留，实为 %s", m.State)
	}
}

// TestMissionListKeepsLiveRunning 普通读**不**把 running 当尸体（验收 1 的反面）。
func TestMissionListKeepsLiveRunning(t *testing.T) {
	defer withMissionEnv(t)()

	now := time.Now()
	if err := saveMissions([]Mission{{
		ID: "m-live", Goal: "g", State: msRunning,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}}); err != nil {
		t.Fatalf("saveMissions: %v", err)
	}
	list := missionList()
	if len(list) != 1 || list[0].State != msRunning {
		t.Fatalf("执行中的行动在普通读时必须保持 running，实为 %+v", list)
	}
}

// TestMissionTrim 超上限淘汰最老终态，不淘汰未收口的。
func TestMissionTrim(t *testing.T) {
	defer withMissionEnv(t)()

	now := time.Now()
	var all []Mission
	for i := 0; i < msMaxCount; i++ {
		all = append(all, Mission{
			ID: "m-old-" + string(rune('A'+i%26)) + itoa(i), State: msDone,
			CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
			ExpiresAt: now.Add(time.Hour),
		})
	}
	all = append(all, Mission{ID: "m-new-done", State: msDone, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)})
	all = append(all, Mission{ID: "m-live", State: msRunning, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)})

	if err := missionTrim(&all); err != nil {
		t.Fatalf("missionTrim: %v", err)
	}
	if len(all) != msMaxCount {
		t.Fatalf("应裁到 %d，实为 %d", msMaxCount, len(all))
	}
	seen := map[string]bool{}
	for _, m := range all {
		seen[m.ID] = true
	}
	if !seen["m-live"] || !seen["m-new-done"] {
		t.Fatalf("未收口的行动和最新终态必须保留：%v", seen["m-live"])
	}
	// 全是未收口时要报错而不是静默丢
	live := []Mission{{ID: "a", State: msRunning}, {ID: "b", State: msPending}}
	live = append(live, make([]Mission, msMaxCount)...)
	for i := range live {
		live[i].State = msRunning
		live[i].ID = "m" + itoa(i)
	}
	if err := missionTrim(&live); err == nil {
		t.Fatalf("无可淘汰的终态时应报错")
	}
}
