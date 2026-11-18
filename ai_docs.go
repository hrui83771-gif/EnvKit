package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
)

// web/docs-index.json 由 tools/docs_index.py 从 web/src/docs.html 生成，
// 与文档页同源：改文档只改 docs.html，索引跟着重建（dist_check 会校验同步）。
//
//go:embed web/docs-index.json
var docsIndexRaw []byte

type docsText struct {
	Zh string `json:"zh"`
	En string `json:"en"`
}

func (t docsText) pick(lang string) string {
	if lang == "en" && strings.TrimSpace(t.En) != "" {
		return t.En
	}
	if strings.TrimSpace(t.Zh) != "" {
		return t.Zh
	}
	return t.En
}

type docsChunk struct {
	ID      string   `json:"id"`
	Anchor  string   `json:"anchor"`
	Heading docsText `json:"heading"`
	Text    docsText `json:"text"`
}

type docsSection struct {
	ID     string      `json:"id"`
	Anchor string      `json:"anchor"`
	Group  docsText    `json:"group"`
	Title  docsText    `json:"title"`
	Chunks []docsChunk `json:"chunks"`
}

type docsCorpus struct {
	Sections []docsSection `json:"sections"`
}

var (
	docsOnce   sync.Once
	docsData   docsCorpus
	docsLoadEr error
	// chunk 级 df：检索打分用。与 chunks 同序。
	docsChunks []docsChunk
	docsParent []int // 每个 chunk 属于第几节
	docsDF     map[string]int
)

func docsLoad() error {
	docsOnce.Do(func() {
		if err := json.Unmarshal(docsIndexRaw, &docsData); err != nil {
			docsLoadEr = fmt.Errorf("文档索引解析失败：%v", err)
			return
		}
		docsDF = make(map[string]int, 512)
		for si, sec := range docsData.Sections {
			for _, ch := range sec.Chunks {
				idx := len(docsChunks)
				docsChunks = append(docsChunks, ch)
				docsParent = append(docsParent, si)
				seen := map[string]bool{}
				for _, t := range docsTerms(sec.Title.pick("zh") + " " + ch.Heading.pick("zh") + " " + ch.Text.pick("zh") + " " + ch.Text.pick("en")) {
					if !seen[t] {
						seen[t] = true
						docsDF[t]++
					}
				}
				_ = idx
			}
		}
	})
	return docsLoadEr
}

// docsStop 文档检索里区分度为零的二字组合与常见疑问词。
// 比记忆检索那份更长：查文档的问句几乎都带"怎么/哪里/为什么"。
var docsStop = map[string]bool{
	"怎么": true, "如何": true, "什么": true, "哪里": true, "在哪": true,
	"为什么": true, "哪些": true, "可以": true, "是否": true, "需要": true,
	"一个": true, "这个": true, "那个": true, "如果": true, "然后": true,
	"我们": true, "你们": true, "已经": true, "时候": true, "不要": true,
	"的先": true, "前先": true, "的话": true, "还是": true, "或者": true,
	"以及": true, "就是": true, "没有": true, "不是": true, "会不": true,
}

// docsTerms 把查询切成检索词：
//   - ASCII 词（含 _ . - /）长度 ≥2，整体保留；
//   - CJK 连续串切成二字组，单字串保留单字。
//
// 用二字组而不是分词，是因为不引词典也能工作——和 memory.go 里
// memKeywords 的思路一致，只是这里加了停用词与 ASCII 词。
func docsTerms(q string) []string {
	q = strings.ToLower(q)
	out := make([]string, 0, 16)
	seen := map[string]bool{}
	add := func(t string) {
		if t == "" || seen[t] || docsStop[t] {
			return
		}
		seen[t] = true
		out = append(out, t)
	}

	var ascii, cjk []rune
	flushASCII := func() {
		if len(ascii) >= 2 {
			add(string(ascii))
		}
		ascii = ascii[:0]
	}
	flushCJK := func() {
		if len(cjk) == 1 {
			add(string(cjk))
		}
		for i := 0; i+1 < len(cjk); i++ {
			add(string(cjk[i : i+2]))
		}
		cjk = cjk[:0]
	}
	for _, r := range q {
		switch {
		case r < 128 && (r == '_' || r == '-' || r == '.' || r == '/' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z')):
			flushCJK()
			ascii = append(ascii, r)
		case r >= 0x2E80:
			flushASCII()
			cjk = append(cjk, r)
		default:
			flushASCII()
			flushCJK()
		}
	}
	flushASCII()
	flushCJK()
	return out
}

// docsScoreChunk 打分：命中词的 idf 之和，标题/小节名命中的加权。
// idf 用来压掉"端口""日志"这种到处都有的词——否则搜"日志在哪"
// 会命中全部含"日志"的块，等于没排序。
func docsScoreChunk(ci int, terms []string, textLower string) float64 {
	if len(terms) == 0 {
		return 0
	}
	n := float64(len(docsChunks))
	ch := docsChunks[ci]
	headLower := strings.ToLower(docsData.Sections[docsParent[ci]].Title.pick("zh") + " " + ch.Heading.pick("zh") + " " + ch.Heading.pick("en"))
	var s float64
	hits := 0
	for _, t := range terms {
		if !strings.Contains(textLower, t) && !strings.Contains(headLower, t) {
			continue
		}
		df := docsDF[t]
		idf := math.Log(1 + n/(1+float64(df)))
		w := 1.0
		if strings.Contains(headLower, t) {
			w = 2.2 // 标题命中比正文命中更能说明"这块就是讲这个的"
		}
		s += idf * w
		hits++
	}
	if hits == 0 {
		return 0
	}
	// 命中词覆盖率：只命中 5 个词里的 1 个，不该和命中 4 个的排一起。
	return s * (1 + 0.35*float64(hits)/float64(len(terms)))
}

type docsHit struct {
	Section docsSection
	Chunk   docsChunk
	Score   float64
}

// docsSearch 返回按相关度排序的前 k 个块。
func docsSearch(query, lang string, k int) []docsHit {
	terms := docsTerms(query)
	if len(terms) == 0 || len(docsChunks) == 0 {
		return nil
	}
	hits := make([]docsHit, 0, len(docsChunks))
	for ci := range docsChunks {
		text := docsChunks[ci].Text.pick(lang)
		if lang != "en" {
			text = docsChunks[ci].Text.pick("zh")
		}
		lower := strings.ToLower(text)
		if sc := docsScoreChunk(ci, terms, lower); sc > 0 {
			hits = append(hits, docsHit{
				Section: docsData.Sections[docsParent[ci]],
				Chunk:   docsChunks[ci],
				Score:   sc,
			})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

// docsExcerpt 取命中处前后各一段，而不是从头截——文档段落开头往往是铺垫。
func docsExcerpt(text string, terms []string, limit int) string {
	lower := strings.ToLower(text)
	at := -1
	for _, t := range terms {
		if i := strings.Index(lower, t); i >= 0 && (at < 0 || i < at) {
			at = i
		}
	}
	runes := []rune(text)
	if at < 0 {
		if len(runes) > limit {
			return string(runes[:limit]) + "…"
		}
		return text
	}
	// at 是字节位置，换算成 rune 位置
	rpos := len([]rune(text[:at]))
	from := rpos - 40
	if from < 0 {
		from = 0
	}
	to := from + limit
	if to > len(runes) {
		to = len(runes)
	}
	head := ""
	if from > 0 {
		head = "…"
	}
	tail := ""
	if to < len(runes) {
		tail = "…"
	}
	return head + string(runes[from:to]) + tail
}

func docsFindSection(id string) *docsSection {
	id = strings.TrimSpace(id)
	for i := range docsData.Sections {
		if docsData.Sections[i].ID == id {
			return &docsData.Sections[i]
		}
	}
	return nil
}

func docsSectionIDs() []string {
	out := make([]string, 0, len(docsData.Sections))
	for _, s := range docsData.Sections {
		out = append(out, s.ID)
	}
	return out
}

// aiDocsLang 归一化语言参数。默认中文：文档原文是中文，
// 英文界面下由模型显式声明或自行翻译，别猜。
func aiDocsLang(args map[string]any) string {
	if v, ok := args["lang"].(string); ok && strings.EqualFold(strings.TrimSpace(v), "en") {
		return "en"
	}
	return "zh"
}

const (
	docsExcerptRunes = 110  // list 模式每条摘录长度
	docsSearchTopK   = 4    // list 带 query 时最多给几条
	docsBodyCap      = 1400 // get_doc_section 单次正文上限（字符）
)

// docsOutline 目录模式：只给节 id、组、标题。
// 正文一律不给——21 节的正文一次全发会把上下文预算吃光。
func docsOutline(lang string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("EnvKit 自带使用文档共 %d 节。用 get_doc_section 取正文，id 见下：\n", len(docsData.Sections)))
	for _, s := range docsData.Sections {
		fmt.Fprintf(&b, "- %s（%s）%s\n", s.ID, s.Group.pick(lang), s.Title.pick(lang))
	}
	return b.String()
}

func docsSearchReport(query, lang string) string {
	terms := docsTerms(query)
	hits := docsSearch(query, lang, docsSearchTopK)
	if len(hits) == 0 {
		var b strings.Builder
		b.WriteString("没有命中。可以先用 list_docs（不带 query）看全部节标题，再按 id 取。\n")
		return b.String()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "按相关度排序，共 %d 条命中：\n", len(hits))
	for i, h := range hits {
		head := h.Chunk.Heading.pick(lang)
		fmt.Fprintf(&b, "\n[%d] %s（%s）› %s\n    anchor=%s\n    %s\n",
			i+1, h.Section.Title.pick(lang), h.Section.ID, head,
			h.Chunk.Anchor, docsExcerpt(h.Chunk.Text.pick(lang), terms, docsExcerptRunes))
	}
	b.WriteString("\n要全文用 get_doc_section(id, heading)。")
	return b.String()
}

// docsBody 取一节（或其中一个小节）的正文，超长截断并列出可继续取的小节。
func docsBody(id, heading, lang string) (string, error) {
	return docsBodyCapTo(id, heading, lang, docsBodyCap)
}

// docsBodyCapTo 把上限做成参数，是为了让「超长时是否给出续取指引」可被稳定断言：
// 文档本身大多数节都在 1400 字以内，写死常量的话截断分支只能靠某一节恰好够长才覆盖到。
func docsBodyCapTo(id, heading, lang string, capRunes int) (string, error) {
	sec := docsFindSection(id)
	if sec == nil {
		return "", fmt.Errorf("没有 id 为 %q 的文档节。可选：%s", id, strings.Join(docsSectionIDs(), ", "))
	}
	heading = strings.TrimSpace(heading)

	if heading != "" {
		for _, ch := range sec.Chunks {
			if ch.ID == heading || ch.Anchor == heading {
				return docsChunkText(sec, ch, lang, capRunes), nil
			}
		}
		ids := make([]string, 0, len(sec.Chunks))
		for _, ch := range sec.Chunks {
			ids = append(ids, ch.ID)
		}
		return "", fmt.Errorf("节 %q 下没有 heading 为 %q 的小节。可选：%s", id, heading, strings.Join(ids, ", "))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "《%s》（%s，anchor=%s）\n", sec.Title.pick(lang), sec.ID, sec.Anchor)
	used := len([]rune(b.String()))
	var rest []string
	for _, ch := range sec.Chunks {
		part := docsChunkText(sec, ch, lang, capRunes)
		n := len([]rune(part))
		// used > 60：保证至少发出第一块，否则上限一调小就只剩一条「都在下面」的空壳。
		if used+n > capRunes && used > 60 {
			rest = append(rest, ch.ID+"（"+ch.Heading.pick(lang)+"）")
			continue
		}
		b.WriteString("\n" + part)
		used += n
	}
	if len(rest) > 0 {
		fmt.Fprintf(&b, "\n\n（本节还有 %d 个片段没发出，用 get_doc_section(id=\"%s\", heading=...) 单独取：%s）",
			len(rest), sec.ID, strings.Join(rest, "；"))
	}
	return b.String(), nil
}

func docsChunkText(sec *docsSection, ch docsChunk, lang string, capRunes int) string {
	head := ch.Heading.pick(lang)
	var b strings.Builder
	if head != "" && head != sec.Title.pick(lang) {
		fmt.Fprintf(&b, "## %s\n", head)
	}
	body := ch.Text.pick(lang)
	runes := []rune(body)
	if len(runes) > capRunes {
		body = string(runes[:capRunes]) + "…（已截断）"
	}
	b.WriteString(body)
	return b.String()
}
