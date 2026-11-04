package main

// ai_doc_page_test.go —— 附件正文的分页契约。
//
// ## 为什么会有这一组
//
// 早先抽取完就在 64KB 处砍断，然后告诉用户"需要后半段请分段拖入"——对一份 PDF
// 这句话是无法执行的。现在改成：**全文落盘，注入只带第一段，剩下的用 read_doc 分页取**。
//
// 这里守的就是这条链上最容易断的两处：
//   - 注入时又把全文塞回去（等于没改，上下文照样爆）；
//   - 注入里给的 offset 与 read_doc 实际分页口径不一致（模型照着提示读，读回同一段，
//     然后要么死循环要么以为文件就这么点）。后者只有"拿提示里的 offset 真读一次"
//     才测得出来，光断言"提示里有 offset"是空转。

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// docInject 走完整的 hydrate 路径，返回用户那条消息最终要发给上游的 Content。
func docInject(t *testing.T, id, name string) string {
	t.Helper()
	msgs := []aiMsg{{Role: "user", Content: "总结这份文件", Docs: []aiDocRef{{ID: id, Name: name}}}}
	return aiDocHydrate(msgs)[0].Content
}

// tailOf 取末尾一小段，用于失败信息（整份注入内容太长，打出来反而看不到重点）。
func docTail(s string) string {
	if len(s) <= 400 {
		return s
	}
	return "…" + s[len(s)-400:]
}

func mustParseOffset(t *testing.T, s string) int {
	t.Helper()
	m := regexp.MustCompile(`read_doc\(doc_id="[0-9a-f]{32}", offset=(\d+)\)`).FindStringSubmatch(s)
	if m == nil {
		t.Fatalf("注入内容里没有 read_doc(...) 的分页提示：%s", docTail(s))
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 2 {
		t.Fatalf("提示里的 offset 不合理：%q", m[1])
	}
	return n
}

// mustParseLastLine 取提示里"第 X-Y 行"的 Y —— 第一段到底给到第几行。
func mustParseLastLine(t *testing.T, s string) int {
	t.Helper()
	m := regexp.MustCompile(`第 \d+-(\d+) 行`).FindStringSubmatch(s)
	if m == nil {
		t.Fatalf("注入内容里没有标明行范围，模型无从判断读到哪了：%s", docTail(s))
	}
	n, _ := strconv.Atoi(m[1])
	if n < 1 {
		t.Fatalf("行范围不合理：%q", m[1])
	}
	return n
}

// 大文档：只注入第一段 + 明确告诉模型怎么读后续。
func TestDocInjectGivesFirstSliceAndTellsHowToContinue(t *testing.T) {
	withTempDocDir(t)
	text, want := numberedLines(4000, 100) // 约 400KB，远超单次预算
	id := mustStoreDoc(t, text)

	out := docInject(t, id, "大文档.pdf")
	if len(out) >= len(text)/2 {
		t.Fatalf("注入内容 %d 字节，几乎是全文（%d 字节）—— 又把整份塞进上下文了", len(out), len(text))
	}
	if !strings.Contains(out, `read_doc(doc_id="`+id+`"`) {
		t.Fatalf("没告诉模型怎么读后续：%s", docTail(out))
	}
	if !strings.Contains(out, "共 4000 行") {
		t.Fatalf("没说清全文规模，模型无法判断\"还有多少\"：%s", docTail(out))
	}

	// 关键：提示与实现必须是同一个口径。差一行的话模型要么重复读、要么漏掉一段，
	// 而两种症状在界面上都只是"回答得不完整"。
	off := mustParseOffset(t, out)
	last := mustParseLastLine(t, out)
	if off != last+1 {
		t.Fatalf("提示里写 offset=%d，但第一段只到第 %d 行 —— 口径差一行", off, last)
	}
	body := aiDocRead(map[string]any{"doc_id": id, "offset": off})
	if !strings.Contains(body, want[last]) {
		t.Fatalf("按 offset=%d 没读到第 %d 行（第一段之后的第一行）", off, last+1)
	}
	if strings.Contains(body, want[last-1]) {
		t.Fatalf("按 offset=%d 读回了第一段的最后一行 —— 两段重叠", off)
	}
}

// 小文档：原样全给，不额外加一句"这是全部内容"（每轮都多几十字节，却没有新信息）。
func TestDocInjectSmallFileHasNoPagingHint(t *testing.T) {
	withTempDocDir(t)
	id := mustStoreDoc(t, "这是一份很短的说明\n第二行")
	out := docInject(t, id, "短.txt")
	if strings.Contains(out, "read_doc") {
		t.Fatalf("小文件不该出现分页提示：%s", out)
	}
	if !strings.Contains(out, "第二行") {
		t.Fatalf("小文件应完整注入：%s", out)
	}
}

// 找不到 / 形状不对的 id：必须说清楚是"过期或记错"，不能只说"失败"。
func TestDocReadRejectsUnknownOrBadID(t *testing.T) {
	withTempDocDir(t)
	if out := aiDocRead(map[string]any{"doc_id": "0123456789abcdef0123456789abcdef"}); !strings.Contains(out, "24 小时") {
		t.Fatalf("过期提示没写清楚：%s", out)
	}
	// 目录穿越：id 直接当文件名用，形状白名单必须挡住
	for _, bad := range []string{"../../config.json", "..\\config.json", "", "abc"} {
		out := aiDocRead(map[string]any{"doc_id": bad})
		if !strings.Contains(out, "读取失败") {
			t.Fatalf("非法 id %q 未被拒绝：%s", bad, out)
		}
	}
}

// read_doc 必须是"直接执行"级：忘了定级会落到默认的 PolicyConfirm，
// 于是模型每读一段附件就弹一次确认卡 —— 一个 300 页 PDF 能弹十几次。
func TestDocReadIsAutoPolicy(t *testing.T) {
	r, ok := policyRules["read_doc"]
	if !ok {
		t.Fatal("read_doc 没在 policyRules 里定级")
	}
	if r.Level != PolicyAuto {
		t.Fatalf("read_doc 应为 PolicyAuto（只读附件），实际 %v", r.Level)
	}
}

// read_doc 的结果不能被 aiClampToolResult 砍：分页的意义就是把整段交给模型。
func TestDocReadResultNotClamped(t *testing.T) {
	long := strings.Repeat("x", 5000)
	if got := aiClampToolResult("read_doc", long); got != long {
		t.Fatalf("read_doc 结果被截断到 %d 字节", len(got))
	}
}

// 抽取侧不再按 64KB 截断：全文都要落到盘上，否则 read_doc 读到的"后续"是不存在的。
func TestExtractHandlerStoresFullTextBeyondBudget(t *testing.T) {
	withTempDocDir(t)
	// 3000 行 × 100 字节 = 约 300KB 纯文本，远超 64KB 预算
	text, _ := numberedLines(3000, 100)
	body, _ := json.Marshal(map[string]string{
		"name":    "big.log",
		"content": "data:text/plain;base64," + base64.StdEncoding.EncodeToString([]byte(text)),
	})
	w := httptest.NewRecorder()
	handleAIExtract(w, httptest.NewRequest(http.MethodPost, "/api/ai/extract", bytes.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("抽取失败：%d %s", w.Code, w.Body.String())
	}
	var res struct {
		ID      string `json:"id"`
		Chars   int    `json:"chars"`
		Trunc   bool   `json:"truncated"`
		Content string `json:"text"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if res.Trunc {
		t.Fatal("普通大小（300KB）的文本不该被判为截断")
	}
	if res.Chars < 300000 {
		t.Fatalf("chars=%d 说明正文被截断了，read_doc 就读不到后续", res.Chars)
	}
	stored, err := aiDocLoad(res.ID)
	if err != nil {
		t.Fatalf("落盘正文读不回来：%v", err)
	}
	if len(stored) != len(text) {
		t.Fatalf("落盘长度 %d 与原文本 %d 不符", len(stored), len(text))
	}
	if strings.Contains(res.Content, "L0001") {
		t.Fatal("响应里不该带正文（带回去就会被塞进输入框）")
	}
}
