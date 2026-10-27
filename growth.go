package main

// growth.go —— Agent 闭环 Phase 3：成长指标（把「越用越聪明」变成曲线）。
//
// 设计文档：docs/DESIGN-agent-loop.md §4.2。
//
// 三条约法，改之前先读：
//
// 1) **只用确定性数据**（missions.json / playbooks.json / 审计），不调模型。
//    指标的用途是"证明或证伪"，一旦中间经过一次模型转述，它就只是另一种说法。
//    同一个数据集，换个人来算必须得到同一个数。
//
// 2) **每个比率都写清分子分母**，且分母为 0 时返回 rateNA（-1），
//    界面显示"—"。0/0 显示成 0% 是撒谎：它看起来像"全都没成"。
//
// 3) **一轮只落一个桶**：done/failed/canceled/expired 之和必须等于 Missions。
//    这条写成断言（growthAssertBuckets）。各桶之和 ≠ 分母说明分类口径有重叠，
//    那是比数字难看严重得多的问题——两个页面会算出两个不同的"解决率"，
//    而用户没法判断哪个是真的。
//
// 刻意**不做**的：跨项目合并（不同项目的成功率不可比）、
// 用模型给处置质量打分（不可复算）、把"用户是否满意"塞进指标（不可观测）。

import (
	"fmt"
	"sort"
	"time"
)

// rateNA 分母为 0 时的哨兵值。界面必须显示 "—" 而不是 0%。
//
// 用负数而不是 0：0 是一个合法的比率（一次都没成），
// 两者在 JSON 里必须能区分开，否则前端无从判断该显示 0% 还是 "—"。
const rateNA = -1.0

// GrowthBucket 一个时间桶（某一周，或 "all"）的行动统计。
//
// 字段顺序即界面列顺序。每一项都能从 missions.json 直接数出来，
// 不需要二次推断。
type GrowthBucket struct {
	Key      string `json:"key"` // "2026-W43" 或 "all"
	Missions int    `json:"missions"`
	Done     int    `json:"done"`
	// FirstTry 无人工修正即 done（Plan 里没有失败步骤）。
	FirstTry int `json:"first_try"`
	Failed   int `json:"failed"`
	Canceled int `json:"canceled"`
	Expired  int `json:"expired"`

	Steps       int `json:"steps"`        // 已落盘步骤总数
	FailedSteps int `json:"failed_steps"` // 其中失败步骤数
	// Minutes 从创建到收口的墙钟时间合计。含用户离开的时间——
	// 这是刻意的：它衡量"这件事拖了多久"，不是"AI 算了多久"。
	Minutes float64 `json:"minutes"`

	PbkTotal int `json:"pbk_total"` // 套了预案的行动数
	PbkOK    int `json:"pbk_ok"`    // 套了预案且复验通过
}

// GrowthRates 比率视图。全部是百分数（0~100），rateNA 表示无数据。
//
// 分开一个结构而不是塞进 Bucket：Bucket 是可加总的原始计数（多周相加有意义），
// 比率不可加总（两周各 50% 不等于合计 50%）。混在一起迟早会有人去加它。
type GrowthRates struct {
	FirstTry   float64 `json:"first_try_rate"`
	PbkHit     float64 `json:"pbk_hit_rate"`
	PbkOK      float64 `json:"pbk_ok_rate"`
	UserFix    float64 `json:"user_fix_rate"`
	AvgSteps   float64 `json:"avg_steps"`
	AvgMinutes float64 `json:"avg_minutes"`
}

// GrowthLessons 经验层的计数（口径与 missions 不同，单独一栏，不混算）。
type GrowthLessons struct {
	Total      int `json:"total"`
	RepeatFail int `json:"repeat_fail"`
	UserFix    int `json:"user_fix"`
	VerifyFail int `json:"verify_fail"`
	Denied     int `json:"denied"`
	Slow       int `json:"slow"`
}

// GrowthReport 一份成长报表。
type GrowthReport struct {
	Days    int            `json:"days"`
	From    string         `json:"from"`  // 窗口起点（含）
	To      string         `json:"to"`    // 生成时刻
	Weeks   []GrowthBucket `json:"weeks"` // 按周全量，最老在前
	Total   GrowthBucket   `json:"total"`
	Rates   GrowthRates    `json:"rates"`
	Lessons GrowthLessons  `json:"lessons"`
	// Note 数据不足时如实说明。空 = 没什么要额外解释的。
	Note string `json:"note,omitempty"`
}

// growthDefaultDays 默认窗口：8 周。
//
// 8 而不是 4：4 周的样本量下，"这周成功率"经常是 1/1 或 0/2，
// 那种数字会让人做出完全错误的趋势判断。
const growthDefaultDays = 56

// growthReport 生成报表（纯计算，无副作用，可直接在单测里调）。
func growthReport(days int) GrowthReport {
	if days <= 0 {
		days = growthDefaultDays
	}
	if days < 7 {
		days = 7
	}
	if days > 365 {
		days = 365
	}
	now := time.Now()
	from := now.AddDate(0, 0, -days)

	rep := GrowthReport{
		Days:  days,
		From:  from.Format("2006-01-02"),
		To:    now.Format("2006-01-02 15:04:05"),
		Total: GrowthBucket{Key: "all"},
	}
	byWeek := map[string]*GrowthBucket{}
	for _, m := range missionList() {
		if !missionTerminal(m.State) {
			continue // 未收口的不进任何桶：它还没有结论
		}
		// 用收口时间（UpdatedAt）分桶：「这周解决了多少」按解决时刻归周。
		// 用 CreatedAt 会让一场跨周的处置落在它开始的那一周，看起来像
		// "上周积压了一堆，这周零产出"。
		if m.UpdatedAt.Before(from) {
			continue
		}
		k := isoWeekKey(m.UpdatedAt)
		b, ok := byWeek[k]
		if !ok {
			b = &GrowthBucket{Key: k}
			byWeek[k] = b
		}
		growthAdd(b, m)
		growthAdd(&rep.Total, m)
	}
	for _, b := range byWeek {
		rep.Weeks = append(rep.Weeks, *b)
	}
	// 周键 "2026-W43" 的字典序就是时间序（年四位、周补零），不必解析回时间。
	sort.Slice(rep.Weeks, func(i, j int) bool { return rep.Weeks[i].Key < rep.Weeks[j].Key })

	rep.Lessons = growthCountLessons()
	rep.Rates = growthRates(rep.Total, rep.Lessons)
	rep.Note = growthNote(rep)
	return rep
}

// growthAdd 把一条终态行动计入桶。
func growthAdd(b *GrowthBucket, m Mission) {
	b.Missions++
	switch m.State {
	case msDone:
		b.Done++
		if growthFirstTry(m) {
			b.FirstTry++
		}
	case msFailed:
		b.Failed++
	case msCanceled:
		b.Canceled++
	case msExpired:
		b.Expired++
	}
	for _, st := range m.Plan {
		b.Steps++
		if !st.OK {
			b.FailedSteps++
		}
	}
	if d := m.UpdatedAt.Sub(m.CreatedAt); d > 0 {
		b.Minutes += d.Minutes()
	}
	if len(m.Draft) > 0 || m.Playbook != "" {
		b.PbkTotal++
		if m.Result != nil && m.Result.Verified {
			b.PbkOK++
		}
	}
}

// growthAssertBuckets 校验「一轮只落一个桶」。返回 false 表示口径出问题。
//
// 单独抽出来是为了能在测试里直接调：把它埋在 growthReport 里就只能
// 通过构造数据去间接验证，而那种测试通常写不成。
func growthAssertBuckets(b GrowthBucket) bool {
	return b.Done+b.Failed+b.Canceled+b.Expired == b.Missions
}

// growthFirstTry 「无人工修正即 done」。
//
// 判据只看记录里的事实：终态 done，且已落盘的步骤里没有失败步骤。
// 不去猜"用户有没有在心里修正过"——那不可观测，猜出来的数字不能进报表。
// 注意它比 Done 更严：done 但中间试错过的，不算首次解决。
func growthFirstTry(m Mission) bool {
	if m.State != msDone {
		return false
	}
	for _, st := range m.Plan {
		if !st.OK {
			return false
		}
	}
	return true
}

// growthRates 由计数算比率。分母为 0 一律 rateNA。
func growthRates(b GrowthBucket, ls GrowthLessons) GrowthRates {
	pct := func(a, c int) float64 {
		if c <= 0 {
			return rateNA
		}
		return round1(float64(a) * 100 / float64(c))
	}
	avg := func(a, c int) float64 {
		if c <= 0 {
			return rateNA
		}
		return round1(float64(a) / float64(c))
	}
	return GrowthRates{
		FirstTry: pct(b.FirstTry, b.Missions),
		PbkHit:   pct(b.PbkTotal, b.Missions),
		PbkOK:    pct(b.PbkOK, b.PbkTotal),
		// 用户修正率 = user_fix 经验数 / 终态行动数。
		// 分子来自经验层（30 天审计窗口），分母来自行动层（days 窗口），
		// 两个窗口不一定相同 —— 这是这一项唯一的口径弱点，界面上会标出来。
		UserFix:    pct(ls.UserFix, b.Missions),
		AvgSteps:   avg(b.Steps, b.Missions),
		AvgMinutes: round1(b.Minutes / float64(maxInt(b.Missions, 1))),
	}
}

// growthCountLessons 数各类经验。用 lessonsFor（带缓存）而不是重新提取。
func growthCountLessons() GrowthLessons {
	ls := lessonsFor()
	var out GrowthLessons
	out.Total = len(ls)
	for _, l := range ls {
		switch l.Kind {
		case lsRepeatFail:
			out.RepeatFail++
		case lsUserFix:
			out.UserFix++
		case lsVerifyFail:
			out.VerifyFail++
		case lsDenied:
			out.Denied++
		case lsSlow:
			out.Slow++
		}
	}
	return out
}

// growthNote 如实说明数据不足，而不是给一个漂亮的空报表。
func growthNote(rep GrowthReport) string {
	if rep.Total.Missions == 0 {
		return "窗口内还没有收口的行动。指标要等真实处置发生后才成立——这份报表不会用占位数字充数。"
	}
	if rep.Total.Missions < 5 {
		return fmt.Sprintf("窗口内只有 %d 条收口行动：单项比率（尤其首次解决率与预案有效率）"+
			"在这个样本量下波动很大，只适合看有没有明显异常，不适合看趋势。", rep.Total.Missions)
	}
	if rep.Lessons.Total == 0 {
		return "行动有记录但还没有沉淀出经验：经验提取的阈值（同一动作失败 3 次等）刻意保守，样本没到就不会写。"
	}
	return ""
}

// isoWeekKey ISO 8601 周键，如 "2026-W43"。
//
// 用 ISO 周而不是自然月：周与周等长，做环比时不会出现"这个月只有 28 天"。
func isoWeekKey(t time.Time) string {
	y, w := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w)
}

// maxInt 小工具（Go 1.21 有内置 max，但这个仓库里用的是 int 版本的局部函数名，
// 避免与将来引入的内置 max 混淆）。
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
