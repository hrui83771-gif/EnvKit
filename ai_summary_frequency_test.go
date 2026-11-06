package main

// ai_summary_frequency_test.go —— 把「摘要压缩频率」变成可复现的确定性数字。
//
// 为什么不是起一次长对话看日志：真模型有随机性、一次观测给不出倍数，还要花 token。
// 这里直接驱动**生产用的 aiSummaryCut**（不是另写一份复刻逻辑），模拟逐回合增长的
// 历史，在 10000（旧默认）与 16000（本机 11/6 新值）两组配置下分别统计触发次数 /
// 首次触发回合 / 压缩后水位。零网络、零 token，go test ./... 自动带。
//
// 判据不能只看「两个数字谁大谁小」，否则夹具没触发过也会假绿：
//  1. 空转防护 —— 两组都必须真的触发过摘要；
//  2. 机理断言 —— 每次压缩后水位必须回落到阈值以下（降频的真正原因）；
//  3. 独立预测 —— 用「压完水位 + 每回合增量」独立算出下次触发回合，与实际对照。

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// 30 回合而不是 24：16000 组在第 14 回合才首次触发，按独立预测的 12 回合间隔
// 第二次在第 26 回合。会话太短就凑不出「间隔」这个前提，独立预测断言会被跳过 ——
// 与其 SKIP，不如把夹具加长让它真的跑到。
const freqTurns = 30

type freqResult struct {
	After      int   `json:"after_tokens"`
	Turns      int   `json:"turns"`
	TurnTokens int   `json:"turn_tokens"`
	Fires      int   `json:"fires"`
	FirstTurn  int   `json:"first_fire_turn"`
	FireTurns  []int `json:"fire_turns"`
	Cuts       []int `json:"cuts"`
	PreTotals  []int `json:"pre_totals"`
	WaterAfter []int `json:"water_after"`
}

// freqTurnMsgs 一个 Agent 工具轮的消息增量：短提问 + 两段大工具结果 + 结论。
// 全确定性（不用随机），长度贴近 11/4 日志里「一轮就上千 token」的形态。
func freqTurnMsgs(turn int) []aiMsg {
	pad := func(n int) string { return strings.Repeat("记", n) }
	return []aiMsg{
		{Role: "user", Content: fmt.Sprintf("第%d轮：帮我查一下后端状态和端口占用", turn)},
		{Role: "assistant", Content: "我先读取日志并核对监听端口。" + pad(40)},
		{Role: "tool", Name: "get_logs", Content: "日志片段：" + pad(400)},
		{Role: "tool", Name: "read_file", Content: "配置片段：" + pad(500)},
		{Role: "assistant", Content: "结论：端口正常，无需改动。" + pad(110)},
	}
}

// freqCompressed 模拟 aiMaybeSummarize 的输出：head 换成一条 ≤400 字备忘，
// 尾部原样保留。摘要本身约 450 token，与 state.go 注释里「压完约 3400」同一口径。
func freqCompressed(msgs []aiMsg, cut int) []aiMsg {
	summary := aiMsg{Role: "user", Content: aiSummaryPrefix + strings.Repeat("要", 400) + "</untrusted_data>"}
	out := make([]aiMsg, 0, len(msgs)-cut+1)
	out = append(out, summary)
	out = append(out, msgs[cut:]...)
	return out
}

// freqMeasure 在指定阈值下跑完整段会话；退出前一定把全局 dev 段还原，
// 避免污染同包其它测试（withDevCfg 走 t.Cleanup，这里要在同一次测试里切两组）。
func freqMeasure(after int) freqResult {
	old := cfg.Dev
	cfg.Dev = &DevConfig{SummaryAfterTokens: after, SummaryKeepTokens: 3000, SummarizeKeepMsgs: 12}
	defer func() { cfg.Dev = old }()

	r := freqResult{After: after, Turns: freqTurns, TurnTokens: sumTokens(freqTurnMsgs(1))}
	var msgs []aiMsg
	for turn := 1; turn <= freqTurns; turn++ {
		msgs = append(msgs, freqTurnMsgs(turn)...)
		before := sumTokens(msgs)
		cut := aiSummaryCut(msgs)
		if cut <= 0 {
			continue
		}
		r.Fires++
		if r.FirstTurn == 0 {
			r.FirstTurn = turn
		}
		r.FireTurns = append(r.FireTurns, turn)
		r.Cuts = append(r.Cuts, cut)
		r.PreTotals = append(r.PreTotals, before)
		msgs = freqCompressed(msgs, cut)
		r.WaterAfter = append(r.WaterAfter, sumTokens(msgs))
	}
	return r
}

// freqPredictGap 独立于 aiSummaryCut：压完水位 w，每回合增加 turnTokens，
// 触发条件是 total > after，所以还要 ceil((after-w)/turnTokens) 个回合。
func freqPredictGap(after, w, turnTokens int) int {
	gap := (after-w)/turnTokens + 1
	if gap < 1 {
		return 1
	}
	return gap
}

func TestSummaryFrequencyQuantified(t *testing.T) {
	r10 := freqMeasure(10000)
	r16 := freqMeasure(16000)

	report := map[string]any{
		"old_10000": r10,
		"new_16000": r16,
		"fires_reduction_pct": func() int {
			if r10.Fires == 0 {
				return 0
			}
			return (r10.Fires - r16.Fires) * 100 / r10.Fires
		}(),
		"predicted_gap_old": freqPredictGap(10000, firstWater(r10), r10.TurnTokens),
		"predicted_gap_new": freqPredictGap(16000, firstWater(r16), r16.TurnTokens),
	}
	if b, err := json.Marshal(report); err == nil {
		t.Logf("FREQ_REPORT_JSON:%s", b)
	}

	// 1. 空转防护：两组都必须真的触发过，否则「16000 更少」可能是 0 vs 0。
	if r10.Fires == 0 || r16.Fires == 0 {
		t.Errorf("夹具不构成前提：两组都必须触发过摘要，实际 old=%d new=%d", r10.Fires, r16.Fires)
	}
	// 2. 降频本身：次数更少、首次更晚。
	if r16.Fires >= r10.Fires {
		t.Errorf("提高阈值应减少触发次数：old=%d new=%d", r10.Fires, r16.Fires)
	}
	if r16.FirstTurn <= r10.FirstTurn {
		t.Errorf("提高阈值应推迟首次触发：old=第%d回合 new=第%d回合", r10.FirstTurn, r16.FirstTurn)
	}
	// 3. 机理：每次压缩后必须回落到阈值以下，否则下一回合立刻又触发。
	for _, r := range []freqResult{r10, r16} {
		for i, w := range r.WaterAfter {
			if w >= r.After {
				t.Errorf("after=%d 第%d次压缩后水位 %d 仍不低于阈值，降频机理不成立", r.After, i+1, w)
			}
			if w <= 0 {
				t.Errorf("after=%d 第%d次压缩后水位异常：%d", r.After, i+1, w)
			}
		}
		if len(r.Cuts) != r.Fires || len(r.FireTurns) != r.Fires {
			t.Errorf("after=%d 观测点数量对不上：fires=%d cuts=%d turns=%d",
				r.After, r.Fires, len(r.Cuts), len(r.FireTurns))
		}
		for _, cut := range r.Cuts {
			if cut%aiSumQuantum != 0 {
				t.Errorf("切分点必须按 %d 条量化，实际 %d", aiSumQuantum, cut)
			}
		}
	}
	// 4. 独立预测：水位 + 每回合增量算出的间隔，必须与实际间隔相差 ≤2 回合。
	//    这条把「数字变小」钉到成因上，而不是碰巧两组配置跑出不同结果。
	for _, r := range []freqResult{r10, r16} {
		if r.Fires < 2 {
			t.Errorf("夹具前提不成立：after=%d 在 %d 回合内只触发 %d 次，凑不出间隔可比；"+
				"应加长会话而不是跳过这条断言", r.After, r.Turns, r.Fires)
			continue
		}
		for i := 0; i+1 < len(r.FireTurns); i++ {
			actual := r.FireTurns[i+1] - r.FireTurns[i]
			pred := freqPredictGap(r.After, r.WaterAfter[i], r.TurnTokens)
			diff := actual - pred
			if diff < 0 {
				diff = -diff
			}
			if diff > 2 {
				t.Errorf("after=%d 第%d段间隔：实际 %d 回合、按水位独立预测 %d 回合，偏差 %d（>2）",
					r.After, i+1, actual, pred, diff)
			}
		}
	}
}

func firstWater(r freqResult) int {
	if len(r.WaterAfter) == 0 {
		return 0
	}
	return r.WaterAfter[0]
}
