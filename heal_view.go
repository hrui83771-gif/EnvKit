package main

// heal_view.go —— 自愈态势与影子晋升的**只读视图**（2026-11-16，功能 ①②）
//
// 解决什么：守护循环和影子事务一直在生产路径上真跑（guard.go / shadow.go），
// 但它们做过的事只落在审计与 shadow-*.jsonl 里，界面上没有一条时间轴能看。
// 本文件把既有事实聚合成界面可消费的两块，不写任何状态、不新增端点，
// 只在 /api/guard/status 上扩字段。
//
// 铁律：
//   - 分类只认客观字段（actor / action / verify / detail 的结构化部分），
//     **不读中文文案猜意图**——文案会改，字段不会。
//   - 影子评估的 result=ok 表示"评估通过"，**不是恢复成功**。视图里
//     必须带 stopped_before=execute 与 label 区分，避免把 20 次通过读成
//     20 次恢复（shadow.go 文件头已钉死这条）。
//   - 空态是"尚无记录"，不是"没有能力"——新机器没跑过守护，不能显示成
//     "守护不工作"。
//   - 分母为 0 返回 -1（rateNA 同口径），界面显示 "—" 而不是 0%。

import (
	"sort"
	"strings"
)

// healKind 时间轴条目类型。取值固定，前端按此着色/改名，不解析中文。
const (
	healRecover  = "recover"    // 自动恢复已发起（审计 result=started）
	healVerified = "verified"   // 复验通过（actor=guard, verify 非空, result=ok）
	healUnverif  = "unverified" // 已拉起但复验未完成（result=unverified）
	healFailed   = "failed"     // 复验失败（result=fail）
	healCascade  = "cascade"    // 跨节点关联压制：决定不救，必须可见
	healShadow   = "shadow"     // 影子评估：在 Execute 前停止，未执行
	healOther    = "other"      // 其它 guard 记录（skip_* 等），原样带出
)

// healKindOf 从审计条目客观地判定条目类型。纯函数。
//
// 判定顺序有讲究：影子先于其它（actor 唯一），级联先于恢复
// （级联压制也用 recover 的 action，但从不执行），复验先于发起
// （两条同 action，靠 Verify 字段区分）。
func healKindOf(e AuditEntry) string {
	if e.Actor == actShadow {
		return healShadow
	}
	if e.Actor != actGuard {
		return healOther
	}
	if strings.Contains(e.Detail, "关联压制") {
		return healCascade
	}
	if e.Verify != "" {
		switch e.Result {
		case resOK:
			return healVerified
		case resUnverified:
			return healUnverif
		case resFail:
			return healFailed
		}
	}
	if e.Result == resStart {
		return healRecover
	}
	return healOther
}

// healItem 时间轴上的一条事实。字段名与界面契约固定。
type healItem struct {
	Ts     string `json:"ts"`
	Kind   string `json:"kind"`
	Action string `json:"action"`
	Target string `json:"target"`
	Result string `json:"result"`
	Detail string `json:"detail,omitempty"`
	Verify string `json:"verify,omitempty"`
	// ShadowOnly 只有影子条目为 true：它意味着"若执行会做什么"，
	// 而不是"做了什么"。前端据此加显眼的未执行标记。
	ShadowOnly bool `json:"shadow_only,omitempty"`
}

// healTimeline 把两类审计合流成时间轴（纯函数，无 IO）。
// entries 可以是不同 actor 的记录；按 Ts 排序（审计时间戳是定宽字符串，
// 字典序即时间序），取最新 limit 条。
func healTimeline(entries []AuditEntry, limit int) []healItem {
	items := make([]healItem, 0, len(entries))
	for _, e := range entries {
		kind := healKindOf(e)
		items = append(items, healItem{
			Ts: e.Ts, Kind: kind, Action: e.Action, Target: e.Target,
			Result: e.Result, Detail: e.Detail, Verify: e.Verify,
			ShadowOnly: kind == healShadow,
		})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Ts > items[j].Ts })
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items
}

// healCollectAudit 采集守护与影子两类审计的原始条目。
// 两条流都按 actor 过滤（审计文件本身按日期分），合并后交给纯函数排序。
// 每条各取 limit 条再合流——若只取总数 limit，影子记录可能被恢复记录挤掉。
func healCollectAudit(limit int) []AuditEntry {
	if limit <= 0 {
		limit = 30
	}
	guard := auditQuery("all", actGuard, limit)
	shadow := auditQuery("all", actShadow, limit)
	out := make([]AuditEntry, 0, len(guard)+len(shadow))
	out = append(out, guard...)
	out = append(out, shadow...)
	return out
}

// shadowAgg 一条策略的累计影子统计。
type shadowAgg struct {
	Strategy string  `json:"strategy"`
	Total    int     `json:"total"`     // 历史影子评估总次数
	Pass     int     `json:"pass"`      // 其中评估通过次数（≠恢复成功）
	PassRate float64 `json:"pass_rate"` // -1 = 无记录，界面显示 "—"
	LastTs   string  `json:"last_ts,omitempty"`
	LastPass bool    `json:"last_pass"` // 最近一次是否通过（连续计数的直观体现）
}

// shadowAggregate 从影子记录流聚合每策略统计（纯函数）。
// 同一策略按 Ts 正序读到的最后一条即最近一次。
func shadowAggregate(recs []ShadowRecord) []shadowAgg {
	idx := map[string]int{}
	out := make([]shadowAgg, 0)
	for _, r := range recs {
		i, ok := idx[r.Strategy]
		if !ok {
			i = len(out)
			idx[r.Strategy] = i
			out = append(out, shadowAgg{Strategy: r.Strategy, PassRate: rateNA})
		}
		a := &out[i]
		a.Total++
		if r.Pass {
			a.Pass++
		}
		if r.Ts >= a.LastTs {
			a.LastTs = r.Ts
			a.LastPass = r.Pass
		}
	}
	for i := range out {
		if out[i].Total > 0 {
			out[i].PassRate = float64(out[i].Pass) * 100 / float64(out[i].Total)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Strategy < out[j].Strategy })
	return out
}

// healStrategyNames 看板该显示哪些策略名（纯函数）。
// 口径：已配置策略 ∪ 影子流里出现过的策略。原 shadowStatusForAPI 只遍历
// 已配置服务——一条策略若从配置里删了，它历史跑过的 20 次影子就从看板
// 消失，用户会以为从没评估过。并集保证"跑过的都在"。
func healStrategyNames(configured, streamed []string) []string {
	seen := make(map[string]bool, len(configured)+len(streamed))
	out := make([]string, 0, len(configured)+len(streamed))
	for _, name := range append(append([]string{}, configured...), streamed...) {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func keysOfReadiness(list []PromotionReadiness) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, s.Strategy)
	}
	return out
}

// healShadowView 影子晋升看板的数据。
// 复用 shadowStatusForAPI 的 readiness（唯一口径），只补累计统计与空态说明。
func healShadowView() map[string]any {
	base := shadowStatusForAPI()
	recs := shadowReadAll()
	aggs := shadowAggregate(recs)
	base["aggregates"] = aggs

	// promotionReadiness 对未注册策略会返回 Route=pending + Blocker="策略未注册"，
	// 如实显示，不伪造晋升状态。
	configured, _ := base["strategies"].([]PromotionReadiness)
	streamed := make([]string, 0, len(aggs))
	for _, a := range aggs {
		streamed = append(streamed, a.Strategy)
	}
	byName := map[string]PromotionReadiness{}
	for _, s := range configured {
		byName[s.Strategy] = s
	}
	strategies := make([]PromotionReadiness, 0)
	for _, name := range healStrategyNames(keysOfReadiness(configured), streamed) {
		if s, ok := byName[name]; ok {
			strategies = append(strategies, s)
		} else {
			strategies = append(strategies, promotionReadiness(name))
		}
	}
	base["strategies"] = strategies

	if len(strategies) == 0 {
		base["empty_note"] = "当前没有配置了恢复策略的可守护服务，也没有影子评估记录；影子评估需在「程序配置 → 链端」开启守护后才会产生。"
	} else {
		base["empty_note"] = ""
	}
	return base
}

// guardStatusView 扩展的守护状态视图。
// status / recent 保持原样——老页面与 AI 工具都读它们，不能改语义。
func guardStatusView() map[string]any {
	return map[string]any{
		"status":   guardSnapshot(),
		"recent":   guardRecentView(),
		"timeline": healTimeline(healCollectAudit(30), 30),
		"shadow":   healShadowView(),
	}
}

// guardRecentView 原来的最近恢复动作（含过滤），原样抽出来保持兼容。
func guardRecentView() []map[string]any {
	recent := auditQuery("all", actGuard, 8)
	acts := make([]map[string]any, 0, len(recent))
	for _, e := range recent {
		if !strings.Contains(e.Action, "recover") && !strings.Contains(e.Action, "unreachable") {
			continue
		}
		acts = append(acts, map[string]any{
			"ts": e.Ts, "action": e.Action, "target": e.Target,
			"result": e.Result, "detail": e.Detail, "verify": e.Verify,
		})
		if len(acts) >= 5 {
			break
		}
	}
	return acts
}
