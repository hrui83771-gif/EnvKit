package main

import "testing"

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

// aiSummaryCut：短历史不摘要；切分点必须按 aiSumQuantum 量化，
// 否则每来一条新消息边界就移动一次，摘要缓存永远不命中、每回合多打一次上游。
func TestAISummaryCut(t *testing.T) {
	if got := aiSummaryCut(aiSummarizeAfter); got != 0 {
		t.Errorf("边界条数(%d)不应触发摘要，cut = %d", aiSummarizeAfter, got)
	}
	if got := aiSummaryCut(aiSummarizeKeep); got != 0 {
		t.Errorf("短历史不应摘要，cut = %d", got)
	}
	c := aiSummaryCut(aiSummarizeAfter + 2)
	if c <= 0 {
		t.Fatalf("超过阈值应触发摘要，cut = %d", c)
	}
	if c%aiSumQuantum != 0 {
		t.Errorf("切分点必须按 %d 量化，got %d", aiSumQuantum, c)
	}
	// 量化稳定性：逐条增长时，切分点多数回合保持不变（缓存命中）
	stable := 0
	prev := aiSummaryCut(aiSummarizeAfter + 1)
	for i := aiSummarizeAfter + 2; i <= aiSummarizeAfter+12; i++ {
		c := aiSummaryCut(i)
		if c%aiSumQuantum != 0 {
			t.Fatalf("n=%d 切分点 %d 未量化", i, c)
		}
		if c == prev {
			stable++
		}
		prev = c
	}
	if stable < 6 {
		t.Errorf("11 步增长中切分点仅 %d 次不变（%d），量化粒度疑似失效", stable, prev)
	}
	// 保留量：摘要后最近消息不会被切走（cut 量化只会让保留变多，不会变少）
	if got := aiSummaryCut(200); 200-got < aiSummarizeKeep {
		t.Errorf("cut=%d 会把最近消息切到不足 %d 条", got, aiSummarizeKeep)
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
