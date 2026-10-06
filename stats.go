package main

// stats.go —— 审计与轨迹的时序统计（看板数据源）
//
// ## 为什么需要它
//
// 此前所有指标都是**累计值**：1000 条审计算出一个比例、305 条轨迹算出一个均值。
// 累计值看不出趋势——「复验通过率 95%」是真的稳，还是从 60% 慢慢爬到 95%？
// 「今天失败 4 次」是比昨天多还是少？**都答不出来。**
//
// 看板要回答的是趋势问题，所以必须按时间分桶重算。
//
// ## 数据从哪来（两个源，各有分工）
//
// |源 | 覆盖 | 用途 |
// |---|---|---|
// | audit-YYYYMMDD.jsonl | 30 天 | 操作量、结果分布、主体分布、复验结论 |
// | trace-YYYYMMDD.jsonl | 14 天 | 任务结局、token 用量、工具调用 |
//
// 审计留30 天、轨迹留 14 天（`cleanupOldTraces`），
// 所以**看板的时间轴按 30 天画，但轨迹类指标最多只能覆盖 14 天**——
// 这个差异要显式告诉前端，不能让它把两类指标画在同一根轴上。
//
// ## 三条口径纪律（与 eval/metrics 一脉相承）
//
// 1. **无样本报「无样本」，不报 0。** 没数据不是零。
// 2. **分母不同不可平均。** 复验率的分母是「有复验的次数」，不是总操作数。
// 3. **缓存细分与总量分开。** `HasCache=false` 时 Hit/Miss 无意义，
//    报成 0 会把「便宜」和「贵」显示成同一个数。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ===== 类型 =====

// DayStat 某一天的一行。
type DayStat struct {
	Date string `json:"date"` // 2006-01-02

	// 审计侧
	Ops         int            `json:"ops"`          // 操作总数
	OK          int            `json:"ok"`           // ok
	Fail        int            `json:"fail"`         // fail
	Denied      int            `json:"denied"`       // 被裁决器拦下（**预期行为，不算失败**）
	Started     int            `json:"started"`      // 异步已启动
	ByActor     map[string]int `json:"by_actor"`     // 主体分布
	ByResult    map[string]int `json:"by_result"`    // 结果分布
	VerifyCount int            `json:"verify_count"` // 带了复验结论的操作数
	// VerifyOK 是「复验通过」的条数。**分母是 VerifyCount 而不是 Ops**——
	// 没复验的操作既不是通过也不是失败，混进去会让比率失去意义。
	VerifyOK int `json:"verify_ok"`

	// 轨迹侧
	Traces        int64 `json:"traces"`
	TracesSuccess int64 `json:"traces_success"`
	TracesFailed  int64 `json:"traces_failed"`
	TracesPartial int64 `json:"traces_partial"`
	TracesAborted int64 `json:"traces_aborted"`
	TracesWithUse int64 `json:"traces_with_usage"`

	// Token（**仅统计带 usage 的轨迹**）
	TokenPrompt     int64 `json:"token_prompt"`
	TokenCompletion int64 `json:"token_completion"`
	TokenCacheHit   int64 `json:"token_cache_hit"`
	TokenHasCache   bool  `json:"token_has_cache"`
}

// Totals 区间合计。看板顶部那排大数字。
type Totals struct {
	Days int `json:"days"`

	Ops        int            `json:"ops"`
	OK         int            `json:"ok"`
	Fail       int            `json:"fail"`
	Denied     int            `json:"denied"`
	Started    int            `json:"started"`
	ByActor    map[string]int `json:"by_actor"`
	Actions    map[string]int `json:"actions"` // Top N 动作
	LatencyP50 int64          `json:"latency_p50"`
	LatencyP90 int64          `json:"latency_p90"`

	VerifyCount int     `json:"verify_count"`
	VerifyOK    int     `json:"verify_ok"` // 复验通过数
	VerifyRate  float64 `json:"verify_rate"`
	// VerifyRateHasSample=false 表示无样本（VerifyRate 为 -1）。
	// **不能只报 0** —— 0 是「测到了、结果是 0」，与「没测到」是两件事。
	VerifyRateHasSample bool `json:"verify_rate_has_sample"`

	Traces        int64 `json:"traces"`
	TracesSuccess int64 `json:"traces_success"`
	TracesFailed  int64 `json:"traces_failed"`
	TracesPartial int64 `json:"traces_partial"`
	TracesAborted int64 `json:"traces_aborted"`

	TokenTotal      int64 `json:"token_total"`
	TokenPrompt     int64 `json:"token_prompt"`
	TokenCompletion int64 `json:"token_completion"`
	TokenCacheHit   int64 `json:"token_cache_hit"`
	TokenHasCache   bool  `json:"token_has_cache"`
	TokenSamples    int64 `json:"token_samples"` // 带 usage 的轨迹数

	// 覆盖范围诚实说明：审计 30 天、轨迹 14 天，画在同一张图上会误导。
	AuditDaysAvailable int `json:"audit_days_available"`
	TraceDaysAvailable int `json:"trace_days_available"`
	TraceRetentionDays int `json:"trace_retention_days"`
}

// StatsBoard 看板响应。
type StatsBoard struct {
	GeneratedAt string    `json:"generated_at"`
	Days        []DayStat `json:"days"` // 旧 → 新，画图顺序友好
	Totals      Totals    `json:"totals"`
	// Notices 是给前端的口头提醒（哪条指标没样本、时间轴为什么长度不一）。
	Notices []string `json:"notices"`
}

// ===== 统计 =====

// statNoSample 是「无样本」的哨兵值。**不能用 0**——
// 0 表示「测到了、结果是 0」，那是两件事。
const statNoSample = -1.0

// statsRange 读取最近 days 天的审计与轨迹，按天分桶。
func statsRange(days int) *StatsBoard {
	if days <= 0 {
		days = 30
	}
	if days > 90 {
		days = 90
	}
	cutoff := time.Now().AddDate(0, 0, -(days - 1))
	cutDay := cutoff.Format("20060102")

	b := &StatsBoard{
		GeneratedAt: time.Now().Format("2006-01-02 15:04:05"),
		Days:        []DayStat{},
	}

	// 桶：日期 → 下标
	var dateList []string
	idx := map[string]int{}
	for i := 0; i < days; i++ {
		d := time.Now().AddDate(0, 0, -i).Format("20060102")
		dateList = append(dateList, d)
		idx[d] = len(dateList) - 1
	}
	dateListReversed := make([]string, len(dateList))
	for i, d := range dateList {
		dateListReversed[len(dateList)-1-i] = d // 反转成旧→新
	}
	dayStats := make([]DayStat, days)
	for i, d := range dateListReversed {
		dayStats[i] = DayStat{
			Date: d, ByActor: map[string]int{}, ByResult: map[string]int{},
		}
	}

	// ---- 审计 ----
	auditDays := auditDays()
	for _, d := range auditDays {
		if d < cutDay {
			continue
		}
		i, ok := idx[d]
		if !ok {
			continue
		}
		es := readAuditFile(filepath.Join(auditDir(), "audit-"+d+".jsonl"))
		for _, e := range es {
			ds := &dayStats[i]
			ds.Ops++
			ds.ByActor[e.Actor]++
			ds.ByResult[e.Result]++
			switch e.Result {
			case resOK:
				ds.OK++
			case resFail:
				ds.Fail++
			case resDenied:
				ds.Denied++
			case resStart:
				ds.Started++
			}
			if e.Verify != "" {
				ds.VerifyCount++
				if strings.Contains(e.Verify, "已复验通过") || strings.Contains(e.Verify, "通过") {
					ds.VerifyOK++
				}
			}
			b.Totals.Ops++
			b.Totals.OK += boolInt(e.Result == resOK)
			b.Totals.Fail += boolInt(e.Result == resFail)
			b.Totals.Denied += boolInt(e.Result == resDenied)
			b.Totals.Started += boolInt(e.Result == resStart)
			if e.Verify != "" {
				b.Totals.VerifyCount++
				if strings.Contains(e.Verify, "已复验通过") || strings.Contains(e.Verify, "通过") {
					b.Totals.VerifyOK++
				}
			}
		}
	}
	b.Totals.AuditDaysAvailable = len(auditDays)

	// ---- 轨迹 ----
	traceFiles, _ := filepath.Glob(filepath.Join(traceDir(), "trace-*.jsonl"))
	traceDaySet := map[string]bool{}
	var lats []int64
	for _, f := range traceFiles {
		base := filepath.Base(f)
		d := strings.TrimSuffix(strings.TrimPrefix(base, "trace-"), ".jsonl")
		if len(d) != 8 || d < cutDay {
			continue
		}
		traceDaySet[d] = true
		i, ok := idx[d]
		if !ok {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, ln := range strings.Split(string(raw), "\n") {
			ln = strings.TrimSpace(ln)
			if ln == "" {
				continue
			}
			var t Trace
			if json.Unmarshal([]byte(ln), &t) != nil {
				continue
			}
			ds := &dayStats[i]
			ds.Traces++
			b.Totals.Traces++
			switch t.Outcome {
			case outSuccess:
				ds.TracesSuccess++
				b.Totals.TracesSuccess++
			case outFailed:
				ds.TracesFailed++
				b.Totals.TracesFailed++
			case outPartial:
				ds.TracesPartial++
				b.Totals.TracesPartial++
			default:
				ds.TracesAborted++
				b.Totals.TracesAborted++
			}
			if t.Usage != nil {
				ds.TracesWithUse++
				ds.TokenPrompt += t.Usage.Prompt
				ds.TokenCompletion += t.Usage.Completion
				ds.TokenCacheHit += t.Usage.Hit
				if t.Usage.HasCache {
					ds.TokenHasCache = true
				}
				b.Totals.TokenSamples++
				b.Totals.TokenPrompt += t.Usage.Prompt
				b.Totals.TokenCompletion += t.Usage.Completion
				b.Totals.TokenCacheHit += t.Usage.Hit
				if t.Usage.HasCache {
					b.Totals.TokenHasCache = true
				}
			}
		}
	}
	b.Totals.TraceDaysAvailable = len(traceDaySet)
	b.Totals.TraceRetentionDays = 14

	// ---- 延迟（只有带 dur_ms 的记录才有）----
	{
		var all []int64
		for _, d := range auditDays {
			if d < cutDay {
				continue
			}
			for _, e := range readAuditFile(filepath.Join(auditDir(), "audit-"+d+".jsonl")) {
				if e.DurMs > 0 {
					all = append(all, e.DurMs)
				}
			}
		}
		if len(all) > 0 {
			sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
			b.Totals.LatencyP50 = all[len(all)/2]
			b.Totals.LatencyP90 = all[minInt(len(all)-1, len(all)*90/100)]
			lats = all
		}
	}
	_ = lats

	// ---- 主体与动作分布 ----
	b.Totals.ByActor = map[string]int{}
	b.Totals.Actions = map[string]int{}
	for _, d := range auditDays {
		if d < cutDay {
			continue
		}
		for _, e := range readAuditFile(filepath.Join(auditDir(), "audit-"+d+".jsonl")) {
			b.Totals.ByActor[e.Actor]++
			b.Totals.Actions[e.Action]++
		}
	}

	// ---- 复验率：无样本报 -1 ----
	if b.Totals.VerifyCount > 0 {
		b.Totals.VerifyRate = float64(b.Totals.VerifyOK) / float64(b.Totals.VerifyCount)
		b.Totals.VerifyRateHasSample = true
	} else {
		b.Totals.VerifyRate = statNoSample
		b.Totals.VerifyRateHasSample = false
	}
	b.Totals.TokenTotal = b.Totals.TokenPrompt + b.Totals.TokenCompletion

	// ---- 口径提醒 ----
	if !b.Totals.VerifyRateHasSample {
		b.Notices = append(b.Notices,
			"复验率无样本：区间内没有带复验结论的操作记录")
	}
	if b.Totals.TokenSamples == 0 {
		b.Notices = append(b.Notices,
			"token 无数据：轨迹里的 usage 字段是 v2.7 才加的，此前的对话没有记录")
	}
	if b.Totals.TraceDaysAvailable < b.Totals.AuditDaysAvailable {
		b.Notices = append(b.Notices,
			"轨迹只保留 14 天、审计保留 30 天，因此「任务结局」与「token」两行覆盖更短")
	}

	b.Days = dayStats
	return b
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// handleStats 看板数据接口。只读，不落审计。
func handleStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		n := 0
		for _, c := range v {
			if c < '0' || c > '9' {
				n = 0
				break
			}
			n = n*10 + int(c-'0')
		}
		if n > 0 {
			days = n
		}
	}
	_ = json.NewEncoder(w).Encode(statsRange(days))
}
