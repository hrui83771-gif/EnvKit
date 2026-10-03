package main

// lesson.go —— v2.0 M1：经验沉淀（从审计流计算的记忆层）
//
// 回答用户四问中的第二次"Agent 有没有必要从第一次学到东西"。
//
// 三个关键设计决策，动手前先想清楚，改了会连带失效：
//
// 1) **学失败模式，不学成功路径。**
//    成功路径下次就会失效（IP 变了、版本升级、依赖更新），记住"上次这么干成功了"
//    下次照样撞墙。失败模式才是结构性的、可复用的知识。
//
// 2) **经验 = 审计的一个视图，不是一份新存储。**
//    不落盘、不进 config.json。理由有三：零状态不一致风险（不会与审计对不上）；
//    审计 30 天自动清理 → 经验自动过期，而"过期的经验本来就该忘"正是我们要的语义；
//    每条经验天然能追溯到具体审计条目，可解释性不用额外做。
//
// 3) **提取必须用确定性规则，不用 LLM。**
//    审计是结构化日志，规则提取成本为零、结果可复现、**不会编造**。
//    很多"AI 学习"项目栽在用模型归纳日志——那是让幻觉有了写入权限。
//    只有"给人看的措辞"才可能需要润色，而那一步交给用户在页面上改。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Lesson 一条经验。
type Lesson struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`     // repeat_fail | user_fix | denied | slow | verify_fail
	Title    string   `json:"title"`    // 一句话结论（进提示词的就是这句）
	Trigger  string   `json:"trigger"`  // 命中条件：动作 + 目标
	Precheck string   `json:"precheck"` // 建议的动作前预检（M2 用）
	Evidence []string `json:"evidence"` // 支撑证据（审计时间+动作+结果），供人核对
	Hits     int      `json:"hits"`
	LastHit  string   `json:"last_hit"`
	Enabled  bool     `json:"enabled"` // 用户可否决；否决状态外部持久化（见 lessonOffFile）
}

// 经验类型
const (
	lsRepeatFail = "repeat_fail" // 同一件事反复失败
	lsUserFix    = "user_fix"    // AI 试错 → 用户手动修好了（信息量最大）
	lsDenied     = "denied"      // 反复被安全策略拒绝
	lsSlow       = "slow"        // 这条路特别慢
	lsVerifyFail = "verify_fail" // 复验反复同一归类
)

// 经验提取的阈值。刻意保守：宁可少提取，也不要给出一堆噪声污染上下文。
const (
	lsRepeatMin  = 3 // 同一 (action,target) 失败次数达到此值才算"反复"
	lsDeniedMin  = 2 // 同一路径被拒次数
	lsSlowFactor = 3 // 耗时超过同动作中位数的倍数
	lsSlowMinN   = 3 // 慢路径判定所需的最少样本
	lsVerifyMin  = 2 // 同一 err_kind 出现次数
	lsMaxScan    = 2000
	lsMaxEvid    = 4 // 每条经验最多带几条证据
)

// userFixWindow R2 判定窗口：AI 连续失败后，多久以内出现用户成功算"人修好了"。
const userFixWindow = 30 * time.Minute

// ---------- 缓存 ----------

type lessonCache struct {
	sig     string
	at      time.Time
	lessons []Lesson
}

var (
	lessonMu     sync.Mutex
	lessonCacheV *lessonCache
)

// 提取是纯计算但要读多个审计文件（几 MB），缓存到下次审计有写入为止。
const lessonCacheTTL = 2 * time.Minute

// lessonSig 用审计文件的大小做缓存键：只看元数据，不读内容。
// 有新审计写入 → 大小变化 → 缓存失效。免去维护"第几条"的复杂度。
func lessonSig() string {
	days := auditDays()
	if len(days) == 0 {
		return "empty"
	}
	var sb strings.Builder
	for _, d := range days {
		st, err := os.Stat(filepath.Join(auditDir(), "audit-"+d+".jsonl"))
		if err != nil {
			continue
		}
		sb.WriteString(d)
		sb.WriteString(":")
		sb.WriteString(strconv.FormatInt(st.Size(), 10))
		sb.WriteString(";")
	}
	return sb.String()
}

// ---------- 用户的否决状态 ----------
//
// 经验正文是审计的纯视图（不可编辑，否则等于给"伪造证据骗 AI"开口子），
// 但用户的否决是外部状态，必须持久化——单独存一张"已否决 id"表。
// 抽成变量供单测替换，否则测试会写到用户真实的 memories.json 旁边。

var lessonOffFile = func() string { return filepath.Join(exeDir(), "lessons-off.json") }

var lessonOffMu sync.Mutex

func loadLessonOff() map[string]bool {
	b, err := os.ReadFile(lessonOffFile())
	if err != nil {
		return map[string]bool{}
	}
	m := map[string]bool{}
	_ = json.Unmarshal(b, &m)
	return m
}

func saveLessonOff(m map[string]bool) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := lessonOffFile() + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, lessonOffFile())
}

// lessonToggle 翻转一条经验的启用状态。
// 返回 (新的启用状态, 经验是否存在)。id 不存在时返回 (false,false)，
// 调用方据此回 404——把"不存在"和"已禁用"分成两个返回值，避免语义含混。
func lessonToggle(id string) (enabled, ok bool) {
	lessonOffMu.Lock()
	defer lessonOffMu.Unlock()
	m := loadLessonOff()
	if _, in := m[id]; !in {
		// 只有当前确实存在的经验才允许写入否决表，否则界面上的陈年条目会污染它
		exists := false
		for _, l := range lessonsFor() {
			if l.ID == id {
				exists = true
				break
			}
		}
		if !exists {
			return false, false
		}
	}
	if m[id] {
		delete(m, id)
	} else {
		m[id] = true
	}
	if err := saveLessonOff(m); err != nil {
		return false, true
	}
	return !m[id], true
}

// lessonsFor 返回当前经验（带缓存）。
func lessonsFor() []Lesson {
	sig := lessonSig()
	lessonMu.Lock()
	if c := lessonCacheV; c != nil && c.sig == sig && time.Since(c.at) < lessonCacheTTL {
		out := c.lessons
		lessonMu.Unlock()
		return out
	}
	lessonMu.Unlock()

	ls := extractLessons(auditQuery("all", "", lsMaxScan))
	off := loadLessonOff()
	for i := range ls {
		ls[i].Enabled = !off[ls[i].ID]
	}

	lessonMu.Lock()
	lessonCacheV = &lessonCache{sig: sig, at: time.Now(), lessons: ls}
	lessonMu.Unlock()
	return ls
}

// lessonInvalidate 清缓存。写操作（用户开关、改记忆）后要调，否则界面上改了不生效。
func lessonInvalidate() {
	lessonMu.Lock()
	lessonCacheV = nil
	lessonMu.Unlock()
}

// ---------- 提取 ----------

func extractLessons(entries []AuditEntry) []Lesson {
	if len(entries) == 0 {
		return []Lesson{}
	}
	// auditQuery 返回新在前；提取需要时间正序（判断"先失败后成功"）
	asc := make([]AuditEntry, len(entries))
	copy(asc, entries)
	for i, j := 0, len(asc)-1; i < j; i, j = i+1, j-1 {
		asc[i], asc[j] = asc[j], asc[i]
	}

	byAction := map[string][]int{} // "action\x00target" → 下标
	for i, e := range asc {
		k := e.Action + "\x00" + e.Target
		byAction[k] = append(byAction[k], i)
	}
	byDeniedPath := map[string][]int{} // 被拒的路径 → 下标
	for i, e := range asc {
		if e.Actor != actAI || !strings.Contains(e.Action, "denied") {
			continue
		}
		// 路径在 detail 里，target 存的是工具名（aiExploreDenied 的调用约定）。
		// 早先直接用 target，结果生成的是「别再试 read_file」这种毫无信息量的经验。
		p := deniedPath(e)
		if p == "" {
			continue
		}
		byDeniedPath[p] = append(byDeniedPath[p], i)
	}

	var out []Lesson
	seen := map[string]bool{} // 同一条不重复生成

	add := func(l Lesson) {
		if l.ID == "" || seen[l.ID] {
			return
		}
		seen[l.ID] = true
		out = append(out, l)
	}

	// ---- R1 重复失败 ----
	for k, idx := range byAction {
		fails := 0
		var ev []string
		var last string
		for _, i := range idx {
			e := asc[i]
			if isFailResult(e.Result) {
				fails++
				ev = append(ev, evLine(e))
				last = e.Ts
			}
		}
		if fails < lsRepeatMin {
			continue
		}
		action, target := splitKey(k)
		add(Lesson{
			ID: lsID(lsRepeatFail, action, target), Kind: lsRepeatFail,
			Title: fmt.Sprintf("「%s」在目标 %s 上已经失败 %d 次，别再原样重试——先确认前提是否变了",
				action, displayTarget(target), fails),
			Trigger:  action + " @ " + displayTarget(target),
			Precheck: precheckFor(action),
			Evidence: lastN(ev, lsMaxEvid), Hits: fails, LastHit: last,
		})
	}

	// ---- R2 AI 试错 → 用户手动修好 ----
	// 这是信息量最大的一条规则：审计里同时记着 AI 的尝试和用户的成功操作，
	// 这个对比是纯 Agent 框架拿不到的（它们的日志没有"用户手动介入"这一维）。
	for i := 0; i < len(asc)-1; i++ {
		e := asc[i]
		if e.Actor != actAI || !isFailResult(e.Result) {
			continue
		}
		// 向后找同 action 的第一次成功，且必须是 user/guard 做的
		for j := i + 1; j < len(asc) && j <= i+8; j++ {
			u := asc[j]
			if u.Action != e.Action {
				continue
			}
			if u.Actor == actAI {
				break // AI 连续在同一件事上打转，没出现"人接手"
			}
			if u.Actor != actUser && u.Actor != actGuard {
				break
			}
			if !isOKResult(u.Result) {
				break
			}
			if !withinFixWindow(e.Ts, u.Ts) {
				break
			}
			action := e.Action
			add(Lesson{
				ID: lsID(lsUserFix, action, e.Target), Kind: lsUserFix,
				Title: fmt.Sprintf("你上次让 AI 处理「%s」失败了，随后由你手动处理并成功（%s）——"+
					"同类问题先问你自己的做法，别让 AI 重复踩", action, u.Ts),
				Trigger:  action + " @ " + displayTarget(e.Target),
				Precheck: "ask_user_first",
				Evidence: []string{evLine(e), evLine(u)}, Hits: 1, LastHit: u.Ts,
			})
			break
		}
	}

	// ---- R3 反复被拒（能纠正 AI 自身的行为模式） ----
	for path, idx := range byDeniedPath {
		if len(idx) < lsDeniedMin {
			continue
		}
		var ev []string
		var last string
		for _, i := range idx {
			ev = append(ev, evLine(asc[i]))
			last = asc[i].Ts
		}
		add(Lesson{
			ID: lsID(lsDenied, path, ""), Kind: lsDenied,
			Title: fmt.Sprintf("AI 已尝试 %d 次访问 %s 并被安全策略拒绝——别再试了，它按设计不可读",
				len(idx), displayTarget(path)),
			Trigger:  "read " + displayTarget(path),
			Precheck: "none",
			Evidence: lastN(ev, lsMaxEvid), Hits: len(idx), LastHit: last,
		})
	}

	// ---- R4 慢路径 ----
	durs := map[string][]int64{}
	for _, e := range asc {
		if e.DurMs > 0 && isOKResult(e.Result) {
			durs[e.Action] = append(durs[e.Action], e.DurMs)
		}
	}
	for action, v := range durs {
		if len(v) < lsSlowMinN {
			continue
		}
		s := append([]int64(nil), v...)
		sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
		median := s[len(s)/2]
		var slow []string
		var worst int64
		var last string
		for _, e := range asc {
			if e.Action == action && e.DurMs > median*lsSlowFactor && isOKResult(e.Result) {
				slow = append(slow, evLine(e))
				if e.DurMs > worst {
					worst = e.DurMs
				}
				last = e.Ts
			}
		}
		if len(slow) < 2 {
			continue
		}
		add(Lesson{
			ID: lsID(lsSlow, action, ""), Kind: lsSlow,
			Title: fmt.Sprintf("「%s」正常耗时约 %dms，但出现过 %dms 的慢路径——执行前先想清楚是不是选错方式了",
				action, median, worst),
			Trigger:  action,
			Precheck: "none",
			Evidence: lastN(slow, lsMaxEvid), Hits: len(slow), LastHit: last,
		})
	}

	// ---- R5 复验反复同一归类 ----
	//
	// 只看 result=fail 的条目。踩过的坑：第一版不看 result，直接去 verify 文本里
	// 找方括号/冒号抠错误码，结果把「已复验通过：链端已恢复且共识在推进」这种
	// **成功**结论也当成了失败模式——于是 AI 每次都被告知"这类操作重试没用"，
	// 而实际上它成功了。判断成败有现成的 result 字段，不该去解析中文文本。
	verr := map[string][]int{}
	for i, e := range asc {
		if e.Result != resFail || e.Verify == "" {
			continue
		}
		kind := verifyErrKind(e.Verify)
		if kind == "" {
			continue
		}
		k := e.Action + "\x00" + kind
		verr[k] = append(verr[k], i)
	}
	for k, idx := range verr {
		if len(idx) < lsVerifyMin {
			continue
		}
		action, kind := splitKey(k)
		var ev []string
		var last string
		for _, i := range idx {
			ev = append(ev, evLine(asc[i]))
			last = asc[i].Ts
		}
		add(Lesson{
			ID: lsID(lsVerifyFail, action, kind), Kind: lsVerifyFail,
			Title: fmt.Sprintf("「%s」的复验已 %d 次以 %s 失败——这类问题重试同一个操作没用，要换思路",
				action, len(idx), kind),
			Trigger:  action + " → " + kind,
			Precheck: precheckFor(action),
			Evidence: lastN(ev, lsMaxEvid), Hits: len(idx), LastHit: last,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Hits != out[j].Hits {
			return out[i].Hits > out[j].Hits
		}
		return out[i].LastHit > out[j].LastHit
	})
	if out == nil {
		out = []Lesson{}
	}
	return out
}

// precheckFor 给 M2 用：这类动作执行前应该先查什么。
func precheckFor(action string) string {
	switch {
	case strings.Contains(action, "start_service") || action == "start":
		return "verify_environment:web|backend"
	case strings.Contains(action, "db_backup") || action == "backup":
		return "verify_environment:db"
	case strings.Contains(action, "chain"):
		return "verify_environment:chain"
	default:
		return ""
	}
}

// ---------- 相关性匹配与注入 ----------

// lessonMatchScore 粗粒度相关性：任务描述里出现动作名或目标就算相关。
// 刻意不用分词与向量——这里的召回率要求不高，而误召回会污染上下文。
func lessonMatchScore(l Lesson, task string) int {
	if strings.TrimSpace(task) == "" {
		return 0
	}
	t := strings.ToLower(task)
	score := 0
	action := strings.ToLower(l.Trigger)
	if i := strings.IndexAny(action, " @→"); i > 0 {
		action = action[:i]
	}
	if action != "" && strings.Contains(t, action) {
		score += 3
	}
	// 中文任务里通常不含英文动作名，退化为看 kind 的中文线索
	if score == 0 {
		for _, kw := range kindKeywords(l.Kind) {
			if strings.Contains(t, kw) {
				score++
			}
		}
	}
	return score
}

func kindKeywords(kind string) []string {
	switch kind {
	case lsDenied:
		return []string{"读取", "文件", "配置", "读一下"}
	case lsSlow:
		return []string{"慢", "卡", "超时"}
	case lsUserFix:
		return []string{"失败", "不行", "坏了", "启动不了"}
	default:
		return nil
	}
}

// lessonsForTask 返回与当前任务最相关的 n 条（0 = 不限）。
// 全量注入会让上下文爆炸——5 条规则产出的经验通常 90% 与当前任务无关。
func lessonsForTask(task string, n int) []Lesson {
	all := lessonsFor()
	if n <= 0 || len(all) <= n {
		return all
	}
	type scored struct {
		l     Lesson
		score int
	}
	var hits []scored
	for _, l := range all {
		if s := lessonMatchScore(l, task); s > 0 {
			hits = append(hits, scored{l, s})
		}
	}
	if len(hits) == 0 {
		return all[:n]
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].l.Hits > hits[j].l.Hits
	})
	out := make([]Lesson, 0, n)
	for i, h := range hits {
		if i >= n {
			break
		}
		out = append(out, h.l)
	}
	return out
}

// lessonBrief 注入提示词用的极简形态：每条只给标题（≤60 字），证据不进上下文。
func lessonBrief(ls []Lesson) string {
	if len(ls) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, l := range ls {
		t := l.Title
		if len([]rune(t)) > 60 {
			t = string([]rune(t)[:60]) + "…"
		}
		sb.WriteString("- ")
		sb.WriteString(t)
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// ---------- 工具函数 ----------

// deniedPathRe 从拒绝详情里抠出被拒的路径。
// 详情形如「拒绝读取：路径不在已配置的项目目录内（D:\BCGD\FarmTrace）」或
// 「(该文件因安全策略不可读)：D:\x\config.json」，两种都要能取到路径。
var (
	winPathRe = regexp.MustCompile(`[A-Za-z]:\\[^\s，。）)]+`)
)

// deniedPath 取被拒的路径。取不到就返回空——宁可少一条经验，
// 也不要把「别再试 read_file」这种没有信息量的结论喂给模型。
func deniedPath(e AuditEntry) string {
	if p := winPathRe.FindString(e.Detail); p != "" {
		return p
	}
	// 兜底：详情里可能直接写了文件名
	if e.Detail != "" && !strings.Contains(e.Detail, "：") {
		return ""
	}
	return ""
}

func lsID(kind, action, target string) string {
	return kind + "|" + action + "|" + target
}

func splitKey(k string) (string, string) {
	i := strings.Index(k, "\x00")
	if i < 0 {
		return k, ""
	}
	return k[:i], k[i+1:]
}

func evLine(e AuditEntry) string {
	return e.Ts + " [" + e.Actor + "] " + e.Action + " → " + e.Result
}

func lastN(v []string, n int) []string {
	if len(v) <= n {
		return v
	}
	return v[len(v)-n:]
}

func isFailResult(r string) bool {
	return r == resFail || r == resDenied
}

func isOKResult(r string) bool {
	return r == resOK
}

// verifyErrKind 从复验结论里抠出错误归类（如 verify_fail / spawn_fail）。
// 只认 OpResult.String() 失败分支的固定前缀「失败[kind]：」——那是唯一可靠的锚点。
// 早期版本还会去匹配中文冒号后的内容，结果把成功结论的正文也当成错误码。
var verifyErrRe = regexp.MustCompile(`失败\[([a-z_]+)\]`)

func verifyErrKind(v string) string {
	if m := verifyErrRe.FindStringSubmatch(v); m != nil {
		return m[1]
	}
	return ""
}

func withinFixWindow(failTS, okTS string) bool {
	t1, e1 := time.ParseInLocation("2006-01-02 15:04:05.000", strings.TrimSpace(failTS), time.Local)
	t2, e2 := time.ParseInLocation("2006-01-02 15:04:05.000", strings.TrimSpace(okTS), time.Local)
	if e1 != nil || e2 != nil {
		return false
	}
	d := t2.Sub(t1)
	return d >= 0 && d <= userFixWindow
}

func displayTarget(t string) string {
	if t == "" {
		return "(未指定)"
	}
	if len([]rune(t)) > 40 {
		return string([]rune(t)[:40]) + "…"
	}
	return t
}

// lessonsJSON 供快照注入与前端使用。
func lessonsJSON(task string, n int) string {
	ls := lessonsForTask(task, n)
	if len(ls) == 0 {
		return ""
	}
	b, err := json.Marshal(ls)
	if err != nil {
		return ""
	}
	return string(b)
}
