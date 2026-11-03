package main

// ai_doc_test.go —— 文档附件（拖进对话区的 docx/pdf/文本）的契约测试。
//
// 这里守的是三件**会静默出错**的事：
//   - 正文落盘/读回：读不回来时模型对着空气回答，界面一切正常；
//   - 包裹边界：文件名里的 `">` 能提前闭合 <untrusted_data>，等于让文件内容
//     自己决定"这段是指令还是数据"；
//   - docs 键不许发给上游：多一个自定义键，部分 OpenAI 兼容实现直接 400。

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func withTempDocDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := aiDocDirFn
	aiDocDirFn = func() string { return dir }
	t.Cleanup(func() { aiDocDirFn = old })
	return dir
}

func mustStoreDoc(t *testing.T, text string) string {
	t.Helper()
	id, err := aiDocStore(text)
	if err != nil {
		t.Fatalf("落盘失败：%v", err)
	}
	return id
}

// ---------- 落盘 / 读回 ----------

func TestDocStoreLoadRoundTrip(t *testing.T) {
	dir := withTempDocDir(t)
	text := "第一行：连接超时\n第二行：端口被占用\n"
	id := mustStoreDoc(t, text)

	// 文件名直接就是 id，所以形状必须白名单（防目录穿越）
	if !aiAttachIDOK(id) {
		t.Fatalf("id 形状不对：%q", id)
	}
	if _, err := os.Stat(filepath.Join(dir, id)); err != nil {
		t.Fatalf("正文没落盘：%v", err)
	}
	got, err := aiDocLoad(id)
	if err != nil {
		t.Fatalf("读回失败：%v", err)
	}
	if got != text {
		t.Fatalf("读回内容不一致\n实际：%q\n期望：%q", got, text)
	}
}

func TestDocStoreRejectsEmpty(t *testing.T) {
	withTempDocDir(t)
	if _, err := aiDocStore("   \n\t "); err == nil {
		t.Fatal("空正文不该落盘（会产生一份读不出任何内容的附件）")
	}
}

func TestDocLoadRejectsBadID(t *testing.T) {
	dir := withTempDocDir(t)
	// 目录穿越：id 直接当文件名用，形状必须挡在读取之前
	if err := os.WriteFile(filepath.Join(dir, "..", "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../secret.txt", "..\\secret.txt", "", strings.Repeat("a", 31), strings.Repeat("g", 32)} {
		if _, err := aiDocLoad(id); err == nil {
			t.Fatalf("非法 ID %q 不该被读取", id)
		}
	}
}

// ---------- 注入 prompt ----------

func TestDocHydrateAppendsWrappedText(t *testing.T) {
	withTempDocDir(t)
	id := mustStoreDoc(t, "姓名：黄锐\n端口：8888")
	msgs := []aiMsg{{
		Role:    "user",
		Content: "这份报告说了什么？",
		Docs:    []aiDocRef{{ID: id, Name: "报告.pdf", Kind: "pdf", Chars: 12}},
	}}
	out := aiDocHydrate(msgs)
	c := out[0].Content
	// 用户那句话在前、材料在后：先问什么再给材料，对模型更自然（与图片块同序）
	if !strings.HasPrefix(c, "这份报告说了什么？\n<untrusted_data source=\"file:报告.pdf\">\n") {
		t.Fatalf("拼接形状不对：%q", c)
	}
	if !strings.Contains(c, "姓名：黄锐") || !strings.HasSuffix(c, "</untrusted_data>") {
		t.Fatalf("正文没有完整注入：%q", c)
	}
	// 引用用完必须清掉：上游从没见过 docs 这个键
	if len(out[0].Docs) != 0 {
		t.Fatalf("hydrate 后 Docs 应清空，实际 %+v", out[0].Docs)
	}
}

// 只有附件、没写字的消息也要能注入：'"就这个文件，你看下"本来就常常一个字都不打'。
func TestDocHydrateWithoutUserText(t *testing.T) {
	withTempDocDir(t)
	id := mustStoreDoc(t, "AAA")
	out := aiDocHydrate([]aiMsg{{Role: "user", Content: "", Docs: []aiDocRef{{ID: id, Name: "a.txt"}}}})
	c := out[0].Content
	if strings.HasPrefix(c, "\n") || !strings.HasPrefix(c, "<untrusted_data source=\"file:a.txt\">\nAAA") {
		t.Fatalf("没有用户文字时正文不该带前导空行：%q", c)
	}
}

// 文件名里的引号/尖括号不能把包裹标签提前闭合。
func TestDocHydrateSanitizesName(t *testing.T) {
	withTempDocDir(t)
	id := mustStoreDoc(t, "正文")
	out := aiDocHydrate([]aiMsg{{Role: "user", Content: "看", Docs: []aiDocRef{{ID: id, Name: `a"><b>.txt`}}}})
	c := out[0].Content
	if strings.Count(c, "<untrusted_data") != 1 || strings.Count(c, "</untrusted_data>") != 1 {
		t.Fatalf("包裹标签数量不对（文件名逃逸了）：%q", c)
	}
	if strings.Contains(c, `"><b>`) {
		t.Fatalf("文件名里的危险字符没被替换：%q", c)
	}
}

// 附件过了保留期被清掉：必须留下占位文字并点名是哪份，不能静默当成"用户没给材料"。
func TestDocHydrateMissingFileDegrades(t *testing.T) {
	withTempDocDir(t)
	out := aiDocHydrate([]aiMsg{{
		Role: "user", Content: "看看这个",
		Docs: []aiDocRef{{ID: strings.Repeat("c", 32), Name: "gone.pdf"}},
	}})
	c := out[0].Content
	if strings.Contains(c, "<untrusted_data") {
		t.Fatalf("读不到文件时不该注入空包裹：%q", c)
	}
	if !strings.Contains(c, "gone.pdf") || !strings.Contains(c, "只保留 24 小时") {
		t.Fatalf("占位文字要写清是哪份没了、为什么：%q", c)
	}
	if !strings.Contains(c, "看看这个") {
		t.Fatalf("用户原话不能丢：%q", c)
	}
}

// docs 是前端 ↔ 后端专用键，任何形状的消息都不许把它带给上游。
func TestDocRefNotSerializedToUpstream(t *testing.T) {
	plain := aiMsg{Role: "user", Content: "hi", Docs: []aiDocRef{{ID: strings.Repeat("a", 32), Name: "a.pdf"}}}
	b, err := json.Marshal(plain)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	if strings.Contains(string(b), "docs") {
		t.Fatalf("docs 键不许发给上游：%s", b)
	}
	// 带图的消息走 content 块数组那条分支，同样不能出现 docs
	multi := aiMsg{Role: "user", Content: "hi", Images: []aiImage{{ID: strings.Repeat("b", 32), DataURL: "data:image/png;base64,AA"}},
		Docs: []aiDocRef{{ID: strings.Repeat("a", 32), Name: "a.pdf"}}}
	b2, _ := json.Marshal(multi)
	if strings.Contains(string(b2), "docs") {
		t.Fatalf("带图消息里 docs 也不许出现：%s", b2)
	}
}

// ---------- 抽取端点 ----------

// 端点的契约变了：**只回 id，不回正文**。回正文等于又给了前端一次"把几千字塞进输入框"的机会。
func TestExtractHandlerStoresTextAndReturnsRef(t *testing.T) {
	withTempDocDir(t)
	body, _ := json.Marshal(map[string]any{
		"name": "app.log", "mime": "text/plain",
		"content": "data:text/plain;base64," + base64.StdEncoding.EncodeToString([]byte("第一行日志\n第二行")),
	})
	rec := httptest.NewRecorder()
	handleAIExtract(rec, httptest.NewRequest(http.MethodPost, "/api/ai/extract", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("抽取应成功，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID    string `json:"id"`
		Kind  string `json:"kind"`
		Chars int    `json:"chars"`
		Text  string `json:"text"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("返回不是 JSON：%s", rec.Body.String())
	}
	if out.Text != "" {
		t.Fatalf("响应里不该带正文：%q", out.Text)
	}
	if !aiAttachIDOK(out.ID) {
		t.Fatalf("没返回合法引用 id：%+v", out)
	}
	// "第一行日志" 5 字 + "\n" + "第二行" 3 字
	if out.Kind != "text" || out.Chars != 9 {
		t.Fatalf("kind/chars 不对：%+v", out)
	}
	text, err := aiDocLoad(out.ID)
	if err != nil {
		t.Fatalf("落盘的正文读不回来：%v", err)
	}
	if text != "第一行日志\n第二行" {
		t.Fatalf("落盘内容被改动了：%q", text)
	}
}

// 抽取失败不能落盘：否则目录里会攒下一堆"有引用、没内容"的空壳。
func TestExtractHandlerDoesNotStoreOnFailure(t *testing.T) {
	dir := withTempDocDir(t)
	ole := []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1, 0, 0, 0, 0}
	body, _ := json.Marshal(map[string]any{
		"name": "old.doc", "mime": "application/msword", "content": base64.StdEncoding.EncodeToString(ole),
	})
	rec := httptest.NewRecorder()
	handleAIExtract(rec, httptest.NewRequest(http.MethodPost, "/api/ai/extract", bytes.NewReader(body)))
	if rec.Code == http.StatusOK {
		t.Fatal("旧版 Office 必须拒绝")
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if aiAttachIDOK(e.Name()) {
			t.Fatalf("失败时不该留下文件：%s", e.Name())
		}
	}
}

// ---------- 清理 ----------

func TestDocGCRemovesExpired(t *testing.T) {
	dir := withTempDocDir(t)
	old := filepath.Join(dir, strings.Repeat("a", 32))
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(-aiDocTTL - time.Hour)
	if err := os.Chtimes(old, ts, ts); err != nil {
		t.Fatal(err)
	}
	// 目录里还有别的文件：不认识的形状一律不许动
	foreign := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(foreign, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	aiDocGC()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("超过保留期的文档没被清掉")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("非附件文件被误删：%v", err)
	}
}

// 清理逻辑写好了但没在启动时接上，等于没写（ticker 等不了，只能断言调用点存在）。
func TestDocGCIsWiredIntoMain(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("读 main.go 失败：%v", err)
	}
	if !strings.Contains(string(src), "aiDocStartGC()") {
		t.Fatal("main.go 里没有调用 aiDocStartGC()：文档附件不会自动清理了")
	}
}

// 注入逻辑写好了但没接进对话循环，等于"文件拖进来了、模型什么都没收到" ——
// 而且界面上一切正常（卡片在、字数在），只有模型答非所问能暴露。
// 这类"调用点被删掉"的回退，测 hydrate 函数本身是抓不到的。
func TestDocHydrateIsWiredIntoLoop(t *testing.T) {
	src, err := os.ReadFile("ai_loop.go")
	if err != nil {
		t.Fatalf("读 ai_loop.go 失败：%v", err)
	}
	if !strings.Contains(string(src), "aiDocHydrate(msgs)") {
		t.Fatal("ai_loop.go 里没有调用 aiDocHydrate(msgs)：文档正文不会进上下文")
	}
}
