package main

// growth_test.go —— Phase 3 成长指标的口径测试。
//
// 这一层最容易出的错不是算错，而是**口径重叠**：同一轮行动同时被算进
// done 和 failed，于是两个页面给出两个不同的"解决率"，而用户没法判断
// 哪个是真的。所以第一条用例就是桶互斥的断言。
//
// 第二条是"空数据必须诚实"：0/0 返回 rateNA 而不是 0%，
// 因为 0% 会被读成"一次都没成"。

import (
	"fmt"
	"testing"
	"time"
)

// withGrowthEnv 隔离行动文件并预置数据集。
func withGrowthEnv(t *testing.T, ms []Mission) func() {
	t.Helper()
	cleanup := withMissionEnv(t)
	if err := saveMissions(ms); err != nil {
		t.Fatalf("预置行动数据失败：%v", err)
	}
	return cleanup
}

// gm 造一条终态行动（默认落在窗口内、未过期）。
func gm(id, state string, verified bool, pbk string, plan ...PlanStepRecord) Mission {
	at := time.Now().Add(-24 * time.Hour)
	return Mission{
		ID: id, Goal: "测试目标 " + id, State: state,
		Trigger:   Trigger{Kind: "manual"},
		Playbook:  pbk,
		Plan:      plan,
		Result:    &OpResult{Verified: verified},
		CreatedAt: at.Add(-30 * time.Minute),
		UpdatedAt: at,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
}

func stepOK(ok bool) PlanStepRecord {
	return PlanStepRecord{Seq: 1, Tool: "restart_service", Target: "backend", OK: ok}
}

// 一轮只能落一个桶
func TestGrowthBucketsDoNotOverlap(t *testing.T) {
	defer withGrowthEnv(t, []Mission{
		gm("m1", msDone, true, "", stepOK(true)),                // 干净完成
		gm("m2", msDone, true, "", stepOK(false), stepOK(true)), // 试错后才 done
		gm("m3", msFailed, false, ""),
		gm("m4", msCanceled, false, ""),
		gm("m5", msExpired, false, ""),
	})()

	rep := growthReport(56)
	b := rep.Total
	if b.Missions != 5 {
		t.Fatalf("终态行动应为 5 条，实际 %d", b.Missions)
	}
	if !growthAssertBuckets(b) {
		t.Fatalf("各桶之和必须等于总数：done=%d failed=%d canceled=%d expired=%d total=%d",
			b.Done, b.Failed, b.Canceled, b.Expired, b.Missions)
	}
	if b.Done != 2 || b.Failed != 1 || b.Canceled != 1 || b.Expired != 1 {
		t.Errorf("分桶结果不对：%+v", b)
	}
	// 首次解决只算「没有失败步骤」的那条
	if b.FirstTry != 1 {
		t.Errorf("首次解决应为 1（m2 试错过不算），实际 %d", b.FirstTry)
	}
	if rep.Rates.FirstTry != 20.0 {
		t.Errorf("首次解决率应为 20.0%%，实际 %v", rep.Rates.FirstTry)
	}
}

// 没数据就如实说没数据，不要给 0%
func TestGrowthEmptyIsHonest(t *testing.T) {
	defer withGrowthEnv(t, nil)()

	rep := growthReport(56)
	if rep.Total.Missions != 0 {
		t.Fatalf("空数据集不该凭空有行动，实际 %d", rep.Total.Missions)
	}
	if rep.Rates.FirstTry != rateNA || rep.Rates.PbkHit != rateNA ||
		rep.Rates.PbkOK != rateNA || rep.Rates.AvgSteps != rateNA {
		t.Errorf("分母为 0 的比率必须是 rateNA（%v），实际 %+v", rateNA, rep.Rates)
	}
	if rep.Note == "" {
		t.Error("空数据必须带一句说明，否则界面只能显示一片零")
	}
}

// 未收口的行动不进任何桶：它还没有结论
func TestGrowthIgnoresOpenMissions(t *testing.T) {
	open := gm("m-open", msRunning, false, "")
	defer withGrowthEnv(t, []Mission{open})()

	if rep := growthReport(56); rep.Total.Missions != 0 {
		t.Fatalf("running 的行动不该进报表，实际 %d", rep.Total.Missions)
	}
}

// 套了预案的行动单独计数；没套过时分母为 0 → rateNA
func TestGrowthPlaybookRates(t *testing.T) {
	defer withGrowthEnv(t, []Mission{
		gm("m1", msDone, true, "p-1", stepOK(true)),
		gm("m2", msFailed, false, "p-1", stepOK(false)),
		gm("m3", msDone, true, ""),
	})()

	rep := growthReport(56)
	if rep.Total.PbkTotal != 2 || rep.Total.PbkOK != 1 {
		t.Fatalf("预案统计不对：%+v", rep.Total)
	}
	// 命中率 = 套预案数 / 行动数 = 2/3
	if rep.Rates.PbkHit != 66.7 {
		t.Errorf("预案命中率应为 66.7%%，实际 %v", rep.Rates.PbkHit)
	}
	// 有效率 = 复验通过数 / 套预案数 = 1/2
	if rep.Rates.PbkOK != 50 {
		t.Errorf("预案有效率应为 50%%，实际 %v", rep.Rates.PbkOK)
	}

	// 去掉带预案的两条：分母归零必须是 rateNA，不是 0
	cleanup := withGrowthEnv(t, []Mission{gm("m9", msDone, true, "")})
	defer cleanup()
	if r := growthReport(56); r.Rates.PbkOK != rateNA {
		t.Errorf("没套过预案时有效率应为 rateNA，实际 %v", r.Rates.PbkOK)
	}
}

// 窗口外的行动不进报表（否则"最近 8 周"是假的）
func TestGrowthWindowFilters(t *testing.T) {
	old := gm("m-old", msDone, true, "")
	old.UpdatedAt = time.Now().AddDate(0, 0, -90)
	old.CreatedAt = old.UpdatedAt
	defer withGrowthEnv(t, []Mission{old, gm("m-new", msDone, true, "")})()

	if rep := growthReport(56); rep.Total.Missions != 1 {
		t.Fatalf("90 天前的行动不该进 56 天窗口，实际 %d", rep.Total.Missions)
	}
}

// ISO 周键：跨年那周不能算错（2026-12-31 属于 2026-W53）
func TestISOWeekKey(t *testing.T) {
	cases := []struct {
		t    time.Time
		want string
	}{
		{time.Date(2026, 10, 27, 12, 0, 0, 0, time.Local), "2026-W44"},
		{time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local), "2026-W01"},
		// 2027-01-01 是周五，按 ISO 属于 2026 年第 53 周
		{time.Date(2027, 1, 1, 12, 0, 0, 0, time.Local), "2026-W53"},
	}
	for _, c := range cases {
		if got := isoWeekKey(c.t); got != c.want {
			t.Errorf("%s 的 ISO 周应为 %s，实际 %s", c.t.Format("2006-01-02"), c.want, got)
		}
	}
}

// 按周分桶：两条不同周的行动要落在两个桶里，且最老在前
func TestGrowthWeekBuckets(t *testing.T) {
	a := gm("m-a", msDone, true, "")
	a.UpdatedAt = time.Now().AddDate(0, 0, -20)
	b := gm("m-b", msFailed, false, "")
	defer withGrowthEnv(t, []Mission{a, b})()

	rep := growthReport(56)
	if len(rep.Weeks) != 2 {
		t.Fatalf("应有两个周桶，实际 %d：%+v", len(rep.Weeks), rep.Weeks)
	}
	if rep.Weeks[0].Key >= rep.Weeks[1].Key {
		t.Errorf("周桶必须按时间升序（最老在前），实际 %s -> %s",
			rep.Weeks[0].Key, rep.Weeks[1].Key)
	}
	// 两个周桶相加必须等于总数（周维度与总量口径一致）
	sum := 0
	for _, w := range rep.Weeks {
		sum += w.Missions
	}
	if sum != rep.Total.Missions {
		t.Errorf("各周之和 %d 应等于总数 %d（口径不一致）", sum, rep.Total.Missions)
	}
}

// 小样本必须提示，不能让人看着 1/1 = 100% 下结论
func TestGrowthSmallSampleNote(t *testing.T) {
	defer withGrowthEnv(t, []Mission{gm("m1", msDone, true, "")})()

	rep := growthReport(56)
	if rep.Note == "" {
		t.Fatal("只有 1 条记录时必须提示样本量不足")
	}
	if !containsAny(rep.Note, "样本", "波动", "条") {
		t.Errorf("提示要说明为什么不足，实际：%s", rep.Note)
	}
	if fmt.Sprintf("%v", rep.Rates.FirstTry) != "100" {
		t.Errorf("1/1 的比率本身是 100（不该被篡改），实际 %v", rep.Rates.FirstTry)
	}
}
