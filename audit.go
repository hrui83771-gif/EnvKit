package main

// audit.go —— 操作审计流（JSONL，只追加，与业务日志分离）
//
// 设计要点：
//  1. 业务日志回答"系统发生了什么"，审计回答"谁、何时、对什么、做了什么变更、结果如何"。
//     二者必须分文件：业务日志会被轮转覆盖，审计不能。
//  2. 只追加、不修改、不回滚；每天一个文件 audit-YYYYMMDD.jsonl，独立保留 30 天。
//  3. 写操作入口必须埋点；参数与详情统一脱敏（密码/API Key 抹成 ***）。
//  4. 审计失败（磁盘只读等）只告警，绝不阻断业务——审计是记录手段，不是控制手段。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AuditEntry 一条审计记录。
type AuditEntry struct {
	Ts      string `json:"ts"`                 // 2006-01-02 15:04:05.000
	Actor   string `json:"actor"`              // user | ai | guard | system
	Action  string `json:"action"`             // 动作名，如 db_backup / apply_sql / config_save
	Target  string `json:"target"`             // 目标对象，如库名、服务名、文件路径
	Params  string `json:"params"`             // 入参摘要（已脱敏）
	Result  string `json:"result"`             // ok | fail | denied | started | unverified（v3.0 T1.6）
	Detail  string `json:"detail,omitempty"`   // 结果补充（已脱敏）
	DurMs   int64  `json:"dur_ms,omitempty"`   // 耗时
	Verify  string `json:"verify,omitempty"`   // 复验结论（v2.0 P2：执行之后有没有真的验过、验出什么）
	TraceID string `json:"trace_id,omitempty"` // v2.2 M9：所属任务轨迹；无任务时为空
}

// 动作主体
const (
	actUser  = "user"
	actAI    = "ai"
	actGuard = "guard"
	actSys   = "system"
	// actShadow（v3.0 T2.8）：影子评估。只记录不执行——
	// Observe→Plan 走完就在 Execute 前停止，留"若执行会做什么"的痕。
	actShadow = "shadow"
)

// 结果取值
const (
	resOK     = "ok"
	resFail   = "fail"
	resDenied = "denied"
	resStart  = "started"
	// resUnverified（v3.0 T1.6，修 G11 前半）：执行了但没证明恢复。
	// 与 fail 分开是硬需求——"还没验到"和"没做成"的下一步完全相反：
	// 前者该继续等/继续验，后者该回滚或升级。审计里二者同形，
	// 读的人（和判据）就会把"未证明"误读成"失败"。
	resUnverified = "unverified"
)

var (
	auditMu     sync.Mutex
	auditFile   *os.File
	auditDay    string
	auditFileAt string // 已打开文件的路径，目录被替换（单测）时需重开
)

// auditPath 当前审计文件路径。
// v2.2 M9：用 auditDir() 而不是 exeDir()——写也必须走可替换目录。
// 此前读走 auditDir、写走 exeDir，导致单测把审计写进用户真实目录，
// 且单测之间互相污染（全量跑时 TestTraceIDInAudit 就因此失败）。
func auditPath() string {
	return filepath.Join(auditDir(), "audit-"+time.Now().Format("20060102")+".jsonl")
}

// auditWrite 追加一条记录（内部持锁）。
// v2.2 M9：自动带上当前 trace_id，**现有所有调用点零改动**即可获得任务归属。
func auditWrite(e AuditEntry) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	if e.TraceID == "" {
		e.TraceID = currentTraceID()
		if e.TraceID != "" {
			// 补写一次：上面已经 marshal 过一次，这里需要重新序列化才带上 id
			if b2, err2 := json.Marshal(e); err2 == nil {
				b = b2
			}
		}
	}
	auditMu.Lock()
	defer auditMu.Unlock()
	day := time.Now().Format("20060102")
	p := auditPath()
	if auditFile == nil || auditDay != day || auditFileAt != p {
		if auditFile != nil {
			_ = auditFile.Close()
			auditFile = nil
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			// v3.0 T1.6（修 G11 后半）：目录只读/被替换等场景，放弃落盘
			// 但必须可见。旧实现每次调用都 warn 一次（日志被刷爆）且从不
			// 升级——"审计静默丢失"这件事本身成了不可观测事故。
			auditFailNotice(fmt.Sprintf("审计文件无法写入（%v），本次操作未留痕：%s", err, p))
			return
		}
		auditFile, auditDay, auditFileAt = f, day, p
	}
	if _, werr := auditFile.WriteString(string(b) + "\n"); werr != nil {
		// v3.0 T1.6：写失败（磁盘满/句柄失效）以前被 `_, _ =` 完全丢弃——
		// "全程留痕"在磁盘满的那一刻悄悄变成幻觉，而没人知道。
		auditFailNotice(fmt.Sprintf("审计写入失败（%v），本条记录丢失：%s", werr, auditPath()))
	}
}

// ---------- 审计写入失败的可见化（v3.0 T1.6，修 G11 后半） ----------
//
// 旧实现两处静默：OpenFile 失败只 warn（且每次调用都 warn，日志被刷爆）；
// WriteString 的错误被完全忽略——磁盘满/句柄失效时审计静默丢失，
// "全程留痕"成了幻觉，而这件事本身无场景、无人知。
// 现在：warn + 升级提示（alertDispatch），5 分钟冷却内只出一次；
// **不阻断业务**——审计是观察者不是执行者，不能因为记不上账就把活停了
//（plan-v3.0 负向场景"审计存储故障"的验收口径即此：业务不阻断 +
// 有 warn 与升级提示，且审计缺失本身被记录为事故）。

var (
	auditFailMu   sync.Mutex
	auditFailLast time.Time
)

const auditFailCooldown = 5 * time.Minute

// auditFailEscalate 升级动作。抽成变量供单测替换（真实实现发通知）。
//
// ⚠️ 为什么真实实现放在 init() 里赋值而不是直接写在 var 初始化器上：
// alertDispatch → sendWebhookAlert → auditStart → auditWrite → auditFailNotice
// → auditFailEscalate 是一条静态环——直接写初始化器会被 Go 判
// "initialization cycle"（编译不过）。放 init() 里赋值即可断环：
// 初始化期它是空操作，而 init() 跑完才可能有任何业务调用它。
var auditFailEscalate = func(why string) {}

func init() {
	auditFailEscalate = func(why string) {
		alertDispatch("EnvKit 审计写入失败", why)
	}
}

// auditFailNotice 审计写失败的统一出口：warn + 升级，冷却内只出一次。
func auditFailNotice(why string) {
	auditFailMu.Lock()
	now := time.Now()
	quiet := now.Sub(auditFailLast) < auditFailCooldown
	if !quiet {
		auditFailLast = now
	}
	auditFailMu.Unlock()
	if quiet {
		return // 冷却期内静默：审计写失败时每条操作都来一遍提醒就是刷爆
	}
	warn(scSys, "审计", "%s", why)
	auditFailEscalate(why)
}

// auditNow 立即记录一条已完成的操作。
func auditNow(actor, action, target, params, result, detail string) {
	auditWrite(AuditEntry{
		Ts:     time.Now().Format("2006-01-02 15:04:05.000"),
		Actor:  actor,
		Action: action,
		Target: target,
		Params: auditSanitize(params),
		Result: result,
		Detail: auditSanitize(detail),
	})
	// v2.3 N1：用户主动操作且成功 → 可能是"接手 AI 搞不定的动作"。
	// 挂在 auditWrite 上是唯一的入口级做法——现有几十个调用点一个都不用改，
	// 漏网的风险从"靠每个调用点自觉"降为"不可能漏"。
	// 真正的判定在 humanFix 里（还要过 trace 上下文、动作同类、AI 失败过三关）。
	if actor == actUser {
		humanFix(policyActionKey(action, target), target, result)
	}
	// v2.3 N2：成败计数。成功也要记——只记失败的话，
	// "这个做法不行"一旦写进经验就永远不会撤销，哪怕后来证明它其实是通的。
	// 计数只在动作已有对应经验时才写（lessonIDFor 内部过滤），
	// 所以不是每条审计都产生一次磁盘写。
	if id := lessonIDFor(action, target); id != "" {
		recordOutcome(id, action, result)
	}
}

// confirmReason 按界面语言返回二次确认文案（英文界面不能弹中文）。
func confirmReason(lang, zh, en string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(lang)), "en") {
		return en
	}
	return zh
}

// auditStart 记录一条开始执行的操作，返回 finish 回调（自动计算耗时）。
// 用法：defer auditStart(actUser, "apply_sql", db, path)("ok", "")
func auditStart(actor, action, target, params string) func(result, detail string) {
	start := time.Now()
	return func(result, detail string) {
		auditWrite(AuditEntry{
			Ts:     time.Now().Format("2006-01-02 15:04:05.000"),
			Actor:  actor,
			Action: action,
			Target: target,
			Params: auditSanitize(params),
			Result: result,
			Detail: auditSanitize(detail),
			DurMs:  time.Since(start).Milliseconds(),
		})
		metAudit(actor, action, result)
		// v2.3 N2：与 auditNow 同一个理由——真实写操作走的是这条路径，
		// 不在这里计数就等于"备份/还原这类关键动作的成败不计入置信度"。
		if id := lessonIDFor(action, target); id != "" {
			recordOutcome(id, action, result)
		}
	}
}

// ---------- 脱敏 ----------
// auditSanitize 把配置中的密钥从任意字符串里抹掉，避免"审计记录了密码"这种反效果。
func auditSanitize(s string) string {
	if s == "" {
		return ""
	}
	cfgMu.Lock()
	secrets := []string{
		cfg.MySQL.Password,
		cfg.Projects.MySQLPass,
		cfg.Chain.SSHPassword,
	}
	if cfg.AI != nil {
		secrets = append(secrets, cfg.AI.APIKey)
	}
	cfgMu.Unlock()
	for _, sec := range secrets {
		if len(sec) >= 3 {
			s = strings.ReplaceAll(s, sec, "***")
		}
	}
	if len(s) > 500 {
		s = s[:500] + "…(截断)"
	}
	return s
}

// readAuditFile 读一份审计文件（每行一条 JSON）。坏行跳过——审计文件可能被人为编辑过，
// 读不出来也不能让查看页整个打不开。
func readAuditFile(p string) []AuditEntry {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	out := make([]AuditEntry, 0)
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var e AuditEntry
		if json.Unmarshal([]byte(ln), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// auditDir 审计目录的来源。抽成变量是为了让单测在临时目录里造数据，
// 而不是往用户真实的审计文件里写测试记录。
var auditDir = exeDir

// auditDays 列出有审计记录的日期，倒序（新在前）。
func auditDays() []string {
	entries, err := os.ReadDir(auditDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "audit-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		if d := strings.TrimSuffix(strings.TrimPrefix(name, "audit-"), ".jsonl"); len(d) == 8 {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}

// auditQuery 按天与主体查审计，返回最新的 n 条（新在前）。
// day 为空或 "all" 时跨天合并——默认只读今天的话，刚启动的当天往往空空如也。
func auditQuery(day, actor string, n int) []AuditEntry {
	if n <= 0 {
		n = 200
	}
	days := auditDays()
	if day != "" && day != "all" {
		days = []string{day}
	}
	// 先按日期正序（旧→新）拼接，这样"取尾部 n 条"才等于"取最新的 n 条"
	for i, j := 0, len(days)-1; i < j; i, j = i+1, j-1 {
		days[i], days[j] = days[j], days[i]
	}
	var all []AuditEntry
	dir := auditDir()
	for _, d := range days {
		all = append(all, readAuditFile(filepath.Join(dir, "audit-"+d+".jsonl"))...)
	}
	if actor != "" {
		kept := make([]AuditEntry, 0, len(all))
		for _, e := range all {
			if strings.EqualFold(e.Actor, actor) {
				kept = append(kept, e)
			}
		}
		all = kept
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	// 反转成新在前——页面从上往下看符合直觉
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	if all == nil {
		all = []AuditEntry{}
	}
	return all
}

// auditTail 读取最近 n 条审计记录（供诊断报告用）。
func auditTail(n int) []AuditEntry {
	if n <= 0 {
		n = 50
	}
	all := readAuditFile(auditPath())
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}

// handleAudit 审计查看接口。只读，不落审计——否则"看一眼"也会把审计刷爆。
func handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	writeJSON(w, map[string]any{
		"ok":      true,
		"days":    auditDays(),
		"entries": auditQuery(r.URL.Query().Get("day"), strings.TrimSpace(r.URL.Query().Get("actor")), n),
	})
}

// cleanupOldAudits 清理过期审计文件（与业务日志分开保留策略）。
// 保留天数从 v3.4 起由 dev 段配置（audit_retention_days，默认 30）。
func cleanupOldAudits() {
	dir := exeDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -devSnap().auditRetentionDays())
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "audit-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		datePart := strings.TrimSuffix(strings.TrimPrefix(name, "audit-"), ".jsonl")
		t, perr := time.Parse("20060102", datePart)
		if perr != nil || !t.Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// auditSummary 给诊断报告用：最近审计条目的单行摘要。
func auditSummary(n int) []string {
	es := auditTail(n)
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, fmt.Sprintf("%s [%s] %s %s → %s%s",
			e.Ts, e.Actor, e.Action, e.Target, e.Result,
			func() string {
				if e.Detail != "" {
					return "（" + e.Detail + "）"
				}
				return ""
			}()))
	}
	return out
}

// ---------- AI 工具：读操作审计 ----------

// AI 一次能看多少条审计。与 db_query 同一思路：默认值够日常回答，
// 硬上限防止模型要「全部」把上下文撑爆。
const (
	aiAuditMaxLines    = 100
	aiAuditDefaultLine = 20
)

// aiAuditActorAlias 主体别名表。模型（或用户转述）可能给中文、大写、
// 口语词；不折算的话筛出来是空结果，模型会以为「守护从来没干活过」——
// **把「查不到」误会成「没发生过」比没有工具更坏**。
var aiAuditActorAlias = map[string]string{
	"user": actUser, "用户": actUser,
	"ai": actAI, "助手": actAI,
	"guard": actGuard, "守护": actGuard,
	"system": actSys, "系统": actSys,
	// v3.0 T2.8：影子评估。**必须能按主体筛出来**——
	// 晋升评审的第一手材料就是这些记录，筛不到等于没有。
	"shadow": actShadow, "影子": actShadow,
}

// aiAuditDay 把模型给的日期折算成 YYYYMMDD。
// 接受 20261017 / 2026-10-17 / 2026/10/17，以及今天 / 昨天 这类口语词；
// 认不出的必须报错——静默按「全部」处理会让模型以为查过了。
func aiAuditDay(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "":
		return "", nil // 空 = 跨天取最近（auditQuery 的口径）
	case "today", "今天":
		return time.Now().Format("20060102"), nil
	case "yesterday", "昨天":
		return time.Now().AddDate(0, 0, -1).Format("20060102"), nil
	}
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	if len(digits) != 8 {
		return "", fmt.Errorf("日期无法识别：%q（用 YYYYMMDD，如 20261017；也接受 2026-10-17 / 今天 / 昨天；留空取最近）", s)
	}
	if _, err := time.Parse("20060102", digits); err != nil {
		return "", fmt.Errorf("日期不存在：%q（用 YYYYMMDD，如 20261017）", s)
	}
	return digits, nil
}

func init() {
	aiToolRegistry["get_audit_log"] = aiTool{
		Desc: "读取操作审计记录——也就是界面上「操作审计」页的数据：谁、何时、做了什么、结果如何（含复验结论）。" +
			"用于回答「最近做了哪些操作」「守护自动恢复过吗」「有没有失败的操作」「某天发生了什么」这类问题。" +
			"只读，不会修改任何数据，因此无需你确认。" +
			"注意：①day 留空=跨天取最近若干条，指定某天用 YYYYMMDD（也接受 2026-10-17 / 今天 / 昨天）；" +
			"②actor 可只留 user(用户手动) / ai(助手执行) / guard(守护自动) / system(系统) / shadow(影子评估，只记录不执行) 某一类；" +
			"③审计只保留 30 天，更早的没有记录，别反复找。",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"day":   map[string]any{"type": "string", "description": "哪一天：YYYYMMDD（如 20261017）、2026-10-17、今天/昨天。留空则跨天取最近若干条"},
			"actor": map[string]any{"type": "string", "description": "操作主体：user=用户手动，ai=AI 助手执行，guard=守护自动恢复，system=系统。留空=全部"},
			"lines": map[string]any{"type": "integer", "description": "最近多少条，默认 20，上限 100"},
		}, "required": []string{}},
		Execute: func(args map[string]any) (string, error) {
			day, _ := args["day"].(string)
			actor, _ := args["actor"].(string)
			n := aiAuditDefaultLine
			if v, ok := args["lines"].(float64); ok && v > 0 {
				n = int(v)
			}
			if n > aiAuditMaxLines {
				n = aiAuditMaxLines
			}
			d, err := aiAuditDay(day)
			if err != nil {
				return "", err
			}
			a := strings.ToLower(strings.TrimSpace(actor))
			if a != "" {
				mapped, ok := aiAuditActorAlias[a]
				if !ok {
					return "", fmt.Errorf("未知的操作主体：%q（可选 user / ai / guard / system，留空=全部）", actor)
				}
				a = mapped
			}
			// 查询本身也留痕（同 db_query 口径：AI 替用户查过什么要能回答）。
			// 页面刷新不落审计是怕「看一眼」刷爆文件；工具调用有预算上限，量级不同。
			fin := auditStart(actAI, "audit_query", "day="+d+" actor="+a, fmt.Sprintf("n=%d", n))
			out, cnt := aiAuditText(d, a, n)
			fin(resOK, fmt.Sprintf("返回 %d 条", cnt))
			// 详情列可能带第三方输出与用户入参，按不可信数据包裹（配合系统提示词第 1 条）
			return "<untrusted_data>\n" + out + "\n</untrusted_data>", nil
		},
	}
}

// aiAuditText 把审计记录整理成模型可读的紧凑文本，返回 (文本, 条数)。
//
// 为什么不让模型用 read_file 直接读 audit-*.jsonl：
// ① 审计文件在 exe 同级，探索工具被限制在项目目录内，根本够不着；
// ② JSONL 一行一个嵌套对象，模型逐行解析容易漏字段，还常拉回整个文件。
// 这里按 auditQuery 的口径（新在前、可按天/主体筛）一条一行输出。
//
// 词汇与「操作审计」页保持一致：result 用原始值（ok/fail/denied/started/
// unverified），图例在头部给全——模型转述给用户时不会造出页面上不存在的词。
func aiAuditText(day, actor string, n int) (string, int) {
	if n <= 0 {
		n = aiAuditDefaultLine
	}
	if n > aiAuditMaxLines {
		n = aiAuditMaxLines
	}
	days := auditDays()
	var b strings.Builder
	if len(days) == 0 {
		return "目前没有任何审计记录（审计随操作实时积累，只保留 30 天）。", 0
	}
	fmt.Fprintf(&b, "有审计记录的日期（新在前，只保留 30 天）：%s\n", strings.Join(days, "、"))
	b.WriteString("图例：主体 user=用户手动 ai=助手执行 guard=守护自动 system=系统 shadow=影子评估（只记录不执行）；" +
		"结果 ok=成功 fail=失败 denied=被拦下 started=已启动 unverified=执行了但未证明恢复\n")
	es := auditQuery(day, actor, n)
	if len(es) == 0 {
		b.WriteString("该条件下没有记录。")
		return b.String(), 0
	}
	fmt.Fprintf(&b, "以下是最新 %d 条（时间 | 主体 | 动作 | 目标 | 结果 | 详情/复验）：\n", len(es))
	for _, e := range es {
		line := e.Ts + " | " + e.Actor + " | " + e.Action
		if e.Target != "" {
			line += " | " + e.Target
		}
		line += " | " + e.Result
		det := e.Detail
		if e.Verify != "" {
			if det != "" {
				det += "；" + e.Verify
			} else {
				det = e.Verify
			}
		}
		if det != "" {
			line += " | " + det
		}
		b.WriteString(line + "\n")
	}
	return b.String(), len(es)
}
