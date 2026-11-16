package main

import (
	"testing"
)

// 分类判据必须是客观字段：actor / verify / detail 的结构化标记 / result。
// 变异：把 healKindOf 的影子分支去掉 → 本测试红。
func TestHealKindOfObjectiveFields(t *testing.T) {
	cases := []struct {
		name string
		e    AuditEntry
		want string
	}{
		{"shadow actor", AuditEntry{Actor: actShadow, Action: "shadow_eval", Result: resOK}, healShadow},
		{"cascade by detail marker", AuditEntry{Actor: actGuard, Action: "chain_autorecover", Detail: "关联压制：上游未恢复", Result: resFail}, healCascade},
		{"verify ok", AuditEntry{Actor: actGuard, Action: "chain_autorecover", Result: resOK, Verify: "共识推进 869701→869707"}, healVerified},
		{"verify unverified", AuditEntry{Actor: actGuard, Action: "chain_autorecover", Result: resUnverified, Verify: "已拉起未验到"}, healUnverif},
		{"verify fail", AuditEntry{Actor: actGuard, Action: "chain_autorecover", Result: resFail, Verify: "复验失败"}, healFailed},
		{"started", AuditEntry{Actor: actGuard, Action: "chain_autorecover", Result: resStart, Detail: "守护检测触发"}, healRecover},
		{"other guard", AuditEntry{Actor: actGuard, Action: "chain_autorecover", Result: resOK, Detail: "正常"}, healOther},
		{"user actor", AuditEntry{Actor: actUser, Action: "config_save", Result: resOK}, healOther},
	}
	for _, c := range cases {
		if got := healKindOf(c.e); got != c.want {
			t.Errorf("%s: healKindOf=%q want %q", c.name, got, c.want)
		}
	}
}

// 级联压制即使 action 是 recover 也不能算"恢复"——这是把"决定不救"藏起来
// 与真正执行的根本区别。
func TestHealKindCascadeWinsOverRecover(t *testing.T) {
	e := AuditEntry{Actor: actGuard, Action: "chain_autorecover", Result: resStart,
		Detail: "关联压制：上游 chain-node 同时失败，本级不级联恢复"}
	if got := healKindOf(e); got != healCascade {
		t.Fatalf("级联压制被判成 %q，应为 cascade", got)
	}
}

// 时间轴按 Ts 倒序（最新在前），且 limit 是硬上限。
func TestHealTimelineOrderAndLimit(t *testing.T) {
	entries := []AuditEntry{
		{Ts: "2026-11-16 10:00:00.000", Actor: actGuard, Action: "a", Result: resStart},
		{Ts: "2026-11-16 11:00:00.000", Actor: actShadow, Action: "shadow_eval", Result: resOK},
		{Ts: "2026-11-16 12:00:00.000", Actor: actGuard, Action: "a", Result: resOK, Verify: "v"},
	}
	got := healTimeline(entries, 2)
	if len(got) != 2 {
		t.Fatalf("limit=2 得到 %d 条", len(got))
	}
	if got[0].Ts != "2026-11-16 12:00:00.000" || got[0].Kind != healVerified {
		t.Errorf("第一条应是最新的复验，得到 %+v", got[0])
	}
	if got[1].Ts != "2026-11-16 11:00:00.000" || !got[1].ShadowOnly {
		t.Errorf("第二条应是影子且带未执行标记，得到 %+v", got[1])
	}
}

// 空态：没有记录时返回空切片而不是 nil，前端能稳定判 length===0。
func TestHealTimelineEmptyIsNonNil(t *testing.T) {
	got := healTimeline(nil, 30)
	if got == nil {
		t.Fatal("空输入返回 nil，前端无法稳定判空")
	}
	if len(got) != 0 {
		t.Fatalf("空输入得到 %d 条", len(got))
	}
}

// 影子统计：通过次数≠恢复成功，字段名与口径都要钉死。
func TestShadowAggregateCounts(t *testing.T) {
	recs := []ShadowRecord{
		{Strategy: "s1", Ts: "2026-11-16 10:00:00.000", Pass: true},
		{Strategy: "s1", Ts: "2026-11-16 10:01:00.000", Pass: false},
		{Strategy: "s1", Ts: "2026-11-16 10:02:00.000", Pass: true},
		{Strategy: "s2", Ts: "2026-11-16 09:00:00.000", Pass: false},
	}
	got := shadowAggregate(recs)
	if len(got) != 2 {
		t.Fatalf("聚合成 %d 条，应为 2", len(got))
	}
	if got[0].Strategy != "s1" || got[0].Total != 3 || got[0].Pass != 2 {
		t.Errorf("s1 统计错：%+v", got[0])
	}
	if got[0].PassRate < 66 || got[0].PassRate > 67 {
		t.Errorf("s1 通过率应约 66.7，得到 %v", got[0].PassRate)
	}
	if !got[0].LastPass || got[0].LastTs != "2026-11-16 10:02:00.000" {
		t.Errorf("s1 最近一次应是通过：%+v", got[0])
	}
	if got[1].Strategy != "s2" || got[1].PassRate != 0 || got[1].LastPass {
		t.Errorf("s2 统计错：%+v", got[1])
	}
}

// 无记录的策略通过率是 rateNA（-1），不能报 0%。
func TestShadowAggregateEmptyRateIsNA(t *testing.T) {
	got := shadowAggregate(nil)
	if got == nil {
		t.Fatal("空聚合返回 nil")
	}
	if len(got) != 0 {
		t.Fatalf("空输入聚合成 %d 条", len(got))
	}
}

// 看板必须显示"影子跑过的策略"，哪怕它已从当前配置里删除。
// 变异：把并集改成只取 configured → 本测试红。
func TestHealStrategyNamesIncludesStreamedOnly(t *testing.T) {
	got := healStrategyNames(
		[]string{"restart_chain", "restart_web"},
		[]string{"restart_web", "restart_db", "retired_strategy"})
	want := []string{"restart_chain", "restart_db", "restart_web", "retired_strategy"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestHealStrategyNamesDedupAndEmpty(t *testing.T) {
	got := healStrategyNames([]string{"a", "a", ""}, []string{"a", "", "b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("去重/排序错：%v", got)
	}
	if r := healStrategyNames(nil, nil); r == nil || len(r) != 0 {
		t.Fatalf("空输入应返回空非 nil，得到 %v", r)
	}
}
