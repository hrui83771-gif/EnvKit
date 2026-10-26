package main

// ai_mission_test.go —— T2：行动与 AI 回合的接线（绑定/落盘/收口）。
//
// 收口判据是红线 1 的落点：done 只能来自复验，断流只落 paused 不作废。

import (
	"strings"
	"testing"
)

// withAIMissionEnv 一次备齐：行动文件 + 轨迹文件 + 槽位复位。
func withAIMissionEnv(t *testing.T) func() {
	t.Helper()
	undoMission := withMissionEnv(t)
	undoTrace := withTraceEnv(t)
	return func() {
		aiMissionCur = nil
		undoTrace()
		undoMission()
	}
}

// seedMission 造一条指定状态的行动并返回 ID。
func seedMission(t *testing.T, state, trigger string) string {
	t.Helper()
	m, err := missionNew("测试目标", Trigger{Kind: trigger})
	if err != nil {
		t.Fatalf("missionNew: %v", err)
	}
	if state != msPending {
		// 夹具直写状态：种子数据不是转移逻辑的测试对象（转移表另有全量测试）
		m, err = missionUpdate(m.ID, func(x *Mission) error {
			x.State = state
			return nil
		})
		if err != nil {
			t.Fatalf("置状态 %s: %v", state, err)
		}
	}
	return m.ID
}

// runTurn 模拟一个绑定回合：beginTrace → bind → fn（注入结局）→ settle。
func runTurn(t *testing.T, missionID, outcome string, fn func()) string {
	t.Helper()
	traceID := beginTrace("测试", actAI)
	if !aiMissionBind(missionID, traceID) {
		t.Fatalf("绑定 %s 失败", missionID)
	}
	if fn != nil {
		fn()
	}
	traceSetOutcome(outcome)
	aiMissionSettle()
	endTrace(outAborted)
	return traceID
}

func TestAIMissionBind(t *testing.T) {
	defer withAIMissionEnv(t)()

	// pending → running，带 trace_id
	id := seedMission(t, msPending, "manual")
	traceID := beginTrace("测试", actAI)
	defer endTrace(outAborted)
	if !aiMissionBind(id, traceID) {
		t.Fatalf("pending 应可绑定")
	}
	if aiMissionCur == nil || aiMissionCur.id != id {
		t.Fatalf("槽位未绑定：%+v", aiMissionCur)
	}
	m, _ := missionGet(id)
	if m.State != msRunning || m.TraceID != traceID {
		t.Fatalf("绑定后应 running 且带 trace_id，实为 %s / %q", m.State, m.TraceID)
	}
	aiMissionSettle() // 清槽（结局 box 未设 → aborted → paused，这里只验清空）
	if aiMissionCur != nil {
		t.Fatalf("settle 后槽位必须清空")
	}

	// 终态拒绝绑定，且不改状态
	for _, st := range []string{msDone, msFailed, msCanceled, msExpired} {
		bad := seedMission(t, st, "manual")
		if aiMissionBind(bad, "t-x") {
			t.Fatalf("终态 %s 不该绑定成功", st)
		}
		if aiMissionCur != nil {
			t.Fatalf("绑定失败不得留槽")
		}
		if m, _ := missionGet(bad); m.State != st {
			t.Fatalf("绑定失败不得改状态，实为 %s", m.State)
		}
	}

	// 不存在的 ID：不绑定、不崩
	if aiMissionBind("m-nope", "t-x") {
		t.Fatalf("不存在的行动不该绑定成功")
	}
	// 普通对话：空 id 是 no-op
	if aiMissionBind("", "t-x") {
		t.Fatalf("空 id 不该算绑定")
	}
}

func TestAIMissionStepRecord(t *testing.T) {
	defer withAIMissionEnv(t)()

	id := seedMission(t, msPending, "manual")
	traceID := beginTrace("测试", actAI)
	defer endTrace(outAborted)
	if !aiMissionBind(id, traceID) {
		t.Fatalf("绑定失败")
	}
	aiMissionSettle() // 先收掉绑定（这里只测步骤落盘函数本身）
	// 槽位已清：no-op
	aiMissionStep("get_logs", "", "查看日志", true, "……")
	if m, _ := missionGet(id); len(m.Plan) != 0 {
		t.Fatalf("未绑定时不该落步骤")
	}

	// 重新绑定后落两步（一成一败）
	if !aiMissionBind(id, traceID) {
		t.Fatalf("二次绑定失败（paused→running）")
	}
	aiMissionStep("get_system_state", "", "查看系统状态", true, "端口 3000 监听中")
	aiMissionStep("restart_service", "backend", "重启后端", false, "执行出错：超时")
	m, _ := missionGet(id)
	if len(m.Plan) != 2 {
		t.Fatalf("应落 2 步，实为 %d", len(m.Plan))
	}
	if m.Plan[0].Seq != 1 || !m.Plan[0].OK || m.Plan[0].Evidence == "" {
		t.Fatalf("第 1 步不符：%+v", m.Plan[0])
	}
	if m.Plan[1].Seq != 2 || m.Plan[1].OK || m.Plan[1].Target != "backend" {
		t.Fatalf("第 2 步不符：%+v", m.Plan[1])
	}
	aiMissionSettle()
}

func TestAIMissionSettle(t *testing.T) {
	cases := []struct {
		name      string
		trigger   string
		outcome   string
		inject    func() // 回合内注入（步骤/复验）
		wantState string
		wantMsg   string // Result.Msg 或 PauseWhy 的包含串
	}{
		{
			name: "复验通过 → done", trigger: "manual", outcome: outSuccess,
			inject:    func() { aiMissionVerify(OpResult{Ok: true, Verified: true, Msg: "端口监听中"}) },
			wantState: msDone, wantMsg: "端口监听中",
		},
		{
			name: "复验未过 → failed（不是 done）", trigger: "manual", outcome: outSuccess,
			inject:    func() { aiMissionVerify(OpResult{Ok: false, Verified: false, Msg: "端口未监听"}) },
			wantState: msFailed, wantMsg: "端口未监听",
		},
		{
			name: "成功但无复验证据 → failed（红线 1）", trigger: "manual", outcome: outSuccess,
			wantState: msFailed, wantMsg: "没有复验证据",
		},
		{
			name: "报告型巡检成功且步骤全成 → done", trigger: "patrol", outcome: outSuccess,
			inject: func() {
				aiMissionStep("get_system_state", "", "查看系统状态", true, "ok")
				aiMissionStep("get_diag_report", "", "生成诊断报告", true, "ok")
			},
			wantState: msDone, wantMsg: "巡检报告已产出",
		},
		{
			name: "报告型巡检有失败步骤 → failed", trigger: "schedule", outcome: outSuccess,
			inject: func() {
				aiMissionStep("get_diag_report", "", "生成诊断报告", false, "出错")
			},
			wantState: msFailed, wantMsg: "没有复验证据",
		},
		{
			name: "报告型但一步没跑 → failed", trigger: "patrol", outcome: outSuccess,
			wantState: msFailed, wantMsg: "没有复验证据",
		},
		{
			name: "partial（预算耗尽/工具失败）→ paused 可续", trigger: "manual", outcome: outPartial,
			inject:    func() { aiMissionStep("restart_service", "backend", "重启", true, "ok") },
			wantState: msPaused, wantMsg: "可点「继续」",
		},
		{
			name: "failed 回合 → paused 可续（不作废）", trigger: "manual", outcome: outFailed,
			wantState: msPaused, wantMsg: "可点「继续」",
		},
		{
			name: "aborted（断流/停止）→ paused 可续", trigger: "manual", outcome: outAborted,
			inject:    func() { aiMissionStep("get_logs", "", "查日志", true, "ok") },
			wantState: msPaused, wantMsg: "连接中断",
		},
		{
			name: "断在确认卡上 → awaiting_confirm（不是 paused）", trigger: "manual", outcome: outAborted,
			inject:    func() { aiMissionAwaitConfirm("restart_service", "重启后端") },
			wantState: msAwaitingConfirm, wantMsg: "等待确认",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer withAIMissionEnv(t)()
			id := seedMission(t, msPending, c.trigger)
			got := runTurn(t, id, c.outcome, c.inject)
			if got == "" {
				t.Fatalf("traceID 为空")
			}
			m, ok := missionGet(id)
			if !ok {
				t.Fatalf("行动丢失")
			}
			if m.State != c.wantState {
				t.Fatalf("状态应为 %s，实为 %s（pause_why=%q result=%+v）",
					c.wantState, m.State, m.PauseWhy, m.Result)
			}
			gotMsg := m.PauseWhy
			if missionTerminal(m.State) && m.Result != nil {
				gotMsg = m.Result.Msg
			}
			if !strings.Contains(gotMsg, c.wantMsg) {
				t.Fatalf("消息应包含 %q，实为 %q", c.wantMsg, gotMsg)
			}
			if m.State == msDone && (m.Result == nil || !m.Result.Verified) {
				t.Fatalf("done 必须带已复验的 Result，实为 %+v", m.Result)
			}
			if m.State == msPaused && m.PauseWhy == "" {
				t.Fatalf("paused 必须带原因")
			}
		})
	}
}

// TestAIMissionSettleUnbound 普通对话不产生行动记录，settle 是 no-op。
func TestAIMissionSettleUnbound(t *testing.T) {
	defer withAIMissionEnv(t)()
	traceID := beginTrace("测试", actAI)
	defer endTrace(outAborted)
	traceSetOutcome(outSuccess)
	aiMissionSettle() // 不应 panic、不应碰任何行动文件
	if list := missionList(); len(list) != 0 {
		t.Fatalf("未绑定不该产生行动，实为 %d 条", len(list))
	}
	_ = traceID
}

// TestAIMissionResumeContext 续作上下文（定案 3：摘要 + 关键结果全量）。
func TestAIMissionResumeContext(t *testing.T) {
	defer withAIMissionEnv(t)()

	// 未绑定 → 空
	if got := aiMissionResumeContext(); got != "" {
		t.Fatalf("未绑定不该有上下文，实为 %q", got)
	}

	id := seedMission(t, msPending, "manual")
	traceID := beginTrace("测试", actAI)
	defer endTrace(outAborted)
	if !aiMissionBind(id, traceID) {
		t.Fatalf("绑定失败")
	}
	// 首回合（无步骤）→ 空：不打扰正常执行
	if got := aiMissionResumeContext(); got != "" {
		t.Fatalf("无步骤不该注入上下文，实为 %q", got)
	}

	aiMissionStep("get_system_state", "", "查看系统状态", true, "端口 3000 监听中")
	aiMissionStep("restart_service", "backend", "重启后端", false, "执行出错：超时")
	aiMissionAwaitConfirm("restart_service", "重启后端服务")
	ctx := aiMissionResumeContext()
	for _, want := range []string{
		"此前中断",
		"测试目标",        // 目标
		"[成功] 查看系统状态", // 步骤 1（测试注入的 Why）
		"端口 3000 监听中", // 关键结果全量
		"[失败] 重启后端（backend）",
		"执行出错：超时",
		"<untrusted_data>", // 工具输出按不可信数据包裹
		"断点：等待确认「重启后端服务」",
		"verify_result 是唯一判据",
	} {
		if !strings.Contains(ctx, want) {
			t.Errorf("续作上下文缺少 %q：\n%s", want, ctx)
		}
	}
	// 定案 3：不回传对话原文——上下文里不能出现消息历史角色标记
	if strings.Contains(ctx, `"role"`) {
		t.Fatalf("续作上下文不该夹带对话原文")
	}

	// 断点被新步骤清掉
	aiMissionStep("verify_environment", "", "复验", true, "ok")
	if m, _ := missionGet(id); m.Pending != nil {
		t.Fatalf("新步骤落地后断点应清空")
	}
	aiMissionSettle()
}

// TestAIMissionFinishClearsPending 终态不留断点。
func TestAIMissionFinishClearsPending(t *testing.T) {
	defer withAIMissionEnv(t)()
	id := seedMission(t, msRunning, "manual")
	if _, err := missionUpdate(id, func(x *Mission) error {
		x.Pending = &PendingStep{Kind: "confirm", Tool: "restart_service", Note: "重启"}
		return nil
	}); err != nil {
		t.Fatalf("登记断点: %v", err)
	}
	m, err := missionUpdate(id, func(x *Mission) error {
		return missionFinish(x, true, "复验通过")
	})
	if err != nil {
		t.Fatalf("收口: %v", err)
	}
	if m.State != msDone || m.Pending != nil {
		t.Fatalf("done 后不该留断点：%s / %+v", m.State, m.Pending)
	}
}
