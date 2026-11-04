package main

// ai_extract_path_test.go —— 「+号 选文件」那条路（/api/ai/extract 的 path 模式）。
//
// 它和拖拽是同一条抽取流水线，差别只在**字节从哪来**。守两件事：
//
//   - 沙箱内的文档真的能读出来。这是本次修的 bug：以前 +号 选中 docx 只加一条
//     路径引用，模型 read_file 时头 512 字节一判二进制就返回"已跳过"，
//     用户看着文件在列表里、模型却说看不到内容。
//   - 沙箱外与凭据类文件仍然拒绝。这个端点是"用户点选"而不是"模型点名"，
//     但边界一旦和 read_file 不一致，以后没人说得清到底能不能读 .ssh。
//
// 还有一条隐含契约：响应里**不许带正文**（带回去就会被塞进输入框，
// 而用户要的是一个附件卡片）。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 沙箱的搭建复用 explore_test.go 的 withProjectRoot（设成唯一后端目录并自动还原），
// 这里只包一层 HTTP 调用。
func postExtractPath(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"path": path, "name": filepath.Base(path)})
	rec := httptest.NewRecorder()
	handleAIExtract(rec, httptest.NewRequest(http.MethodPost, "/api/ai/extract", bytes.NewReader(body)))
	return rec
}

func TestExtractByPathReadsSandboxDoc(t *testing.T) {
	withTempDocDir(t)
	dir := withProjectRoot(t)

	doc := `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:r><w:t>路径模式读到的正文</w:t></w:r></w:p>
</w:body></w:document>`
	p := filepath.Join(dir, "报告.docx")
	if err := os.WriteFile(p, makeZip(t, zf("word/document.xml", doc)), 0o644); err != nil {
		t.Fatalf("写夹具失败：%v", err)
	}

	rec := postExtractPath(t, p)
	if rec.Code != http.StatusOK {
		t.Fatalf("沙箱内的 docx 应能读出，实际 %d：%s", rec.Code, rec.Body.String())
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
	// 扩展名是 .docx，但真类型必须由魔数定 —— 这里顺带验证"不看扩展名"没被绕开
	if out.Kind != "docx" {
		t.Fatalf("kind=%q want docx", out.Kind)
	}
	if out.Chars <= 0 {
		t.Fatalf("chars=%d，正文没被抽出来", out.Chars)
	}
	text, err := aiDocLoad(out.ID)
	if err != nil {
		t.Fatalf("落盘正文读不回来：%v", err)
	}
	if !strings.Contains(text, "路径模式读到的正文") {
		t.Fatalf("落盘内容不对：%q", text)
	}
	// 卡片上显示的字数必须是落盘正文的真实长度 —— 这条以前只会出现在界面上，
	// 对不上时用户看到的是"0 字"却又能正常提问，最难查的那类
	if out.Chars != len([]rune(text)) {
		t.Fatalf("chars=%d 与落盘正文 %d 字不一致", out.Chars, len([]rune(text)))
	}
}

// 沙箱外：拒绝，并且必须给出"拖进来就行"这条出路。
// 只说"不许读"等于把路堵死 —— 拖拽本来就不受路径限制，这是真的替代办法。
func TestExtractByPathRejectsOutsideSandbox(t *testing.T) {
	withTempDocDir(t)
	withProjectRoot(t) // 沙箱指向另一处临时目录，下面这个在它外面
	outside := t.TempDir()

	p := filepath.Join(outside, "note.txt")
	if err := os.WriteFile(p, []byte("项目目录之外的内容"), 0o644); err != nil {
		t.Fatalf("写夹具失败：%v", err)
	}
	rec := postExtractPath(t, p)
	if rec.Code == http.StatusOK {
		t.Fatalf("沙箱外必须拒绝，实际读了：%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "拖") {
		t.Fatalf("拒绝时要给出替代办法（拖进对话区），实际：%s", rec.Body.String())
	}
}

func TestExtractByPathRejectsSecretFile(t *testing.T) {
	withTempDocDir(t)
	dir := withProjectRoot(t)

	p := filepath.Join(dir, ".env")
	if err := os.WriteFile(p, []byte("DB_PASS=1\n"), 0o644); err != nil {
		t.Fatalf("写夹具失败：%v", err)
	}
	rec := postExtractPath(t, p)
	if rec.Code == http.StatusOK {
		t.Fatalf(".env 必须拒绝，实际读了：%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "凭据") {
		t.Fatalf("理由要说清是凭据类文件：%s", rec.Body.String())
	}
}

func TestExtractByPathRejectsMissingAndDir(t *testing.T) {
	withTempDocDir(t)
	dir := withProjectRoot(t)

	if rec := postExtractPath(t, filepath.Join(dir, "没有这个.docx")); rec.Code == http.StatusOK {
		t.Fatalf("不存在的文件必须报错：%s", rec.Body.String())
	}
	// 目录要单独拦：os.ReadFile 对目录在 Windows 上会返回一句系统错误，
	// 直接透给用户就是"Access is denied"，看不懂也不可操作
	if rec := postExtractPath(t, dir); rec.Code == http.StatusOK {
		t.Fatalf("目录必须报错：%s", rec.Body.String())
	}
}
