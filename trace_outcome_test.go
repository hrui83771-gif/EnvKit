package main

// trace_outcome_test.go —— 轨迹结局与 token 落盘
//
// ## 这个文件为什么存在
//
// 一次真实盘点发现：`handleAIChat` 里的 `traceOutcome := "aborted"`
// **声明后从未被赋值**，而 `endTrace` 只在 `defer` 里读它。
// 结果是磁盘上 305 条轨迹的 `outcome` **100% 是 `aborted`**——
// `success` / `failed` 这些常量**只在测试代码里被传入过**，生产路径一个都没走到。
//
// ## 为什么单测没抓到
//
// 所有结局常量在各自的测试里都被正确传给了 `endTrace`，
// 于是**每个测试都是绿的**。缺的是一条**从生产入口出发**的断言：
//「走一遍 handleAIChat，结束后 outcome 应该是判定出来的值，而不是兜底值」。
//
// 这与本项目反复遇到的一类问题同源：
// **测试验证的是被测函数的行为，不是生产路径有没有走到它。**

import (
	"testing"
)

// 本文件复用 trace_test.go 里已有的 withTraceEnv() 与 readTraces(t)——
// **不要重复定义**：Go 不允许同名 helper 并存，而重复的 helper 还会
// 让两个文件的目录重定向逻辑分叉（一个用 tracePath 函数、一个用硬编码路径）。
// 第一版就踩了这个：自己写了一份readTraces(t, dir)，
// 结果与 trace_test.go 的 readTraces(t) 冲突，四个既有测试编译不过。

// TestTraceOutcomeBoxPrecedence 结局一旦明确判定，不被后来的调用覆盖。
//
// 场景：主循环判了 success，之后某条收尾路径（比如 defer）再写一次 aborted。
// 若允许覆盖，成功会被静默改回 aborted —— 正是原 bug 的形态。
func TestTraceOutcomeBoxPrecedence(t *testing.T) {
	b := &traceOutcomeBox{}
	if got := b.get(); got != outAborted {
		t.Fatalf("初始应为 aborted，实际 %q", got)
	}
	b.set(outSuccess)
	if got := b.get(); got != outSuccess {
		t.Fatalf("set(success) 后应为 success，实际 %q", got)
	}
	// 之后再写别的，不应覆盖已确定的判定
	b.set(outFailed)
	if got := b.get(); got != outSuccess {
		t.Fatalf("已确定的 success 不应被覆盖，实际 %q", got)
	}
}

// TestTraceOutcomeBoxSetBeforeGet box 为零值时也要能读。
func TestTraceOutcomeBoxSetBeforeGet(t *testing.T) {
	var b traceOutcomeBox
	if got := b.get(); got != outAborted {
		t.Fatalf("零值 box 应返回 aborted，实际 %q", got)
	}
}

// TestAITurnOutcomeJudgement 结局判据表。
//
// 核心是**不能把「模型说了话」当「事情做成了」**：
// 有工具失败但答出内容时必须是 partial 而非 success。
func TestAITurnOutcomeJudgement(t *testing.T) {
	cases := []struct {
		name       string
		toolFail   bool
		contentLen int
		holdMenu   bool
		want       string
	}{
		{"正常答完", false, 100, false, outSuccess},
		{"短回答也算答完", false, 1, false, outSuccess},
		{"工具失败但答出内容", true, 100, false, outPartial},
		{"工具失败且没内容", true, 0, false, outFailed},
		{"菜单式无法纠正", false, 100, true, outFailed},
		{"菜单式且工具失败", true, 0, true, outFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := aiTurnOutcome(c.toolFail, c.contentLen, c.holdMenu)
			if got != c.want {
				t.Errorf("aiTurnOutcome(%v,%d,%v) = %q，期望 %q",
					c.toolFail, c.contentLen, c.holdMenu, got, c.want)
			}
		})
	}
}

// TestUsageAccumulatesAcrossTurns token 必须跨多轮工具调用累加。
//
// 真实场景：一个回合里AI 调了 3 次工具，上游报了 3 次 usage。
// 只记最后一次会让「本次对话花了多少 token」严重低估。
func TestUsageAccumulatesAcrossTurns(t *testing.T) {
	b := &traceOutcomeBox{}
	b.addUsage(&aiUsage{Prompt: 100, Completion: 50})
	b.addUsage(&aiUsage{Prompt: 200, Completion: 80})
	b.addUsage(&aiUsage{Prompt: 300, Completion: 100})
	u := b.usage()
	if u == nil {
		t.Fatal("usage 应非 nil")
	}
	if u.Prompt != 600 {
		t.Errorf("Prompt 累加 = %d，期望 600", u.Prompt)
	}
	if u.Completion != 230 {
		t.Errorf("Completion 累加 = %d，期望 230", u.Completion)
	}
	if u.Turns != 3 {
		t.Errorf("Turns = %d，期望 3", u.Turns)
	}
}

// TestUsageZeroIsNotRecorded 上游没报 usage 时，usage 应为 nil 而不是全 0。
//
// 区别很重要：nil =「没有数据」，全 0 =「跑了但一个 token 都没花」——
// 后者在事实上不可能。把它记成0 会让看板出现假的「零消耗」。
func TestUsageZeroIsNotRecorded(t *testing.T) {
	b := &traceOutcomeBox{}
	b.addUsage(&aiUsage{})
	if u := b.usage(); u != nil {
		t.Errorf("全 0 usage 不应被记录，实际 %+v", u)
	}
	b2 := &traceOutcomeBox{}
	if u := b2.usage(); u != nil {
		t.Errorf("未 addUsage 时应为 nil，实际 %+v", u)
	}
}

// TestUsageCacheFieldsPreserved 缓存细分必须保留且标记 HasCache。
//
// DeepSeek 会报缓存命中/未命中，命中率直接影响成本判断。
// 抹掉 Hit/Miss 等于把「便宜」和「贵」显示成同一个数。
func TestUsageCacheFieldsPreserved(t *testing.T) {
	b := &traceOutcomeBox{}
	b.addUsage(&aiUsage{Prompt: 1000, Completion: 100, Hit: 700, Miss: 300, HasCache: true})
	u := b.usage()
	if u == nil {
		t.Fatal("usage 应非 nil")
	}
	if !u.HasCache {
		t.Error("HasCache 应为 true")
	}
	if u.Hit != 700 || u.Miss != 300 {
		t.Errorf("缓存细分 = hit %d / miss %d，期望 700/300", u.Hit, u.Miss)
	}
}

// TestEndTraceUsesBoxOutcome endTrace 必须优先取 box 里的判定。
//
// 这条是**本文件的核心**：原 bug 就是 endTrace 只读了调用方传的兜底值。
// 传入 outAborted 但box 里已判success，落盘的必须是 success。
func TestEndTraceUsesBoxOutcome(t *testing.T) {
	defer withTraceEnv(t)()

	beginTrace("测试任务", actAI)
	traceSetOutcome(outSuccess)
	traceStep(phAction, actAI, "get_system_state", "", resOK, 10, "", "")
	traceSetOutcome(outSuccess)
	endTrace(outAborted) // 调用方仍传兜底值

	trs := readTraces(t)
	if len(trs) != 1 {
		t.Fatalf("应落盘 1 条轨迹，实际 %d", len(trs))
	}
	if trs[0].Outcome != outSuccess {
		t.Errorf("落盘 outcome = %q，期望 success（box 判定应优先于调用方兜底）", trs[0].Outcome)
	}
}

// TestEndTraceFallsBackWhenBoxSilent box 未判定时才用调用方的值。
func TestEndTraceFallsBackWhenBoxSilent(t *testing.T) {
	defer withTraceEnv(t)()

	beginTrace("没判定的任务", actAI)
	endTrace(outAborted)

	trs := readTraces(t)
	if len(trs) != 1 {
		t.Fatalf("应落盘 1 条，实际 %d", len(trs))
	}
	if trs[0].Outcome != outAborted {
		t.Errorf("box 未判定时应为 aborted，实际 %q", trs[0].Outcome)
	}
}

// TestEndTraceAttachesUsage 落盘的轨迹要带 token 用量。
func TestEndTraceAttachesUsage(t *testing.T) {
	defer withTraceEnv(t)()

	beginTrace("用量任务", actAI)
	recordTurnUsage(&aiUsage{Prompt: 1234, Completion: 567})
	traceSetOutcome(outSuccess)
	endTrace(outAborted)

	trs := readTraces(t)
	if len(trs) != 1 {
		t.Fatalf("应落盘 1 条，实际 %d", len(trs))
	}
	if trs[0].Usage == nil {
		t.Fatal("轨迹应带 usage 字段")
	}
	if trs[0].Usage.Prompt != 1234 || trs[0].Usage.Completion != 567 {
		t.Errorf("usage = %d/%d，期望 1234/567",
			trs[0].Usage.Prompt, trs[0].Usage.Completion)
	}
}

// TestEndTraceOutcomeNotAborted 回归：一条真的走完全程的轨迹不该是 aborted。
//
// 这条测试存在的意义是**防复发**。原 bug 的特征是
// 「所有生产轨迹的 outcome 都等于兜底值」——
// 那是一个可以机械断言的性质，所以就用它当哨兵。
func TestEndTraceOutcomeNotAborted(t *testing.T) {
	defer withTraceEnv(t)()

	// 模拟一次正常对话：开了轨迹 → 调过复验工具 → 判成功 → 收尾
	beginTrace("把项目跑起来", actAI)
	// 用 verify_ 开头的工具：traceToolStep 只对 verify_* 或返回里带复验结论的
	// 结果追加 verify 步。第一版用 get_system_state 却断言 Verified=true，
	// 那是**断言写错了**，不是代码错了 —— 环境观测本身不产生复验结论。
	traceToolStep("verify_environment", map[string]any{}, "已复验通过：服务就绪", nil, 30)
	traceSetOutcome(aiTurnOutcome(false, 50, false))
	endTrace(outAborted)

	trs := readTraces(t)
	if len(trs) != 1 {
		t.Fatalf("应落盘 1 条，实际 %d", len(trs))
	}
	if trs[0].Outcome == outAborted {
		t.Error("outcome 仍是 aborted —— 死变量可能回来了")
	}
	if trs[0].Outcome != outSuccess {
		t.Errorf("outcome = %q，期望 success", trs[0].Outcome)
	}
	if !trs[0].Verified {
		t.Error("有 verify 步的轨迹 Verified 应为 true")
	}
}

// TestTraceSetOutcomeWithoutTrace 无轨迹时静默忽略，不 panic。
func TestTraceSetOutcomeWithoutTrace(t *testing.T) {
	// 确保没有当前轨迹
	traceMu.Lock()
	traceCur = nil
	traceOutcomeCur = nil
	traceMu.Unlock()

	traceSetOutcome(outSuccess)          // 不应panic
	recordTurnUsage(&aiUsage{Prompt: 1}) // 不应 panic
}
