package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDocsIndexLoadsFromEmbeddedCorpus(t *testing.T) {
	if err := docsLoad(); err != nil {
		t.Fatalf("索引加载失败：%v", err)
	}
	if len(docsData.Sections) < 20 {
		t.Fatalf("节数 %d，至少应有 20 节（文档缩水或索引没重建）", len(docsData.Sections))
	}
	if len(docsChunks) < 40 {
		t.Fatalf("块数 %d，切块没生效", len(docsChunks))
	}
	// 每个 chunk 都要有非空正文，否则检索命中后给不出内容。
	for i, ch := range docsChunks {
		if strings.TrimSpace(ch.Text.Zh) == "" && strings.TrimSpace(ch.Text.En) == "" {
			t.Fatalf("第 %d 块（%s）正文为空", i, ch.Anchor)
		}
	}
	// 锚点必须唯一：文档页是按锚点跳转的，重复锚点会跳错。
	seen := map[string]bool{}
	for _, ch := range docsChunks {
		if seen[ch.Anchor] {
			t.Fatalf("锚点重复：%s", ch.Anchor)
		}
		seen[ch.Anchor] = true
	}
}

// 目录模式的代价必须可控：它会在每次"文档问题"的第一轮被调用。
// 21 节的标题 ≈ 数百 token；一旦有人往目录里塞正文，这条会先红。
func TestDocsOutlineIsCheap(t *testing.T) {
	if err := docsLoad(); err != nil {
		t.Fatalf("索引加载失败：%v", err)
	}
	out := docsOutline("zh")
	if n := len([]rune(out)); n > 1200 {
		t.Fatalf("目录 %d 字，超出预算 1200——目录里不该有正文", n)
	}
	for _, s := range docsData.Sections {
		if !strings.Contains(out, s.ID) {
			t.Fatalf("目录里缺少节 %s", s.ID)
		}
	}
	// 正文绝对不能出现在目录里（这是"全量注入"的入口，必须守住）。
	if strings.Contains(out, "assistant") || len([]rune(out)) > 2*len(docsData.Sections)*60 {
		t.Fatalf("目录疑似混入正文")
	}
}

// 检索必须把"该看的那一节"排到前面，而不是把含关键词的块全捞出来。
func TestDocsSearchRanksRightSection(t *testing.T) {
	if err := docsLoad(); err != nil {
		t.Fatalf("索引加载失败：%v", err)
	}
	cases := []struct {
		query string
		want  string // 期望出现在第一条或前两条的节 id
		why   string
	}{
		{"日志文件落在哪个目录", "start", "日志落盘写在「程序启动与日志」"},
		{"怎么彻底卸载干净", "upgrade", "卸载写在「升级、回滚与卸载」"},
		{"手机连不上控制台", "mobile", "手机访问单独一节"},
		{"端口被占用怎么办", "troubleshoot", "按症状查的排障索引"},
		{"覆盖升级会不会冲掉配置文件", "upgrade", "升级与 config 的关系"},
	}
	for _, c := range cases {
		hits := docsSearch(c.query, "zh", 4)
		if len(hits) == 0 {
			t.Fatalf("%q 一条都没命中（%s）", c.query, c.why)
		}
		found := false
		for i, h := range hits {
			if i >= 2 {
				break
			}
			if h.Section.ID == c.want {
				found = true
				break
			}
		}
		if !found {
			var got []string
			for i, h := range hits {
				if i >= 4 {
					break
				}
				got = append(got, h.Section.ID)
			}
			t.Fatalf("%q 的前两条是 %v，期望 %s（%s）", c.query, got, c.want, c.why)
		}
	}
}

// 已知词不能因为 idf 高就乱排：查一个只在一节里出现的词，必须命中那一节。
func TestDocsSearchExactTerm(t *testing.T) {
	if err := docsLoad(); err != nil {
		t.Fatalf("索引加载失败：%v", err)
	}
	hits := docsSearch("web_build.py", "zh", 3)
	if len(hits) == 0 {
		t.Fatal("搜构建脚本名一条都没命中——代码块没进索引？")
	}
	if hits[0].Section.ID != "install" {
		t.Fatalf("web_build.py 首条命中 %s，期望 install", hits[0].Section.ID)
	}
}

func TestDocsBodyTruncatesAndListsRest(t *testing.T) {
	if err := docsLoad(); err != nil {
		t.Fatalf("索引加载失败：%v", err)
	}
	// 用 200 字的上限逼出截断分支：文档本身多数节都在 1400 字以内，
	// 靠「找一节恰好够长的」来覆盖这条路径是不稳的（第一版就写错了前提）。
	const cap = 200
	body, err := docsBodyCapTo("cheatsheet", "", "zh", cap)
	if err != nil {
		t.Fatalf("取正文失败：%v", err)
	}
	if !strings.Contains(body, "get_doc_section") {
		t.Fatal("截断后没告诉模型怎么继续取剩下的片段——它只能放弃")
	}
	// 第二块之后的内容必须被点名，否则模型知道「还有」却不知道 heading 叫什么。
	sec := docsFindSection("cheatsheet")
	withheld := 0
	for _, ch := range sec.Chunks {
		if strings.Contains(body, "\n## "+ch.Heading.pick("zh")) {
			continue
		}
		if strings.Contains(body, ch.ID+"（") {
			withheld++
		}
	}
	if withheld < 2 {
		t.Fatalf("没发出的片段只有 %d 个被点名（应逐个列出 id），模型无法续取", withheld)
	}
	// 至少发出第一块：上限再小也不能只回一句「都在下面」。
	// 注意不能用「有没有 ## 标题」判——每节的第一块是小节前的导语，本来就不带标题前缀。
	if strings.Contains(body, "本节还有") {
		first := firstRunes(sec.Chunks[0].Text.pick("zh"), 24)
		if !strings.Contains(body, first) {
			t.Fatalf("被上限压成空壳：连第一块正文都没发出（期望含 %q）", first)
		}
	}
	// 按小节取必须能拿到完整的一段。
	one, err := docsBodyCapTo("cheatsheet", "cs-port", "zh", cap)
	if err != nil {
		t.Fatalf("按 heading 取失败：%v", err)
	}
	if strings.Contains(one, "还有") && strings.Contains(one, "没发出") {
		t.Fatal("单小块不该再被截断")
	}
	if !strings.Contains(one, "cs-port") && len([]rune(one)) < 20 {
		t.Fatalf("单小节正文过短：%q", one)
	}
}

// 上限按 rune 计而不是字节：中文一个字节数被截半会切出半个字。
func TestDocsBodyCapCountsRunesNotBytes(t *testing.T) {
	if err := docsLoad(); err != nil {
		t.Fatalf("索引加载失败：%v", err)
	}
	const cap = 120
	out, err := docsBodyCapTo("start", "", "zh", cap)
	if err != nil {
		t.Fatalf("取正文失败：%v", err)
	}
	if !utf8.ValidString(out) {
		t.Fatal("截断后不是合法 UTF-8——按字节切了中文字符")
	}
	for _, r := range out {
		if r == '\uFFFD' {
			t.Fatalf("出现替换字符，说明截断切坏了中文：%q", firstRunes(out, 80))
		}
	}
}

func TestDocsBodyRejectsUnknownID(t *testing.T) {
	if err := docsLoad(); err != nil {
		t.Fatalf("索引加载失败：%v", err)
	}
	_, err := docsBody("no-such-section", "", "zh")
	if err == nil {
		t.Fatal("不存在的 id 必须报错，否则模型会以为拿到了内容")
	}
	// 报错要列出可选 id：模型据此重试，而不是放弃或再猜一次。
	if !strings.Contains(err.Error(), "install") {
		t.Fatalf("报错没给出可选 id：%v", err)
	}
}

// 全量守卫：任何一块的英文侧都不许是中文原文。
//
// TestDocsBodyEnglishDiffers 只抽查了一块，而 P/NOTE 的解析曾经有个系统性漏洞：
// 它们是 P('中文','English') 两个位置参数，却被当成 z(...) 只取第一个，
// 于是**每个段落的英文侧都成了中文**（29 块），抽查恰好抽到 TABLE 为主的块就漏了。
func TestDocsNoChineseInEnglishSide(t *testing.T) {
	if err := docsLoad(); err != nil {
		t.Fatalf("索引加载失败：%v", err)
	}
	bad := []string{}
	for _, ch := range docsChunks {
		en := ch.Text.En
		cjk := 0
		for _, r := range en {
			if r >= '\u4e00' && r <= '\u9fff' {
				cjk++
			}
		}
		if cjk > 20 { // 20 字以内允许（文件名、命令里混带的中文）
			bad = append(bad, ch.Anchor)
		}
	}
	if len(bad) > 0 {
		t.Fatalf("%d 块的英文侧仍是中文（第一块 %s）：检查 docs_index.py 的 P/NOTE 解析",
			len(bad), bad[0])
	}
}

// 英文界面下取英文原文：中文和英文必须真的是两份，不能把中文当英文返回。
func TestDocsBodyEnglishDiffers(t *testing.T) {
	if err := docsLoad(); err != nil {
		t.Fatalf("索引加载失败：%v", err)
	}
	zh, err := docsBody("upgrade", "upgrade-overwrite", "zh")
	if err != nil {
		t.Fatalf("取中文失败：%v", err)
	}
	en, err := docsBody("upgrade", "upgrade-overwrite", "en")
	if err != nil {
		t.Fatalf("取英文失败：%v", err)
	}
	if zh == en {
		t.Fatal("英文与中文正文一样，说明 lang 参数没生效")
	}
	if strings.ContainsAny(firstRunes(en, 60), "的一是不了在人有我") && !strings.Contains(en, "the") {
		t.Fatalf("英文正文里还是中文：%q", firstRunes(en, 60))
	}
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// 工具层：args 解析与默认值。模型常把 lang 传成 "EN" / 空串。
func TestDocToolsArgs(t *testing.T) {
	if err := docsLoad(); err != nil {
		t.Fatalf("索引加载失败：%v", err)
	}
	if got := aiDocsLang(map[string]any{}); got != "zh" {
		t.Fatalf("默认语言应为 zh，实际 %s", got)
	}
	if got := aiDocsLang(map[string]any{"lang": "EN"}); got != "en" {
		t.Fatalf("lang=EN 应归一成 en，实际 %s", got)
	}
	out, err := aiToolRegistry["list_docs"].Execute(map[string]any{})
	if err != nil {
		t.Fatalf("list_docs 失败：%v", err)
	}
	if !strings.Contains(out, "install") {
		t.Fatalf("list_docs 没列出节 id：%s", firstRunes(out, 120))
	}
	body, err := aiToolRegistry["get_doc_section"].Execute(map[string]any{"id": "start"})
	if err != nil {
		t.Fatalf("get_doc_section 失败：%v", err)
	}
	if !strings.Contains(body, "envkit-") {
		t.Fatalf("日志节的正文里应出现日志文件名 envkit-*：%s", firstRunes(body, 160))
	}
}
