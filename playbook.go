package main

// playbook.go —— Agent 闭环 Phase 2：预案库（Playbook）。
//
// 设计文档：docs/DESIGN-agent-loop.md 3.1。
//
// 与 lesson.go 的分工（防重复建设，最重要的一条）：
//   - Lesson   = **一句话经验**，给模型看的提示词素材，自由文本，靠语义相似匹配；
//   - Playbook = **结构化动作序列 + 匹配键 + 复验统计**，给系统匹配、给模型照做。
//     两者不是替代关系：Playbook.LessonIDs 反向引用 Lesson（注入提示词用）。
//
// 三条硬约束（与 Phase 1 红线同源）：
//  1. 套预案只省「推理怎么修」，**不省确认**：本文件不提供任何执行入口，
//     写步骤照常走 aiConfirmGate + PolicyGate（红线 2）；
//  2. 成功只认复验：Stats.Succ 只在 mission 以 verify 通过收口时 +1（红线 1），
//     执行完没复验一律记 fail —— 宁可低估，不发假徽章；
//  3. 匹配必须**确定性**：同样的输入永远给同一个答案，且并列时有显式排序规则
//     （分数 → 成功率 → ID 字典序）。否则「为什么这次推 A 不推 B」永远说不清，
//     而说不清的推荐等于不可审计的推荐。
//
// 锁序（与 mission.go 的 msMu 互不嵌套 —— 两把锁各自独立获取）：
//   - 持 pbkMu 时**绝不**取 msMu；持 msMu 时**绝不**取 pbkMu。
//   - 需要跨两者时，先释放一把再取另一把（见 pbkFromMission / aiMissionSettle）。

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

// ===== 状态与常量 =====

const (
	pbkActive  = "active"  // 正常可推荐
	pbkReview  = "review"  // 待复审：连续复验未通过达限，不再推荐套用
	pbkRetired = "retired" // 人工停用：永不推荐，保留历史
)

const (
	// pbkMaxCount 文件里最多保留的预案数（防无限增长；先淘汰 retired，再淘汰最久未用的）。
	pbkMaxCount = 100
	// pbkMaxSteps 单个预案最多步骤数：再多就不是「省推理」而是「照抄一篇文章」。
	pbkMaxSteps = 20
	// pbkReviewStreak 连续几次复验未通过进 review（设计 3.1 定案 3 次）。
	pbkReviewStreak = 3
)

// ===== 实体 =====

// PlaybookMatcher 触发特征（结构化，供触发器匹配）。
//
// 语义是 **AND 门槛 + 加分**，刻意不用模糊打分：
//   - Services / ErrKinds / Triggers 只要有值就必须命中，否则直接排除；
//   - Keywords 有值时必须至少命中一个，命中越多分越高（上限 3）；
//   - 四项全空的 matcher **永不匹配**（否则它会吞掉所有告警，而用户看不出为什么）。
//
// 为什么不做「任一项命中即可」：一个「服务=backend」的预案被拿去修链端，
// 比「没匹配到、走完整 AI 诊断」糟得多 —— 后者只是慢，前者是错。
type PlaybookMatcher struct {
	Services []string `json:"services,omitempty"`  // 服务 ID（web/backend/mysql/chain/webase）
	ErrKinds []string `json:"err_kinds,omitempty"` // 症状类别（verify_fail/exit/…）
	Triggers []string `json:"triggers,omitempty"`  // 来源（guard/webhook/patrol/manual）
	Keywords []string `json:"keywords,omitempty"`  // 标题+详情里的关键词（大小写不敏感）
}

// PlaybookStep 一步动作模板。
//
// Args 是**展示与提示用的参数模板**（占位符 {svc} / {detail}），不是执行参数：
// Phase 2 的执行入口始终是「AI 回合 + 确认卡」，系统不直接拿它去调工具。
// 这么设计的原因很实在：一旦这里能直接执行，Playbook 就变成了绕过 PolicyGate
// 的第三条执行路径，红线 2 守不住。
type PlaybookStep struct {
	Tool   string            `json:"tool"`
	Target string            `json:"target,omitempty"`
	Why    string            `json:"why,omitempty"`
	Args   map[string]string `json:"args,omitempty"`
}

// PbkStats 执行统计。Succ 只认复验通过（红线 1）。
type PbkStats struct {
	Used       int    `json:"used"`
	Succ       int    `json:"succ"`
	Fail       int    `json:"fail"`
	Streak     int    `json:"streak"` // 连续复验未通过次数（成功的清零）
	LastUsedAt string `json:"last_used_at,omitempty"`
}

// Playbook 一个可复用的处置预案。
type Playbook struct {
	ID      string          `json:"id"`    // p-<时间>-<短随机>
	Title   string          `json:"title"` // 「后端反复崩溃：清端口→重启→复验」
	Matcher PlaybookMatcher `json:"matcher"`
	Steps   []PlaybookStep  `json:"steps"`
	// LessonIDs 关联经验（Phase 3 沉淀时回填；现在只做展示与提示注入）。
	LessonIDs []string `json:"lesson_ids,omitempty"`
	Stats     PbkStats `json:"stats"`
	State     string   `json:"state"`
	// SourceMission 人工沉淀的来源行动 ID（可追溯：这条预案是从哪次处置抄下来的）。
	SourceMission string    `json:"source_mission,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ===== 持久化 =====

// playbookFile 预案文件路径。抽成变量供单测替换（同 missionFile 手法）。
var playbookFile = func() string { return filepath.Join(exeDir(), "playbooks.json") }

var pbkMu sync.Mutex

// loadPlaybooks 读预案文件。坏条跳过而不是整体失败——文件可能被手工编辑。
func loadPlaybooks() []Playbook {
	b, err := os.ReadFile(playbookFile())
	if err != nil {
		return []Playbook{}
	}
	var out []Playbook
	if err := json.Unmarshal(b, &out); err != nil {
		// 与 missions.json 同策略：不返回半截数据，留 .bak 兜底
		return []Playbook{}
	}
	if out == nil {
		return []Playbook{}
	}
	return out
}

// savePlaybooks 原子写 + 备份。
func savePlaybooks(ps []Playbook) error {
	b, err := json.MarshalIndent(ps, "", " ")
	if err != nil {
		return err
	}
	p := playbookFile()
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	if _, err := os.Stat(p); err == nil {
		_ = os.Remove(p + ".bak")
		_ = os.Rename(p, p+".bak")
	}
	return os.Rename(tmp, p)
}

// pbkTrim 控总量：先淘汰 retired，再淘汰最久没用的，最后才动 active。
func pbkTrim(all *[]Playbook) error {
	for len(*all) > pbkMaxCount {
		idx := -1
		rank := func(p Playbook) int {
			switch p.State {
			case pbkRetired:
				return 0
			case pbkReview:
				return 1
			default:
				return 2
			}
		}
		for i, p := range *all {
			if idx < 0 {
				idx = i
				continue
			}
			ri, rj := rank(p), rank((*all)[idx])
			if ri < rj || (ri == rj && p.UpdatedAt.Before((*all)[idx].UpdatedAt)) {
				idx = i
			}
		}
		*all = append((*all)[:idx], (*all)[idx+1:]...)
	}
	return nil
}

// ===== 查询 =====

// pbkList 预案列表，新的在前。
func pbkList() []Playbook {
	pbkMu.Lock()
	defer pbkMu.Unlock()
	all := loadPlaybooks()
	sort.SliceStable(all, func(i, j int) bool { return all[i].UpdatedAt.After(all[j].UpdatedAt) })
	return all
}

// pbkGet 按 ID 取预案。
func pbkGet(id string) (Playbook, bool) {
	pbkMu.Lock()
	defer pbkMu.Unlock()
	for _, p := range loadPlaybooks() {
		if p.ID == id {
			return p, true
		}
	}
	return Playbook{}, false
}

// pbkUpdate 按 ID 变更一条预案（fn 内直接改），落盘。
func pbkUpdate(id string, fn func(*Playbook) error) (Playbook, error) {
	pbkMu.Lock()
	defer pbkMu.Unlock()
	all := loadPlaybooks()
	for i := range all {
		if all[i].ID != id {
			continue
		}
		p := &all[i]
		if err := fn(p); err != nil {
			return Playbook{}, err
		}
		p.UpdatedAt = time.Now()
		if err := pbkTrim(&all); err != nil {
			return Playbook{}, err
		}
		if err := savePlaybooks(all); err != nil {
			return Playbook{}, err
		}
		return *p, nil
	}
	return Playbook{}, fmt.Errorf("预案不存在：%s", id)
}

// ===== 匹配（纯函数，表驱动测试的落点）=====

// PlaybookQuery 一次匹配的输入。
//
// 全部来自**已有事实**：服务 ID、症状类别、触发来源、告警文本。
// 不让模型参与匹配（设计 3.1「机器可匹配的键」），否则推荐不可复现。
type PlaybookQuery struct {
	SvcID   string
	ErrKind string
	Trigger string
	Text    string // 标题 + 详情，关键词匹配用
}

// pbkScore 单个预案对一次查询的得分与是否匹配。返回值 ok=false 表示门槛没过。
//
// 打分是**权重常量**而不是"感觉"：服务 4 > 症状 2 > 来源 1 > 关键词每个 1（上限 3）。
// 排序时服务最重，因为它是最硬的定位信息（同一症状在 web 和 chain 上处置完全不同）。
func pbkScore(p Playbook, q PlaybookQuery) (int, bool) {
	m := p.Matcher
	score := 0
	groups := 0

	if len(m.Services) > 0 {
		groups++
		if !pbkHas(m.Services, q.SvcID) {
			return 0, false
		}
		score += 4
	}
	if len(m.ErrKinds) > 0 {
		groups++
		if !pbkHas(m.ErrKinds, q.ErrKind) {
			return 0, false
		}
		score += 2
	}
	if len(m.Triggers) > 0 {
		groups++
		if !pbkHas(m.Triggers, q.Trigger) {
			return 0, false
		}
		score += 1
	}
	if len(m.Keywords) > 0 {
		groups++
		text := strings.ToLower(q.Text)
		hits := 0
		for _, k := range m.Keywords {
			k = strings.ToLower(strings.TrimSpace(k))
			if k != "" && strings.Contains(text, k) {
				hits++
			}
		}
		if hits == 0 {
			return 0, false
		}
		if hits > 3 {
			hits = 3 // 关键词再多也只是加分项，不该盖过服务定位
		}
		score += hits
	}
	if groups == 0 {
		return 0, false // 全空 matcher：永不匹配
	}
	return score, true
}

// pbkPick 在候选里选最优（纯函数）。
//
// 并列规则（必须有，且必须写死）：
//  1. 分数高者优先；
//  2. 同分比成功率（Used=0 视为 0%，未验证的预案不该压过已验证的）；
//  3. 再同分比 ID 字典序（保证结果与文件顺序无关）。
func pbkPick(all []Playbook, q PlaybookQuery) (Playbook, int, bool) {
	var best Playbook
	bestScore := -1
	found := false
	for _, p := range all {
		if p.State != pbkActive {
			continue // review / retired 不再推荐
		}
		s, ok := pbkScore(p, q)
		if !ok {
			continue
		}
		if !found || s > bestScore ||
			(s == bestScore && pbkBetter(p, best)) {
			best, bestScore, found = p, s, true
		}
	}
	return best, bestScore, found
}

// pbkBetter a 是否优于 b（只在分数相同时调用）。
func pbkBetter(a, b Playbook) bool {
	ra, rb := pbkRate(a.Stats), pbkRate(b.Stats)
	if ra != rb {
		return ra > rb
	}
	return a.ID < b.ID
}

// pbkRate 成功率（百分比整数）。Used=0 → 0（不是 100：没跑过不等于好用）。
func pbkRate(s PbkStats) int {
	if s.Used <= 0 {
		return 0
	}
	return s.Succ * 100 / s.Used
}

// pbkMatch 按查询匹配当前生效的预案（读盘 + 选优）。
func pbkMatch(q PlaybookQuery) (Playbook, bool) {
	p, _, ok := pbkPick(pbkList(), q)
	return p, ok
}

// ===== 应用到行动 =====

// pbkApplyToMission 给行动挑预案并起草步骤。返回是否套上了预案。
//
// **只写 Playbook / Draft 两个字段，不动 Plan**：
// Plan 的语义是"已经发生的事"（断点续作的回放点、报告型行动的完成判据），
// 把未执行的步骤塞进去会让这两个语义同时失真 —— 这是最容易犯的错。
func pbkApplyToMission(id, svcID, errKind string) (Playbook, bool) {
	m, ok := missionGet(id)
	if !ok {
		return Playbook{}, false
	}
	q := PlaybookQuery{
		SvcID:   svcID,
		ErrKind: errKind,
		Trigger: m.Trigger.Kind,
		Text:    m.Goal + " " + m.Trigger.Detail,
	}
	p, ok := pbkMatch(q)
	if !ok {
		return Playbook{}, false
	}
	if _, err := missionUpdate(id, func(x *Mission) error {
		x.Playbook = p.ID
		x.Draft = append([]PlaybookStep(nil), p.Steps...)
		return nil
	}); err != nil {
		warn(scSys, "预案", "套用预案 %s 到行动 %s 失败：%v", p.ID, id, err)
		return Playbook{}, false
	}
	info(scSys, "预案", "行动 %s 命中预案「%s」（%d 步），等用户批准", id, p.Title, len(p.Steps))
	auditNow(actSys, "playbook_match", m.ID, p.ID, resOK, p.Title)
	return p, true
}

// pbkSettle 回写一次执行结果（成功只认复验通过）。
//
// 连败达限 → state=review（界面标黄「待复审」、不再推荐）。
// 为什么不直接 retired：连败有可能是环境变了（不是预案错了），
// 留 review 给人一条"看一眼再决定"的路，比一刀切停用更接近事实。
func pbkSettle(id string, verified bool) {
	if strings.TrimSpace(id) == "" {
		return
	}
	p, err := pbkUpdate(id, func(x *Playbook) error {
		x.Stats.Used++
		x.Stats.LastUsedAt = time.Now().Format("2006-01-02 15:04:05")
		if verified {
			x.Stats.Succ++
			x.Stats.Streak = 0
		} else {
			x.Stats.Fail++
			x.Stats.Streak++
			if x.Stats.Streak >= pbkReviewStreak && x.State == pbkActive {
				x.State = pbkReview
			}
		}
		return nil
	})
	if err != nil {
		warn(scSys, "预案", "统计回写失败（%s）：%v", id, err)
		return
	}
	if verified {
		ok(scSys, "预案", "「%s」复验通过（累计 %d/%d）", p.Title, p.Stats.Succ, p.Stats.Used)
		return
	}
	if p.State == pbkReview {
		warn(scSys, "预案", "「%s」连续 %d 次复验未通过，已转入待复审、不再推荐", p.Title, p.Stats.Streak)
		auditNow(actSys, "playbook_review", p.ID, "", resFail,
			fmt.Sprintf("连续 %d 次复验未通过", p.Stats.Streak))
		return
	}
	warn(scSys, "预案", "「%s」本次复验未通过（连续 %d 次）", p.Title, p.Stats.Streak)
}

// ===== 人工沉淀 =====

// pbkFromMission 把一次完成的行动打包成预案（人工点「存为预案」时才调）。
//
// 参数是**行动 ID 而不是 Mission 结构**：调用方手上的副本很容易过期
// （比如刚 missionUpdate 写完 Plan 又拿旧变量来沉淀），而这类 bug 的表现是
// 「说没有已完成步骤」，查起来完全不指向真正的原因。这里自己读一遍最新状态。
//
// 只从**记录本身**取信息：步骤序列来自 Plan，匹配键来自 Trigger。
// 不做任何"智能推断"——推断出来的匹配键会在下一次告警时把预案喂给错误的场景，
// 而那时没人知道是这里猜错了。
func pbkFromMission(id, title string) (Playbook, error) {
	m, ok := missionGet(id)
	if !ok {
		return Playbook{}, fmt.Errorf("行动不存在：%s", id)
	}
	if len(m.Plan) == 0 {
		return Playbook{}, fmt.Errorf("这次行动还没有已完成步骤，没有可打包的内容")
	}
	steps := make([]PlaybookStep, 0, len(m.Plan))
	for _, st := range m.Plan {
		steps = append(steps, PlaybookStep{
			Tool:   st.Tool,
			Target: st.Target,
			Why:    truncateRunes(strings.TrimSpace(st.Why), 200),
		})
		if len(steps) >= pbkMaxSteps {
			break
		}
	}
	if err := pbkCheckSteps(steps); err != nil {
		return Playbook{}, err
	}

	matcher := PlaybookMatcher{Triggers: []string{m.Trigger.Kind}}
	svc := missionSvcID(m.Trigger.Detail)
	if svc != "" {
		matcher.Services = []string{svc}
	}
	t := strings.TrimSpace(title)
	if t == "" {
		t = strings.TrimSpace(m.Goal)
	}
	if t == "" {
		return Playbook{}, fmt.Errorf("预案标题不能为空")
	}
	now := time.Now()
	p := Playbook{
		ID:            "p-" + now.Format("20060102-150405") + "-" + traceRand(),
		Title:         truncateRunes(t, 120),
		Matcher:       matcher,
		Steps:         steps,
		LessonIDs:     pbkLessonIDsFor(m, svc),
		State:         pbkActive,
		SourceMission: m.ID,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	pbkMu.Lock()
	defer pbkMu.Unlock()
	all := loadPlaybooks()
	// 同一个行动只沉淀一次。不去重的话，连点两下「存为预案」就会得到两条
	// 标题/步骤/统计完全一样的预案 —— 界面上两行长得一模一样，删掉一条另一条
	// 还在，看起来就像"删不掉"。**去重必须在锁内读文件**，否则和别的写路径赛跑。
	for _, e := range all {
		if e.SourceMission == m.ID {
			return Playbook{}, fmt.Errorf("这次行动已经存过预案「%s」，不用再存一次", e.Title)
		}
	}
	all = append(all, p)
	if err := pbkTrim(&all); err != nil {
		return Playbook{}, err
	}
	if err := savePlaybooks(all); err != nil {
		return Playbook{}, err
	}
	return p, nil
}

// pbkLessonIDsFor 找出这次处置关联到哪些经验（Phase 3.3「预案与记忆互检」）。
//
// 复用 lessonIDFor 而不是另写一套匹配：它只认 repeat_fail / verify_fail
// （「某个做法行不行」类结论），正是会被预案执行结果影响置信度的那些。
//
// **只回填，不写计数**。这一点是刻意的：预案执行的成败已经通过
// missionSediment → auditNow → lessonIDFor → recordOutcome 走过一遍了，
// 在这里再记一次就是双倍计数，而双倍计数的坏处不是数字难看 ——
// 是置信度被同一次事件抬高两档，"这条经验靠得住"变成了一句假话。
func pbkLessonIDsFor(m Mission, svc string) []string {
	if svc == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, st := range m.Plan {
		tool := strings.TrimSpace(st.Tool)
		if tool == "" {
			continue
		}
		id := lessonIDFor(tool, svc)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// pbkCheckSteps 校验步骤里的工具名（设计评审点名的"静默失效"防线）。
//
// 为什么必须在**保存时**查而不是执行时查：执行时才发现等于用户等到点了「继续」
// 之后才被告知预案坏了，而那时他已经在等结果。工具改名是迟早的事，
// 预案必须能立刻发现自己过期。
func pbkCheckSteps(steps []PlaybookStep) error {
	if len(steps) == 0 {
		return fmt.Errorf("预案至少要有一个步骤")
	}
	var bad []string
	for i, st := range steps {
		name := strings.TrimSpace(st.Tool)
		if name == "" {
			bad = append(bad, fmt.Sprintf("第 %d 步没有工具名", i+1))
			continue
		}
		if _, ok := aiToolRegistry[name]; !ok {
			bad = append(bad, fmt.Sprintf("第 %d 步的工具 %s 不存在", i+1, name))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("预案步骤有问题：%s", strings.Join(bad, "；"))
	}
	return nil
}

// missionSvcID 从 guard/webhook 的 Trigger.Detail 里取服务 ID。
//
// 存储格式是 "svcID | 说明"（见 missionFromGuard）。取不到就返回空 ——
// 空表示"这条预案不按服务限定"，比猜一个错的服务好。
//
// 只认 `[A-Za-z0-9_-]`：Detail 是人写的自由文本，稍不留神整句中文也会被当成
// 服务 ID 存进 Match 里。那种预案永远不会命中，而用户会以为"它怎么不推荐了"。
func missionSvcID(detail string) string {
	i := strings.Index(detail, "|")
	head := detail
	if i >= 0 {
		head = detail[:i]
	}
	head = strings.TrimSpace(head)
	if head == "" || len(head) > 32 {
		return ""
	}
	for _, c := range head {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return ""
		}
	}
	return head
}

// pbkHas 大小写不敏感地判断 v 是否在列表里（空串永不命中）。
func pbkHas(list []string, v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	for _, s := range list {
		if strings.EqualFold(strings.TrimSpace(s), v) {
			return true
		}
	}
	return false
}
