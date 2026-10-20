// strategy_map.go —— 第四张表：注入类别 ↔ 产品恢复策略（T2.9，plan §4.5）
//
// 四表同源（plan §4.5 末段）：
//
//	① fault_inject.KINDS        元数据（desc / recoverable_by_ai / note）
//	② fault_inject.INJECTORS    实现
//	③ recovery_rate.INJECTIONS  判据配置（expect_autonomous / forbidden_actions）
//	④ 本表                      产品侧恢复策略
//
// 前三表在 Python 侧（tools/），本表在 Go 侧。**一致性断言横跨两边**：
// Go 侧验"映射指向的策略真实存在"，Python 侧验"映射的键是真实注入类别、
// 且每个自主恢复类别都有策略或一条带理由的豁免"。
//
// 为什么值得专门一张表：判据可以在测一个产品里根本不存在的策略——
// 那时分数照样漂亮，但它证明的不是"产品会恢复"，是"判据自己跑通了"。
package main

// injectionToStrategy 注入类别 → 产品侧恢复策略名。
//
// 多个类别共用一条策略是正常的：kill_service / crash_on_next / hang_service
// 的恢复动作都是"把服务拉起来"，差别在触发条件（策略表的 Trigger 字段），
// 不在恢复动作。
var injectionToStrategy = map[string]string{
	"kill_service":      "restart_backend",
	"crash_on_next":     "restart_backend",
	"hang_service":      "restart_backend",
	"slow_start":        "wait_slow_start",
	"hold_port":         "port_conflict",
	"fake_listen":       "port_conflict",
	"corrupt_backup":    "refresh_backup",
	"unreadable_backup": "refresh_backup",
	"db_down":           "db_restart",
}

// injectionWithoutStrategy 刻意**不**配策略的注入类别，以及理由。
//
// 为什么必须带理由："忘了配"和"刻意不配"看起来完全一样（都是一行缺失），
// 但前者是缺陷、后者是设计。不带理由的话，新增一个自主恢复类别时
// 漏配策略会被这条静默吞掉。
var injectionWithoutStrategy = map[string]string{
	"stale_log_ok":     "日志陈旧但服务健康，没有故障可恢复",
	"ok_then_die":      "时序现象（先好后坏），恢复策略无法在注入窗口内生效",
	"liar_service":     "服务端自报 503（配置/依赖级根因），守护侧无策略可修，只能如实上报",
	"delayed_refail":   "恢复后又坏是 fixture 人造缺陷（无工具可移除），正确行为是停止重试并如实上报",
	"audit_unwritable": "环境/旁路故障：产品已按 G11 降级（warn+升级不阻断业务），无恢复策略可言",
	"ai_unreachable":   "上游依赖不可用：AI 链路自身失效，正确行为是诚实报错而非任何恢复动作",
	"dual_fault":       "叠加故障中库名类根因不可修（同 db_down），整体不判自主恢复",
	"refire_service":   "崩溃循环靠重试上限与断路器截住（txbudget），守护策略只会加剧循环",
}

// strategyToInjections 反向索引：策略 → 会触发它的注入类别。
// 用于报告与影子模式评估（"这条策略在评测里被哪些场景验过"）。
func strategyToInjections() map[string][]string {
	out := map[string][]string{}
	for k, s := range injectionToStrategy {
		out[s] = append(out[s], k)
	}
	return out
}
