// shadow.go —— 影子模式与晋升规则（v3.0 第二阶段 T2.8，plan §4.4）
//
// 要解决什么：一条新恢复策略，没有证据就不能自动执行；但"不执行"
// 不等于"不评估"。影子事务把 Observe→Plan 完整走一遍（含风险分级与
// 前置检查），**在 Execute 前停止**，记录三件事：
//
//	若执行会做什么（命令，已过 R1 安全规则）
//	预期复验规范（恢复后拿哪些客观证据说话）
//	评估结论（计划可生成吗、前置检查过得了吗）
//
// 晋升规则（plan §4.4）：同一策略**连续 N 次（默认 20）**影子评估通过
// **且**有人工审批字段（auto_approved + approved_by + approved_at），
// 两个条件缺一不可——
//
//	只凭通过率放行 = 把通过率当成了审批人；
//	只凭签名放行   = 把签名当成了证据。
//
// 唯一的显式逃逸口是 Grandfathered（基线既有行为的人工标注，
// 见 strategy.go 的 restart_chain）——它本身也受 strategyValidate
// 约束：标了 grandfathered 就必须带审批三字段，它不是免审批。
//
// **影子评估的"通过"不是"恢复成功"**：影子从不执行，所以无从恢复。
// Pass 的含义是"若批准，这个计划大概率能执行且前置检查能过"。
// 记录里 StoppedBefore=execute 把这件事钉死，防止读报告的人
// 把 20 次通过读成 20 次恢复成功——那正是本项目反复批的
// "把执行当成恢复"。
//
// 落点说明（与 plan §4.6 任务表文件列的差异）：任务表写的是
// ai_loop.go/ai_confirm.go，那是动守护循环之前写的。拓扑无关化
// （T2.6）之后，**守护循环是唯一的无人值守执行点**；AI 路径的
// 每个动作都过人确认闸门，不需要影子。所以影子事务住在 guard.go
// 的恢复分支前，记录流在本文件。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// shadowPassNeed 晋升所需的连续影子评估通过次数（plan §4.4 默认 20）。
// 抽成 var 是为了单测能改小——晋升规则的行为不该依赖"真跑 20 轮故障"。
var shadowPassNeed = 20

// shadowRetentionDays 影子记录保留天数。与审计一致：30 天。
// 影子流是晋升证据，证据也有保鲜期——无限增长的文件本身会成为事故。
const shadowRetentionDays = 30

// ShadowVerify 预期复验规范：晋升评审时要看"恢复后拿什么证据说话"。
// 不用他类的判据冒充（链端看 block_height，备份看 file+size+sha256）。
type ShadowVerify struct {
	Target   string   `json:"target"`
	Evidence []string `json:"evidence,omitempty"`
	WaitSecs int      `json:"wait_secs"`
}

// ShadowRecord 一次影子评估的完整记录。
// 这份结构同时进审计流（actor=shadow）与 shadow-<day>.jsonl——
// 审计给人翻，JSONL 给聚合报告（docs/eval/shadow-*.json）与晋升计数用。
type ShadowRecord struct {
	Ts       string `json:"ts"`       // 2006-01-02 15:04:05.000
	Service  string `json:"service"`  // service id
	Label    string `json:"label"`    // 人类可读名
	Strategy string `json:"strategy"` // 策略名

	// Trigger 探针失败态（ Observation.ErrKind）。
	Trigger string `json:"trigger"`
	// StrategyTrigger 策略声明的触发条件原文，与 Trigger 并排放——
	// 晋升评审时要能一眼比对"这次失败是不是这个策略该管的那个失败"。
	// 策略触发条件与观测失败态的匹配是自由文本，不做机器判定
	// （词表路线没有终点，本项目已决议不走），并排给人看。
	StrategyTrigger string   `json:"strategy_trigger,omitempty"`
	Evidence        []string `json:"evidence,omitempty"` // 探针证据（客观事实）

	// 若执行会做什么。planErr != nil 时 WouldCommand 为空。
	WouldAction  string `json:"would_action"`
	WouldCommand string `json:"would_command,omitempty"`

	RiskTier       string       `json:"risk_tier"`          // 风险分级（来自策略）
	Precheck       []string     `json:"precheck,omitempty"` // 前置检查逐项结论
	ExpectedVerify ShadowVerify `json:"expected_verify"`    // 预期复验规范

	Conclusion string `json:"conclusion"` // 评估结论（人读）
	// Pass 本次影子评估是否通过：计划可生成且前置检查可过。
	// **不是"恢复成功"**——影子不执行，无从恢复（见文件头）。
	Pass bool `json:"pass"`
	// Blockers 未通过的原因（晋升计数一次不过即清零，原因必须可查）。
	Blockers []string `json:"blockers,omitempty"`
	// StoppedBefore 影子事务停在哪里。恒为 "execute"——
	// 它是给读记录的人的硬事实：这条记录里没有任何动作被执行。
	StoppedBefore string `json:"stopped_before"`
}

// shadowConclude 纯函数：影子事务的评估结论。
// 拆成纯函数是为了把"计划不可用 / 前置检查不过 / 可晋升"三种结论
// 逐条钉在单测里——结论文字是给人看的，判断逻辑必须可证。
func shadowConclude(plan Plan, planErr error, precheckBad []string) (conclusion string, pass bool, blockers []string) {
	if planErr != nil {
		return fmt.Sprintf("恢复计划无法生成（%v）——计划不可用即不可晋升，未进入执行",
			planErr), false, []string{planErr.Error()}
	}
	if len(precheckBad) > 0 {
		return "前置检查未通过：" + strings.Join(precheckBad, "；") + "——未进入执行",
			false, append([]string{}, precheckBad...)
	}
	return fmt.Sprintf("计划可生成（%s）且前置检查可过；已在 Execute 前停止，未执行任何动作",
		plan.Command), true, nil
}

// shadowRecordOf 组装一条影子记录（纯函数，无 IO）。
func shadowRecordOf(svc Service, strat RecoveryStrategy, obs Observation,
	plan Plan, planErr error, precheckLines, precheckBad []string) ShadowRecord {
	conclusion, pass, blockers := shadowConclude(plan, planErr, precheckBad)
	rec := ShadowRecord{
		Ts:              time.Now().Format("2006-01-02 15:04:05.000"),
		Service:         svc.ID,
		Label:           svc.Label,
		Strategy:        strat.Name,
		Trigger:         obs.ErrKind,
		StrategyTrigger: strat.Trigger,
		Evidence:        obs.Evidence,
		RiskTier:        strat.RiskTier,
		Precheck:        precheckLines,
		Conclusion:      conclusion,
		Pass:            pass,
		Blockers:        blockers,
		StoppedBefore:   "execute",
	}
	target := strat.VerifySpec.Target
	if strings.TrimSpace(target) == "" {
		target = verifyTargetOf(svc)
	}
	rec.ExpectedVerify = ShadowVerify{
		Target:   target,
		Evidence: strat.VerifySpec.Evidence,
		WaitSecs: strat.VerifySpec.WaitSecs,
	}
	if planErr == nil {
		rec.WouldAction = plan.Action
		rec.WouldCommand = plan.Command
	} else {
		rec.WouldAction = "restart"
	}
	return rec
}

// shadowAuditDetail 审计详情：若执行会做什么 + 预期复验 + 结论。
// 一条 Detail 说清三件事，翻审计的人不用再去翻 JSONL。
// （auditSanitize 会把超长部分截断并标注——完整版在 shadow-<day>.jsonl。）
func shadowAuditDetail(rec ShadowRecord) string {
	var b strings.Builder
	if rec.WouldCommand != "" {
		b.WriteString("若执行会做什么：" + rec.WouldAction + " " + rec.WouldCommand + "；")
	} else {
		b.WriteString("若执行会做什么：计划无法生成；")
	}
	fmt.Fprintf(&b, "预期复验：%s[%s] 等待≤%ds；结论：%s",
		rec.ExpectedVerify.Target, strings.Join(rec.ExpectedVerify.Evidence, ","),
		rec.ExpectedVerify.WaitSecs, rec.Conclusion)
	return b.String()
}

// shadowMarkDetail 守护状态卡的简短描述（firstLines 会再截一次）。
func shadowMarkDetail(rec ShadowRecord) string {
	return fmt.Sprintf("%s：影子评估（未执行）%s", rec.Label, rec.Conclusion)
}

// shadowPrechecks 评估策略声明的前置检查。**只读，不消费任何配额**：
// 影子事务不占预算、不熔断、不动断路器——它是一个评估，不是一个动作。
//
//	group_lock  组锁：能走到这里就是拿到了（guardOne 持锁中）
//	budget      全局动作预算余量（txBudgetRemain 只读）
//	breaker     崩溃断路器状态（svcBreakerTripped 只读）
//	deps        依赖健康：关联器已放行 = 本轮无上游失败
//	attended    有人值守：影子不执行，人工审阅影子记录即为值守
func shadowPrechecks(svc Service, strat RecoveryStrategy) (lines, bad []string) {
	target := svc.Guard.auditTargetOf(svc.ID)
	for _, p := range strat.Precheck {
		switch p {
		case PrecheckGroupLock:
			lines = append(lines, "group_lock=已持有")
		case PrecheckBudget:
			remain := txBudgetRemain()
			if remain > 0 {
				lines = append(lines, fmt.Sprintf("budget=余量 %d 次", remain))
			} else {
				lines = append(lines, "budget=余量 0")
				bad = append(bad, "全局动作预算已耗尽（窗口内恢复类动作达上限）")
			}
		case PrecheckBreaker:
			if svcBreakerTripped(target) {
				lines = append(lines, "breaker=已熔断")
				bad = append(bad, "崩溃断路器已熔断（该目标连续失败达上限）")
			} else {
				lines = append(lines, "breaker=未熔断")
			}
		case PrecheckDeps:
			lines = append(lines, "deps=本轮无上游失败（关联器已放行）")
		case PrecheckAttended:
			lines = append(lines, "attended=影子未执行，人工审阅影子记录即为值守")
		default:
			// 未知项：不静默当成通过。strategyValidate 本就拒绝未知项，
			// 这里是运行期兜底——**判定输入里出现不认识的东西必须显式**。
			lines = append(lines, p+"=未评估（未知前置检查项）")
			bad = append(bad, "未知前置检查项："+p)
		}
	}
	return lines, bad
}

// ---------- 记录流：shadow-<day>.jsonl ----------

var (
	shadowMu sync.Mutex
	// shadowDir 影子流目录。抽成 var 与 auditDir 同一理由：
	// 单测在临时目录里造数据，不往用户真实目录写测试记录。
	shadowDir = auditDir
)

// shadowPath 当天的影子流文件。与审计文件同目录、同命名风格、
// 同 30 天保留——它俩是同一种东西：只追加的事实流。
func shadowPath() string {
	return filepath.Join(shadowDir(), "shadow-"+time.Now().Format("20060102")+".jsonl")
}

// shadowEmit 追加一条影子记录。
// 写失败只告警不阻断：审计流（actor=shadow）是主记录，
// JSONL 是给聚合与晋升计数用的第二份——第二份丢了
// 不能把第一份也搭进去（与 T1.6 审计写失败的口径一致）。
func shadowEmit(rec ShadowRecord) {
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	shadowMu.Lock()
	defer shadowMu.Unlock()
	f, err := os.OpenFile(shadowPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		warn(scSys, "影子", "影子记录文件无法写入（%v）：%s", err, shadowPath())
		return
	}
	defer f.Close()
	if _, werr := f.WriteString(string(b) + "\n"); werr != nil {
		warn(scSys, "影子", "影子记录写入失败（%v），本条丢失：%s", werr, shadowPath())
	}
}

// shadowDays 列出有影子记录的日期，倒序（新在前）。与 auditDays 同构。
func shadowDays() []string {
	entries, err := os.ReadDir(shadowDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "shadow-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		if d := strings.TrimSuffix(strings.TrimPrefix(name, "shadow-"), ".jsonl"); len(d) == 8 {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}

// shadowReadAll 按时间正序读全部影子记录。
// "连续 N 次"必须看全历史——只读当天的话，跨过午夜的连续会断档，
// 而断档方向是"永远晋升不了"（把对判成错的那种坏）。
func shadowReadAll() []ShadowRecord {
	days := shadowDays()
	// days 是倒序（新在前），反转为正序（旧→新）
	for i, j := 0, len(days)-1; i < j; i, j = i+1, j-1 {
		days[i], days[j] = days[j], days[i]
	}
	var out []ShadowRecord
	for _, d := range days {
		b, err := os.ReadFile(filepath.Join(shadowDir(), "shadow-"+d+".jsonl"))
		if err != nil {
			continue // 单天读不出不该让整个晋升计数瘫掉
		}
		for _, ln := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(ln) == "" {
				continue
			}
			var r ShadowRecord
			if json.Unmarshal([]byte(ln), &r) == nil {
				out = append(out, r)
			}
		}
	}
	return out
}

// shadowPassesOf 某策略当前**连续**影子评估通过次数。
// 一次不过即清零："20 次里混 1 次失败"说明策略还没稳定，
// 不算够——这是 plan §4.4 "连续 N 次"的字面要求。
func shadowPassesOf(strategy string) int {
	n := 0
	for _, r := range shadowReadAll() {
		if r.Strategy != strategy {
			continue
		}
		if r.Pass {
			n++
		} else {
			n = 0
		}
	}
	return n
}

// cleanupOldShadows 清理超过保留期的影子文件（与审计同策略）。
func cleanupOldShadows() {
	dir := shadowDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -shadowRetentionDays)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "shadow-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		datePart := strings.TrimSuffix(strings.TrimPrefix(name, "shadow-"), ".jsonl")
		t, perr := time.Parse("20060102", datePart)
		if perr != nil || !t.Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// ---------- 晋升规则 ----------

// 晋升路线取值（PromotionReadiness.Route）：
const (
	RouteGrandfathered = "grandfathered" // 基线既有行为的人工标注（非影子晋升）
	RoutePromoted      = "promoted"      // 已晋升：审批字段齐备
	RouteReady         = "ready"         // 证据齐备（连续 N 次通过），只差人工签字
	RoutePending       = "pending"       // 证据不足，继续影子积累
)

// PromotionReadiness 一条策略的晋升就绪度。
type PromotionReadiness struct {
	Strategy string `json:"strategy"`
	Route    string `json:"route"` // grandfathered / promoted / ready / pending

	Passes int `json:"passes"` // 当前连续影子通过次数
	Need   int `json:"need"`   // 晋升所需次数

	Approved   bool   `json:"approved"` // 人工审批三字段是否齐备
	ApprovedBy string `json:"approved_by,omitempty"`
	ApprovedAt string `json:"approved_at,omitempty"`

	// AutoExecutable 守护循环当前是否允许无人值守执行它
	// （strategyAutoExecutable 的结论，原样带出，避免两处口径）。
	AutoExecutable bool `json:"auto_executable"`

	// Eligible 影子晋升路线的两个条件是否都满足。
	// grandfathered 策略恒为 true——它的"满足"来自基线审批记录，
	// 路线标注在 Route 里，不靠 Eligible 区分。
	Eligible bool `json:"eligible"`

	// Blockers 还缺什么。空 = 无事可做。
	Blockers []string `json:"blockers,omitempty"`
}

// promotionReadiness 一条策略的影子晋升就绪度（plan §4.4）。
//
// **两个条件缺一不可**：
//
//	① 连续 N 次影子评估通过（策略被证据反复验过"可规划"）；
//	② 人工审批字段齐备（auto_approved + approved_by + approved_at）。
//
// 静态表无法校验运行期的通过数——连续 N 次的核验在这里发生
// （读影子流），"缺一不可"由 Eligible 的断言保证（见 shadow_test.go）。
func promotionReadiness(name string) PromotionReadiness {
	r := PromotionReadiness{Strategy: name, Need: shadowPassNeed, Route: RoutePending}
	s, ok := strategyByName(name)
	if !ok {
		r.Blockers = []string{"策略未注册：" + name}
		return r
	}
	r.Passes = shadowPassesOf(name)
	r.Approved = s.AutoApproved &&
		strings.TrimSpace(s.ApprovedBy) != "" && strings.TrimSpace(s.ApprovedAt) != ""
	r.ApprovedBy, r.ApprovedAt = s.ApprovedBy, s.ApprovedAt
	r.AutoExecutable = strategyAutoExecutable(s)
	switch {
	case s.Grandfathered:
		r.Route = RouteGrandfathered
		r.Eligible = true
	case r.Approved:
		r.Route = RoutePromoted
		r.Eligible = true
	case r.Passes >= r.Need:
		r.Route = RouteReady
		r.Blockers = []string{"缺人工审批（需 auto_approved=true 且填 approved_by 与 approved_at）"}
	default:
		r.Blockers = []string{
			fmt.Sprintf("连续影子通过 %d/%d", r.Passes, r.Need),
			"缺人工审批（需 auto_approved=true 且填 approved_by 与 approved_at）",
		}
	}
	return r
}

// shadowEvidenceOK 守护循环执行前的晋升核验：这条策略有没有
// 无人值守执行的**证据**。
//
// 两条路：grandfathered（显式标注的基线/代码评审审批，逃逸口），
// 或影子路线（连续 N 次通过）。**只凭签名没有证据的策略拒绝执行**——
// 那一行 auto_approved=true 是个人决定，不是证据；而逃逸口必须
// 显式标注，让它在代码评审与静态检查里可见（strategyValidate 强制
// grandfathered 也带审批三字段）。
func shadowEvidenceOK(s RecoveryStrategy) bool {
	if s.Grandfathered {
		return true
	}
	return shadowPassesOf(s.Name) >= shadowPassNeed
}

// shadowStatusForAPI 影子晋升就绪度（供 /api/guard/status 与诊断用）。
// 按当前可守护服务的策略去重——同一策略被多个 service 引用只报一次。
func shadowStatusForAPI() map[string]any {
	seen := map[string]bool{}
	list := make([]PromotionReadiness, 0)
	for _, svc := range guardedServices(effectiveServicesOrWarn()) {
		if svc.Recovery == "" || seen[svc.Recovery] {
			continue
		}
		seen[svc.Recovery] = true
		list = append(list, promotionReadiness(svc.Recovery))
	}
	return map[string]any{"need": shadowPassNeed, "strategies": list}
}
