package main

// svcstate.go —— v2.2 M12：服务运行时注册表
//
// ## 为什么要有这个
//
// M10（端口漂移）与 M11（复验结论）各自加了一个包级 map，
// 继续加崩溃计数就会变成三个分散的 map —— 这是典型的坏味道：
// 状态没法一次快照、清理要分别调用、并发时容易漏。
//
// 这里把它们合并成**一个注册表**，每服务一条记录。
// 外部函数名保持不变（svcRememberPort / svcLastVerify / …），
// 既有测试零改动，内部实现统一。
//
// ## 只记录，不自动重启
//
// 本项刻意**不做崩溃自动重启**：
//
//   - 自动重启是"替用户做决定"。用户正在调试时反复重启会直接干扰排查；
//   - 配置错误导致的崩溃，重启一万次也没用，只会把日志刷爆；
//   - 停止服务后守护又把它拉起来，与用户意图直接冲突。
//
// 真正该做的是**把状态说清楚**：崩了几次、错误是什么、下次重启该等多久。
// 要不要重启、什么时候重启，由人决定。这是 v2.2 的定位——让状态可见，
// 而不是替用户自动修复。自动重启若要做，应是 v2.3 之后的可配置项。
//
// ## 崩溃历史只留内存
//
// 落盘会与审计重复（审计里已经有 start_service 的 fail 记录与 verify 结论）。
// 这里的价值是**聚合**：把散落在审计里的失败收敛成"这个服务今天崩了 3 次"，
// 这是审计做不到的——审计是流水，不做统计。

import (
	"fmt"
	"sync"
	"time"
)

// CrashEvent 一次崩溃记录。
type CrashEvent struct {
	At      string `json:"at"`
	ErrKind string `json:"err_kind"`
	Why     string `json:"why"`
	Port    int    `json:"port,omitempty"`
}

// svcRuntimeRec 单个服务的运行时记录。
type svcRuntimeRec struct {
	// Port 最近一次验证通过的端口（漂移检测用）
	Port int
	// Verify 复验结论
	Verified   bool
	Conclusion string
	ErrKind    string
	VerifyAt   string
	// 崩溃
	Restarts  int
	LastErr   string
	LastErrAt string
	Backoff   string // 建议退避时长（基于连续失败次数）
	Crashes   []CrashEvent
	LastEvid  []string // 最近一次复验的证据
	StartedAt string   // 本轮启动时间

	// v3.0 T1.9：崩溃循环断路器（plan §3.2.3）。
	// backoffSteps 原本只是建议表、无强制消费方——连续失败到限后 AI 照样
	// 重启、守护照样拉、秒退照样重试，node0 segfault 反复离线就是这么来的。
	// 现在达限即进熔断态：拒绝一切自动重启，出口只有三个——
	// 人工复位 / 冷静期后一次探针 / 复验通过。
	BreakerTripped   bool      // 熔断中
	BreakerTrippedAt time.Time // 本次熔断起点（冷静期从这算）
	BreakerHalfOpen  bool      // 冷静期已过、放行过探针（探针失败即再熔断）
}

// svcMaxCrashEvents 单服务保留的崩溃历史条数。
const svcMaxCrashEvents = 10

// backoffSteps 退避建议表。连续失败越多，越该等——但要有上限，
// 否则"等服务自己好"会变成永远等下去。
var backoffSteps = []struct {
	after int
	wait  time.Duration
	why   string
}{
	{1, 0, "首次失败：直接重启即可，多半是偶发（端口瞬时占用、依赖刚起好）"},
	{2, 3 * time.Second, "连续第 2 次失败：等 3 秒再试，可能是端口尚未释放"},
	{3, 10 * time.Second, "连续第 3 次失败：等 10 秒；仍失败说明不是偶发，先看日志再重启"},
	{4, 30 * time.Second, "连续第 4 次失败：等 30 秒；反复重启只会刷屏，优先排查根因"},
	{5, 120 * time.Second, "连续 5 次以上失败：停止盲目重启，去「程序启动」日志看崩溃原因"},
}

var svcRT = struct {
	sync.Mutex
	m map[string]*svcRuntimeRec
}{m: map[string]*svcRuntimeRec{}}

// svcRec 取记录（不存在则创建）。调用方必须已持有锁。
func svcRecLocked(target string) *svcRuntimeRec {
	r := svcRT.m[target]
	if r == nil {
		r = &svcRuntimeRec{}
		svcRT.m[target] = r
	}
	return r
}

func svcWith(target string, fn func(r *svcRuntimeRec)) {
	svcRT.Lock()
	defer svcRT.Unlock()
	fn(svcRecLocked(target))
}

func svcSnapshot(target string) svcRuntimeRec {
	svcRT.Lock()
	defer svcRT.Unlock()
	if r := svcRT.m[target]; r != nil {
		cp := *r
		cp.Crashes = append([]CrashEvent(nil), r.Crashes...)
		cp.LastEvid = append([]string(nil), r.LastEvid...)
		return cp
	}
	return svcRuntimeRec{}
}

// svcAllRecords 供诊断与状态中心快照使用。
func svcAllRecords() map[string]svcRuntimeRec {
	svcRT.Lock()
	defer svcRT.Unlock()
	out := make(map[string]svcRuntimeRec, len(svcRT.m))
	for k, v := range svcRT.m {
		cp := *v
		cp.Crashes = append([]CrashEvent(nil), v.Crashes...)
		cp.LastEvid = append([]string(nil), v.LastEvid...)
		out[k] = cp
	}
	return out
}

// ===== 崩溃循环断路器（v3.0 T1.9，plan §3.2.3） =====
//
// 三态状态机（closed → open → half-open → closed/open）：
//
//	closed    正常。Restarts 未达限，自动重启受常规节制（txRecoveryGate）。
//	open      熔断。连续失败达限进入：拒绝一切自动重启（AI/守护/秒退重试），
//	          出口只有人工复位（svcBreakerReset）或冷静期自然结束。
//	half-open 冷静期过后放行一次探针：Restarts 回退到限值-1，让这次重启
//	          能过 txAttemptGate（否则"冷静期自然解除"是死代码——
//	          txAttemptGate 看到 Restarts 仍达限会永远拒下去）。
//	          探针复验通过 → closed；探针又崩 → 立即再熔断（冷静期重新起算）。
//
// 每次状态迁移都进审计 + 通知（svcBreakerEscalate）——熔断这个决定本身
// 必须可追溯，否则"为什么重启被拒了"永远说不清。

const (
	breakerAfterDefault    = 3               // 连续失败达限进熔断（对齐 backoffSteps 第 3 档）
	breakerCooldownDefault = 5 * time.Minute // 熔断冷静期
)

// breakerAfter 连续失败多少次进熔断（配置可覆盖，0 = 默认 3）。
func breakerAfter() int {
	cfgMu.Lock()
	n := cfg.Recovery.BreakerAfter
	cfgMu.Unlock()
	if n <= 0 {
		return breakerAfterDefault
	}
	return n
}

// breakerCooldown 熔断冷静期（配置可覆盖，0 = 默认 5 分钟）。
func breakerCooldown() time.Duration {
	cfgMu.Lock()
	secs := cfg.Recovery.BreakerCooldownSecs
	cfgMu.Unlock()
	if secs <= 0 {
		return breakerCooldownDefault
	}
	return time.Duration(secs) * time.Second
}

// svcBreakerNotify 断路器迁移的通知出口。抽成变量是为了单测能替换掉
// 真实通知——升级链路本身也要可测（与 txOnRefuse 同一立场）。
var svcBreakerNotify = func(title, why string) { alertDispatch(title, why) }

// svcBreakerEscalate 状态迁移的落点：审计 + 日志 + 通知，三处都要留痕。
func svcBreakerEscalate(target, action, result, why string) {
	auditNow(actSys, action, target, "", result, why)
	warn(scSys, "断路器", "%s：%s", target, why)
	svcBreakerNotify("EnvKit 崩溃断路器", target+" "+why)
}

// svcBreakerGate 恢复类动作（重启）过断路器。拒绝时错误里说清三件事：
// 现在什么态、还剩多久、怎么出去——否则调用方（AI/守护）只会看到一句
// "被拒绝"，然后换个写法再试，那就白熔断了。
func svcBreakerGate(target, kind string) error {
	after, cooldown := breakerAfter(), breakerCooldown()
	var transition string
	var restarts int
	var remain time.Duration

	svcRT.Lock()
	r := svcRecLocked(target)
	now := time.Now()
	restarts = r.Restarts
	switch {
	case !r.BreakerTripped && restarts >= after:
		// 达限即熔断。这里也兜底一次：正常路径下 svcRecordCrash 在崩溃
		// 当时就熔断了，走到这里说明限值被配置调小过，状态机要自己修正。
		r.BreakerTripped, r.BreakerTrippedAt, r.BreakerHalfOpen = true, now, false
		transition = "trip"
	case r.BreakerTripped && now.Sub(r.BreakerTrippedAt) >= cooldown:
		// 冷静期已过：放行一次探针。Restarts 回退到限值-1——
		// 不清的话 txAttemptGate 会以"Restarts 仍达限"永远拒下去，
		// "冷静期自然解除"就成了纸面出口。
		r.BreakerTripped, r.BreakerHalfOpen = false, true
		r.Restarts = after - 1
		transition = "halfopen"
	}
	if r.BreakerTripped {
		remain = cooldown - now.Sub(r.BreakerTrippedAt)
		if remain < 0 {
			remain = 0
		}
	}
	svcRT.Unlock()

	switch transition {
	case "trip":
		svcBreakerEscalate(target, "breaker_tripped", resFail, fmt.Sprintf(
			"连续失败 %d 次达限（本次动作 %s），进入熔断态：拒绝一切自动重启，冷静期 %v 后放行一次探针，或人工复位",
			restarts, kind, cooldown))
		return fmt.Errorf("%s已进入熔断态（连续失败 %d 次）：拒绝自动重启——先看「程序启动」日志里的崩溃原因，%v 后放行一次探针重启，或人工复位断路器",
			target, restarts, cooldown)
	case "halfopen":
		svcBreakerEscalate(target, "breaker_halfopen", resOK, fmt.Sprintf(
			"熔断冷静期 %v 已过，放行一次探针重启（%s）：探针复验通过即解除熔断，再崩立即重新熔断", cooldown, kind))
		return nil
	}
	if remain > 0 {
		return fmt.Errorf("%s处于熔断态（剩余冷静期 %v）：拒绝自动重启——等待冷静期结束放行探针，或人工复位断路器",
			target, remain.Round(time.Second))
	}
	return nil
}

// svcBreakerTripped 快速查询：是否处于熔断态（exec.go 秒退重试等
// 不需要完整 gate 语义的点用；完整判定走 svcBreakerGate）。
func svcBreakerTripped(target string) bool { return svcSnapshot(target).BreakerTripped }

// svcBreakerStatus 断路器状态（诊断与单测）：熔断中 / 剩余冷静期 / 连续失败数。
func svcBreakerStatus(target string) (tripped bool, remain time.Duration, restarts int) {
	rec := svcSnapshot(target)
	if !rec.BreakerTripped {
		return false, 0, rec.Restarts
	}
	remain = breakerCooldown() - time.Since(rec.BreakerTrippedAt)
	if remain < 0 {
		remain = 0
	}
	return true, remain, rec.Restarts
}

// svcBreakerReset 人工复位断路器：清熔断态并清零连续失败计数。
// 为什么连计数一起清：只清状态不清计数的话，下一次 gate 调用会因
// "Restarts 仍达限"立刻再熔断——复位成了空操作（这个坑被
// TestBreakerManualReset 抓到了）。出口必须是真出口。
// 防滥用的设计在入口而非这里：本函数的调用方只有人工 HTTP 端点
// （/api/program/breaker-reset），AI 工具表里没有 reset——AI 被熔断
// 拒绝时该做的是升级给人，而不是自己拆保险。全局动作预算（窗口计数）
// 不随复位清空，那一层防的是"整个环境被刷爆"，与人工复位无关。
func svcBreakerReset(target string) {
	var was bool
	svcWith(target, func(r *svcRuntimeRec) {
		was = r.BreakerTripped || r.BreakerHalfOpen
		r.BreakerTripped, r.BreakerHalfOpen = false, false
		r.Restarts = 0
		r.Backoff = ""
	})
	if was {
		svcBreakerEscalate(target, "breaker_reset", resOK, "人工复位断路器：熔断态解除、连续失败计数清零（全局动作预算不受影响）")
	}
}

// ===== 崩溃记录 =====

// svcRecordCrash 记一次崩溃（启动后退出 / 复验判失败）。
func svcRecordCrash(target, errKind, why string, port int) {
	var transition string
	var restarts int
	svcWith(target, func(r *svcRuntimeRec) {
		r.Restarts++
		r.LastErr = why
		r.LastErrAt = time.Now().Format("2006-01-02 15:04:05")
		r.Backoff = backoffAdvice(r.Restarts)
		r.Crashes = append(r.Crashes, CrashEvent{
			At: r.LastErrAt, ErrKind: errKind, Why: why, Port: port,
		})
		if len(r.Crashes) > svcMaxCrashEvents {
			r.Crashes = r.Crashes[len(r.Crashes)-svcMaxCrashEvents:]
		}
		// 崩溃后复验结论作废：服务已经不在了，"已复验通过"是历史而非现状
		r.Verified = false
		r.Conclusion = ""
		r.ErrKind = errKind
		// v3.0 T1.9：断路器。两种熔断时机：
		//   · 探针又崩（half-open 期间）→ 立即再熔断，冷静期重新起算；
		//   · 连续失败达限 → 在这里熔断，不等某个 gate 调用来发现
		//     （verifyService 等路径直接记崩溃、不过 gate，"没被 gate
		//     看到的崩溃"永远不进状态机的话，断路器就是个半聋的装置）。
		restarts = r.Restarts
		if (r.BreakerHalfOpen || restarts >= breakerAfter()) && !r.BreakerTripped {
			r.BreakerTripped, r.BreakerTrippedAt, r.BreakerHalfOpen = true, time.Now(), false
			transition = "trip"
		}
	})
	if transition == "trip" {
		svcBreakerEscalate(target, "breaker_tripped", resFail, fmt.Sprintf(
			"连续失败 %d 次达限，进入熔断态：拒绝一切自动重启（AI/守护/秒退重试），冷静期 %v 后放行一次探针，或人工复位",
			restarts, breakerCooldown()))
	}
}

// backoffAdvice 按连续失败次数给出退避建议。
func backoffAdvice(fails int) string {
	for _, s := range backoffSteps {
		if fails <= s.after {
			return fmt.Sprintf("连续失败 %d 次 → %s", fails, s.why)
		}
	}
	last := backoffSteps[len(backoffSteps)-1]
	return fmt.Sprintf("连续失败 %d 次 → %s", fails, last.why)
}

// svcRestartCount 连续失败次数。
func svcRestartCount(target string) int { return svcSnapshot(target).Restarts }

// svcBackoff 取退避建议。
func svcBackoff(target string) string { return svcSnapshot(target).Backoff }

// svcCrashes 取崩溃历史（新在前）。
func svcCrashes(target string) []CrashEvent {
	rec := svcSnapshot(target)
	out := make([]CrashEvent, 0, len(rec.Crashes))
	for i := len(rec.Crashes) - 1; i >= 0; i-- {
		out = append(out, rec.Crashes[i])
	}
	return out
}

// ===== 端口漂移（M10 迁入，函数名不变） =====

// svcRememberPort 记住验证通过的端口，返回上一次的值（无则 0）。
func svcRememberPort(target string, port int) int {
	svcRT.Lock()
	defer svcRT.Unlock()
	r := svcRecLocked(target)
	prev := r.Port
	r.Port = port
	return prev
}

// svcPortOf 最近一次验证通过的端口。
func svcPortOf(target string) (int, bool) {
	svcRT.Lock()
	defer svcRT.Unlock()
	r := svcRT.m[target]
	if r == nil || r.Port <= 0 {
		return 0, false
	}
	return r.Port, true
}

// svcForgetPort 停服务时清掉端口记录，避免下次重启拿旧端口做漂移比对。
func svcForgetPort(target string) { svcWith(target, func(r *svcRuntimeRec) { r.Port = 0 }) }

// ===== 复验结论（M11 迁入，函数名不变） =====

// svcRecordVerify 记录一次复验结论。
func svcRecordVerify(target, conclusion, errKind string, ok bool) {
	var transition string
	svcWith(target, func(r *svcRuntimeRec) {
		r.Verified = ok
		r.Conclusion = conclusion
		r.ErrKind = errKind
		r.VerifyAt = time.Now().Format("2006-01-02 15:04:05")
		if ok {
			r.Restarts = 0
			r.LastErr = ""
			r.Backoff = ""
			// v3.0 T1.9：复验通过 = 服务真的活了，熔断态随之解除
			//（含 half-open 探针成功）。计数清零与状态清零必须同时——
			// 只清计数不清状态，熔断会挂到永远；只清状态不清计数，
			// txAttemptGate 立刻再把它拒一次，两个机制说辞打架。
			if r.BreakerTripped || r.BreakerHalfOpen {
				r.BreakerTripped, r.BreakerHalfOpen = false, false
				transition = "recover"
			}
		}
	})
	if transition == "recover" {
		svcBreakerEscalate(target, "breaker_recovered", resOK, "复验通过，熔断解除：服务已客观恢复，连续失败计数清零")
	}
}

// svcLastVerify 取最近一次复验结论。
func svcLastVerify(target string) string { return svcSnapshot(target).Conclusion }

// svcLastVerifyAt 复验时间。
func svcLastVerifyAt(target string) string { return svcSnapshot(target).VerifyAt }

// svcLastVerifyErr 复验的错误归类与是否通过。
func svcLastVerifyErr(target string) (string, bool) {
	rec := svcSnapshot(target)
	return rec.ErrKind, rec.Verified
}

// svcForgetVerify 清掉复验记录。
func svcForgetVerify(target string) {
	svcWith(target, func(r *svcRuntimeRec) {
		r.Verified, r.Conclusion, r.ErrKind, r.VerifyAt = false, "", "", ""
	})
}

// ===== 统一清理 =====

// svcReset 停服务时调用：清掉该服务全部运行时记录。
// 崩溃历史也一并清——服务停了就不再处于"崩溃中"，留着会误导。
// 断路器状态随记录一起清：人工停服务本身就是一次显式复位。
func svcReset(target string) {
	svcRT.Lock()
	delete(svcRT.m, target)
	svcRT.Unlock()
}
