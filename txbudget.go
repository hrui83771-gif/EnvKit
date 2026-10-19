// txbudget.go —— v3.0 T1.5：恢复事务的两层节制（修 G5/G6）
//
// 背景（plan-v3.0 §2.4）：旧实现有五套互不相通的重试策略——
// runRetry 3 次（retry.go）、execBackground 秒退 2 次（exec.go）、
// AI 上游 2 次（ai_loop.go）、start_service 自身无上限。恢复事务因此
// 无法声明"本事务最大尝试 N 次"；也没有"整个环境在一段时间内总共能动
// 几次"的预算——崩溃循环于是变成重启循环，node0 segfault 反复离线
// 正是这个根因模式。
//
// 两层节制，分工明确：
//
//	· per-target 尝试上限（txAttemptGate）：防"同一服务反复失败反复重启"。
//	  计数唯一事实源是 svcRecordCrash 累加的 Restarts（svcstate.go），
//	  复验通过即清零（svcRecordVerify 的 ok 分支）——不另建计数器，
//	  两个计数器迟早说辞打架。
//	· 全局动作预算（txBudgetTake）：防"不同策略连环自动执行刷爆环境"。
//	  单位窗口内恢复类动作总数上限，超限整体熔断并升级。
//
// 前者回答"这个服务还能再试一次吗"，后者回答"整个环境还能再动吗"。
// 任一层拒绝即整体拒绝：恢复动作最怕的就是"每一层都觉得该别人负责"。
package main

import (
	"fmt"
	"sync"
	"time"
)

const (
	txWindowDefault      = 10 * time.Minute // 全局预算窗口默认 10 分钟
	txMaxActionsDefault  = 12               // 窗口内恢复类动作默认上限
	txMaxAttemptsDefault = 3                // per-target 连续失败默认上限（对齐 backoffSteps 第 3 档）
	txRefusedCooldown    = 5 * time.Minute  // 同一拒绝原因的升级冷却（防审计/通知刷屏）
)

// txAction 一笔恢复类动作（窗口内计数用）。
type txAction struct {
	At   time.Time
	Kind string
}

var (
	txMu        sync.Mutex
	txActions   []txAction
	txRefusedAt = map[string]time.Time{} // 每种拒绝原因的最近升级时刻
)

// txOnRefuse 拒绝时的升级动作（审计 + 通知）。
// 抽成变量是为了单测能替换掉真实通知——升级链路本身也要可测。
var txOnRefuse = func(kind, why string) {
	auditNow(actSys, "action_budget_exceeded", kind, "", resFail, why)
	alertDispatch("EnvKit 恢复动作被节制拒绝", why)
}

// txRefuseEscalate 带冷却的升级：同一原因 5 分钟内只升级一次。
// 没有冷却的话，守护循环每 30 秒拒绝一次就会把审计和通知刷爆——
// 那正是预算要防的"刷爆"，只是换了个地方。
func txRefuseEscalate(key, kind, why string) {
	txMu.Lock()
	last := txRefusedAt[key]
	now := time.Now()
	if now.Sub(last) < txRefusedCooldown {
		txMu.Unlock()
		return
	}
	txRefusedAt[key] = now
	txMu.Unlock()
	txOnRefuse(kind, why)
}

// txBudgetTake 全局动作预算：窗口内未超限则记一笔并放行；超限拒绝并升级。
// 查与记在同一把锁内完成——并发恢复场景下两个动作不能同时通过
// （各自看到"还剩一个额度"然后都放行，预算就漏了）。
func txBudgetTake(kind string) error {
	window, maxActions := txBudgetConf()
	txMu.Lock()
	now := time.Now()
	cut := now.Add(-window)
	kept := make([]txAction, 0, len(txActions)+1)
	for _, a := range txActions {
		if a.At.After(cut) {
			kept = append(kept, a)
		}
	}
	if len(kept) >= maxActions {
		why := fmt.Sprintf("全局动作预算超限：%v 内恢复类动作已达 %d 次上限（本次 %s），已整体熔断——等待窗口滑过，或人工调用 txBudgetReset 复位后重试",
			window, maxActions, kind)
		txActions = kept // 过期记录照样回收，别让窗口无限增长
		txMu.Unlock()
		txRefuseEscalate("budget:"+kind, kind, why)
		return fmt.Errorf("%s", why)
	}
	txActions = append(kept, txAction{At: now, Kind: kind})
	txMu.Unlock()
	return nil
}

// txBudgetReset 清空窗口计数（人工复位入口）。
func txBudgetReset() {
	txMu.Lock()
	txActions = nil
	txMu.Unlock()
}

// txBudgetUsed 窗口内已用的动作数（诊断与单测用）。
func txBudgetUsed() int {
	window, _ := txBudgetConf()
	txMu.Lock()
	defer txMu.Unlock()
	cut := time.Now().Add(-window)
	n := 0
	for _, a := range txActions {
		if a.At.After(cut) {
			n++
		}
	}
	return n
}

// txBudgetRemain 窗口内剩余预算（只读，不占额度）。
// 影子事务（T2.8）要评估"若执行，预算这关过不过"——
// 直接调 txBudgetTake 会真的吃掉一个额度，而影子根本不执行。
// 评估和动作必须分开记账，否则"只看不试"也会把环境试爆。
func txBudgetRemain() int {
	_, maxActions := txBudgetConf()
	remain := maxActions - txBudgetUsed()
	if remain < 0 {
		return 0
	}
	return remain
}

// txAttemptGate per-target 尝试上限：连续失败（Restarts）已达上限即拒绝。
// Restarts 由 svcRecordCrash 累加、复验通过时清零——唯一事实源，
// 这里只读不写。max_attempts<=0 用默认 3。
func txAttemptGate(target string) error {
	max := txMaxAttempts()
	used := svcRestartCount(target)
	if used < max {
		return nil
	}
	why := fmt.Sprintf("恢复尝试上限：%s 连续失败已达 %d 次（上限 %d 次），停止自动重试——%s",
		target, used, max, svcBackoff(target))
	txRefuseEscalate("attempts:"+target, "tx_attempts_"+target, why)
	return fmt.Errorf("%s", why)
}

// txRecoveryGate 恢复动作入口的两层节制（AI 工具与守护循环共用）。
// 顺序：先断路器（v3.0 T1.9，熔断态整个动作直接拒，且拒绝时升级），
// 再 per-target 上限（只读、免费），后全局预算（占额度）——
// 反正要被上限拒的动作，不该消耗预算；熔断中的环境，连"读计数决定
// 要不要试"都不必做。
func txRecoveryGate(kind string, targets ...string) error {
	for _, t := range targets {
		if err := svcBreakerGate(t, kind); err != nil {
			return err
		}
		if err := txAttemptGate(t); err != nil {
			return err
		}
	}
	return txBudgetTake(kind)
}

// ---------- 参数（零值 = 内置默认） ----------

func txBudgetConf() (time.Duration, int) {
	cfgMu.Lock()
	r := cfg.Recovery
	cfgMu.Unlock()
	window, maxActions := txWindowDefault, txMaxActionsDefault
	if r.WindowSecs > 0 {
		window = time.Duration(r.WindowSecs) * time.Second
	}
	if r.MaxActions > 0 {
		maxActions = r.MaxActions
	}
	return window, maxActions
}

func txMaxAttempts() int {
	cfgMu.Lock()
	n := cfg.Recovery.MaxAttempts
	cfgMu.Unlock()
	if n <= 0 {
		return txMaxAttemptsDefault
	}
	return n
}
