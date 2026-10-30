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
//   · 结局 success + 无复验，三条路只有两条能判 done：
//     ① 报告型（巡检/晨报，trigger=patrol|schedule）以「步骤全部成功且 >0」为完成；
//     ② **纯只读行动**（v3.6）以「全部步骤成功且没有任何写工具」为完成 ——
//     只读检查没有环境状态可复验，拿动作判据卡它会让「检查数据库」这种
//     一步成功、什么都没改的行动永远 failed（判据把对判成了错）；
//   · 结局 success + 无复验 + 有写操作 → failed（宁可错杀，不发假徽章）；
//   · 结局 partial/failed/aborted（含断流、用户点停止、预算耗尽）→ paused 带原因，
//     用户回来点「继续」——**断流不作废整轮**，这正是 Mission 存在的意义。
//
// ① 与 ② 都只是"用客观事实替代复验"，红线 1 没有被放宽：
// done 仍只能由 missionFinish 落，且依据是落盘的步骤记录，不是模型的自述。

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

// aiMissionResumeContext 行动上下文（Phase 1 定案 3 + Phase 2 修订）。
//
// **修订原因（真实断链）**：原实现只在「有已完成步骤」时注入，于是
// 「全新 pending 行动 → 点继续 → 前端只发一句『继续执行该行动』」时
// 模型拿不到目标，只能反问"你想让我做什么"。手动新建行动没暴露这个坑
// （那边把 goal 当消息正文发了），但巡检/晨报/守护/入站产出的行动全都中招，
// 而这几种恰恰没有人替它说目标。
//
// 现在的口径：
//   - 绑定即注入，**目标始终带上**（空目标才返回空）；
//   - 有已完成步骤 → 附「此前中断」摘要 + 每步结论全量 + 断点；
//   - 有预案起草步骤 → 附建议顺序（省掉"推理怎么修"，但**不省确认**）；
//   - 没步骤也没预案 → 只带目标 + 一句"这是第一次执行"。
//
// 仍不回传前半段对话原文（定案 3：请求体可控、缓存友好）。
// 证据是工具输出（不可信数据），按库内惯例包 untrusted_data。
func aiMissionResumeContext() string {
	s := aiMissionCur
	if s == nil {
		return ""
	}
	m, ok := missionGet(s.id)
	if !ok {
		return ""
	}
	goal := strings.TrimSpace(m.Goal)
	if goal == "" {
		return "" // 没有目标就没有可注入的东西
	}
	var b strings.Builder
	b.WriteString("\n\n<mission_context>\n")
	if len(m.Plan) > 0 {
		b.WriteString("你正在继续一个此前中断的「行动」。不要重头开始，也不要重复已完成的步骤。\n")
	} else {
		b.WriteString("这是「行动」的第一次执行，此前没有已完成步骤。\n")
	}
	b.WriteString("目标：" + goal + "\n")
	if m.Trigger.Kind != "" {
		b.WriteString("来源：" + m.Trigger.Kind + triggerDetailNote(m.Trigger.Detail) + "\n")
	}
	if m.PauseWhy != "" {
		b.WriteString("中断原因：" + m.PauseWhy + "\n")
	}
	if len(m.Plan) > 0 {
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
	}
	if m.Pending != nil {
		b.WriteString("断点：等待确认「" + m.Pending.Note + "」（工具 " + m.Pending.Tool + "）——请提醒用户确认，不要自行执行。\n")
	}
	// 预案起草步骤（Phase 2）：只给顺序，不给"免确认"的借口。
	if len(m.Draft) > 0 {
		title := m.Playbook
		if p, ok := pbkGet(m.Playbook); ok {
			title = p.Title
		}
		b.WriteString("本行动已套用预案「" + title + "」，建议按这个顺序做（可据实际情况调整）：\n")
		for i, st := range m.Draft {
			line := itoa(i+1) + ". " + st.Tool
			if st.Target != "" {
				line += "（" + st.Target + "）"
			}
			if st.Why != "" {
				line += " —— " + st.Why
			}
			if t, ok := aiToolRegistry[st.Tool]; ok && t.Write {
				line += "【写操作，必须先确认】"
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("预案只是省掉「推理怎么修」，**不省确认**：每个写步骤照常弹确认卡。\n")
	}
	b.WriteString("规则：成败仍以系统复验结论为准（verify_result 是唯一判据）；从断点继续，补做未完成的步骤。\n")
	b.WriteString("</mission_context>")
	return b.String()
}

// triggerDetailNote 触发摘要的一句话补充（有内容才拼）。
func triggerDetailNote(detail string) string {
	d := strings.TrimSpace(detail)
	if d == "" {
		return ""
	}
	return "（" + truncateRunes(d, 160) + "）"
}

// missionReadOnlyDone 判断一次行动是否「纯只读且全部成功」（v3.6）。
//
// 为什么需要这条：手动发起的只读检查（"检查数据库""看看日志"）没有任何写操作，
// 也就没有环境状态可复验，模型既不会也不需要调 verify_environment。用
// 「必须有复验证据」去卡它，会让一步成功、什么都没改的行动永远 failed ——
// 进而把首次解决率、R6 连败、预案命中率一起算错，用户看到的是一排 0。
//
// 判据仍然是**客观事实**，不是"模型说它做完了"（红线 1 没有放宽）：
//   - 每一步都成功（落盘记录的 ok 字段，来自工具执行结果）；
//   - 每一步用的工具都在注册表里，且不是写工具。
//
// 注册表里查不到的工具名一律按「写」处理：证明不了它没动东西，就不给它免复验。
func missionReadOnlyDone(id string) (int, bool) {
	m, ok := missionGet(id)
	if !ok || len(m.Plan) == 0 {
		return 0, false
	}
	for _, st := range m.Plan {
		if !st.OK {
			return 0, false
		}
		t, exists := aiToolRegistry[st.Tool]
		if !exists || t.Write {
			return 0, false
		}
	}
	return len(m.Plan), true
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
	// fin 收口 + 回写预案统计。放一个闭包里而不是每处都写：
	// 收口有五条出口，漏掉任何一处的统计回写都会让"这套预案靠不靠得住"
	// 永远少记一次 —— 而那种缺失看不出来。
	fin := func(fn func(*Mission) error) {
		m, err := missionUpdate(s.id, fn)
		if err != nil {
			warn(scSys, "行动", "行动 %s 收口失败：%v", s.id, err)
			return
		}
		missionPlaybookSettle(m)
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
			fin(func(x *Mission) error { return missionFinish(x, s.verify.Verified, msg) })
			return
		case s.report && s.steps > 0 && s.fails == 0:
			fin(func(x *Mission) error {
				return missionFinish(x, true, "巡检报告已产出（"+itoa(s.steps)+" 步全部成功）")
			})
			return
		default:
			// 纯只读行动（v3.6）：没有任何改动可复验，逐步成功即视为完成。
			// 有写操作的行动仍一律 failed —— 那才是红线 1 要守的地方。
			if n, ro := missionReadOnlyDone(s.id); ro {
				fin(func(x *Mission) error {
					return missionFinish(x, true, "只读检查完成（"+itoa(n)+" 步全部成功，未改动任何东西）")
				})
				return
			}
			// 没有复验证据：failed 而不是 done（红线 1）
			fin(func(x *Mission) error {
				return missionFinish(x, false, "回合结束但没有复验证据（有写操作的行动须调用 verify_environment 复验）")
			})
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
	fin(func(x *Mission) error {
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
	})
	warn(scSys, "行动", "行动 %s 暂停：%s", s.id, why)
}

// missionPlaybookSettle 行动收口后回写预案统计。
//
// 只有**终态**才算一次执行：paused / awaiting_confirm 是中断，不是结果，
// 拿它记 fail 会让"这套预案靠不靠得住"被断网次数污染。
// 成功判据仍是 Result.Verified（红线 1）—— 不看 mission 状态是 done 就算成功，
// 因为报告型行动的 done 表示"产出完成"，与"这套处置有效"不是一回事，
// 但报告型行动本来也不会带预案（晨报不套预案），这里只做防御。
func missionPlaybookSettle(m Mission) {
	if strings.TrimSpace(m.Playbook) == "" || !missionTerminal(m.State) {
		return
	}
	verified := m.Result != nil && m.Result.Verified
	pbkSettle(m.Playbook, verified)
}
