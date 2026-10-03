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

// ===== 崩溃记录 =====

// svcRecordCrash 记一次崩溃（启动后退出 / 复验判失败）。
func svcRecordCrash(target, errKind, why string, port int) {
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
	})
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
	svcWith(target, func(r *svcRuntimeRec) {
		r.Verified = ok
		r.Conclusion = conclusion
		r.ErrKind = errKind
		r.VerifyAt = time.Now().Format("2006-01-02 15:04:05")
		if ok {
			r.Restarts = 0
			r.LastErr = ""
			r.Backoff = ""
		}
	})
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
func svcReset(target string) {
	svcRT.Lock()
	delete(svcRT.m, target)
	svcRT.Unlock()
}
