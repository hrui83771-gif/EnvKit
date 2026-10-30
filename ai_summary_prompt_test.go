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
	aiSumCache.failKey, aiSumCache.failAt = "", time.Time{}
	aiSumMu.Unlock()
}

// 摘要失败后不能每回合重试：模型对摘要请求持续报错时（只接受流式 / 额度耗尽），
// 每个回合都先白等一次最长 60s 的前置调用 —— 用户体感同样是"老在压缩"。
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

	msgs := make([]aiMsg, 0, 60)
	for i := 0; i < 60; i++ {
		msgs = append(msgs, aiMsg{Role: "user", Content: strings.Repeat("测", 250)})
	}
	if aiSummaryCut(msgs) <= 0 {
		t.Fatal("用例构造不对：这段历史应当触发摘要")
	}
	for i := 0; i < 3; i++ { // 模拟连续 3 个回合
		if got := aiMaybeSummarize(msgs, nil); len(got) != len(msgs) {
			t.Fatalf("第 %d 回合：摘要失败时必须原样返回原始历史", i+1)
		}
	}
	if calls != 1 {
		t.Fatalf("同一段历史失败后不该每回合重试：3 个回合共调用了 %d 次", calls)
	}
}
