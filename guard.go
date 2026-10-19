// guard.go —— 拓扑无关的守护巡检循环（v3.0 第二阶段 T2.6，plan §4.1）
//
// 旧实现（chain.go 的 startGuardLoop）把"巡检"和"链端"焊死在一起：
// 循环体里直接写 sshLocalPortOpen、runChainStart、verifyChain。
// 于是每加一种要守护的环境，就得复制一整份循环——而复制出来的第二份
// 一定会和第一份漂移，且漂移方向永远是"新那份悄悄变松"。
//
// 现在：**循环只认识 Service、适配器接口和策略元数据**，不认识任何拓扑。
//
//	遍历 services → 组锁 → Probe → 不健康且策略允许 → 恢复事务 → Verify → 三态出口
//
// 链端专属逻辑全在 topo_chain.go 里；本文件禁现拓扑名（R5，T2.9 静态检查）。
//
// 行为对齐（plan §4.6 T2.6 验收）：审计动作名/目标、组、通知开关全部来自
// Service.GuardSpec，推导值与 v3.0.1 基线逐字相同，所以
// chain_autorecover / webase_autorecover 这两条审计条目不变。
package main

import (
	"fmt"
	"strconv"
	"time"
)

// startGuardLoopTopo 拓扑无关守护循环入口。由 startGuardLoop 调用（main.go 不改）。
//
// 没有可守护的服务时直接返回——与旧循环"没有 SSH 主机就返回"等价：
// 空转的巡检除了刷日志没有任何意义。
func startGuardLoopTopo() {
	svcs := guardedServices(effectiveServicesOrWarn())
	if len(svcs) == 0 {
		return
	}
	go func() {
		first := true
		for {
			interval := guardInterval()
			time.Sleep(time.Duration(interval) * time.Second)
			if !guardEnabled() {
				first = true
				continue
			}
			// 每轮重取：用户可能刚改了配置（关守护、加服务、改间隔）
			svcs := guardedServices(effectiveServicesOrWarn())
			if len(svcs) == 0 {
				first = true
				continue
			}
			// 本轮所有 service 的探针结论，供依赖抑制判断用（plan §4.3 最小版）
			unhealthy := map[string]bool{}
			for i := range svcs {
				guardOne(svcs[i], svcs, unhealthy, first)
			}
			first = false
		}
	}()
}

// guardEnabled 守护是否开启。取所有可守护服务的通知开关之或——
// 与旧代码的 c.ChainGuard 对应（链端守护开关）。
func guardEnabled() bool {
	for _, s := range guardedServices(effectiveServicesOrWarn()) {
		if s.Guard.Notify || s.Recovery != "" {
			// 有恢复策略即视为参与守护；通知开关只决定要不要弹窗
			return true
		}
	}
	return false
}

// guardInterval 巡检间隔（秒，最小 30）。
// 取所有可守护服务里的最小值——最敏感的那个说了算。
//
// 间隔来自 GuardSpec.IntervalSecs（配置），**不是某个拓扑专属字段**：
// 旧代码读 ChainGuardSecs，那是因为它只守护链端；照搬过来就等于
// 核心循环仍然认识链（R5）。
func guardInterval() int {
	best := 0
	for _, s := range guardedServices(effectiveServicesOrWarn()) {
		v := s.Guard.IntervalSecs
		if v <= 0 {
			v = s.Probe.Timeout
		}
		if v > 0 && (best == 0 || v < best) {
			best = v
		}
	}
	if best < 30 {
		best = 30
	}
	return best
}

// effectiveServicesOrWarn 取生效清单；配置有问题时告警并返回空
// （**不静默退回旧字段**——那会让"配置写错"表现得像"没有服务"）。
func effectiveServicesOrWarn() []Service {
	svcs, problems := effectiveServices(cfg)
	if len(problems) > 0 {
		warn(scSys, "守护", "服务清单配置有 %d 处问题，本轮不巡检：%v",
			len(problems), problems)
		return nil
	}
	return svcs
}

// guardedServices 只取"配了恢复策略"的 service。
// 与旧行为一致：旧循环只守护链端，而推导出的清单里只有 chain/webase 有策略。
// **刻意不给 web/backend/mysql 挂策略**——那会扩大无人值守自动恢复的作用面，
// 而这件事没有任何实跑证据支撑（plan §7.1 豁免风险）。
func guardedServices(svcs []Service) []Service {
	var out []Service
	for _, s := range svcs {
		if s.Recovery != "" && strategyKnown(s.Recovery) {
			out = append(out, s)
		}
	}
	return out
}

// guardOne 巡检并（必要时）恢复单个 service。
//
// unhealthy 是本轮已探过的不健康 service 集合，用于依赖抑制：
// 依赖不健康时本级不恢复——否则链全宕时会发起两个恢复事务，
// 而第二个必然失败（它的前置就是第一个要修的东西）。
func guardOne(svc Service, all []Service, unhealthy map[string]bool, first bool) {
	probe, rec, ver, hasAdapter := adapterFor(svc.Adapter)
	if !hasAdapter {
		warn(scSys, "守护", "service %s 的适配器 %s 未注册，跳过", svc.ID, svc.Adapter)
		return
	}
	strat, hasStrat := strategyByName(svc.Recovery)
	if !hasStrat {
		// guardedServices 已过滤，这里纯防御
		return
	}
	action := svc.Guard.auditActionOf(svc.ID)
	target := svc.Guard.auditTargetOf(svc.ID)

	// 组锁：拿不到就记 skip，**不静默跳过**。
	// 静默跳过的后果是用户看到"守护在跑"但恢复从未发生。
	h, granted := beginTaskHG(svc.Label+"守护巡检", []string{svc.Guard.groupOf(svc.ID)}, true)
	if !granted {
		guardMark("skip_busy", busyReason(svc.Guard.groupOf(svc.ID)), false)
		return
	}
	defer h.Done()

	obs := probe.Observe(svc)
	if obs.Healthy {
		unhealthy[svc.ID] = false
		if first {
			info(scSys, "守护", "后台守护已开启，当前%s正常", svc.Label)
		}
		guardMark("ok", svc.Label+"正常", true)
		return
	}
	unhealthy[svc.ID] = true

	// 共同原因（SSH 不可达 / 凭据被拒 / 探测不到）：**不发起恢复**。
	// 恢复命令本身就靠 SSH 送达，连不上时重启毫无意义；
	// 且这是"一次上游故障影响多个 service"的信号（plan §4.3）。
	if isCommonCause(obs.ErrKind) {
		guardSetUnreachable(1)
		guardMark("skip_unreachable", obs.ErrKind, false)
		warn(scSys, "守护", "%s 探针报共同原因（%s），本轮不执行自动恢复：%s",
			svc.Label, obs.ErrKind, firstLine(fmt.Sprint(obs.Evidence)))
		return
	}

	// 依赖抑制（plan §4.3 最小版，完整关联在 T2.7）：
	// 依赖不健康时本级只记录"受影响"，不各自恢复。
	if dep, blocked := dependencyUnhealthy(svc, all, unhealthy); blocked {
		guardMark("cascade_suppressed", dep, false)
		auditNow(actGuard, action, target,
			fmt.Sprintf("依赖 %s 不健康，本级不恢复", dep), resFail,
			"上游故障未恢复前不级联恢复")
		return
	}

	// 探测不到 ≠ 服务坏了。对它发起恢复就是误恢复。
	if obs.ErrKind == "probe_no_clue" {
		guardMark("skip_probe", "无法判定 "+svc.Label+" 状态："+obs.ErrKind, false)
		return
	}

	// 未晋升的策略只记录不执行（plan §4.4 的最小版，完整影子事务在 T2.8）。
	//
	// **这不是可选的礼貌，是边界**：一个没有审批人的自动恢复动作，
	// 出了事没有人可追溯。已晋升的照旧自动执行——restart_chain 在
	// v3.0.1 基线里就是这么跑的，把它改成影子会是行为回退，
	// 所以它带着审批记录（见 strategy.go）。
	if !strategyAutoExecutable(strat) {
		guardMark("shadow", svc.Label+"：策略未晋升，仅记录不执行", false)
		auditNow(actGuard, action, target,
			fmt.Sprintf("%s 未响应（%s）", svc.Label, obs.ErrKind), resFail,
			"影子评估：策略未获人工晋升，守护不自动执行")
		warn(scSys, "守护", "%s 需要恢复，但策略 %s 未晋升为可自动执行，本轮只记录",
			svc.Label, strat.Name)
		return
	}

	// 两层节制（T1.5）：预算 + per-target 尝试上限 + 熔断。
	// 无人值守的自动重启超限即熔断升级——没有节制的自动重启会把环境刷爆，
	// 用户看到的是通知刷屏，而真正的故障原因没人去看。
	if err := txRecoveryGate(action, target); err != nil {
		guardMark("budget_blocked", err.Error(), false)
		warn(scSys, "守护", "%s 自动恢复被节制拒绝：%v", svc.Label, err)
		return
	}

	// 规划。规划失败 = 配置不全或命令不安全，**不执行**。
	plan, err := rec.Plan(svc, Action{Kind: "restart", Why: obs.ErrKind})
	if err != nil {
		guardMark("plan_failed", err.Error(), false)
		warn(scSys, "守护", "%s 恢复计划无法生成：%v", svc.Label, err)
		return
	}

	warn(scSys, "守护", "%s 未响应，自动重启", svc.Label)
	recordRecover()
	guardMark("recover", svc.Label+"未响应，已自动重启", true)
	auditNow(actGuard, action, target,
		fmt.Sprintf("%s 未响应（%s）", svc.Label, obs.ErrKind), resStart, "守护检测触发")
	if svc.Guard.Notify {
		alertDispatch(svc.Label+"未响应", "后台守护正在自动重启...")
	}

	rec.Execute(plan)

	// 复验：无人值守的自动恢复，"成没成"只认这一步的结论（三态出口）。
	vr := ver.Verify(svc, svc.Verify)
	auditVerify(actGuard, action, target, vr)
	switch {
	case vr.Ok && vr.Verified:
		guardMark("recover", vr.Msg, true)
		ok(scSys, "守护", "%s 已自动恢复并通过复验：%s", svc.Label, vr.Msg)
		if svc.Guard.Notify {
			alertDispatch(svc.Label+"已恢复", "自动重启成功并通过复验："+vr.Msg)
		}
	case vr.Ok:
		// 已拉起但复验未完成。**不能算成功**——那把"执行了"当成"恢复了"。
		// 同时要计数：守护的失败不记，重试上限就只掐 AI 不掐守护，
		// 那正是最该掐的那一个。
		guardMark("recover", "已拉起但复验未完成："+vr.Msg, false)
		warn(scSys, "守护", "%s 已拉起但复验未完成：%s", svc.Label, vr.String())
		svcRecordCrash(svc.ID, errKindVerifyFail, firstLines(vr.Msg, 120), 0)
		if svc.Guard.Notify {
			alertDispatch(svc.Label+"恢复待确认", "已拉起但复验未通过："+vr.Msg)
		}
	default:
		guardMark("recover", "自动恢复失败："+vr.Msg, false)
		fail(scSys, "守护", "%s 自动恢复失败：%s", svc.Label, vr.String())
		svcRecordCrash(svc.ID, vr.ErrKind, firstLines(vr.Msg, 120), 0)
		if svc.Guard.Notify {
			alertDispatch(svc.Label+"恢复失败", "自动重启未成功，请打开 EnvKit 检查")
		}
	}
}

// isCommonCause 探针结论是否属于"共同原因"（多个 service 会同时中招的上游故障）。
//
// 这三类的共同点是：**不是本 service 自己的故障**。对它们发起恢复，
// 修的是症状不是病因，而且会重复失败直到预算耗尽。
func isCommonCause(errKind string) bool {
	switch errKind {
	case "unreachable", "auth_failed", "probe_no_clue":
		return true
	}
	return false
}

// dependencyUnhealthy 依赖里是否有不健康的。返回 (依赖 id, 是否被抑制)。
//
// 为什么需要：四节点全宕时如果每个 service 各发起一个恢复事务，
// 会把环境刷爆，而且后三个必然失败（它们的前置正是第一个要修的东西）。
// 这是 plan §4.3 跨节点事件关联的**最小可用版**——
// 完整版（时间窗 + 共同依赖归因 + cascade_suppressed 报告）在 T2.7。
func dependencyUnhealthy(svc Service, all []Service, unhealthy map[string]bool) (string, bool) {
	for _, dep := range svc.DependsOn {
		if unhealthy[dep] {
			return dep, true
		}
	}
	return "", false
}

// portStr 端口转字符串（探针证据用）。
func portStr(p int) string { return strconv.Itoa(p) }
