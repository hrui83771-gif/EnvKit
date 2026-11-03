package main

// trace_api.go —— 轨迹的查询接口（界面用）
//
// ## 为什么需要它
//
// 轨迹一直在落盘（`trace-YYYYMMDD.jsonl`），`traceQuery` 与 `traceSummary`
// 也早就写好了 —— 但**没有任何一处调用它们**：界面上那个"轨迹"页渲染的是
// **浏览器内存里的本轮节点**，刷新即失。于是这份数据只服务于内部统计
// （briefing / stats），人看不到。
//
// 这里的三个纯函数（traceRowOf / traceMatches / traceStatsOf）刻意不与 IO 耦合：
// 过滤与聚合的判据必须能单独测，否则"本机恰好没有失败轨迹"会让断言空转。
//
// ## 隐私边界（沿用 trace_phase.go 的口径）
//
// 轨迹只含动作名、目标引用、结果、耗时与摘要，**不含用户输入原文、
// 文件内容、凭据**。所以这个接口可以原样把轨迹还给界面。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// traceRow 轨迹的列表视图。**不含 Steps** ——
// 单条轨迹最多 120 步、每步证据 300 字，50 条一起发就是几 MB，
// 而列表页只需要"这是哪次、走了多少步、成没成"。明细由 /api/trace 单独取。
type traceRow struct {
	TraceID  string      `json:"trace_id"`
	Goal     string      `json:"goal,omitempty"`
	Actor    string      `json:"actor"`
	Started  string      `json:"started"`
	Ended    string      `json:"ended,omitempty"`
	Outcome  string      `json:"outcome,omitempty"`
	Verified bool        `json:"verified"`
	Steps    int         `json:"steps"`
	DurMs    int64       `json:"dur_ms,omitempty"`
	Usage    *TraceUsage `json:"usage,omitempty"`
	// 四个环节的计数：列表上要能一眼看出"这次 AI 试了几次、错了几次、
	// 有没有靠人接手"，而不是点进去数。
	AICalls  int `json:"ai_calls"`
	AIFails  int `json:"ai_fails"`
	Verifies int `json:"verifies"`
	Humans   int `json:"humans"`
	Policies int `json:"policies"`
	// Summary 与 traceSummary() 同一份产出：界面上不必再拼一遍字符串，
	// 否则"界面说的"和"日志/报告说的"会慢慢分叉。
	Summary string `json:"summary,omitempty"`
}

// traceStats 一批轨迹的聚合。空集时各比率为 0 且 Total=0，
// 界面据此显示"暂无数据"而不是"成功率 0%（看上去像全都失败了）"。
type traceStats struct {
	Total    int     `json:"total"`
	Success  int     `json:"success"`
	Failed   int     `json:"failed"`
	Partial  int     `json:"partial"`
	Aborted  int     `json:"aborted"`
	Verified int     `json:"verified"`
	Humans   int     `json:"humans"`
	AvgSteps float64 `json:"avg_steps"`
	AvgPromp float64 `json:"avg_prompt_tokens"`
	AvgCompl float64 `json:"avg_completion_tokens"`
	// CacheHitPct 是**按 token 加权**的命中率（总 hit / 总 (hit+miss)），
	// 不是各回合百分比的算术平均——后者会让一条小回合和一条大回合等权。
	CacheHitPct float64 `json:"cache_hit_pct"`
}

// traceRowOf 把一条完整轨迹压成列表视图。纯函数。
func traceRowOf(tr *Trace) traceRow {
	if tr == nil {
		return traceRow{}
	}
	r := traceRow{
		TraceID: tr.TraceID, Goal: tr.Goal, Actor: tr.Actor,
		Started: tr.Started, Ended: tr.Ended, Outcome: tr.Outcome,
		Verified: tr.Verified, Steps: len(tr.Steps), Usage: tr.Usage,
		DurMs: traceDurMs(tr.Started, tr.Ended),
	}
	for _, s := range tr.Steps {
		switch s.Phase {
		case phPolicy:
			r.Policies++
		case phHuman:
			r.Humans++
		case phVerify:
			r.Verifies++
		}
		if s.Actor == actAI && s.Phase == phAction {
			r.AICalls++
			if s.Result == resFail {
				r.AIFails++
			}
		}
	}
	r.Summary = traceSummary(tr)
	return r
}

// traceDurMs 起止时间差。解析失败返回 0（界面上显示"—"而不是负数或天文数字）。
func traceDurMs(started, ended string) int64 {
	if started == "" || ended == "" {
		return 0
	}
	const layout = "2006-01-02 15:04:05.000"
	a, err1 := time.ParseInLocation(layout, started, time.Local)
	b, err2 := time.ParseInLocation(layout, ended, time.Local)
	if err1 != nil || err2 != nil {
		return 0
	}
	d := b.Sub(a)
	if d <= 0 {
		return 0
	}
	return d.Milliseconds()
}

// traceMatches 过滤判据。q 传小写化的关键词。
//
// 关键词要能搜到**动作名**：用户记得的是"那天它反复重启后端"，
// 而不是 trace_id。所以命中范围包含 goal / trace_id / 每步的 action 与 target。
func traceMatches(tr *Trace, f traceFilter, qLower string) bool {
	if tr == nil {
		return false
	}
	if f.Outcome != "" && tr.Outcome != f.Outcome {
		return false
	}
	if f.OnlyBad && !traceIsBad(tr) {
		return false
	}
	if !f.Since.IsZero() {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05.000", tr.Started, time.Local); err == nil && t.Before(f.Since) {
			return false
		}
	}
	if qLower == "" {
		return true
	}
	if strings.Contains(strings.ToLower(tr.Goal), qLower) || strings.Contains(strings.ToLower(tr.TraceID), qLower) {
		return true
	}
	for _, s := range tr.Steps {
		if strings.Contains(strings.ToLower(s.Action), qLower) ||
			strings.Contains(strings.ToLower(s.Target), qLower) {
			return true
		}
	}
	return false
}

// traceIsBad 是否是"有问题"的轨迹。
//
// 判据只看**结局与工具失败**：没用复验不算问题（问一句"区块链是什么"本来
// 就不需要复验），把它算进"异常"会让异常列表里全是正常对话。
func traceIsBad(tr *Trace) bool {
	if tr == nil {
		return false
	}
	if tr.Outcome == outFailed || tr.Outcome == outAborted {
		return true
	}
	for _, s := range tr.Steps {
		if s.Phase == phFailure {
			return true
		}
	}
	return false
}

// traceStatsOf 聚合一批列表行。纯函数。
func traceStatsOf(rows []traceRow) traceStats {
	st := traceStats{Total: len(rows)}
	if len(rows) == 0 {
		return st
	}
	var sumSteps int
	var nUsage int
	var sumPrompt, sumCompl, sumHit, sumMiss int64
	for _, r := range rows {
		switch r.Outcome {
		case outSuccess:
			st.Success++
		case outFailed:
			st.Failed++
		case outPartial:
			st.Partial++
		case outAborted:
			st.Aborted++
		}
		if r.Verified {
			st.Verified++
		}
		st.Humans += r.Humans
		sumSteps += r.Steps
		if r.Usage != nil {
			nUsage++
			sumPrompt += r.Usage.Prompt
			sumCompl += r.Usage.Completion
			sumHit += r.Usage.Hit
			sumMiss += r.Usage.Miss
		}
	}
	st.AvgSteps = round1(float64(sumSteps) / float64(len(rows)))
	// 平均值只对**有用量数据**的回合取平均：没报 usage 的回合是"无数据"，
	// 按 0 算会把平均值拉低，看起来像"最近变省了"。
	if nUsage > 0 {
		st.AvgPromp = round1(float64(sumPrompt) / float64(nUsage))
		st.AvgCompl = round1(float64(sumCompl) / float64(nUsage))
	}
	if d := sumHit + sumMiss; d > 0 {
		st.CacheHitPct = round1(100 * float64(sumHit) / float64(d))
	}
	return st
}

// round1 复用 eval_api.go 里的同名实现（保留一位小数）：
// 同一件事只该有一份实现，否则两处的取整规则迟早分叉。

func uq(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// traceFindByID 从盘上找一条轨迹。扫最近的若干天，命中即返回。
func traceFindByID(id string) *Trace {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(traceDir(), "trace-*.jsonl"))
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, ln := range strings.Split(string(b), "\n") {
			ln = strings.TrimSpace(ln)
			if ln == "" || !strings.Contains(ln, id) {
				// 先做一次廉价的子串预筛：反序列化整条轨迹是这里最贵的操作，
				// 而绝大多数行根本不包含这个 id。
				continue
			}
			var t Trace
			if json.Unmarshal([]byte(ln), &t) != nil {
				continue
			}
			if t.TraceID == id {
				return &t
			}
		}
	}
	return nil
}

// handleTraces GET /api/traces —— 历史轨迹列表 + 聚合统计
//
// 参数：n（条数，默认 50，上限 500）、outcome、q（关键词）、days（最近 N 天）、
// onlybad=1（只看有问题）
func handleTraces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	q := r.URL.Query()
	n, _ := strconv.Atoi(q.Get("n"))
	if n <= 0 || n > 500 {
		n = 50
	}
	days, _ := strconv.Atoi(q.Get("days"))
	f := traceFilter{
		Outcome: strings.TrimSpace(q.Get("outcome")),
		Q:       strings.TrimSpace(q.Get("q")),
		OnlyBad: q.Get("onlybad") == "1",
	}
	if days > 0 {
		f.Since = time.Now().AddDate(0, 0, -days)
	}
	list := traceQueryFiltered(n, f)
	rows := make([]traceRow, 0, len(list))
	for i := range list {
		rows = append(rows, traceRowOf(&list[i]))
	}
	writeJSON(w, map[string]any{
		"ok":             true,
		"traces":         rows,
		"stats":          traceStatsOf(rows),
		"retention_days": devSnap().traceRetentionDays(),
	})
}

// handleTraceOne GET /api/trace?id=xxx —— 单条轨迹的全部步骤
func handleTraceOne(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "missing id", 400)
		return
	}
	tr := traceFindByID(id)
	if tr == nil {
		// 找不到就明说。不要返回空轨迹让人以为"这次什么都没做"——
		// 被保留期清掉、还是 id 传错，对用户是两句完全不同的话。
		writeJSONStatus(w, 404, map[string]any{
			"ok": false, "error": "这条轨迹已不在磁盘上（可能超过保留期被清理，或 id 不正确）",
		})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "trace": tr, "row": traceRowOf(tr)})
}
