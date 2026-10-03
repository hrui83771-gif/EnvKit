package main

// trace_phase.go —— v2.3 N1：Trace 的 plan / policy / human 三个环节
//
// v2.2 的 Trace 落地了 action / evidence / verify / failure / success，
// 但这三个环节的缺失让"一次任务是怎么走完的"仍然看不完整：
//
//   - **plan**   缺 → 不知道 AI 先试了什么、为什么是这个顺序。
//                 序列本身就是数据：同一目标连续三轮都从第一步开始试，
//                 说明它没记住上次的结论。
//   - **policy** 缺 → 审计里有 policy_forbidden 记录，但那是**结果**；
//                 轨迹里看不出"这一步被判成什么档位、依据哪条规则"。
//                 归因需要 rule 名，不只是 level。
//   - **human** 缺 → Learning 线最想要的一维信号拿不到：
//                 AI 试 A 失败、用户手动做 B 成功——这个对比是纯 Agent
//                 框架的日志里根本没有的（它们不记录"人接手"）。
//
// ## Human Trace 的判定为什么必须严格
//
// "用户手动做了 B" ≠ "用户在监督本任务"。用户可能是碰巧做别的事、
//  impatient 抢先点了一下。误判一次就会生成一条错误经验，
// 而经验会持续影响后续所有任务——**噪声代价远高于漏报**。
//
// 所以这里只取最稳的一种模式（全部条件都满足才记 human 步）：
//
//  1. 同一 Trace 上下文（currentTraceID 非空，即确实在某个任务进行中）
//  2. 动作同类（policyAction 归一后相同，如都是 "start_service:web"）
//  3. 该动作此前被 AI 尝试过且**失败过**（有人接手的前提是有人搞砸了）
//  4. 短窗口内成功（humanFixWindow）
//
// **不做跨任务关联**——用户在任务 A 结束后做的一切不算任务 A 的监督信号。
// 那会把"用户碰巧做的事"变成经验，污染程度远超收益。
//
// ## 隐私边界
//
// 只采过程不采内容：这里记的是动作名、目标引用、结果、耗时。
// 不记用户输入的文本、不记文件内容、不记任何凭据。

import (
	"strings"
	"time"
)

// ===== plan 环节 =====

const (
	// phPlan   规划/推理步
	phPlan = "plan"
	// phPolicy 策略裁决步
	phPolicy = "policy"
	// phHuman  人工介入步
	phHuman = "human"
)

// tracePlan 记一次规划/推理决策。
//
// 为什么单独记而不是并进 action：指标"无效操作次数"看的是**执行序列**，
// 而"AI 有没有换思路"看的是**决策序列**。混在一起就分不清
// "它换了方法但都失败了"和"它从头到尾用同一方法"。
//
// note 只允许写判断与理由，**不写用户输入原文**（隐私边界）。
func tracePlan(note, evidence string) {
	traceStep(phPlan, actAI, "plan", "", "ok", 0, "", firstLines(note, 300))
	if evidence != "" {
		traceStep(phPlan, actAI, "plan_basis", "", "ok", 0, "", firstLines(evidence, 300))
	}
}

// tracePlanFromTool 按工具结果推断一条推理记录。
//
// 不做真正的"推理链记录"——模型内部思维链拿不到，也不该拿。
// 这里记的是**可核查的决策依据**：选了哪个动作、依据是什么。
func tracePlanFromTool(tool string, args map[string]any) {
	tgt := policyTargetOf(tool, args)
	note := "选择动作 " + tool
	if tgt != "" {
		note += " @ " + tgt
	}
	tracePlan(note, planBasisOf(tool, args))
}

// planBasisOf 给出这个动作的依据摘要——是"看项目配置得来的"还是"猜的"。
// 依据来源决定后续能不能复用同一条决策。
func planBasisOf(tool string, args map[string]any) string {
	var b []string
	if s, _ := args["script"].(string); s != "" {
		if lp := currentLaunchPlan(); lp.WebScript != "" {
			if s == lp.WebScript {
				b = append(b, "前端启动脚本来自"+launchSourceLabel(lp.WebSource))
			} else {
				b = append(b, "前端脚本由调用方指定（与项目推断的 "+lp.WebScript+" 不同）")
			}
		} else {
			b = append(b, "前端脚本由调用方指定，项目未能推断出候选")
		}
	}
	if svc, _ := args["service"].(string); svc != "" {
		if st := runtimeService(svc); st.Phase != phStopped {
			b = append(b, "当前状态 "+string(st.Phase)+"，先查状态再决定动作")
		} else {
			b = append(b, "当前处于停止态，确需启动")
		}
	}
	if len(b) == 0 {
		return ""
	}
	return strings.Join(b, "；")
}

func launchSourceLabel(src string) string {
	switch src {
	case "config":
		return "用户配置"
	case "package.json":
		return "package.json 推断"
	case "none":
		return "无（识别失败）"
	}
	if src == "" {
		return "未知来源"
	}
	return src
}

// ===== policy 环节 =====

// tracePolicy 记一次策略裁决。
//
// 记 rule 名而不只是 level：level 回答"能不能做"，
// rule 回答"依据哪一条"。归因到具体规则才知道该改规则还是改默认档。
func tracePolicy(v PolicyVerdict, decision string) {
	// auto 档不记：只读操作每秒十几次，全记会把轨迹冲垮。
	// PolicyGate 侧已用 policyRecord 落审计，这里只补轨迹视角。
	if v.Level == PolicyAuto {
		return
	}
	ev := "level=" + string(v.Level) + " rule=" + v.Rule + " decision=" + decision
	if v.OptIn {
		ev += " opt_in"
	}
	traceStep(phPolicy, v.Actor, v.Action, v.Target, decision, 0, "", ev)
}

// tracePolicyDenied 便捷入口：裁决结果是拒绝。
func tracePolicyDenied(v PolicyVerdict) { tracePolicy(v, "denied") }

// tracePolicyConfirmed 便捷入口：用户点了确认。
func tracePolicyConfirmed(v PolicyVerdict) { tracePolicy(v, "confirmed") }

// ===== human 环节 =====

// humanFixWindow 人工接手的判定窗口：AI 失败后多久内的成功算"人接手了"。
//
// 取 30 分钟与经验提取的 R2 规则（userFixWindow）一致——
// 同一个阈值，避免"提取经验时算接手、记轨迹时不算"这种口径分歧。
const humanFixWindow = 30 * time.Minute

// humanFix 记录一次人工介入。调用方是所有"用户主动操作"的审计写入处。
//
// 参数是 policyAction 归一后的动作键（如 "start_service:web"），
// 由 humanActionKey 计算。target 用于人看的说明。
func humanFix(actionKey, target, result string) {
	if currentTraceID() == "" {
		// 不在任何任务上下文里 → 这是用户自己的独立操作，不是"接手"。
		// 这是四条判定里最先拦的一条：它挡掉了绝大多数噪声。
		return
	}
	if result != resOK {
		// 人工操作自己失败了：这不构成监督信号（人也没搞掂）。
		return
	}
	if !traceAIFailedAction(actionKey) {
		// 前提不成立：AI 从没在这条轨迹里试过这个动作且失败。
		// 用户做自己本来就要做的事（他没等 AI 试过）不属于"接手"。
		return
	}
	traceStep(phHuman, actUser, "human_intervention", target, resOK, 0, "",
		"用户手动完成 "+actionKey+"（AI 此前尝试失败）——同类问题先问用户做法")
}

// traceAIFailedAction 这条轨迹里 AI 是否已对该动作失败过。
func traceAIFailedAction(actionKey string) bool {
	traceMu.Lock()
	defer traceMu.Unlock()
	if traceCur == nil {
		return false
	}
	for _, s := range traceCur.Steps {
		if s.Actor != actAI {
			continue
		}
		if s.Result != resFail {
			continue
		}
		if policyActionKey(s.Action, s.Target) == actionKey {
			return true
		}
	}
	return false
}

// policyActionKey 把动作+目标归一成一个可比较的键。
//
// 核心规则：**参数不进键，目标才进键**。
// AI 试 `npm run serve` 失败、用户改用 `npm run dev` 成功——
// 这在键看来必须是"同一个动作、换了参数"，而不是两个无关动作。
// 这正是 Human Trace 最有价值的一类信号（"人怎么修的"），
// 参数不同会让朴素的比较把它当成两件事，信号就此丢失。
//
// 注意：PolicyGate 的**档位判定**用的是原始 target（script 参数在那里
// 决定 forbidden 与否），这里用的是归一后的键（只用于"是否同类"的比较）。
// 两者目的不同，故意不共用同一套——别把它们混起来改。
func policyActionKey(action, target string) string {
	a := action
	if i := strings.IndexAny(a, " \x00"); i > 0 {
		a = a[:i]
	}
	// 剥掉 script=xxx：它是参数，描述"怎么做"，不是"对什么做"。
	// 必须**按字段丢弃整段**——只取 key 名会把参数值（"serve"）留下来，
	// 那等于参数还是进了键，AI 试 serve 与用户改 dev 仍然对不上号。
	// 踩过这个坑：第一版写成 rest=参数值，产出 "start_service:serve"。
	var keep []string
	for _, f := range strings.Fields(target) {
		if strings.HasPrefix(f, "script=") {
			continue
		}
		keep = append(keep, f)
	}
	target = strings.Join(keep, " ")
	if target == "" {
		return a
	}
	return a + ":" + target
}

// humanActionKeyFor 从审计条目算人工介入的动作键（供审计写入处调用）。
func humanActionKeyFor(action, target string) string {
	return policyActionKey(action, target)
}

// ===== 汇总视图 =====

// traceSummary 给 UI 与 AI 的一句话轨迹摘要。
// 目的是让人/模型一眼看出"这次任务绕了几圈"，而不是读完整 JSON。
func traceSummary(tr *Trace) string {
	if tr == nil || len(tr.Steps) == 0 {
		return ""
	}
	var aiCalls, aiFails, verifies, humans, policies int
	var actions []string
	seen := map[string]bool{}
	for _, s := range tr.Steps {
		switch s.Phase {
		case phPolicy:
			policies++
		case phHuman:
			humans++
		case phVerify:
			verifies++
		}
		if s.Actor == actAI && s.Phase == phAction {
			aiCalls++
			if s.Result == resFail {
				aiFails++
			}
			k := policyActionKey(s.Action, s.Target)
			if !seen[k] {
				seen[k] = true
				actions = append(actions, k)
			}
		}
	}
	out := "轨迹 " + tr.TraceID + "：AI 调用 " + itoa(aiCalls) + " 次（失败 " + itoa(aiFails) + "）"
	if len(actions) > 0 {
		out += "，动作序列 " + strings.Join(actions, " → ")
	}
	if policies > 0 {
		out += "；策略裁决 " + itoa(policies) + " 次"
	}
	if verifies > 0 {
		out += "；复验 " + itoa(verifies) + " 次"
	}
	if humans > 0 {
		out += "；**人工接手 " + itoa(humans) + " 次**"
	}
	if tr.Verified {
		out += "；已复验通过"
	} else {
		out += "；**未取得复验结论**"
	}
	return out
}
