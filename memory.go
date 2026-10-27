package main

// memory.go —— v2.0 M3：用户记忆（用户写、用户管、AI 必读）
//
// 与 M1 的自动经验是两件不同的事，界面上分两个区块，不要混：
//
//	M1 自动经验：从审计算出来的**建议**，可能不准，用户可以逐条关掉。
//	M3 用户记忆：用户自己写的**事实与规矩**，AI 必须遵守。
//
// 为什么不做"让模型自己形成策略"（原 M3 方案）：
// 自生策略不可控（用户不知道它下次要干什么）、不可验证（对错没判据）、
// 不可审计（绕开了"已知工具 + 确认闸门 + 审计"这三件套）。
// 用户手写记忆是同一诉求的安全形态：你写你管，随时能改、能看、能删。
//
// 存储刻意独立于 config.json：
//   - config.json 会被 DPAPI 加密、被 dist_check 扫敏感串、被 mergeFormConfig 保护
//   - 记忆是高频增删的文本数据，混进配置会被表单自动保存的坑牵连
//   - 独立文件便于用户直接编辑备份，也便于导入导出

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Memory 一条用户记忆。
type Memory struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Tags   string `json:"tags,omitempty"` // 逗号分隔，用于相关性匹配
	Always bool   `json:"always"`         // true=每回合都注入；false=仅相关时注入
	// Global v2.3 N2：true=适用于任何项目。
	//
	// 默认必须是 false。"上次这台机器上 npm run serve 就行"换个前端项目
	// 就完全错——记忆跨项目泛化是"AI 越用越笨"的主要来源。
	// 真正通用的规矩（"备份前先停后端"）由用户显式勾这一项。
	Global bool `json:"global,omitempty"`
	// ScopeFP 创建时的项目指纹（v2.3 N2）。由程序写入，用户不必手填。
	// 空值 = 老条目，视为无指纹（仍然注入，见 memoriesFor 的注释）。
	ScopeFP  string `json:"scope_fp,omitempty"`
	Source   string `json:"source"` // manual | import
	Created  string `json:"created"`
	Stale    bool   `json:"stale,omitempty"`     // 作用域已不匹配
	StaleWhy string `json:"stale_why,omitempty"` // 为什么失效
}

// memoryFile 记忆文件路径。抽成变量供单测替换（同 auditDir 手法）。
var memoryFile = func() string { return filepath.Join(exeDir(), "memories.json") }

var memMu sync.Mutex

// memMax 单条上限与总条数上限：记忆是要进提示词的，不能无限长。
const (
	memMaxText  = 500
	memMaxCount = 100
)

// loadMemories 读记忆文件。坏行跳过而不是整体失败——记忆文件常被手工编辑过。
func loadMemories() []Memory {
	b, err := os.ReadFile(memoryFile())
	if err != nil {
		return []Memory{}
	}
	out := []Memory{}
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var m Memory
		if json.Unmarshal([]byte(ln), &m) != nil {
			continue
		}
		if strings.TrimSpace(m.Text) == "" {
			continue
		}
		out = append(out, m)
	}
	return out
}

// saveMemories 原子写 + 备份（与配置同样的保护级别：记忆丢了用户会心疼）。
//
// **超过上限必须报错，不能静默截断**：旧实现 `ms[:memMaxCount]` 会把刚 append
// 进来的新记忆直接切掉，接口却仍回"已保存"——这是最坏的一种数据丢失：
// 用户以为存上了，实际下次对话就没了。
func saveMemories(ms []Memory) error {
	if len(ms) > memMaxCount {
		return fmt.Errorf("记忆条数已达上限 %d，请先删除不用的条目再添加", memMaxCount)
	}
	p := memoryFile()
	var sb strings.Builder
	for _, m := range ms {
		b, err := json.Marshal(m)
		if err != nil {
			continue
		}
		sb.Write(b)
		sb.WriteString("\n")
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(sb.String()), 0600); err != nil {
		return err
	}
	if _, err := os.Stat(p); err == nil {
		_ = os.Remove(p + ".bak")
		_ = os.Rename(p, p+".bak")
	}
	return os.Rename(tmp, p)
}

func memID(text string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return "m" + hex.EncodeToString(h[:6])
}

// memUpsert 新增或更新（按内容哈希去重，重复导入不会产生垃圾）。
func memUpsert(text, tags string, always bool, source string) (Memory, bool, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Memory{}, false, fmt.Errorf("记忆内容为空")
	}
	if n := len([]rune(text)); n > memMaxText {
		return Memory{}, false, fmt.Errorf("单条记忆最长 %d 字，当前 %d 字", memMaxText, n)
	}
	memMu.Lock()
	defer memMu.Unlock()
	ms := loadMemories()
	id := memID(text)
	for i := range ms {
		if ms[i].ID == id {
			// 已存在：更新标签与开关，不动创建时间
			ms[i].Tags = tags
			ms[i].Always = always
			return ms[i], false, saveMemories(ms)
		}
	}
	m := Memory{ID: id, Text: text, Tags: tags, Always: always, Source: source,
		Created: time.Now().Format("2006-01-02 15:04:05"),
		// v2.3 N2：记录创建时的项目指纹，换项目后这条会自动失效。
		// 已在别的项目创建过的同内容记忆沿用旧指纹（那是"更新"不是"新建"）。
		ScopeFP: projectFingerprint()}
	ms = append(ms, m)
	if err := saveMemories(ms); err != nil {
		return Memory{}, false, err
	}
	return m, true, nil
}

func memDelete(id string) error {
	memMu.Lock()
	defer memMu.Unlock()
	ms := loadMemories()
	out := ms[:0]
	found := false
	for _, m := range ms {
		if m.ID == id {
			found = true
			continue
		}
		out = append(out, m)
	}
	if !found {
		return fmt.Errorf("记忆不存在")
	}
	return saveMemories(out)
}

func memToggle(id string) error {
	memMu.Lock()
	defer memMu.Unlock()
	ms := loadMemories()
	for i := range ms {
		if ms[i].ID == id {
			ms[i].Always = !ms[i].Always
			return saveMemories(ms)
		}
	}
	return fmt.Errorf("记忆不存在")
}

// memSetGlobal 设置某条记忆是否跨项目适用（v2.3 N2）。
//
// 切到 global 时清掉 ScopeFP：指纹已无意义，留着会让用户以为
// "这条仍然绑定在某个项目上"。
func memSetGlobal(id string, on bool) error {
	memMu.Lock()
	defer memMu.Unlock()
	ms := loadMemories()
	for i := range ms {
		if ms[i].ID != id {
			continue
		}
		ms[i].Global = on
		if on {
			ms[i].ScopeFP = ""
		} else {
			ms[i].ScopeFP = projectFingerprint()
		}
		return saveMemories(ms)
	}
	return fmt.Errorf("记忆不存在")
}

// memImportBatch 批量导入（每行一条，或 JSON 数组）。
// 导入是低频操作，但对用户来说就是"把我写好的东西灌进去"，所以要给足反馈。
func memImportBatch(text string) (added, updated, failed int) {
	items := parseImport(text)
	for _, it := range items {
		_, isNew, err := memUpsert(it.text, it.tags, it.always, "import")
		switch {
		case err != nil:
			failed++
		case isNew:
			added++
		default:
			updated++
		}
	}
	return
}

type importItem struct {
	text   string
	tags   string
	always bool
}

// parseImport 容忍三种格式：纯文本每行一条、key: value 文本、JSON 数组。
// 用户从别处复制来的东西格式不可控，解析器要尽量宽容，错了再明确报错。
func parseImport(s string) []importItem {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	// JSON 数组
	if strings.HasPrefix(s, "[") {
		var arr []struct {
			Text   string `json:"text"`
			Tags   string `json:"tags"`
			Always bool   `json:"always"`
		}
		if json.Unmarshal([]byte(s), &arr) == nil {
			out := make([]importItem, 0, len(arr))
			for _, a := range arr {
				if strings.TrimSpace(a.Text) != "" {
					out = append(out, importItem{text: a.Text, tags: a.Tags, always: a.Always})
				}
			}
			return out
		}
	}
	var out []importItem
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") || strings.HasPrefix(ln, "//") {
			continue
		}
		ln = strings.TrimPrefix(ln, "- ")
		ln = strings.TrimPrefix(ln, "* ")
		it := importItem{text: ln}
		// 支持 "记忆文本 | 标签1,标签2" 这种带标签的行
		if i := strings.LastIndex(ln, " | "); i > 0 {
			it.text = strings.TrimSpace(ln[:i])
			it.tags = strings.TrimSpace(ln[i+3:])
		}
		if strings.EqualFold(it.tags, "always") {
			it.always = true
			it.tags = ""
		}
		if it.text != "" {
			out = append(out, it)
		}
	}
	return out
}

// ---------- 注入 ----------

// MemoryView 记忆 + **本轮**注入状态。Injected/Reason 是视图字段，不落盘：
// 界面需要回答"我写的这条为什么没生效"，而落盘只会让文件里多出会过期的假状态。
type MemoryView struct {
	Memory
	Injected bool   `json:"injected"`
	Reason   string `json:"reason,omitempty"`
}

// memoriesFor 返回要注入的记忆：always 的全给，其余按任务相关性给。
//
// 顺序与上限的规则都收在 memoriesView 里——**界面看到的和模型收到的必须是同一次计算**，
// 否则"预览说会注入、实际没注入"就是新的不信任来源。
func memoriesFor(task string, max int) []Memory {
	vs := memoriesView(task, max)
	out := make([]Memory, 0, len(vs))
	for _, v := range vs {
		if v.Injected {
			out = append(out, v.Memory)
		}
	}
	return out
}

// memoriesView 计算全量记忆的注入状态与原因。
//
// 三个修过的坑（都来自"写了却不生效"）：
//  1. 作用域失效只算在内存里、从不返回给界面 → 用户看不到"已失效"，以为功能坏了；
//  2. always 超过上限时按创建时间**升序**截断，被丢的反而是刚写的新规矩；
//  3. 相关性只看 2-gram 是否出现（s>0 即命中），"先"这种字就能误召回。
func memoriesView(task string, max int) []MemoryView {
	ms := loadMemories()
	if max <= 0 {
		max = 20
	}
	cur := projectFingerprint()
	views := make([]MemoryView, 0, len(ms))
	var alwaysIdx, restIdx []int
	for i := range ms {
		v := MemoryView{Memory: ms[i]}
		if st, why := memStaleOf(ms[i], cur); st {
			v.Stale, v.StaleWhy = true, why
			v.Reason = "已失效：" + why
			views = append(views, v)
			continue
		}
		views = append(views, v)
		ix := len(views) - 1
		if ms[i].Always {
			alwaysIdx = append(alwaysIdx, ix)
		} else {
			restIdx = append(restIdx, ix)
		}
	}

	// always：新的排前面。旧实现升序截断，超过上限时丢掉的是"最新写的规矩"，
	// 用户完全无从得知——这里改成保留最新，并把落选的写成可见原因。
	sort.SliceStable(alwaysIdx, func(a, b int) bool {
		return views[alwaysIdx[a]].Created > views[alwaysIdx[b]].Created
	})
	picked := 0
	for _, vi := range alwaysIdx {
		if picked < max {
			views[vi].Injected = true
			picked++
			continue
		}
		views[vi].Reason = "超过每轮注入上限（" + strconv.Itoa(max) + " 条），本条未注入"
	}

	// 相关记忆：标签命中算强信号，正文 2-gram 算弱信号，累计到 2 分才算命中。
	t := strings.ToLower(strings.TrimSpace(task))
	type hit struct {
		vi    int
		score int
	}
	var hits []hit
	if t != "" {
		for _, vi := range restIdx {
			if s := memScore(views[vi].Memory, t); s >= 2 {
				hits = append(hits, hit{vi, s})
			}
		}
	}
	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].score != hits[b].score {
			return hits[a].score > hits[b].score
		}
		return views[hits[a].vi].Created > views[hits[b].vi].Created
	})
	for _, h := range hits {
		if picked >= max {
			views[h.vi].Reason = "超过每轮注入上限（" + strconv.Itoa(max) + " 条），本条未注入"
			continue
		}
		views[h.vi].Injected = true
		picked++
	}
	for _, vi := range restIdx {
		if !views[vi].Injected && views[vi].Reason == "" {
			if t == "" {
				views[vi].Reason = "仅相关时注入；本轮任务为空"
			} else {
				views[vi].Reason = "与本轮任务不相关（未命中标签/关键词）"
			}
		}
	}
	return views
}

// memStaleOf 判断一条记忆的作用域是否已经失效（Global 与老数据恒为适用）。
func memStaleOf(m Memory, cur string) (bool, string) {
	if m.Global || m.ScopeFP == "" {
		return false, ""
	}
	if m.ScopeFP != cur {
		return true, "创建时属于另一个项目（" + currentScopeDirLabel() + " 已不同）"
	}
	return false, ""
}

// memScore 相关性打分：标签 3 分、正文关键词每命中一个 1 分。
// 阈值 2 分意味着"一个标签"或"两个不同关键词"才注入——比旧版"出现一个二字组合就注入"稳得多。
func memScore(m Memory, taskLower string) int {
	s := 0
	for _, tag := range strings.Split(m.Tags, ",") {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag != "" && strings.Contains(taskLower, tag) {
			s += 3
		}
	}
	for _, kw := range memKeywords(m.Text) {
		if strings.Contains(taskLower, kw) {
			s++
		}
	}
	return s
}

// memStop 高频但无区分度的二字组合：任务里出现它们不该把记忆拉进来。
var memStop = map[string]bool{
	"的先": true, "前先": true, "时候": true, "可以": true, "不要": true,
	"一个": true, "这个": true, "那个": true, "如果": true, "然后": true,
	"我们": true, "你们": true, "什么": true, "怎么": true, "已经": true,
}

// memKeywords 从记忆正文里取少量高信息量词（朴素切分，不引 NLP 依赖）。
func memKeywords(text string) []string {
	out := make([]string, 0, 12)
	seen := map[string]bool{}
	runes := []rune(text)
	for i := 0; i+1 < len(runes); i++ {
		w := string(runes[i : i+2])
		if strings.ContainsAny(w, "，。；、：？！\n\t ,.;:?!()（）：") || memStop[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
		if len(out) >= 12 {
			break
		}
	}
	return out
}

// memoriesBrief 注入形态：只给文本，不给 id/时间。
func memoriesBrief(ms []Memory) string {
	if len(ms) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, m := range ms {
		t := m.Text
		if n := len([]rune(t)); n > 80 {
			t = string([]rune(t)[:80]) + "…"
		}
		sb.WriteString("- ")
		sb.WriteString(t)
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func memoriesJSON(task string, max int) string {
	ms := memoriesFor(task, max)
	if len(ms) == 0 {
		return ""
	}
	b, err := json.Marshal(ms)
	if err != nil {
		return ""
	}
	return string(b)
}

// ---------- 工具注册 ----------

// registerLessonTools 挂两个工具：recall_lessons 只读，manage_memories 需确认。
// 记忆相关的一切都进确认闸门——它会长期影响 AI 行为，写错要能一眼看见、一键撤销。
//
// 工具本身**始终注册**，不受总开关影响：关闭记忆层时模型仍可查询"我掌握了哪些记忆"，
// 但拿不到任何注入内容（门禁在 aiHealthSnapshotFor）。这样开关切换不需要重启 Agent 实例，
// 也让界面上的"记忆面板"在关闭状态下依然可读、可编辑存量数据。
func registerLessonTools() {
	aiToolRegistry["recall_lessons"] = aiTool{
		Desc: "查看从历史操作记录里总结出的经验，以及你被要求遵守的用户记忆。" +
			"动手做同类操作前先查一次：若命中「反复失败」的经验，说明原样重试没用，必须换思路；" +
			"若命中「用户手动修好」的经验，先问用户自己的做法。" +
			"优先级：用户当前的明确要求 > 用户记忆 > 自动经验。",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"task":   map[string]any{"type": "string", "description": "要做的事，用于挑相关经验；留空返回全部"},
			"detail": map[string]any{"type": "boolean", "description": "是否带上审计证据（排查时用）"},
		}, "required": []string{}},
		Execute: func(args map[string]any) (string, error) {
			task, _ := args["task"].(string)
			detail, _ := args["detail"].(bool)
			var sb strings.Builder
			// schema 承诺"留空返回全部"，所以空任务必须按**库**列，而不是按"空任务会注入什么"筛。
			// 原先两句都走 memoriesFor("")，而它对空任务只返回 always 条目 —— 于是一条
			// 「按需注入、无标签」的记忆明明躺在库里，这里却输出"没有相关的经验或记忆"。
			// 列表要回答"有什么"，不是"这一轮会注入什么"；两者混淆就是让模型替用户断定数据不存在。
			if strings.TrimSpace(task) == "" {
				if vs := memoriesView("", 0); len(vs) > 0 {
					sb.WriteString("== 用户记忆（共 " + strconv.Itoa(len(vs)) + " 条；★=每轮都注入，·=仅任务相关时注入）==\n")
					for _, v := range vs {
						mark := "· "
						if v.Always {
							mark = "★ "
						}
						line := mark + v.Text
						if !v.Injected && v.Reason != "" {
							line += "  [" + v.Reason + "]"
						}
						sb.WriteString(line + "\n")
					}
				}
			} else if ms := memoriesFor(task, 20); len(ms) > 0 {
				sb.WriteString("== 用户记忆（必须遵守）==\n")
				for _, m := range ms {
					mark := "  "
					if m.Always {
						mark = "* "
					}
					sb.WriteString(mark + m.Text + "\n")
				}
			}
			ls := lessonsForTask(task, 8)
			if len(ls) > 0 {
				sb.WriteString("\n== 历史经验（系统统计，仅供参考）==\n")
				for _, l := range ls {
					sb.WriteString("- " + l.Title + "\n")
					if l.Precheck != "" && l.Precheck != "none" {
						sb.WriteString("  建议先做：" + l.Precheck + "\n")
					}
					if detail && len(l.Evidence) > 0 {
						sb.WriteString("  证据：" + strings.Join(l.Evidence, " | ") + "\n")
					}
				}
			}
			if sb.Len() == 0 {
				return "没有相关的经验或记忆——按常规流程处理，遇到不确定就问用户。", nil
			}
			return strings.TrimRight(sb.String(), "\n"), nil
		},
	}

	aiToolRegistry["manage_memories"] = aiTool{
		Desc: "记录或维护长期记忆（你或用户口述的规矩与事实，下次对话仍然生效）。" +
			"只增删改记忆条目，不碰 EnvKit 自身配置。写入前会请用户确认。" +
			"op=list 会列出**全部**条目并标注「★每轮都注入 / ·仅任务相关时注入」——" +
			"回答\"你记得什么\"必须先 list，别因为本轮提示词里没出现某条就说没有。" +
			"标 · 的条目只是本轮未注入，并不等于没记住；用户想让它每轮生效就 toggle 或提醒他点「设为每轮注入」。",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"op":     map[string]any{"type": "string", "description": "add / delete / toggle / list", "enum": []string{"add", "delete", "toggle", "list"}},
			"text":   map[string]any{"type": "string", "description": "op=add 时的记忆正文（写成一条明确的事实或规矩，不要写成一整段话）"},
			"tags":   map[string]any{"type": "string", "description": "可选标签，逗号分隔；任务相关时自动注入"},
			"always": map[string]any{"type": "boolean", "description": "true=每回合都注入；false=仅任务相关时注入"},
			"id":     map[string]any{"type": "string", "description": "op=delete/toggle 时的记忆 id"},
		}, "required": []string{"op"}},
		Write: true,
		Execute: func(args map[string]any) (string, error) {
			op, _ := args["op"].(string)
			id, _ := args["id"].(string)
			switch op {
			case "add":
				text, _ := args["text"].(string)
				tags, _ := args["tags"].(string)
				always, _ := args["always"].(bool)
				m, isNew, err := memUpsert(text, tags, always, "manual")
				if err != nil {
					return "", err
				}
				lessonInvalidate()
				verb := "已更新记忆"
				if isNew {
					verb = "已记住"
				}
				return fmt.Sprintf("%s：%s（id=%s）", verb, firstLines(m.Text, 60), m.ID), nil
			case "delete":
				if err := memDelete(id); err != nil {
					return "", err
				}
				lessonInvalidate()
				return "已删除该记忆", nil
			case "toggle":
				if err := memToggle(id); err != nil {
					return "", err
				}
				lessonInvalidate()
				return "已切换该记忆的「始终注入」开关", nil
			default:
				// 同 recall_lessons：list 回答"库里有什么"，不能用 memoriesFor("")
				// 那种"空任务会注入什么"的口径 —— 否则按需注入的条目会被报成不存在。
				vs := memoriesView("", 0)
				if len(vs) == 0 {
					return "记忆库是空的（还没有任何条目）", nil
				}
				var sb strings.Builder
				sb.WriteString("共 " + strconv.Itoa(len(vs)) + " 条记忆（★=每轮都注入，·=仅任务相关时注入）：\n")
				for _, v := range vs {
					mark := "·"
					if v.Always {
						mark = "★"
					}
					sb.WriteString(mark + " " + v.ID + "  " + firstLines(v.Text, 50))
					if !v.Injected && v.Reason != "" {
						sb.WriteString("  [" + v.Reason + "]")
					}
					sb.WriteString("\n")
				}
				sb.WriteString("提示：· 的条目在无关任务里不会进上下文；要它每轮都生效用 op=toggle，或让用户点「设为每轮注入」。")
				return strings.TrimRight(sb.String(), "\n"), nil
			}
		},
	}
}
