package main

import (
	"strings"
	"testing"
)

// aiTrimDanglingToolCalls：剥离历史中所有"没有紧跟 tool 结果"的 assistant(tool_calls)。
// 场景：确认卡片弹出后用户无视，直接发新消息 → 悬挂的 tool_calls 留在历史中段，
// 上游 OpenAI 协议直接 400（此前只有 aiTrimLastToolTurn 裁末尾，覆盖不到中段）。
func TestAITrimDanglingToolCalls(t *testing.T) {
	in := []aiMsg{
		{Role: "user", Content: "启动前后端"},
		{Role: "assistant", ToolCalls: []aiToolCall{{ID: "c1"}}}, // 悬挂：后面是 user
		{Role: "user", Content: "算了，先看看状态"},
		{Role: "assistant", ToolCalls: []aiToolCall{{ID: "c2"}}}, // 配对：后面是 tool
		{Role: "tool", Content: "ok"},
		{Role: "assistant", ToolCalls: []aiToolCall{{ID: "c3"}}}, // 末尾悬挂
	}
	out := aiTrimDanglingToolCalls(in)
	if len(out) != 4 {
		t.Fatalf("len(out) = %d, want 4（应剥掉 2 条悬挂、保留配对回合）", len(out))
	}
	if out[0].Role != "user" || out[0].Content != "启动前后端" {
		t.Errorf("out[0] = %s %q, want user 启动前后端", out[0].Role, out[0].Content)
	}
	if out[1].Role != "user" || out[1].Content != "算了，先看看状态" {
		t.Errorf("out[1] = %s %q, want user 算了，先看看状态", out[1].Role, out[1].Content)
	}
	if out[2].Role != "assistant" || len(out[2].ToolCalls) != 1 || out[2].ToolCalls[0].ID != "c2" {
		t.Errorf("out[2] 应保留配对的 assistant(tool_calls c2)")
	}
	if out[3].Role != "tool" {
		t.Errorf("out[3] = %s, want tool", out[3].Role)
	}
}

func TestAITrimDanglingToolCalls_Noop(t *testing.T) {
	in := []aiMsg{
		{Role: "user", Content: "启动"},
		{Role: "assistant", Content: "好的"},
	}
	out := aiTrimDanglingToolCalls(in)
	if len(out) != 2 {
		t.Fatalf("无悬挂时不应改动，len = %d", len(out))
	}
}

// aiSummaryCut：按 token 触发、按条数量化边界。
//
// 两个都要钉住：
//   - 触发不能只看条数（少量巨型工具结果也该摘要）；
//   - 切分点不能每条消息都动，否则摘要缓存永远不命中、每回合多打一次上游。
func msgsWithApproxTokens(n, tokensEach int) []aiMsg {
	out := make([]aiMsg, n)
	for i := range out {
		// aiApproxTokens：ASCII 约 4 字符 = 1 token
		out[i] = aiMsg{Role: "user", Content: strings.Repeat("a", tokensEach*4)}
	}
	return out
}

func TestAISummaryCut(t *testing.T) {
	// 短历史 / 小消息量都不摘要
	if got := aiSummaryCut([]aiMsg{{Role: "user", Content: "启动前端"}, {Role: "assistant", Content: "好的"}}); got != 0 {
		t.Errorf("短历史不应摘要，cut = %d", got)
	}
	if got := aiSummaryCut(msgsWithApproxTokens(10, 50)); got != 0 {
		t.Errorf("500 token 的历史不该摘要，cut = %d", got)
	}

	// 大历史：必须按 6 条量化，且至少给尾部留 6 条。
	// 用例规模要**多于触发阈值**（2026-10-30 默认值 5000 → 10000，见 state.go 的注释）：
	// 60 条 × 200 token ≈ 12000，稳稳越过阈值；写死 8000 的话阈值一调这里就误报。
	big := msgsWithApproxTokens(60, 200) // ≈ 12000 token
	cut := aiSummaryCut(big)
	if cut <= 0 {
		t.Fatalf("约 12000 token 的历史应触发摘要（阈值 %d），cut = %d",
			devSnap().summaryAfterTokens(), cut)
	}
	if cut%aiSumQuantum != 0 {
		t.Fatalf("切分点必须按 %d 条量化，got %d", aiSumQuantum, cut)
	}
	if len(big)-cut < aiSumQuantum {
		t.Fatalf("尾部至少保留 %d 条，实际 %d", aiSumQuantum, len(big)-cut)
	}

	// 量化稳定性：逐条追加 12 条小消息，切分点最多移动 2 次
	moves := 0
	prev := cut
	for i := 0; i < 12; i++ {
		big = append(big, aiMsg{Role: "user", Content: "继续"})
		c := aiSummaryCut(big)
		if c != prev {
			moves++
		}
		prev = c
	}
	if moves > 2 {
		t.Fatalf("12 条追加中切分点移动了 %d 次，量化粒度疑似失效", moves)
	}

	// 一条巨型结果也能触发摘要（旧实现要等满 24 条）
	huge := append(msgsWithApproxTokens(6, 50), aiMsg{Role: "tool", Content: strings.Repeat("b", 40000)})
	if c := aiSummaryCut(huge); c <= 0 || c >= len(huge) {
		t.Fatalf("巨型工具结果应触发摘要且保留最后一条，cut = %d / %d", c, len(huge))
	}
}

// aiApproxTokens：中文按字、ASCII 按 4 字符估；只要求单调且量级正确。
func TestAIApproxTokens(t *testing.T) {
	if got := aiApproxTokens("你好世界"); got != 4 {
		t.Fatalf("4 个汉字应估 4 token，实际 %d", got)
	}
	if got := aiApproxTokens("abcd"); got != 1 {
		t.Fatalf("4 个 ASCII 应估 1 token，实际 %d", got)
	}
	if aiApproxTokens(strings.Repeat("x", 400)) <= aiApproxTokens(strings.Repeat("x", 40)) {
		t.Fatal("token 估算必须随长度单调增长")
	}
}

// aiHistoryKey：前缀内容不变 → 指纹不变（摘要缓存命中）；头部有变化 → 指纹必变。
func TestAIHistoryKey(t *testing.T) {
	a := []aiMsg{{Role: "user", Content: "启动前端"}, {Role: "assistant", Content: "好的"}}
	b := []aiMsg{{Role: "user", Content: "启动前端"}, {Role: "assistant", Content: "好的"}}
	if aiHistoryKey(a) != aiHistoryKey(b) {
		t.Error("相同前缀指纹应一致")
	}
	b[1].Content = "已启动前端服务"
	if aiHistoryKey(a) == aiHistoryKey(b) {
		t.Error("内容变化后指纹必须变化")
	}
	c := append([]aiMsg{}, a...)
	c = append(c, aiMsg{Role: "user", Content: "那再看日志"})
	if aiHistoryKey(a) == aiHistoryKey(c) {
		t.Error("前缀增长后指纹必须变化")
	}
}
