// eval_api.go —— 设置页「开发者配置 → 评测」的后端（/api/eval/status、/api/eval/run）。
//
// ## 为什么 Go 侧不自己算指标
//
// 七维指标与基线对比的口径全在 tools/eval_gate.py 里（含"一轮只能落进一个桶"
// 的分配规则、coverage 与 coverage_judgeable 的拆分、混批次拒绝计算等）。
// Go 侧再去解析 report-*.json 重算一遍，等于造第二份口径 ——
// 两份口径必然漂移，而漂移的方向永远是"看板上比门禁好看"。
//
// 所以这里的做法是：**让门禁自己把结论交出来**。
// eval_gate.py 新增 --json-out <path>（只增不改，默认不写），
// 跑完落一份机器可读的 {ok, problems, snap, base}，Go 原样透给前端。
// 看板上的数字与门禁 stdout 是同一份计算，不可能不一致。
//
// ## 四档的定位（与设置页文案一一对应）
//
//	① smoke    判分器自检     纯内存、无网络、不写文件，秒级
//	② gate     门禁           自检 + R5 静态检查 + 指标基线对比，需 Go 工具链
//	③ recovery 沙箱注入评测   建沙箱→注入故障→真实模型对话→拆沙箱，产出 report-*.json
//	④ tasks    任务评测/A-B   13 题 × 重复次数真实模型对话
//
// ③④ 必须显式确认（前者注入故障、启停沙箱，后者产生真实额度消耗）；
// ①② 只读，一键跑。这条分界与红线"写操作永远过确认"同源：
// 判据是"这次动作会不会改变 EnvKit 之外的状态"，不是耗时长短。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// listenPortCur 当前控制台实际监听的端口（main() 在 findPort 之后写入）。
//
// 评测脚本（tools/ab_memory.py 的 BASE）硬编码 127.0.0.1:18765 ——
// 若 EnvKit 因端口占用顺延到 18766，脚本会连到**另一个实例或连不上**，
// 表现是"评测跑起来了但一直卡在取 token"。这不是脚本的错，也不能靠改环境变量
// 修（它没有读环境变量），唯一诚实的做法是：检测到不一致就**不许启动**。
var listenPortCur int

// ---------- 四档定义 ----------

type evalKind struct {
	Key      string
	Name     string
	Script   string // 相对仓库根
	ReadOnly bool   // false = 破坏性，必须 confirm
	NeedAI   bool   // 需要已配置 API Key
	NeedSelf bool   // 需要 127.0.0.1:18765 上就是本实例
	NeedGo   bool   // 需要 Go 工具链
	Timeout  time.Duration
	// Cost 一句人话：这一档做什么、代价多大。直接进确认框，不写空话。
	Cost string
}

// evalKindList 顺序即界面顺序：越安全的越靠前。
var evalKindList = []evalKind{
	{
		Key: "smoke", Name: "冒烟自检", Script: "tools/recovery_selftest.py",
		ReadOnly: true, Timeout: 3 * time.Minute,
		Cost: "判分器自检：纯内存、无网络、不写文件，秒级返回。它比任何单测都更能挡住判分回归。",
	},
	{
		Key: "gate", Name: "门禁", Script: "tools/eval_gate.py",
		ReadOnly: true, NeedGo: true, Timeout: 20 * time.Minute,
		Cost: "自检 + R5 静态检查 + 与基线逐维比较。需要 Go 工具链，约 10 秒～1 分钟；不改基线、不调模型。",
	},
	{
		Key: "recovery", Name: "恢复率（沙箱注入）", Script: "tools/recovery_sandbox.py",
		NeedAI: true, NeedSelf: true, Timeout: 3 * time.Hour,
		Cost: "建沙箱 → 注入故障（占端口、杀沙箱内进程）→ 真实模型对话判分 → 拆沙箱。产出 report-*.json，门禁靠它比对。",
	},
	{
		Key: "tasks", Name: "任务评测 / A-B", Script: "tools/judge_tasks.py",
		NeedAI: true, NeedSelf: true, Timeout: 4 * time.Hour,
		Cost: "13 道题 × 重复次数，全部走真实模型对话，并按题注入异常。会消耗额度，通常几十分钟。",
	},
}

func evalKindByKey(k string) (evalKind, bool) {
	for _, e := range evalKindList {
		if e.Key == k {
			return e, true
		}
	}
	return evalKind{}, false
}

// ---------- 仓库根定位 ----------

// evalRoot 找评测脚本所在的仓库根（含 tools/eval_gate.py 的目录）。
//
// 只认"脚本真的在那儿"的目录，不做猜测：评测区只在开发仓库里可用，
// 分发出去的 EnvKit.exe 旁边没有 tools/ —— 那种情况下如实报"不可用"，
// 比凭空拼一个路径再报 File not found 强。
func evalRoot() (string, error) {
	var cands []string
	if d := exeDir(); d != "" {
		cands = append(cands, d)
	}
	if wd, err := os.Getwd(); err == nil && wd != "" {
		cands = append(cands, wd)
	}
	for _, base := range cands {
		// 兼容"exe 在 dist/ 里、仓库根在上一级"的布局：只上溯一层。
		for _, d := range []string{base, filepath.Dir(base)} {
			if d == "" || d == "." {
				continue
			}
			if fi, err := os.Stat(filepath.Join(d, "tools", "eval_gate.py")); err == nil && !fi.IsDir() {
				return d, nil
			}
		}
	}
	return "", errors.New("未找到 tools/eval_gate.py：评测区只在 EnvKit 仓库目录下可用（分发版不含 tools/）")
}

// ---------- Python 解析 ----------
//
// Windows 上 PATH 里的 python 有三种坑：Microsoft Store 的应用执行别名
// （运行即退出、不报错）、坏掉的解释器、以及版本不是 3 的老装。
// 与 npm 那次的结论一致：**不实跑校验就不能相信任何一个候选**。
// 但 status 会被前端轮询，所以这里带 60s TTL 缓存；run 时若缓存过期会重新校验。

type pyProbe struct {
	mu   sync.Mutex
	at   time.Time
	path string
	ver  string
	why  string
}

var pyProbeCache pyProbe

const pyProbeTTL = 60 * time.Second

// resolvePython 返回第一个"能跑且是 Python 3"的解释器。
// why 在失败时说明试过哪些候选、各自为什么不行 —— 报错必须能让人照着改。
func resolvePython() (path, ver, why string) {
	pyProbeCache.mu.Lock()
	defer pyProbeCache.mu.Unlock()
	if !pyProbeCache.at.IsZero() && time.Since(pyProbeCache.at) < pyProbeTTL {
		return pyProbeCache.path, pyProbeCache.ver, pyProbeCache.why
	}

	var tried []string
	seen := map[string]bool{}
	var cands []string
	for _, name := range []string{"python.exe", "python"} {
		for _, p := range lookPathAll(name) {
			k := strings.ToLower(p)
			if !seen[k] {
				seen[k] = true
				cands = append(cands, p)
			}
		}
	}
	for _, p := range cands {
		// 实跑一次拿版本：stub 会以 0 或非 0 退出但**没有任何输出**，
		// 所以"能执行"不等于"可用"，必须看它有没有吐出 3.x。
		ctx, cancel := cmdContext(5 * time.Second)
		cmd := buildCmd(ctx, false, p, "-c", "import sys;print('%d.%d'%sys.version_info[:2])")
		cmd.Env = toolEnv(map[string]string{"PYTHONIOENCODING": "utf-8"})
		out, err := cmd.Output()
		cancel()
		v := strings.TrimSpace(string(out))
		if err != nil || !strings.HasPrefix(v, "3") {
			reason := "无输出（疑似应用执行别名 stub）"
			if err != nil {
				reason = err.Error()
			} else if v != "" {
				reason = "版本不兼容：" + clip280(v)
			}
			tried = append(tried, p+" → "+reason)
			continue
		}
		pyProbeCache.at, pyProbeCache.path, pyProbeCache.ver, pyProbeCache.why = time.Now(), p, v, ""
		return p, v, ""
	}
	if len(tried) == 0 {
		tried = append(tried, "PATH 上没有任何 python.exe / python")
	}
	pyProbeCache.at, pyProbeCache.path, pyProbeCache.ver, pyProbeCache.why = time.Now(), "", "", strings.Join(tried, "；")
	return "", "", pyProbeCache.why
}

// resetPythonProbe 让下一次 resolvePython 重新实跑（run 前调用）。
func resetPythonProbe() {
	pyProbeCache.mu.Lock()
	pyProbeCache.at = time.Time{}
	pyProbeCache.mu.Unlock()
}

// ---------- 运行状态 ----------

type evalRunning struct {
	Kind      string  `json:"kind"`
	Name      string  `json:"name"`
	StartedAt string  `json:"started_at"`
	Elapsed   float64 `json:"elapsed_secs"`
	DryRun    bool    `json:"dry_run"`
}

type evalResult struct {
	Kind       string          `json:"kind"`
	Name       string          `json:"name"`
	OK         bool            `json:"ok"`
	ExitCode   int             `json:"exit_code"`
	Seconds    float64         `json:"seconds"`
	FinishedAt string          `json:"finished_at"`
	Summary    string          `json:"summary"`
	Lines      int             `json:"lines"`
	Gate       json.RawMessage `json:"gate,omitempty"`
}

type evalRunState struct {
	kind    string
	name    string
	started time.Time
	dryRun  bool
	// ctx/cancel 是**整档的超时上下文**，claim 时创建、evalExecute 直接用。
	// 由 state 持有而不是在 execute 里新建：只留一个取消源，
	// 否则"用户点停止"与"超时"各握一个 cancel，谁也停不掉对方的子进程。
	ctx    context.Context
	cancel context.CancelFunc
	pid    int
}

var (
	evalMu      sync.Mutex
	evalCurrent *evalRunState
	evalLast    *evalResult
)

// evalClaim 抢占运行位。同一时刻只允许一档在跑：评测脚本都会抢 18765
// 与被测进程，并发的两档会互相注入故障、把彼此的样本搅成噪声。
func evalClaim(k evalKind, dryRun bool) (*evalRunState, func(), bool) {
	evalMu.Lock()
	defer evalMu.Unlock()
	if evalCurrent != nil {
		return nil, nil, false
	}
	ctx, cancel := cmdContext(k.Timeout)
	st := &evalRunState{
		kind: k.Key, name: k.Name, started: time.Now(),
		dryRun: dryRun, ctx: ctx, cancel: cancel,
	}
	evalCurrent = st
	return st, func() {
		evalMu.Lock()
		evalCurrent = nil
		evalMu.Unlock()
		cancel()
	}, true
}

// evalAttachCmd 把真正在跑的子进程挂到运行位（供 /api/eval/run {kind:"stop"} 终止）。
func evalAttachCmd(pid int) {
	evalMu.Lock()
	if evalCurrent != nil {
		evalCurrent.pid = pid
	}
	evalMu.Unlock()
}

// evalStop 停止当前评测：先杀进程树再取消 context。
//
// 为什么不只 cancel：Windows 上 CommandContext 的 kill 只终止直接子进程，
// 沙箱评测会派生 sandbox.py / 被测服务 —— 只杀父进程会留下占着端口的孤儿，
// 下一轮建沙箱直接失败（"上一个沙箱没拆干净"正是这套装置踩过的坑）。
func evalStop() (string, bool) {
	// 锁内把三个字段**拷出来**再用：pid 会被 evalAttachCmd 在子进程 Start()
	// 之后写入，锁外读 st.pid 就是一次数据竞争（go test -race 会抓）。
	evalMu.Lock()
	var kind string
	var pid int
	var cancel context.CancelFunc
	if evalCurrent != nil {
		kind, pid, cancel = evalCurrent.kind, evalCurrent.pid, evalCurrent.cancel
	}
	evalMu.Unlock()
	if kind == "" {
		return "", false
	}
	if pid > 0 {
		evalKillLeftover(pid, "手动停止")
	}
	if cancel != nil {
		cancel()
	}
	return kind, true
}

// ---------- status ----------

type evalKindStat struct {
	Key          string   `json:"key"`
	Name         string   `json:"name"`
	Script       string   `json:"script"`
	ReadOnly     bool     `json:"read_only"`
	Cost         string   `json:"cost"`
	Available    bool     `json:"available"`
	Blockers     []string `json:"blockers"`
	NeedsConfirm bool     `json:"needs_confirm"`
}

type evalReportFile struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Mtime string `json:"mtime"`
	Kind  string `json:"kind"` // report / batch / shadow / other
}

// evalBoard 一块"只读看板"的取数来源。available=false 时 note 说清缺什么，
// 不许显示成 0 或"—"，那会让人以为指标是 0。
type evalBoard struct {
	Name      string `json:"name"`
	Source    string `json:"source"`
	Available bool   `json:"available"`
	Note      string `json:"note"`
}

func handleEvalStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}

	root, rootErr := evalRoot()
	pyPath, pyVer, pyWhy := resolvePython()
	aiKey, aiModel := evalAISnapshot()

	evalMu.Lock()
	cur := evalCurrent
	var running *evalRunning
	if cur != nil {
		running = &evalRunning{
			Kind: cur.kind, Name: cur.name,
			StartedAt: cur.started.Format("15:04:05"),
			Elapsed:   round1(time.Since(cur.started).Seconds()),
			DryRun:    cur.dryRun,
		}
	}
	last := evalLast
	evalMu.Unlock()

	kinds := make([]evalKindStat, 0, len(evalKindList))
	for _, k := range evalKindList {
		bl := evalBlockers(k, root, rootErr, pyPath)
		kinds = append(kinds, evalKindStat{
			Key: k.Key, Name: k.Name, Script: k.Script, ReadOnly: k.ReadOnly,
			Cost: k.Cost, Available: len(bl) == 0, Blockers: bl,
			NeedsConfirm: !k.ReadOnly,
		})
	}

	resp := map[string]any{
		"root": root,
		"python": map[string]any{
			"path": pyPath, "version": pyVer, "ok": pyPath != "", "why": pyWhy,
		},
		"port": map[string]any{
			"listen":         listenPortCur,
			"script_expects": 18765,
			"ok":             listenPortCur == 18765,
			"note":           evalPortNote(),
		},
		"ai":       map[string]any{"key_set": aiKey, "model": aiModel},
		"running":  running,
		"last":     last,
		"kinds":    kinds,
		"baseline": evalBaselineSnap(root),
		"boards":   evalBoards(root),
		"reports":  evalReportList(root),
	}
	if rootErr != nil {
		resp["root_error"] = rootErr.Error()
	}
	writeJSON(w, resp)
}

func evalPortNote() string {
	if listenPortCur == 18765 {
		return ""
	}
	if listenPortCur == 0 {
		return "控制台端口尚未确定"
	}
	return fmt.Sprintf("评测脚本硬编码 127.0.0.1:18765，当前实例在 %d："+
		"请释放 18765（或把「开发者配置 → 控制台端口」改回 18765）后重启，否则脚本会连错实例", listenPortCur)
}

// evalAISnapshot 取"是否配了 key"与模型名。
//
// 只回 key_set 布尔，**不让密钥离开服务端**（前端只需要知道能不能跑评测）。
// cfg.AI 是**指针**：老配置里可以没有 "ai" 段，这里必须判空 ——
// 否则打开评测页就是一次 nil dereference（页面上表现为整块 500）。
func evalAISnapshot() (keySet bool, model string) {
	cfgMu.Lock()
	var ac AIConfig
	if cfg.AI != nil {
		ac = *cfg.AI // 锁内拷贝值，锁外不再碰指针
	}
	cfgMu.Unlock()
	return ac.Enabled && ac.APIKey != "", ac.Model
}

func evalBlockers(k evalKind, root string, rootErr error, pyPath string) []string {
	// 空切片而不是 nil：前端要 `blockers.length`，null 会多一层判空
	// （这类"看起来是空、实际是 null"最容易在界面上变成一行 null）。
	b := []string{}
	if rootErr != nil {
		return []string{rootErr.Error()}
	}
	sp := filepath.Join(root, filepath.FromSlash(k.Script))
	if fi, err := os.Stat(sp); err != nil || fi.IsDir() {
		b = append(b, "缺少脚本 "+k.Script)
	}
	if pyPath == "" {
		_, _, why := resolvePython()
		b = append(b, "未找到可用的 Python 3："+why)
	}
	if k.NeedGo && findExe("go.exe", "go") == "" {
		b = append(b, "未找到 go：门禁的 R5 静态检查要跑 go test")
	}
	if k.NeedAI {
		if ok, _ := evalAISnapshot(); !ok {
			b = append(b, "AI 未启用或未配置 API Key：这一档要真实调用模型")
		}
	}
	if k.NeedSelf && listenPortCur != 18765 {
		b = append(b, evalPortNote())
	}
	return b
}

// evalBaselineSnap 只读 tools/eval_baseline.json 的元信息与各维基线值。
// 不重算、不改写 —— 冻结基线仍只能走命令行 --set-baseline（方案已定）。
func evalBaselineSnap(root string) map[string]any {
	out := map[string]any{"exists": false}
	if root == "" {
		return out
	}
	p := filepath.Join(root, "tools", "eval_baseline.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		out["note"] = "尚未冻结基线（命令行 --set-baseline 生成）"
		return out
	}
	var b struct {
		GeneratedAt  string                    `json:"generated_at"`
		NRunS        int                       `json:"n_runs"`
		NFiles       int                       `json:"n_files"`
		SelftestPass int                       `json:"selftest_pass"`
		Coverage     map[string]any            `json:"coverage"`
		Dims         map[string]map[string]any `json:"dims"`
	}
	if json.Unmarshal(raw, &b) != nil {
		out["note"] = "eval_baseline.json 解析失败（文件可能被手改坏）"
		return out
	}
	dims := map[string]any{}
	for k, d := range b.Dims {
		dims[k] = d["rate"]
	}
	out["exists"] = true
	out["generated_at"] = b.GeneratedAt
	out["n_runs"] = b.NRunS
	out["n_files"] = b.NFiles
	out["selftest_pass"] = b.SelftestPass
	out["coverage"] = b.Coverage["coverage"]
	out["coverage_judgeable"] = b.Coverage["coverage_judgeable"]
	out["dims"] = dims
	return out
}

// evalBoards 八指标看板的"取数来源"清单。
//
// 关键：这里**只说明数据从哪来、在不在**，不自己拼指标。
// 4/5/6/7 项（无效操作/验证准确性/安全违规/误拒率）按 eval/metrics/README.md
// 的定义要从 audit/trace 离线解析，仓库里**没有自动产出脚本** ——
// 那就如实写"未自动产出"，不许拿个 0 冒充。
func evalBoards(root string) []evalBoard {
	if root == "" {
		return nil
	}
	has := func(rel string) bool {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		return err == nil
	}
	repCount := 0
	if ents, err := os.ReadDir(filepath.Join(root, "docs", "eval")); err == nil {
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "report-") && strings.HasSuffix(e.Name(), ".json") {
				repCount++
			}
		}
	}
	return []evalBoard{
		{Name: "任务成功率 / 首次成功率 / 需重试", Source: "docs/eval/success-rate-report.json",
			Available: has("docs/eval/success-rate-report.json"),
			Note:      "由「任务评测」产出；未跑过则为空"},
		{Name: "故障恢复率（七维）", Source: "docs/eval/report-*.json",
			Available: repCount > 0,
			Note:      fmt.Sprintf("由「恢复率（沙箱注入）」按类分文件产出，当前 %d 份；门禁只认这种", repCount)},
		{Name: "经验增益（A/B）", Source: "docs/eval/ab-memory-report.json",
			Available: has("docs/eval/ab-memory-report.json"),
			Note:      "由 tools/ab_memory.py 产出；需 memory_enabled 两档对照"},
		{Name: "无效操作次数 / 验证准确性 / 安全违规率 / 误拒率", Source: "audit-*.jsonl + trace-*.jsonl",
			Available: false,
			Note:      "口径见 eval/metrics/README.md §5-7，当前无自动产出脚本（需按 trace_id 离线解析）；实测数据见 docs/eval/baseline-*.md"},
	}
}

// evalReportList 列 docs/eval 下的产物（只读元信息，不解析内容）。
// 按修改时间倒序，最多 60 条：目录里会随批次累积几十份，全列没有意义。
func evalReportList(root string) []evalReportFile {
	out := []evalReportFile{}
	if root == "" {
		return out
	}
	dir := filepath.Join(root, "docs", "eval")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, evalReportFile{
			Name:  e.Name(),
			Size:  fi.Size(),
			Mtime: fi.ModTime().Format("2006-01-02 15:04"),
			Kind:  evalReportKind(e.Name()),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mtime > out[j].Mtime })
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

func evalReportKind(name string) string {
	switch {
	case strings.HasPrefix(name, "report-"):
		return "report"
	case strings.HasPrefix(name, "sandbox-recovery-"):
		return "batch"
	case strings.HasPrefix(name, "shadow-"):
		return "shadow"
	}
	return "other"
}

// ---------- run ----------

type evalRunReq struct {
	Kind        string `json:"kind"`
	Confirm     bool   `json:"confirm"`
	DryRun      bool   `json:"dry_run"`
	Only        string `json:"only"`
	Repeat      int    `json:"repeat"`
	AllowWrites bool   `json:"allow_writes"`
}

// onlyRe --only 的合法形状（防把一长串 SQL 或路径塞进脚本参数）。
var onlyRe = regexp.MustCompile(`^[A-Za-z0-9_,]{1,200}$`)

func handleEvalRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body evalRunReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "参数错误", 400)
		return
	}

	// 停止：复用同一端点，不为一个动作新开路由（方案只留 3 个新端点）。
	if body.Kind == "stop" {
		if kind, ok := evalStop(); ok {
			auditNow(actUser, "eval_stop", kind, "", resOK, "用户手动停止评测")
			info(scEval, "评测", "已请求停止：%s", kind)
			writeJSON(w, map[string]any{"ok": true, "stopped": kind})
			return
		}
		writeJSON(w, map[string]any{"ok": false, "error": "当前没有正在运行的评测"})
		return
	}

	k, ok := evalKindByKey(body.Kind)
	if !ok {
		http.Error(w, "未知的评测档位："+body.Kind, 400)
		return
	}
	if body.Only != "" && !onlyRe.MatchString(body.Only) {
		http.Error(w, "--only 只接受字母数字下划线与逗号，如 T01,T02 或 kill_service", 400)
		return
	}
	limit := 5
	if k.Key == "tasks" {
		limit = 10
	}
	if body.Repeat < 0 || body.Repeat > limit {
		http.Error(w, fmt.Sprintf("重复次数需在 1~%d 之间（0 = 用脚本默认）", limit), 400)
		return
	}

	root, rootErr := evalRoot()
	if rootErr != nil {
		writeJSONStatus(w, 412, map[string]any{"ok": false, "error": rootErr.Error()})
		return
	}
	// run 前强制重探：缓存里的"好 python"可能在两次点击之间被卸载/换掉。
	resetPythonProbe()
	pyPath, pyVer, _ := resolvePython()
	if blockers := evalBlockers(k, root, nil, pyPath); len(blockers) > 0 {
		writeJSONStatus(w, 412, map[string]any{"ok": false, "error": "前置条件不满足", "blockers": blockers})
		return
	}

	// 抢占运行位（dry-run 也占：它会跑脚本，不能与其它档并发）。
	st, release, claimed := evalClaim(k, body.DryRun)
	if !claimed {
		evalMu.Lock()
		curName, curKind := "", ""
		if evalCurrent != nil {
			curName, curKind = evalCurrent.name, evalCurrent.kind
		}
		evalMu.Unlock()
		writeJSONStatus(w, 409, map[string]any{
			"ok": false, "running": curKind,
			"error": "已有评测在跑：" + curName + "。评测会互相抢 18765 与被测进程，请等它结束或先停止",
		})
		return
	}

	outPath := ""
	if k.Key == "gate" {
		outPath = filepath.Join(os.TempDir(), fmt.Sprintf("envkit-eval-gate-%d.json", os.Getpid()))
	}
	args := evalBuildArgs(k, body, outPath)

	// dry-run：同步跑、短超时，把计划原文交给前端弹窗。
	// 它不改变任何状态（judge_tasks 的 --dry-run 只列计划），所以不需要确认。
	if body.DryRun {
		defer release()
		txt, err := evalRunSync(k, root, pyPath, args, 3*time.Minute)
		if err != nil {
			writeJSONStatus(w, 500, map[string]any{"ok": false, "error": err.Error(), "output": txt})
			return
		}
		writeJSON(w, map[string]any{
			"ok": true, "dry_run": true, "kind": k.Key,
			"repeat": body.Repeat, "model": evalModelName(),
			"output": txt,
		})
		return
	}

	// 破坏性档必须显式确认：428 由服务端强制，前端点不点都绕不过。
	if !k.ReadOnly && !body.Confirm {
		release()
		needConfirm(w, "eval_"+k.Key, k.Name, k.Cost)
		return
	}

	fin := auditStart(actUser, "eval_"+k.Key, k.Name, strings.Join(args, " "))
	go func() {
		res := evalExecute(k, root, pyPath, args, outPath, st, release)
		result := "ok"
		if !res.OK {
			result = resFail
		}
		detail := fmt.Sprintf("%.1fs / %d 行", res.Seconds, res.Lines)
		if res.Summary != "" {
			detail += " / " + res.Summary
		}
		fin(result, detail)
	}()

	writeJSON(w, map[string]any{
		"ok": true, "started": true, "kind": k.Key, "name": k.Name,
		"python": pyPath, "python_version": pyVer,
	})
}

func evalModelName() string {
	_, m := evalAISnapshot()
	if m == "" {
		return "(未配置)"
	}
	return m
}

// evalBuildArgs 组装脚本参数。
//
// --json-out 只给 gate：其余三档的产物是 docs/eval 下的报告文件，
// 由脚本自己写，Go 侧不插手（插手就要复制一份字段口径）。
func evalBuildArgs(k evalKind, b evalRunReq, outPath string) []string {
	var a []string
	// --dry-run 只有两个破坏性档的脚本支持（只列计划、不调模型、不建沙箱）；
	// gate/smoke 的脚本没有这个参数，argparse 会直接以 exit 2 拒掉。
	// 这里不传的后果比"预演没生效"更糟：recovery 的预演会真的开始建沙箱，
	// 3 分钟同步超时把它砍断，留下半截沙箱和占着的端口。
	if b.DryRun && (k.Key == "recovery" || k.Key == "tasks") {
		a = append(a, "--dry-run")
	}
	if b.Repeat > 0 {
		a = append(a, "--repeat", strconv.Itoa(b.Repeat))
	}
	switch k.Key {
	case "gate":
		if outPath != "" {
			a = append(a, "--json-out", outPath)
		}
	case "recovery":
		if b.Only != "" {
			a = append(a, "--only", b.Only)
		}
		// --allow-writes 会自动批准确认卡（白名单里只有 start_service），
		// 不给它的话样本会被确认卡掐断、覆盖率必然偏低。这是用户的选择，
		// 不是默认行为 —— 所以只在显式传 true 时加。
		if b.AllowWrites {
			a = append(a, "--allow-writes")
		}
	case "tasks":
		if b.Only != "" {
			a = append(a, "--only", b.Only)
		}
	}
	return a
}

// evalRunSync 同步跑一小段（dry-run 用），返回合并输出。
func evalRunSync(k evalKind, root, pyPath string, args []string, timeout time.Duration) (string, error) {
	ctx, cancel := cmdContext(timeout)
	defer cancel()
	cmd := buildCmd(ctx, false, pyPath, append([]string{filepath.Join(root, filepath.FromSlash(k.Script))}, args...)...)
	cmd.Dir = root
	cmd.Env = toolEnv(evalEnvExtra())
	out, err := cmd.CombinedOutput()
	txt := string(out)
	if len(txt) > 20000 {
		// dry-run 计划只有几十行；超长说明脚本没进 dry-run 分支，宁可截断也别灌满前端。
		txt = txt[:20000] + "\n…（输出过长已截断）"
	}
	if err != nil {
		return txt, fmt.Errorf("%v", err)
	}
	return txt, nil
}

// evalEnvExtra 评测子进程的环境。
//
// PYTHONIOENCODING 必须给：脚本头部虽然都做了 reconfigure，但那是在
// stdout 是管道时的兜底；先手给 UTF-8 更省事，也免得中文在日志里变问号。
// PYTHONUNBUFFERED 让 print 立即出来 —— 不然 judge_tasks 跑四十分钟，
// 前三十九分钟的日志全卡在缓冲区里，"日志回流"就成了假的。
func evalEnvExtra() map[string]string {
	return map[string]string{
		"PYTHONIOENCODING": "utf-8",
		"PYTHONUNBUFFERED": "1",
	}
}

// evalExecute 真正跑一档，日志逐行进 SSE（scope=eval），结束后落结果快照。
//
// 不复用 execStreamed 的理由：那套带"零输出秒退 → 重试 3 次"的策略，
// 而评测**绝不能重试** —— 重试等于把注入与真实模型调用各跑两遍，
// 既烧额度又会把两批样本混进同一份报告。这里单次执行、只负责说清结果。
func evalExecute(k evalKind, root, pyPath string, args []string, outPath string, st *evalRunState, release func()) *evalResult {
	defer release()

	start := time.Now()
	// 超时上下文来自 state（与 stop 共用一个取消源），这里不再新建。
	ctx := st.ctx

	cmd := buildCmd(ctx, false, pyPath, append([]string{filepath.Join(root, filepath.FromSlash(k.Script))}, args...)...)
	cmd.Dir = root
	cmd.Env = toolEnv(evalEnvExtra())

	w := &lineWriter{scope: scEval, tag: k.Name, svcKey: "eval"}
	cmd.Stdout = w
	cmd.Stderr = w

	info(scEval, "评测", "开始 %s：%s %s（超时 %s）", k.Name, k.Script, strings.Join(args, " "), k.Timeout)
	if err := cmd.Start(); err != nil {
		w.flush()
		res := &evalResult{
			Kind: k.Key, Name: k.Name, OK: false, ExitCode: -1,
			Seconds: round1(time.Since(start).Seconds()), FinishedAt: time.Now().Format("15:04:05"),
			Summary: "启动失败：" + err.Error(),
		}
		evalPublish(res)
		fail(scEval, "评测", "%s 启动失败：%v", k.Name, err)
		return res
	}
	evalAttachCmd(cmd.Process.Pid)

	runErr := cmd.Wait()
	w.flush()

	secs := round1(time.Since(start).Seconds())
	res := &evalResult{
		Kind: k.Key, Name: k.Name, Seconds: secs,
		FinishedAt: time.Now().Format("15:04:05"),
		Lines:      w.n,
		Summary:    w.errSummary(),
	}
	switch {
	case runErr == nil:
		res.OK = true
		res.ExitCode = 0
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.ExitCode = -2
		res.Summary = fmt.Sprintf("超过 %s 仍未结束，已被终止", k.Timeout)
		// 超时后必须自己收尸：脚本可能留了沙箱/占端口进程，
		// 下一次建沙箱会直接撞上"端口被占"。
		evalKillLeftover(cmd.Process.Pid, "超时清理")
	default:
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			res.ExitCode = ee.ExitCode()
		} else {
			res.ExitCode = -1
		}
		if res.Summary == "" {
			res.Summary = runErr.Error()
		}
	}

	// 门禁的机器可读结论：直接透给前端，不在 Go 侧重算任何指标。
	if outPath != "" {
		res.Gate = evalTakeGateJSON(outPath)
		// 摘要改用门禁自己的结论。不这么做的话，errSummary() 会挑到最后一行
		// —— 而门禁失败时的最后一行是一排等号（结论在 problems 列表里），
		// 日志里就成了"门禁 失败：====…"，等于什么都没说。
		if msg := gateSummary(res.Gate); msg != "" {
			res.Summary = msg
		}
	}

	evalPublish(res)
	if res.OK {
		ok(scEval, "评测", "%s 完成：%.1fs，%d 行", k.Name, secs, res.Lines)
	} else {
		fail(scEval, "评测", "%s 失败（exit %d）：%.1fs，%d 行%s",
			k.Name, res.ExitCode, secs, res.Lines, summarySuffix(res.Summary))
	}
	return res
}

func summarySuffix(s string) string {
	if s == "" {
		return ""
	}
	return "；" + s
}

func evalPublish(res *evalResult) {
	evalMu.Lock()
	evalLast = res
	evalMu.Unlock()
}

// gateSummary 从门禁的 JSON 里提炼一句结论（成功/未通过原因）。
// 只是把门禁自己的 problems 拼起来，不判断对错 —— 判据始终在 Python 侧。
func gateSummary(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var g struct {
		OK       bool     `json:"ok"`
		Problems []string `json:"problems"`
	}
	if json.Unmarshal(raw, &g) != nil {
		return ""
	}
	if g.OK {
		return "门禁通过"
	}
	if len(g.Problems) == 0 {
		return "门禁未通过"
	}
	return clip280("门禁未通过：" + strings.Join(g.Problems, "；"))
}

// evalTakeGateJSON 读门禁落的 JSON 并删除临时文件。
// 读不到就当"没有机器可读结论"（旧版脚本没这个参数），不编造。
func evalTakeGateJSON(p string) json.RawMessage {
	raw, err := os.ReadFile(p)
	_ = os.Remove(p)
	if err != nil || len(raw) == 0 {
		return nil
	}
	if !json.Valid(raw) {
		return nil
	}
	return json.RawMessage(raw)
}

// evalKillLeftover 终止评测的整个进程树（手动停止与超时收尸共用）。
//
// 只杀直接子进程是不够的：沙箱评测会派生 sandbox.py 与被测服务，
// 父进程死掉而它们活着，端口就一直被占着，下一轮建沙箱直接失败
// —— "上一个沙箱没拆干净"正是这套装置反复踩到的坑。
func evalKillLeftover(pid int, why string) {
	if pid <= 0 {
		return
	}
	ctx, cancel := cmdContext(20 * time.Second)
	defer cancel()
	c := buildCmd(ctx, false, "taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	c.Env = toolEnv(nil)
	if err := c.Run(); err != nil {
		warn(scEval, "评测", "%s：清理进程树失败（PID %d）：%v", why, pid, err)
		return
	}
	info(scEval, "评测", "%s：已清理进程树（PID %d）", why, pid)
}

// ---------- 小工具 ----------

func round1(f float64) float64 {
	return float64(int64(f*10+0.5)) / 10
}

func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
