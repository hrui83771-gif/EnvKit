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
	"strings"
	"sync"
	"time"
)

// Memory 一条用户记忆。
type Memory struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Tags    string `json:"tags,omitempty"` // 逗号分隔，用于相关性匹配
	Always  bool   `json:"always"`         // true=每回合都注入；false=仅相关时注入
	Source  string `json:"source"`         // manual | import
	Created string `json:"created"`
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
func saveMemories(ms []Memory) error {
	if len(ms) > memMaxCount {
		ms = ms[:memMaxCount]
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
		Created: time.Now().Format("2006-01-02 15:04:05")}
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

// memoriesFor 返回要注入的记忆：always 的全给，其余按任务相关性给。
func memoriesFor(task string, max int) []Memory {
	ms := loadMemories()
	if max <= 0 {
		max = 20
	}
	always := make([]Memory, 0, len(ms))
	var rest []Memory
	for _, m := range ms {
		if m.Always {
			always = append(always, m)
		} else {
			rest = append(rest, m)
		}
	}
	if len(always) >= max {
		sort.Slice(always, func(i, j int) bool { return always[i].Created < always[j].Created })
		return always[:max]
	}
	// 相关性：任务文本命中标签或记忆正文关键词才算相关
	t := strings.ToLower(task)
	type scored struct {
		m     Memory
		score int
	}
	var hits []scored
	for _, m := range rest {
		s := 0
		for _, tag := range strings.Split(m.Tags, ",") {
			tag = strings.ToLower(strings.TrimSpace(tag))
			if tag != "" && strings.Contains(t, tag) {
				s += 2
			}
		}
		// 正文关键词：取长度>=2 的中文片段做朴素包含判断
		for _, kw := range memKeywords(m.Text) {
			if strings.Contains(t, kw) {
				s++
			}
		}
		if s > 0 {
			hits = append(hits, scored{m, s})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].m.Created < hits[j].m.Created
	})
	for _, h := range hits {
		if len(always) >= max {
			break
		}
		always = append(always, h.m)
	}
	return always
}

// memKeywords 从记忆正文里取少量高信息量词（朴素切分，不引 NLP 依赖）。
func memKeywords(text string) []string {
	var out []string
	runes := []rune(text)
	for i := 0; i+1 < len(runes); i++ {
		w := string(runes[i : i+2])
		// 去掉含标点/空白的二字组合
		if strings.ContainsAny(w, "，。；、：？！\n\t ,.;:?!") {
			continue
		}
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
			if ms := memoriesFor(task, 20); len(ms) > 0 {
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
			"只增删改记忆条目，不碰 EnvKit 自身配置。写入前会请用户确认。",
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
				ms := memoriesFor("", 50)
				if len(ms) == 0 {
					return "当前没有记忆条目", nil
				}
				var sb strings.Builder
				for _, m := range ms {
					mark := " "
					if m.Always {
						mark = "★"
					}
					sb.WriteString(mark + m.ID + "  " + firstLines(m.Text, 50) + "\n")
				}
				return strings.TrimRight(sb.String(), "\n"), nil
			}
		},
	}
}
