package main

// ai_mission_resume_flow_test.go —— Phase 1 验收 2 的端到端钉子（假上游，不联网）。
//
// 复现的真实场景：巡检行动跑到一半 SSE 断流 → 整轮作废（旧世界）。
// 现在要求：
//  1. 断流不作废——已发生的步骤留在 Plan 里，行动落 paused；
//  2. 续作不重头——下一轮的 system 里带 <mission_context>（目标 + 已完成
//     步骤 + 关键结果全量，定案 3），模型从断点接着跑；
//  3. 报告型巡检续作跑完 → done（trigger=patrol 的完成判据）。
//
// 假上游手法沿用 ai_confirm_flow_test.go：httptest + cfg.AI 指向桩，
// aiRunLoop 直连（handleAIChat 的绑定/收口在测试里手动接线，与生产一致）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMissionResumeAfterStreamDrop(t *testing.T) {
	const toolName = "zz_test_read"

	// 只读测试工具：Write=false → 不进确认分支，直接执行
	aiToolRegistry[toolName] = aiTool{
		Desc:    "测试用只读工具",
		Schema:  map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}},
		Write:   false,
		Execute: func(map[string]any) (string, error) { return "读到了东西：fixture-42", nil },
	}
	defer delete(aiToolRegistry, toolName)

	var (
		mu         sync.Mutex
		phaseBMsgs [][]aiMsg
	)
	var phase atomic.Int32   // 1=断流轮 2=续作轮
	var inPhase atomic.Int32 // 本轮内的第几次请求
	arrived2 := make(chan struct{}, 1)

	// SSE 帧拼好再整块写：Fprintf(w, 变量) 会触发 vet 的 non-constant format
	sse := func(s string) string { return "data: " + s + "\n\n" }
	toolCallFrame := func(n int) string {
		return sse(fmt.Sprintf(
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_test_%d","type":"function","function":{"name":"%s","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			n, toolName))
	}
	stopFrame := sse(`{"choices":[{"delta":{"content":"Patrol resumed and finished."},"finish_reason":"stop"}]}`)
	usageFrame := sse(`{"choices":[],"usage":{"prompt_tokens":1500,"completion_tokens":20}}`)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []aiMsg `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		p, k := phase.Load(), inPhase.Add(1)
		if p == 2 {
			mu.Lock()
			phaseBMsgs = append(phaseBMsgs, req.Messages)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch {
		case p == 1 && k == 1:
			fmt.Fprint(w, toolCallFrame(1))
		case p == 1 && k >= 2:
			// 断流点：不给响应，挂着直到客户端 ctx 被取消（= SSE 断了）
			select {
			case arrived2 <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return
		case p == 2 && k == 1:
			fmt.Fprint(w, toolCallFrame(2))
		default:
			fmt.Fprint(w, stopFrame)
			fmt.Fprint(w, usageFrame)
		}
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}))
	defer upstream.Close()

	oldAI := cfg.AI
	cfg.AI = &AIConfig{Enabled: true, Provider: "test", BaseURL: upstream.URL, APIKey: "k", Model: "test-model"}
	defer func() { cfg.AI = oldAI }()

	defer withAIMissionEnv(t)()
	withAuditDir(t, nil)
	id := seedMission(t, msPending, "patrol")

	// ---- 第 1 轮：第一步执行完、等第二步响应时断流 ----
	phase.Store(1)
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	recA := httptest.NewRecorder()
	doneA := make(chan struct{})
	go func() {
		defer close(doneA)
		traceID := beginTrace("patrol", actAI)
		aiMissionBind(id, traceID) // handleAIChat 的接线，测试手动等价
		// 英文输入：不命中本地意图路由（preflight 快通道），确保真的打到假上游
		aiRunLoop(ctxA, recA, recA, []aiMsg{{Role: "user", Content: "run the patrol"}}, false, true, "zh")
		aiMissionSettle()
		endTrace(outAborted)
	}()
	select {
	case <-arrived2:
	case <-time.After(20 * time.Second):
		t.Fatal("断流轮没有走到第二步（上游没收到第二次请求）")
	}
	cancelA() // = SSE 断流
	select {
	case <-doneA:
	case <-time.After(30 * time.Second):
		t.Fatal("断流后回合没有退出")
	}

	m, ok := missionGet(id)
	if !ok {
		t.Fatal("行动丢失")
	}
	if m.State != msPaused {
		t.Fatalf("断流后应落 paused，实为 %s（pause_why=%q）", m.State, m.PauseWhy)
	}
	if m.PauseWhy == "" {
		t.Fatalf("paused 必须带原因")
	}
	if len(m.Plan) != 1 {
		t.Fatalf("断流前已执行的步骤必须留住（验收 2 的数据前提），实为 %d 条", len(m.Plan))
	}
	if st := m.Plan[0]; st.Tool != toolName || !st.OK || !strings.Contains(st.Evidence, "fixture-42") {
		t.Fatalf("第 1 步记录不符：%+v", st)
	}

	// ---- 第 2 轮：续作（从前端视角就是带 mission_id 再发一条） ----
	phase.Store(2)
	inPhase.Store(0)
	recB := httptest.NewRecorder()
	doneB := make(chan struct{})
	go func() {
		defer close(doneB)
		traceID := beginTrace("continue the patrol", actAI)
		aiMissionBind(id, traceID) // paused → running
		aiRunLoop(context.Background(), recB, recB,
			[]aiMsg{{Role: "user", Content: "continue the patrol"}}, false, true, "zh")
		aiMissionSettle()
		endTrace(outAborted)
	}()
	select {
	case <-doneB:
	case <-time.After(30 * time.Second):
		t.Fatal("续作轮没有结束")
	}

	// 续作上下文真的进了 system（定案 3：摘要 + 关键结果全量）
	mu.Lock()
	sysMsgs := phaseBMsgs
	mu.Unlock()
	if len(sysMsgs) == 0 {
		t.Fatal("续作轮没有打到上游")
	}
	sys := ""
	for _, mm := range sysMsgs[0] {
		if mm.Role == "system" {
			sys = mm.Content
			break
		}
	}
	for _, want := range []string{
		"<mission_context>",
		"此前中断",
		"不要重头开始",
		"fixture-42", // 第 1 步关键结果全量回传
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("续作 system 缺 %q：\n%s", want, firstLines(sys, 1500))
		}
	}

	// 结果：步骤 1 保留 + 新增步骤 2（不重头），报告型巡检收口 done
	m, _ = missionGet(id)
	if len(m.Plan) != 2 {
		t.Fatalf("续作应追加为 2 步（不重做第 1 步），实为 %d 条", len(m.Plan))
	}
	if m.State != msDone {
		t.Fatalf("报告型巡检续作跑完应 done，实为 %s（pause_why=%q result=%+v）",
			m.State, m.PauseWhy, m.Result)
	}
	if m.Result == nil || !m.Result.Verified {
		t.Fatalf("done 必须带已复验的 Result，实为 %+v", m.Result)
	}
}
