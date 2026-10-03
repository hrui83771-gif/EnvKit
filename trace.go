package main

// trace.go —— v2.2 M9：任务轨迹（Trace）
//
// ## 为什么 Audit 不够
//
// 现在的 audit 是**流水**：一行一条操作。它能回答"谁在什么时候做了什么"，
// 但回答不了"AI 到底有没有在瞎试"——因为流水里没有"这是一次尝试"的概念，
// 同一个任务里 AI 连试五次-start_service 和一次成功的-start_service，
// 在 audit 里长得一模一样，只是多了四行。
//
// 而"无效操作次数"与"验证准确性"这两个指标都依赖这个区分：
// 前者要数"任务内与目标无关的调用"，后者要把助手的最终声明与复验结论比对。
// 缺了 trace_id，这两项指标在基线报告里只能写"不可测"。
//
// ## 最小版范围（v2.2）
//
// 1. `trace_id` 贯通审计——**现有几十个 audit 调用点零改动**（靠原子变量自动填充）
// 2. Trace 结构与落盘，与 Audit **分文件存**
// 3. AI 工具调用记 step
//
// Plan / Policy / Human Intervention / Recovery 四个环节留到 v2.3：
// 它们依赖 Plan 层与 Human Trace，现在记了也只能是空壳。
//
// ## 为什么分文件而不是塞进 audit
//
// |         | Audit                    | Trace              |
// |---------|--------------------------|--------------------|
// | 目的    | 合规留痕、防抵赖          | 优化与学习          |
// | 粒度    | 一条操作一行              | 一次任务一条        |
// | 保留期  | 30 天                    | 短（轨迹数据量大）  |
// | 可变性  | 不可变、只追加            | 可裁剪              |
//
// 把 Trace 塞进 audit 会让审计文件体积翻十倍，而合规留痕并不需要轨迹信息。
//
// ## 并发说明（必须知道的一点）
//
// EnvKit 是单用户本地工具，但守护 goroutine（链端守护）与用户操作可能并发。
// traceID 用 atomic.Value 存全局：**并发时可能把两个任务的操作记到同一条轨迹上**。
// 这是刻意的取舍——为它做 per-goroutine 上下文传递要改所有任务函数签名，
// 收益（轨迹归属精确）远小于代价（几十个调用点与测试的改动面）。
// 真正需要精确归属时（v2.3 的 Human Trace），会改成显式传入。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// traceIDHolder 用 atomic 存当前 traceID，避免每次审计都加锁。
var traceIDHolder atomic.Value // string

// currentTraceID 返回当前任务轨迹 ID；无任务时返回空串。
func currentTraceID() string {
	v, _ := traceIDHolder.Load().(string)
	return v
}

// ===== Trace 环节 =====

// Trace 环节常量。完整十二环节在规划里，这里只落地当前能真实填上的。
const (
	phAction   = "action"   // 执行了一个动作
	phEvidence = "evidence" // 拿到客观证据
	phVerify   = "verify"   // 复验结论
	phFailure  = "failure"  // 失败及归类
	phSuccess  = "success"  // 达成目标
)

// TraceStep 轨迹里的一步。
type TraceStep struct {
	Seq      int    `json:"seq"`
	Phase    string `json:"phase"`  // 见上方环节常量
	Actor    string `json:"actor"`  // user | ai | guard | system
	Action   string `json:"action"` // 工具名或动作名
	Target   string `json:"target,omitempty"`
	Result   string `json:"result,omitempty"`
	DurMs    int64  `json:"dur_ms,omitempty"`
	ErrKind  string `json:"err_kind,omitempty"`
	Evidence string `json:"evidence,omitempty"` // **摘要**，不是全文——Trace 要能裁剪
}

// Trace 一次任务的完整轨迹。
type Trace struct {
	TraceID  string      `json:"trace_id"`
	Goal     string      `json:"goal,omitempty"` // 用户诉求（截断）
	Actor    string      `json:"actor"`          // 发起主体
	Started  string      `json:"started"`
	Ended    string      `json:"ended,omitempty"`
	Outcome  string      `json:"outcome,omitempty"` // success | failed | aborted | partial
	Verified bool        `json:"verified"`          // 是否有复验结论
	Steps    []TraceStep `json:"steps"`
}

// traceMaxSteps 单条轨迹的步数上限。超过后只记数不记内容——
// 死循环的 AI 能把轨迹写到几十 MB，那本身就说明出问题了。
const traceMaxSteps = 120

// traceTruncatedCount 记录被丢弃的步数。
const traceTruncatedCount = 9999

// ===== 生命周期 =====

var (
	traceMu   sync.Mutex
	traceCur  *Trace
	traceFile *os.File
)

// beginTrace 开启一条任务轨迹，并把它设为当前任务。
// 返回 traceID；后续 audit 调用会自动带上它。
func beginTrace(goal, actor string) string {
	id := "t-" + time.Now().Format("20060102-150405") + "-" + traceRand()
	tr := &Trace{
		TraceID: id,
		Goal:    firstLines(strings.TrimSpace(goal), 120),
		Actor:   actor,
		Started: time.Now().Format("2006-01-02 15:04:05.000"),
		Steps:   []TraceStep{},
	}
	traceMu.Lock()
	traceCur = tr
	traceMu.Unlock()
	traceIDHolder.Store(id)
	return id
}

// endTrace 收尾并落盘。outcome 取值见 Trace.Outcome。
func endTrace(outcome string) {
	traceMu.Lock()
	tr := traceCur
	traceCur = nil
	traceMu.Unlock()
	if tr == nil {
		return
	}
	traceIDHolder.Store("")
	tr.Ended = time.Now().Format("2006-01-02 15:04:05.000")
	tr.Outcome = outcome
	for _, s := range tr.Steps {
		if s.Phase == phVerify {
			tr.Verified = true
			break
		}
	}
	traceWrite(tr)
}

// traceStep 记一步。**必须在没有任务时静默丢弃**——
// 守护 goroutine 与用户操作并行，不是每条操作都属于某个 AI 任务。
func traceStep(phase, actor, action, target, result string, durMs int64, errKind, evidence string) {
	traceMu.Lock()
	defer traceMu.Unlock()
	if traceCur == nil {
		return
	}
	tr := traceCur
	if len(tr.Steps) >= traceMaxSteps {
		// 保留一个"被截断"的占位步，让看轨迹的人知道后面还有但没记
		if len(tr.Steps) == traceMaxSteps {
			tr.Steps = append(tr.Steps, TraceStep{
				Seq: tr.Seq(), Phase: "truncated", Action: "steps_exceeded",
			})
		}
		return
	}
	tr.Steps = append(tr.Steps, TraceStep{
		Seq:      len(tr.Steps) + 1,
		Phase:    phase,
		Actor:    actor,
		Action:   action,
		Target:   target,
		Result:   result,
		DurMs:    durMs,
		ErrKind:  errKind,
		Evidence: firstLines(evidence, 300),
	})
}

// Seq 当前步数（线程安全）。
func (t *Trace) Seq() int { return len(t.Steps) }

// currentTraceSnapshot 取当前轨迹的浅拷贝，供 UI / API 展示。
func currentTraceSnapshot() *Trace {
	traceMu.Lock()
	defer traceMu.Unlock()
	if traceCur == nil {
		return nil
	}
	cp := *traceCur
	cp.Steps = append([]TraceStep(nil), traceCur.Steps...)
	return &cp
}

// ===== 落盘 =====

// traceToolStep 把一次 AI 工具执行记成轨迹的一步，并据结果追加 verify/failure 步。
//
// 为什么要拆成两步而不是一步：指标口径里"无效操作"看的是 action 序列，
// 而"验证准确性"要看的是有没有复验结论——混在一条里就都算不清了。
func traceToolStep(tool string, args map[string]any, res string, err error, durMs int64) {
	res2 := "ok"
	if err != nil {
		res2 = "fail"
	}
	traceStep(phAction, actAI, tool, policyTargetOf(tool, args), res2, durMs, "", "")
	if err != nil {
		traceStep(phFailure, actAI, tool, policyTargetOf(tool, args), "fail", 0, "tool_error", firstLines(err.Error(), 200))
		return
	}
	// 复验类工具的结果本身就是证据
	if strings.HasPrefix(tool, "verify_") {
		traceStep(phVerify, actAI, tool, policyTargetOf(tool, args), "ok", 0, "", firstLines(res, 300))
		return
	}
	// 启停类工具返回里有复验结论（aiStartVerify 会走 verify_environment）
	if s := traceExtractVerify(res); s != "" {
		traceStep(phVerify, actAI, tool, policyTargetOf(tool, args), "ok", 0, "", s)
	}
}

// traceExtractVerify 从工具返回文本里抽出复验结论。
// 判据是「已复验通过 / 未复验 / 复验未通过」这几个固定说法——它们由
// aiVerifyText 统一产出，不要在这里重新定义语义。
func traceExtractVerify(res string) string {
	for _, k := range []string{"已复验通过", "复验未通过", "未复验"} {
		if i := strings.Index(res, k); i >= 0 {
			return firstLines(res[i:], 200)
		}
	}
	return ""
}

// traceDir 轨迹所在目录。抽成变量供单测替换。
// 注意：查询与清理都从这里解析目录，而不是各写一遍 exeDir()——
// 之前 tracePath 可替换但 traceQuery 仍写死 exeDir()，导致单测读不到临时文件。
var traceDir = func() string { return exeDir() }

// tracePath 当前轨迹文件（按天）。
var tracePath = func() string {
	return filepath.Join(traceDir(), "trace-"+time.Now().Format("20060102")+".jsonl")
}

func traceWrite(tr *Trace) {
	if tr == nil {
		return
	}
	b, err := json.Marshal(tr)
	if err != nil {
		return
	}
	f, err := os.OpenFile(tracePath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		// 轨迹写不了不该影响业务——它是优化手段，不是控制手段
		warn(scSys, "审计", "轨迹文件无法写入（%v），本次任务轨迹未留痕", err)
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// traceQuery 读取最近 n 条轨迹（新在前），可按结果过滤。
func traceQuery(n int, outcome string) []Trace {
	if n <= 0 {
		n = 50
	}
	var out []Trace
	files, _ := filepath.Glob(filepath.Join(traceDir(), "trace-*.jsonl"))
	for i := len(files) - 1; i >= 0; i-- {
		b, err := os.ReadFile(files[i])
		if err != nil {
			continue
		}
		lines := strings.Split(string(b), "\n")
		for j := len(lines) - 1; j >= 0; j-- {
			ln := strings.TrimSpace(lines[j])
			if ln == "" {
				continue
			}
			var t Trace
			if json.Unmarshal([]byte(ln), &t) != nil {
				continue
			}
			if outcome != "" && t.Outcome != outcome {
				continue
			}
			out = append(out, t)
			if len(out) >= n {
				return out
			}
		}
	}
	return out
}

// cleanupOldTraces 清理过期轨迹文件。保留期比审计短——
// 轨迹用于近期调优，长期没有价值，而它比审计大得多。
func cleanupOldTraces(days int) {
	if days <= 0 {
		days = 14
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	files, _ := filepath.Glob(filepath.Join(traceDir(), "trace-*.jsonl"))
	for _, p := range files {
		name := filepath.Base(p)
		d := strings.TrimSuffix(strings.TrimPrefix(name, "trace-"), ".jsonl")
		t, err := time.Parse("20060102", d)
		if err != nil || t.Before(cutoff) {
			_ = os.Remove(p)
		}
	}
}

// traceSeq 轨迹序号。Windows 上 time.Now() 精度约 0.5~15ms，
// 循环里连续开启轨迹时纳秒时间会重复——实测 200 次连开会撞号，
// 而 trace_id 撞号会让两条任务的审计与轨迹混在一起，指标就算不准了。
var traceSeq atomic.Uint32

// traceRand 生成 6 位后缀（时间 4 位 + 序号 2 位），保证同一时刻多次开启也不撞号。
func traceRand() string {
	const hexd = "0123456789ABCDEF"
	n := uint32(time.Now().UnixNano())
	s := traceSeq.Add(1)
	return string([]byte{
		hexd[(n>>12)&0xF], hexd[(n>>8)&0xF], hexd[(n>>4)&0xF], hexd[n&0xF],
		hexd[(s>>4)&0xF], hexd[s&0xF],
	})
}
