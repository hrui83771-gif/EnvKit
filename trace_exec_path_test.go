package main

// 轨迹记步必须在**执行层**，不能挂在某个调用点上。
//
// ## 这条测试防的是什么（2026-11-03 本机真实数据）
//
// 一条 goal=「停止后端」、outcome=success 的轨迹，落盘后 `steps` 是**空数组**，
// 详情页因此一行都画不出来。渲染没问题，是真没记：
// 写工具全部经"确认续跑"执行（aiConfirmApply → aiExecTool），
// 而记录当时只写在 aiRunLoop 里模型 tool_call 的那一个调用点上，
// 这条路根本不经过它。四个执行入口里漏了三个（预检命中、意图兜底、确认续跑）。
//
// **量结构测不出这个洞**：`steps: []` 是合法 JSON，API 形状、字段类型、
// 前端渲染全都"对"。只有"确实执行了，却查不到这一步"才能说明问题——
// 所以这里断言的是**执行次数与步数对得上**。
//
// 变异验证（已做）：删掉 aiExecTool 里的 traceToolStep → 两条测试都红。

import (
	"net/http/httptest"
	"testing"
)

// 确认续跑：用户点「确认执行」后真正落地的那次执行，必须留下 action 步。
func TestTraceRecordsConfirmPathExec(t *testing.T) {
	defer withTraceEnv(t)()
	defer withAuditEnv(t)()
	got, cleanup := aiConfirmFakeTool(t, "stop_service")
	defer cleanup()

	args := map[string]any{"service": "web"}
	beginTrace("停止后端", actAI)
	aiRememberPending("tc-1", "stop_service", args)
	rec := httptest.NewRecorder()
	out := aiConfirmApply(rec, rec, "stop_service",
		aiConfirmGate("stop_service", "tc-1", true, args))
	endTrace("success")

	if out != "假工具已执行" || len(*got) != 1 {
		t.Fatalf("确认后应真的执行一次，实际 out=%q 执行次数=%d", out, len(*got))
	}
	ts := readTraces(t)
	if len(ts) != 1 {
		t.Fatalf("应落盘 1 条轨迹，实际 %d", len(ts))
	}
	if len(ts[0].Steps) == 0 {
		t.Fatal("确认续跑执行了，轨迹里却没有步——这正是「停止后端」steps 为空的成因")
	}
	s := ts[0].Steps[0]
	if s.Phase != phAction || s.Action != "stop_service" || s.Result != "ok" {
		t.Errorf("首步应是 action/stop_service/ok，实际 %q/%q/%q", s.Phase, s.Action, s.Result)
	}
	// 只记了"执行过"不够：结局判定要靠复验步，这里假工具不带复验结论，
	// 所以不该凭空造一个 verify 步（否则 Verified 会被假数据点亮）。
	for _, st := range ts[0].Steps {
		if st.Phase == phVerify {
			t.Errorf("没有复验结论却出现了 verify 步：%+v", st)
		}
	}
}

// 手机端只读下被拒的写工具也要进轨迹：
// "denied 后仍重试"这个指标就是从这条数据算的，只在成功执行时记步它永远是 0。
func TestTraceRecordsDeniedWrite(t *testing.T) {
	defer withTraceEnv(t)()
	_, cleanup := aiConfirmFakeTool(t, "stop_service")
	defer cleanup()

	oldRO := aiReadOnly.Load()
	aiReadOnly.Store(true)
	defer aiReadOnly.Store(oldRO)

	beginTrace("停止后端", actAI)
	_, err := aiExecTool("stop_service", map[string]any{"service": "web"})
	endTrace("success")

	if err == nil {
		t.Fatal("只读上下文里写工具必须被拒")
	}
	ts := readTraces(t)
	if len(ts) != 1 || len(ts[0].Steps) == 0 {
		t.Fatal("被拒的写工具没有进轨迹（denied 后重试的指标会永远是 0）")
	}
	s := ts[0].Steps[0]
	if s.Phase != phAction || s.Result != "fail" || s.ErrKind != "denied" {
		t.Errorf("应记成 action/fail/denied，实际 %q/%q/%q", s.Phase, s.Result, s.ErrKind)
	}
}

// 白名单外的工具名不执行，也就不该在轨迹里出现一步"动作"——
// 否则"无效操作次数"会被模型瞎报的工具名刷高。
func TestTraceNoStepForUnknownTool(t *testing.T) {
	defer withTraceEnv(t)()

	beginTrace("随便试试", actAI)
	if _, err := aiExecTool("no_such_tool", nil); err == nil {
		t.Fatal("白名单外工具必须报错")
	}
	endTrace("success")

	if ts := readTraces(t); len(ts) == 1 && len(ts[0].Steps) != 0 {
		t.Errorf("没执行的动作不该记步，实际 %d 步", len(ts[0].Steps))
	}
}
