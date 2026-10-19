// strategy.go —— 恢复策略元数据表（v3.0 第二阶段 T2.1，plan §4.5）
//
// 每个**自动恢复类别**必须齐备六字段。为什么必须是表而不是散在各处的 if：
// 自动恢复的可信度建立在"触发条件、前置检查、风险等级、尝试上限、复验标准、
// 回滚定义"六件事都被显式声明过——缺任何一件，那个自动动作就是没有定义的。
// 散在代码里的恢复逻辑恰恰是缺件时最不容易被发现的形式。
//
// 四表同源（T2.9 断言，plan §4.5 末段）：
//
//	本表（产品侧策略） × fault_inject.KINDS × INJECTORS × recovery_rate.INJECTIONS
//
// 判据侧的 expect_autonomous / forbidden_actions 必须能在本表找到对应条目，
// 否则就是"判据在测一个产品里不存在的策略"。
package main

import (
	"fmt"
	"strings"
)

// RecoveryStrategy 一个自动恢复类别的完整定义（plan §4.5）。
type RecoveryStrategy struct {
	Name string `json:"name"`

	// Trigger 触发条件：哪个 probe 的哪个失败态。
	// 写成人看的句子，但必须能对应到 Observation 的字段——
	// "服务好像有问题"不是触发条件，"probe.kind=port 连续 N 轮 LISTENING 失败"才是。
	Trigger string `json:"trigger"`

	// Precheck 前置检查。任一项不过即不执行（不是警告后继续执行）。
	// 取值见 precheck 常量：组锁可获 / 预算余量 / 断路器未熔断 / 依赖健康 / 有人值守。
	Precheck []string `json:"precheck"`

	// RiskTier low=重启类可自动 / high=数据类必须人工（plan §4.5）。
	RiskTier string `json:"risk_tier"`

	// MaxAttempts 最大尝试次数（断路器消费，见 txbudget.go）。
	MaxAttempts int `json:"max_attempts"`

	// VerifySpec 独立复验标准。**该类恢复特有的客观证据字段**，
	// 不用他类的判据冒充——链端要看 block_height，备份要看 file+size+sha256。
	VerifySpec VerifySpec `json:"verify_spec"`

	// Compensate 回滚定义：快照什么、怎么恢复。
	// **无可回滚则必须 high**（plan §4.5）：能自动做但又撤不回来的动作，
	// 不允许出现在 low 档里。
	Compensate string `json:"compensate"`

	// ---- 影子模式晋升（plan §4.4）----
	// 默认三者皆空 = 只影子不执行。晋升需要：连续 N 次影子评估通过
	// **且** 人工审阅，三个字段缺一不可自动——
	// 只凭"影子连续通过"就放行，等于把通过率当成了审批人。
	AutoApproved bool   `json:"auto_approved,omitempty"`
	ApprovedBy   string `json:"approved_by,omitempty"`
	ApprovedAt   string `json:"approved_at,omitempty"`

	// Grandfathered 显式标注"这个审批不来自影子晋升路线"：
	// 它来自基线既有行为或代码评审，由人明确作证。
	// 没有这个标注时，守护循环还要求连续 N 次影子通过作为证据
	// （shadowEvidenceOK）——签名不等于证据。这个标注是**逃逸口
	// 不是免审批**：strategyValidate 强制它也必须带审批三字段，
	// 且它会出现在代码评审与静态检查的视野里。
	Grandfathered bool `json:"grandfathered,omitempty"`
}

// 前置检查项（plan §4.5 precheck）。字符串常量而不是裸字符串，
// 让拼错时能在编译期外至少被 strategyValidate 抓到。
const (
	PrecheckGroupLock = "group_lock" // 资源组锁可获（T1.2）
	PrecheckBudget    = "budget"     // 窗口动作预算有余量（T1.5）
	PrecheckBreaker   = "breaker"    // 崩溃断路器未熔断（T1.9）
	PrecheckDeps      = "deps"       // depends_on 里的依赖健康（T2.7）
	PrecheckAttended  = "attended"   // 需要有人值守（high 档必填）
)

// defaultStrategies 内置策略表。
//
// 与判据侧 INJECTIONS 的对应关系（四表同源，T2.9 断言）：
//
//	restart_backend   ← kill_service / crash_on_next / hang_service
//	wait_slow_start   ← slow_start（**故意不重启**：慢启动时重启会掐死正常的启动）
//	port_conflict     ← hold_port / fake_listen（只报 owner，不杀第三方进程）
//	refresh_backup    ← corrupt_backup / unreadable_backup
//	db_restart        ← db_down（high：数据类，必须人工）
//	restart_chain     ← 链端守护（startGuardLoop；无对应注入类，走真实守护路径）
//
// 没有策略的类别：stale_log_ok / ok_then_die——它们描述的是"没有故障"或
// "时序现象"，造一个恢复策略进去就是在测一个不该存在的动作。
var defaultStrategies = []RecoveryStrategy{
	{
		Name:        "restart_backend",
		Trigger:     "probe.kind=port 连续 2 轮未 LISTENING，且进程已退出或探活超时",
		Precheck:    []string{PrecheckGroupLock, PrecheckBudget, PrecheckBreaker},
		RiskTier:    RiskLow,
		MaxAttempts: 3,
		VerifySpec: VerifySpec{
			Target:   "backend",
			Evidence: []string{"pid", "port", "http_code"},
			WaitSecs: 45,
		},
		Compensate: "重启前记录 pid 与监听端口；失败则回退为「保持停止」并升级人工，不做二次强杀",
	},
	{
		Name:        "wait_slow_start",
		Trigger:     "probe.kind=port 未 LISTENING，但进程存在且启动耗时未超阈值",
		Precheck:    []string{PrecheckGroupLock},
		RiskTier:    RiskLow,
		MaxAttempts: 1,
		VerifySpec: VerifySpec{
			Target:   "backend",
			Evidence: []string{"pid", "port", "http_code"},
			WaitSecs: 180,
		},
		Compensate: "无动作可回滚（本策略只等待）；超时后交回 restart_backend 并计一次尝试",
	},
	{
		Name:        "port_conflict",
		Trigger:     "probe.kind=port 被占用但 owner 不是本服务（第三方进程/僵尸监听者）",
		Precheck:    []string{PrecheckGroupLock},
		RiskTier:    RiskLow,
		MaxAttempts: 1,
		VerifySpec: VerifySpec{
			Target:   "backend",
			Evidence: []string{"port", "owner"},
			WaitSecs: 10,
		},
		Compensate: "无可回滚也不需回滚：本策略**不杀任何进程**，只记录 owner 并报告",
	},
	{
		Name:        "refresh_backup",
		Trigger:     "verify.kind=backup_static 复验失败（文件缺失/大小异常/不是合法 SQL）",
		Precheck:    []string{PrecheckGroupLock, PrecheckBudget},
		RiskTier:    RiskLow,
		MaxAttempts: 2,
		VerifySpec: VerifySpec{
			Target:   "db",
			Evidence: []string{"file", "size", "sha256"},
			WaitSecs: 30,
		},
		Compensate: "旧备份保留至 prune 上限；新备份失败不影响旧备份可用性",
	},
	{
		Name:        "db_restart",
		Trigger:     "probe.kind=command（mysqladmin ping）连续失败且端口未监听",
		Precheck:    []string{PrecheckGroupLock, PrecheckBudget, PrecheckBreaker, PrecheckAttended},
		RiskTier:    RiskHigh,
		MaxAttempts: 1,
		VerifySpec: VerifySpec{
			Target:   "db",
			Evidence: []string{"version", "db_exists"},
			WaitSecs: 60,
		},
		Compensate: "重启 MySQL 服务前确认无进行中的备份/还原；有则排队不重启",
	},
	{
		Name:        "restart_chain",
		Trigger:     "probe.kind=port（远程链端口）未 LISTENING，且 SSH 主机可达",
		Precheck:    []string{PrecheckGroupLock, PrecheckBudget, PrecheckBreaker},
		RiskTier:    RiskLow,
		MaxAttempts: 3,
		VerifySpec: VerifySpec{
			Target:   "chain",
			Evidence: []string{"port", "node_proc", "block_height"},
			WaitSecs: 60,
		},
		Compensate: "重启前记录远程节点进程与区块高度；失败则保持停止并升级人工，不重复强杀",
		// 审批记录：这不是新批的，是**把 v3.0.1 基线里既有的行为记下来**。
		// 链端守护自动重启在 eccd3ba/b3c69af 两版基线上都在跑，
		// T2.6 把它迁进拓扑模型时若悄悄降级成"只影子不执行"，
		// 就是一次没有人会发现的行为回退。
		// Grandfathered：这个审批来自基线而非影子晋升路线（T2.8），
		// 显式标注而不是隐含——逃逸口必须看得见。
		AutoApproved:  true,
		Grandfathered: true,
		ApprovedBy:    "v3.0.1 基线既有行为（链端守护自动重启），T2.6 迁入拓扑模型时保留",
		ApprovedAt:    "2026-10-19",
	},
}

// strategyTable 生效的策略表。目前就是内置表——
// 留一个查表入口而不是让调用方直接摸切片，将来配置侧可覆盖时只改这里。
func strategyTable() []RecoveryStrategy {
	return defaultStrategies
}

// strategyKnown 策略名是否已注册。Service.Recovery 引用未注册的策略 = 硬错误
// （topology.go validateService 会报），**不静默当成"无策略"**——
// 那会让"声明了自动恢复"的服务实际永远不恢复，而且没人会发现。
func strategyKnown(name string) bool {
	if name == "" {
		return false
	}
	for _, s := range strategyTable() {
		if s.Name == name {
			return true
		}
	}
	return false
}

// strategyByName 按名取策略。找不到返回零值 + false（同样不静默给第一个）。
func strategyByName(name string) (RecoveryStrategy, bool) {
	for _, s := range strategyTable() {
		if s.Name == name {
			return s, true
		}
	}
	return RecoveryStrategy{}, false
}

// knownPrecheck 前置检查项是否合法。
func knownPrecheck(p string) bool {
	switch p {
	case PrecheckGroupLock, PrecheckBudget, PrecheckBreaker,
		PrecheckDeps, PrecheckAttended:
		return true
	}
	return false
}

// strategyAutoExecutable 策略当前是否允许**无人值守自动执行**。
//
// 条件（plan §4.4 晋升规则）：low 档 + 已获人工晋升。
// 人工晋升 = auto_approved 且带审批人与时间，三个字段缺一不可
// （strategyValidate 已强制；这里再判一次是因为"校验过"是一次性事实，
//
//	而"现在能不能自动跑"是每次都要问的问题）。
//
// 默认全部不自动：表里只有 restart_chain 带审批记录，因为它在 v3.0.1
// 基线里就是这么跑的（链端守护自动重启）。**把既有的生产行为记成已晋升，
// 比把它降级成影子更诚实**——后者会造成一次无声的行为回退。
func strategyAutoExecutable(s RecoveryStrategy) bool {
	if s.RiskTier != RiskLow {
		return false // high 档永远过人闸，与有没有审批无关
	}
	return s.AutoApproved &&
		strings.TrimSpace(s.ApprovedBy) != "" &&
		strings.TrimSpace(s.ApprovedAt) != ""
}

// strategyValidate 单条策略的齐备性校验（plan §4.5：六字段必须齐备）。
// 返回空切片 = 合法。消息说清缺哪个字段、策略名是什么。
func strategyValidate(s RecoveryStrategy) []string {
	var bad []string
	if s.Name == "" {
		bad = append(bad, "策略名不能为空")
		return bad
	}
	if strings.TrimSpace(s.Trigger) == "" {
		bad = append(bad, fmt.Sprintf("策略 %s 缺 trigger（触发条件）——没有触发条件的自动动作是随机动作", s.Name))
	}
	if len(s.Precheck) == 0 {
		bad = append(bad, fmt.Sprintf("策略 %s 缺 precheck（前置检查）——至少要声明组锁与预算", s.Name))
	}
	for _, p := range s.Precheck {
		if !knownPrecheck(p) {
			bad = append(bad, fmt.Sprintf("策略 %s 的 precheck 含未知项「%s」", s.Name, p))
		}
	}
	if !knownRiskTier(s.RiskTier) {
		bad = append(bad, fmt.Sprintf("策略 %s 的 risk_tier 必须是 %s 或 %s，当前「%s」",
			s.Name, RiskLow, RiskHigh, s.RiskTier))
	}
	if s.MaxAttempts < 1 {
		bad = append(bad, fmt.Sprintf("策略 %s 的 max_attempts 需 ≥1，当前 %d（0 会被当成「永不尝试」）",
			s.Name, s.MaxAttempts))
	}
	if len(s.VerifySpec.Evidence) == 0 {
		bad = append(bad, fmt.Sprintf("策略 %s 缺 verify_spec.evidence（复验要拿哪些客观证据）——"+
			"不声明就等于用「动作执行成功」冒充「环境已恢复」", s.Name))
	}
	// 回滚与风险等级的联动（plan §4.5）：
	//   · 声明了 low 却没有 compensate → 硬错误（能自动做又撤不回来，不允许出现在 low）
	//   · high 必须有人值守前置检查
	if strings.TrimSpace(s.Compensate) == "" && s.RiskTier == RiskLow {
		bad = append(bad, fmt.Sprintf("策略 %s 是 %s 档却没写 compensate——"+
			"无可回滚的动作必须升到 %s 档", s.Name, RiskLow, RiskHigh))
	}
	if s.RiskTier == RiskHigh && !hasPrecheck(s, PrecheckAttended) {
		bad = append(bad, fmt.Sprintf("策略 %s 是 %s 档却没有人值守前置检查（%s）",
			s.Name, RiskHigh, PrecheckAttended))
	}
	// 影子晋升三字段必须齐备（plan §4.4）。
	// 只凭"影子连续通过"就放行 = 把通过率当成了审批人。
	if s.AutoApproved {
		if strings.TrimSpace(s.ApprovedBy) == "" || strings.TrimSpace(s.ApprovedAt) == "" {
			bad = append(bad, fmt.Sprintf("策略 %s 标了 auto_approved 却缺 approved_by/approved_at——"+
				"自动执行必须留审批人与时间", s.Name))
		}
	}
	// Grandfathered 是逃逸口不是免审批（T2.8）：标了它就必须同样带审批三字段。
	// 否则"基线既有"会沦为一块没有审批人的免批金牌。
	if s.Grandfathered && (!s.AutoApproved ||
		strings.TrimSpace(s.ApprovedBy) == "" || strings.TrimSpace(s.ApprovedAt) == "") {
		bad = append(bad, fmt.Sprintf("策略 %s 标了 grandfathered 却缺审批三字段——"+
			"grandfathered 标注的是审批来源（基线/评审），不是免审批", s.Name))
	}
	return bad
}

// hasPrecheck 策略是否声明了某项前置检查。
func hasPrecheck(s RecoveryStrategy, item string) bool {
	for _, p := range s.Precheck {
		if p == item {
			return true
		}
	}
	return false
}

// validateStrategies 整表：逐条齐备性 + 名称唯一。
func validateStrategies(ts []RecoveryStrategy) []string {
	var bad []string
	seen := map[string]bool{}
	for _, s := range ts {
		for _, m := range strategyValidate(s) {
			bad = append(bad, m)
		}
		if seen[s.Name] {
			bad = append(bad, fmt.Sprintf("策略名重复：「%s」", s.Name))
		}
		seen[s.Name] = true
	}
	return bad
}
