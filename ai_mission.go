package main

// ai_mission.go —— 行动（Mission）与 AI 回合的接线（Phase 1 T2）。
//
// 职责三件事，全在 aiChatMu 的保护下（红线 5：同一时刻只有一个执行体）：
//  1. aiMissionBind   —— 回合开始：把请求里的 mission_id 绑到本回合（pending/paused → running）；
//  2. aiMissionStep/Verify —— 回合中：每个工具步骤与复验结论**当场落盘**（断点续作的回放点）；
//  3. aiMissionSettle —— 回合结束：按 trace 结局收口（done/failed/paused）。
//
// 收口判据（设计 2.2 + 红线 1）：
//   · 结局 success + 本回合有复验结论 → 复验过了 done，没过 failed；
//   · 结局 success + 无复验 → 非报告型 failed（宁可错杀，不发假徽章）；
//     报告型（巡检/晨报，trigger=patrol|schedule）以「步骤全部成功且 >0」为完成 ——
//     只读报告没有环境状态可复验，拿动作判据卡它会永远 failed（实现时补入的细化，见设计文档）；
//   · 结局 partial/failed/aborted（含断流、用户点停止、预算耗尽）→ paused 带原因，
//     用户回来点「继续」——**断流不作废整轮**，这正是 Mission 存在的意义。

import (
	"strings"
	"time"
)

// aiMissionSlot 本回合绑定的行动。受 aiChatMu 保护（与 lastVerify 等全局槽同哲学）。
type aiMissionSlot struct {
	id     string
	report bool      // 报告型行动（巡检/晨报）：完成判据是产出，不是环境复验
	steps  int       // 本回合已落盘步骤数
	fails  int       // 本回合失败步骤数
	verify *OpResult // 本回合最近一次复验结论（取走即知成败）
}

// aiMissionCur 当前回合绑定的行动；nil = 本回合没有行动（普通对话）。
var aiMissionCur *aiMissionSlot

// aiMissionBind 回合开始绑定。返回是否绑定成功。
//
// 绑定失败**不阻断对话**：行动列表接口已报过错，对话本身该继续
// （用户要的是干活，不是被一个状态机挡住）。
func aiMissionBind(id, traceID string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	m, err := missionUpdate(id, func(x *Mission) error {
		if missionTerminal(x.State) {
			return errMissionTerminal(x.State)
		}
		// running→running 是 no-op：上一回合异常退出留下的尸体由这里接管
		if err := missionGo(x, msRunning); err != nil {
			return err
		}
		x.TraceID = traceID
		x.PauseWhy = ""
		return nil
	})
	if err != nil {
		warn(scSys, "行动", "绑定行动 %s 失败：%v（本回合按普通对话继续）", id, err)
		return false
	}
	aiMissionCur = &aiMissionSlot{
		id:     m.ID,
		report: m.Trigger.Kind == "patrol" || m.Trigger.Kind == "schedule",
	}
	info(scSys, "行动", "行动 %s 开始执行（trace=%s）", m.ID, traceID)
	return true
}

type missionStateErr string

func (e missionStateErr) Error() string { return "行动已处于终态：" + string(e) }

func errMissionTerminal(s string) error { return missionStateErr(s) }

// aiMissionStep 落盘一个已完成步骤（断点续作的回放点）。
// 普通对话（未绑定行动）时是 no-op。
func aiMissionStep(tool, target, why string, ok bool, evidence string) {
	s := aiMissionCur
	if s == nil {
		return
	}
	s.steps++
	if !ok {
		s.fails++
	}
	rec := PlanStepRecord{
		Seq: s.steps, Tool: tool, Target: target, Why: why,
		OK: ok, Evidence: truncateRunes(strings.TrimSpace(evidence), 300),
		At: time.Now().Format(time.RFC3339),
	}
	// 每步**立即落盘**：断流时已发生的步骤不丢，续作从这里回放。
	// 落盘失败只告警不打断执行——执行本身比记录重要。
	if _, err := missionUpdate(s.id, func(x *Mission) error {
		x.Plan = append(x.Plan, rec)
		// 有新步骤落地 = 断点已过去
		x.Pending = nil
		return nil
	}); err != nil {
		warn(scSys, "行动", "步骤落盘失败（%s: %s）：%v", tool, target, err)
	}
}

// aiMissionVerify 记录本回合最近一次复验结论（判据来自 verify.go，红线 1）。
func aiMissionVerify(vr OpResult) {
	if aiMissionCur == nil {
		return
	}
	cp := vr
	aiMissionCur.verify = &cp
}

// aiMissionAwaitConfirm 写工具弹确认卡时登记断点（Phase 1 T3）。
// 续作时上下文里会带上「断点：待确认 XXX」，模型不必从头推断卡在哪。
func aiMissionAwaitConfirm(tool, note string) {
	s := aiMissionCur
	if s == nil {
		return
	}
	p := &PendingStep{
		Kind: "confirm", Tool: tool,
		Note: truncateRunes(strings.TrimSpace(note), 200),
		At:   time.Now().Format(time.RFC3339),
	}
	if _, err := missionUpdate(s.id, func(x *Mission) error {
		if missionTerminal(x.State) {
			return nil
		}
		x.Pending = p
		return nil
	}); err != nil {
		warn(scSys, "行动", "登记断点失败（%s）：%v", tool, err)
	}
}

// aiMissionResumeContext 续作上下文（定案 3：摘要 + 关键结果全量）。
//
// 只在「有已完成步骤」时产出——第一次执行的回合 Plan 为空，不注入。
// 传给模型的是：目标 + 中断原因 + 每步的工具结论（全量）+ 断点，
// **不回传前半段对话原文**（请求体可控、缓存友好，见设计 2.4）。
// 证据是工具输出（不可信数据），按库内惯例包 untrusted_data。
func aiMissionResumeContext() string {
	s := aiMissionCur
	if s == nil {
		return ""
	}
	m, ok := missionGet(s.id)
	if !ok || len(m.Plan) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n<mission_context>\n")
	b.WriteString("你正在继续一个此前中断的「行动」。不要重头开始，也不要重复已完成的步骤。\n")
	b.WriteString("目标：" + m.Goal + "\n")
	if m.PauseWhy != "" {
		b.WriteString("中断原因：" + m.PauseWhy + "\n")
	}
	b.WriteString("此前已完成步骤（工具结论全量保留，对话原文不再回传）：\n")
	for _, st := range m.Plan {
		mark := "成功"
		if !st.OK {
			mark = "失败"
		}
		desc := st.Why
		if desc == "" {
			desc = st.Tool
		}
		// planLine 的默认文案已含 target，别拼成「（backend）（backend）」
		if st.Target != "" && !strings.Contains(desc, st.Target) {
			desc += "（" + st.Target + "）"
		}
		line := itoa(st.Seq) + ". [" + mark + "] " + desc
		if st.Evidence != "" {
			line += " —— <untrusted_data>" + st.Evidence + "</untrusted_data>"
		}
		b.WriteString(line + "\n")
	}
	if m.Pending != nil {
		b.WriteString("断点：等待确认「" + m.Pending.Note + "」（工具 " + m.Pending.Tool + "）——请提醒用户确认，不要自行执行。\n")
	}
	b.WriteString("规则：成败仍以系统复验结论为准（verify_result 是唯一判据）；从断点继续，补做未完成的步骤。\n")
	b.WriteString("</mission_context>")
	return b.String()
}

// aiMissionSettle 回合结束收口。必须在 endTrace 之前跑（defer LIFO 保证）。
func aiMissionSettle() {
	s := aiMissionCur
	aiMissionCur = nil
	if s == nil {
		return
	}
	defer notifyMissions() // 无论收口成什么，都让列表实时刷新
	outcome := outAborted
	if b := currentOutcomeBox(); b != nil {
		outcome = b.get()
	}
	why := ""
	switch outcome {
	case outSuccess:
		switch {
		case s.verify != nil:
			// 复验是唯一判据：过了 done，没过 failed
			msg := s.verify.Msg
			if msg == "" {
				msg = "复验结论"
			}
			if _, err := missionUpdate(s.id, func(x *Mission) error {
				return missionFinish(x, s.verify.Verified, msg)
			}); err != nil {
				warn(scSys, "行动", "行动 %s 收口失败：%v", s.id, err)
			}
			return
		case s.report && s.steps > 0 && s.fails == 0:
			if _, err := missionUpdate(s.id, func(x *Mission) error {
				return missionFinish(x, true, "巡检报告已产出（"+itoa(s.steps)+" 步全部成功）")
			}); err != nil {
				warn(scSys, "行动", "行动 %s 收口失败：%v", s.id, err)
			}
			return
		default:
			// 没有复验证据：failed 而不是 done（红线 1）
			if _, err := missionUpdate(s.id, func(x *Mission) error {
				return missionFinish(x, false, "回合结束但没有复验证据")
			}); err != nil {
				warn(scSys, "行动", "行动 %s 收口失败：%v", s.id, err)
			}
			return
		}
	case outPartial:
		why = "未完成（工具失败或预算耗尽），可点「继续」"
	case outFailed:
		why = "本回合失败，可点「继续」重试"
	default: // outAborted：断流 / 用户点停止 / 请求异常
		why = "连接中断或已停止，可点「继续」"
	}
	// 落 paused（不是终态）：断流不作废整轮，这正是续作的价值。
	// 例外：断点停在确认卡上 → awaiting_confirm——状态机语义更准
	//（卡片登记 TTL 30 分钟，见 msConfirmTimeout），续作时上下文会提示待确认。
	if _, err := missionUpdate(s.id, func(x *Mission) error {
		if missionTerminal(x.State) { // 理论不可达：收口时应为 running
			return nil
		}
		to := msPaused
		why2 := why
		if outcome == outAborted && x.Pending != nil {
			to = msAwaitingConfirm
			why2 = "等待确认「" + truncateRunes(x.Pending.Note, 60) + "」"
		}
		x.PauseWhy = why2
		return missionGo(x, to)
	}); err != nil {
		warn(scSys, "行动", "行动 %s 收口失败：%v", s.id, err)
		return
	}
	warn(scSys, "行动", "行动 %s 暂停：%s", s.id, why)
}
