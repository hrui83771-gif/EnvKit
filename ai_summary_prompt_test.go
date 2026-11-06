package main

// ai_summary_prompt_test.go —— 摘要请求正文与触发阈值。
//
// 两件事都属于"错了不报错、只是慢慢变差"的类型，只能靠断言钉住：
//  1. 上一轮的摘要在历史里也是 role=user，但**不是用户说的话**。
//     原样当用户消息喂回去，新摘要里会出现「用户：（系统注入的此前对话要点备忘…
//     <untrusted_data>…」，标签嵌套、自指措辞，模型容易被带偏。
//  2. 触发阈值（state.go 的 devDefaultSummaryAfterTokens）决定"多久压一次"。
//     用户在 2026-10-30 反馈压缩太频繁：5000 时压完只剩约 2000 token 余量，
//     一两轮工具就又触发。改成 10000 后 7000 token 的历史必须**不再**触发。

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSummaryPromptDoesNotQuotePreviousSummaryAsUser(t *testing.T) {
	head := []aiMsg{
		{Role: "user", Content: aiSummaryPrefix + "1) 用户要把后端起在 8080。\n2) 已确认启动过一次。</untrusted_data>"},
		{Role: "user", Content: "帮我看看端口为什么没起来"},
		{Role: "assistant", Content: "端口 8080 被占用，已换到 8081。"},
	}
	p := aiSummaryPrompt(head)

	if strings.Contains(p, "用户："+aiSummaryMark) {
		t.Fatalf("上一轮的摘要被当成用户发言喂回去了：\n%s", p)
	}
	if !strings.Contains(p, "此前要点备忘（在此基础上合并更新") {
		t.Fatalf("旧摘要没有作为「已有要点」传递，模型会丢失已压缩的内容：\n%s", p)
	}
	if !strings.Contains(p, "用户：帮我看看端口为什么没起来") {
		t.Fatalf("真实用户消息丢了：\n%s", p)
	}
	if n := strings.Count(p, "<untrusted_data>"); n != 1 {
		t.Fatalf("untrusted_data 只能有外层一层，实际 %d 个：\n%s", n, p)
	}
	if n := strings.Count(p, "</untrusted_data>"); n != 1 {
		t.Fatalf("闭标签只能有一个（内层必须剥掉），实际 %d 个：\n%s", n, p)
	}
}

// sumTokens 用例自检：构造出来的历史到底多重，别用"感觉差不多"当判据。
func sumTokens(msgs []aiMsg) int {
	total := 0
	for _, m := range msgs {
		total += aiMsgTokens(m)
	}
	return total
}

func TestSummaryThresholdDoesNotFireAtSevenK(t *testing.T) {
	// 14 条 × 约 508 token ≈ 7100：旧默认（5000）会压缩，新默认（10000）不该压。
	msgs := make([]aiMsg, 0, 14)
	for i := 0; i < 14; i++ {
		msgs = append(msgs, aiMsg{Role: "user", Content: strings.Repeat("测", 500)})
	}
	total := sumTokens(msgs)
	if total < 6000 || total > 9000 {
		t.Fatalf("用例构造不对：total=%d，期望 6000~9000", total)
	}
	if cut := aiSummaryCut(msgs); cut != 0 {
		t.Fatalf("约 %d token 不该触发摘要（阈值 %d），却切了 %d 条",
			total, devSnap().summaryAfterTokens(), cut)
	}
}

func TestSummaryThresholdFiresWhenReallyLong(t *testing.T) {
	msgs := make([]aiMsg, 0, 26)
	for i := 0; i < 26; i++ {
		msgs = append(msgs, aiMsg{Role: "user", Content: strings.Repeat("测", 500)})
	}
	total := sumTokens(msgs)
	if cut := aiSummaryCut(msgs); cut <= 0 {
		t.Fatalf("约 %d token 已经超过阈值 %d，应当触发摘要，实际 cut=%d",
			total, devSnap().summaryAfterTokens(), cut)
	} else if cut%aiSumQuantum != 0 {
		t.Fatalf("切分点必须按 %d 条量化（否则摘要缓存永不命中），实际 %d", aiSumQuantum, cut)
	}
}

// resetSumCache 摘要缓存是全局的：测试之间必须清干净，否则"上一条用例留下的缓存"
// 会让本用例根本不发起调用，看起来像是冷却生效了（假绿）。
func resetSumCache() {
	aiSumMu.Lock()
	aiSumCache.key, aiSumCache.text = "", ""
	aiSumCache.failAt, aiSumCache.failStreak = time.Time{}, 0
	aiSumMu.Unlock()
}

// longHistory 造一段必定触发摘要的历史；growOneTurn 模拟 Agent 工具轮
// 每回合新增的消息（一轮里常有 user + tool_calls + 多个 tool 结果 + 结论）。
func longHistory() []aiMsg {
	msgs := make([]aiMsg, 0, 100)
	for i := 0; i < 60; i++ {
		msgs = append(msgs, aiMsg{Role: "user", Content: strings.Repeat("测", 250)})
	}
	return msgs
}

func growOneTurn(msgs []aiMsg) []aiMsg {
	for i := 0; i < 8; i++ {
		msgs = append(msgs, aiMsg{Role: "assistant", Content: strings.Repeat("答", 250)})
	}
	return msgs
}

// 摘要失败后不能每回合重试：模型对摘要请求持续报错时（只接受流式 / 额度耗尽），
// 每个回合都先白等一次最长 60s 的前置调用 —— 用户体感同样是"老在压缩"。
//
// 夹具必须让历史**逐回合增长**：真实对话每回合都会追加新消息、head 指纹随之变化。
// 旧版拿同一份 msgs 跑 3 回合是假绿 —— 那样按指纹精确匹配的冷却也能挡住，
// 恰恰测不出"指纹一变就每回合重发"这个真实缺陷（2026-11-04 日志实证）。
func TestSummaryFailureIsNotRetriedEveryTurn(t *testing.T) {
	resetSumCache()
	oldFn := aiSummarizeHistoryFunc
	calls := 0
	aiSummarizeHistoryFunc = func(head []aiMsg) (string, error) {
		calls++
		return "", errors.New("空响应")
	}
	defer func() {
		aiSummarizeHistoryFunc = oldFn
		resetSumCache()
	}()

	msgs := longHistory()
	keys := map[string]bool{}
	for turn := 0; turn < 3; turn++ {
		cut := aiSummaryCut(msgs)
		if cut <= 0 {
			t.Fatalf("第 %d 回合：历史应当触发摘要，cut=%d（夹具前提不成立）", turn+1, cut)
		}
		keys[aiHistoryKey(msgs[:cut])] = true
		if got := aiMaybeSummarize(msgs, nil); len(got) != len(msgs) {
			t.Fatalf("第 %d 回合：摘要失败时必须原样返回原始历史", turn+1)
		}
		msgs = growOneTurn(msgs)
	}
	// 前提校验：head 指纹必须真的变过，否则本用例测不到缺陷（按 key 冷却也能挡住）。
	if len(keys) < 2 {
		t.Fatalf("夹具无效：3 个回合的 head 指纹只有 %d 种，测不出按指纹冷却的缺陷", len(keys))
	}
	// 前两次尝试，第三次起全局冷却；每回合都发就是缺陷。
	if calls != aiSumFailStreakMin {
		t.Fatalf("连续失败后应只尝试 %d 次，实际 %d 次（每回合重发即缺陷）", aiSumFailStreakMin, calls)
	}
}

// 冷却不能把正常摘要一起挡死：上游恢复、成功一次后必须清零连续失败计数。
// 否则一次故障会把后续 5 分钟里所有（本该成功的）摘要调用全挡掉。
func TestSummaryCooldownClearsAfterSuccess(t *testing.T) {
	resetSumCache()
	oldFn := aiSummarizeHistoryFunc
	fail := true
	calls := 0
	aiSummarizeHistoryFunc = func(head []aiMsg) (string, error) {
		calls++
		if fail {
			return "", errors.New("空响应")
		}
		return "1) 用户要把后端起在 8080。", nil
	}
	defer func() {
		aiSummarizeHistoryFunc = oldFn
		resetSumCache()
	}()

	msgs := longHistory()
	for i := 0; i < aiSumFailStreakMin; i++ {
		aiMaybeSummarize(msgs, nil)
		msgs = growOneTurn(msgs)
	}
	// 把冷却拨到窗口外（不真等 5 分钟），并让上游恢复。
	fail = false
	aiSumMu.Lock()
	aiSumCache.failAt = time.Now().Add(-aiSumFailCooldown - time.Second)
	aiSumMu.Unlock()
	if got := aiMaybeSummarize(msgs, nil); len(got) == len(msgs) {
		t.Fatal("冷却过期且上游恢复后应当成功生成摘要")
	}
	aiSumMu.Lock()
	streak, at := aiSumCache.failStreak, aiSumCache.failAt
	aiSumMu.Unlock()
	if streak != 0 || !at.IsZero() {
		t.Fatalf("成功后必须清零连续失败计数与失败时刻，实际 streak=%d at=%v", streak, at)
	}

	// 清零后再失败一次：不该被旧的冷却挡住，要重新给足尝试机会。
	// 只清摘要缓存 key（隔离"前缀命中"这一条无关路径），不动失败计数。
	aiSumMu.Lock()
	aiSumCache.key = ""
	aiSumMu.Unlock()
	fail = true
	before := calls
	msgs = growOneTurn(msgs)
	aiMaybeSummarize(msgs, nil)
	if calls != before+1 {
		t.Fatalf("成功后重新失败应能再次尝试（计数已清零），实际新增调用 %d 次", calls-before)
	}
}
