package main

// ai_extract_test.go —— 文档抽取的行为契约。
//
// 这里所有夹具都是**现造**的（zip + XML / 手拼 PDF），不放二进制样本：
// 真文件动辄几百 KB，而且它们只能证明"这一份能读"，改个元素名就回归不了。
// 造出来的夹具能精确断言"表格变成 TSV 两行""sheet10 排在 sheet2 后面"这种口径。

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"testing"
	"unicode/utf16"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// makeZip 现造一个 zip 容器。
//
// 用**有序切片**而不是 map：map 遍历顺序随机，夹具里条目的先后就不固定，
// 于是"排序失效"这个变异有时被抓住、有时不被抓住 —— 这种测试比没有更糟，
// 因为它会让人相信已经覆盖了。
type zipEntry struct{ name, content string }

func zf(name, content string) zipEntry { return zipEntry{name, content} }

func makeZip(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatalf("zip create %s: %v", e.name, err)
		}
		if _, err := io.WriteString(w, e.content); err != nil {
			t.Fatalf("zip write %s: %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// makePDF 手拼一个单页 PDF（无压缩流），偏移量现算 —— xref 错一个字节
// 整个文件就解不开，所以这里不能偷懒写死。
func makePDF(content string) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	objs := []string{
		"",
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Kids[3 0 R]/Count 1>>",
		"<</Type/Page/Parent 2 0 R/MediaBox[0 0 300 200]/Contents 4 0 R" +
			"/Resources<</Font<</F1 5 0 R>>>>>>",
		fmt.Sprintf("<</Length %d>>\nstream\n%s\nendstream", len(content), content),
		"<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>",
	}
	offsets := make([]int, len(objs))
	for i := 1; i < len(objs); i++ {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i, objs[i])
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objs))
	for i := 1; i < len(objs); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<</Size %d/Root 1 0 R>>\nstartxref\n%d\n%%%%EOF\n", len(objs), xref)
	return buf.Bytes()
}

// ---------- 魔数分流 ----------

func TestExtractSniff(t *testing.T) {
	utf16le := append([]byte{0xFF, 0xFE}, []byte("h\x00i\x00")...)
	cases := []struct {
		name string
		raw  []byte
		want string
	}{
		{"pdf", []byte("%PDF-1.7\n..."), "pdf"},
		{"zip-docx", []byte("PK\x03\x04rest"), "zip"},
		{"zip-empty", []byte("PK\x05\x06"), "zip"},
		{"ole2", append(append([]byte{}, aiOLE2Magic...), 0, 0), "ole2"},
		{"nul-binary", []byte("abc\x00def"), "bin"},
		{"utf16-bom-is-text", utf16le, "text"},
		{"plain", []byte("package main\n"), "text"},
		// 存在 NUL 但只在前 8KB 之外：不当作二进制（大文件里偶尔有尾部填充）
		{"nul-far-away", append(bytes.Repeat([]byte("a"), 9000), 0), "text"},
	}
	for _, c := range cases {
		if got := aiExtractSniff(c.raw); got != c.want {
			t.Errorf("%s: sniff=%q want %q", c.name, got, c.want)
		}
	}
}

// ---------- docx ----------

func TestExtractDOCX(t *testing.T) {
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:r><w:t>第一段</w:t></w:r></w:p>
<w:p><w:r><w:t>姓名：</w:t></w:r><w:r><w:t>张三</w:t></w:r></w:p>
<w:tbl>
<w:tr><w:tc><w:p><w:r><w:t>地区</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>金额</w:t></w:r></w:p></w:tc></w:tr>
<w:tr><w:tc><w:p><w:r><w:t>华东</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>1200</w:t></w:r></w:p></w:tc></w:tr>
</w:tbl>
<w:p><w:r><w:t>结尾</w:t></w:r></w:p>
</w:body></w:document>`
	res, err := aiExtractBytes(makeZip(t, zf("word/document.xml", doc)))
	if err != nil {
		t.Fatalf("docx extract: %v", err)
	}
	if res.Kind != "docx" {
		t.Fatalf("kind=%q want docx", res.Kind)
	}
	lines := nonEmptyLines(res.Text)
	// 分段：第一段 / 姓名(两个 run 必须拼起来) / 表格两行 / 结尾
	if len(lines) != 5 {
		t.Fatalf("lines=%d want 5:\n%s", len(lines), res.Text)
	}
	if lines[0] != "第一段" {
		t.Errorf("line0=%q", lines[0])
	}
	// 关键：同一个 <w:p> 里的多个 <w:t> 必须拼成一行，不能拆开
	if lines[1] != "姓名：张三" {
		t.Errorf("line1=%q want 姓名：张三（同一段落的多个 run 应拼接）", lines[1])
	}
	// 关键：表格一行里的两个单元格要用 tab 分开（TSV），不能各占一行
	if fields := strings.Split(lines[2], "\t"); len(fields) != 2 || fields[0] != "地区" || fields[1] != "金额" {
		t.Errorf("table row0=%q want 地区<TAB>金额", lines[2])
	}
	if fields := strings.Split(lines[3], "\t"); len(fields) != 2 || fields[0] != "华东" || fields[1] != "1200" {
		t.Errorf("table row1=%q want 华东<TAB>1200", lines[3])
	}
	if lines[4] != "结尾" {
		t.Errorf("line4=%q", lines[4])
	}
}

// ---------- xlsx ----------

func TestExtractXLSX(t *testing.T) {
	shared := `<?xml version="1.0" encoding="UTF-8"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<si><t>姓名</t></si><si><r><t>张</t></r><r><t>三</t></r></si>
</sst>`
	sheet := func(rows string) string {
		return `<?xml version="1.0" encoding="UTF-8"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>` + rows + `</sheetData></worksheet>`
	}
	// 第 2 个 sheet 故意排在字典序更后的位置（sheet10 vs sheet2），
	// 用来锁住"按数字排序而不是按字符串排序"
	s2 := sheet(`<row r="1"><c r="A1"><v>2号表</v></c></row>`)
	s10 := sheet(`<row r="1"><c r="A1"><v>10号表</v></c></row>`)
	// **写入顺序刻意是 10 → 2 → 1**：排序一旦失效，输出顺序就跟着反过来，
	// 断言才有机会红。若按 1 → 2 → 10 写，即使完全不排序也碰巧是对的。
	raw := makeZip(t,
		zf("xl/workbook.xml", `<workbook/>`),
		zf("xl/sharedStrings.xml", shared),
		zf("xl/worksheets/sheet10.xml", s10),
		zf("xl/worksheets/sheet2.xml", s2),
		// 主表：t="s" 是共享字符串索引；无 t 的取 <v> 原文；公式取缓存值
		zf("xl/worksheets/sheet1.xml", sheet(
			`<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="C1" t="s"><v>9</v></c></row>`+
				`<row r="2"><c r="A2"><v>42</v></c><c r="B2"><f>SUM(A1:A2)</f><v>84</v></c></row>`+
				`<row r="3"><c r="A3"/><c r="B3"><v>换个行</v></c></row>`)),
	)
	res, err := aiExtractBytes(raw)
	if err != nil {
		t.Fatalf("xlsx extract: %v", err)
	}
	if res.Kind != "xlsx" {
		t.Fatalf("kind=%q", res.Kind)
	}
	// sheet2 必须在 sheet10 之前
	i2 := strings.Index(res.Text, "2号表")
	i10 := strings.Index(res.Text, "10号表")
	if i2 < 0 || i10 < 0 || i2 > i10 {
		t.Fatalf("sheet 顺序错：sheet2@%d sheet10@%d\n%s", i2, i10, res.Text)
	}
	if !strings.Contains(res.Text, "姓名\t张三") {
		t.Errorf("共享字符串没有展开：\n%s", res.Text)
	}
	// 索引越界（9 超过表长）不能 panic，也不能把 "9" 当内容
	if strings.Contains(res.Text, "\t9") {
		t.Errorf("越界索引被当成内容输出了：\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "84") {
		t.Errorf("公式缓存值丢了：\n%s", res.Text)
	}
}

// ---------- pptx ----------

func TestExtractPPTX(t *testing.T) {
	slide := func(body string) string {
		return `<?xml version="1.0" encoding="UTF-8"?>
<p:sld xmlns:p="p" xmlns:a="a"><p:cSld><p:spTree><p:sp><p:txBody>` + body + `</p:txBody></p:sp></p:spTree></p:cSld></p:sld>`
	}
	// 写入顺序 2 → 1，排序失效就会原样输出
	raw := makeZip(t,
		zf("ppt/presentation.xml", `<p:presentation xmlns:p="p"/>`),
		zf("ppt/slides/slide2.xml", slide(`<a:p><a:r><a:t>第二页要点</a:t></a:r></a:p>`)),
		zf("ppt/slides/slide1.xml", slide(`<a:p><a:r><a:t>标题页</a:t></a:r></a:p>`)),
	)
	res, err := aiExtractBytes(raw)
	if err != nil {
		t.Fatalf("pptx extract: %v", err)
	}
	if res.Kind != "pptx" {
		t.Fatalf("kind=%q", res.Kind)
	}
	if strings.Index(res.Text, "标题页") > strings.Index(res.Text, "第二页要点") {
		t.Errorf("幻灯片顺序错：\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "第 1 页") || !strings.Contains(res.Text, "第 2 页") {
		t.Errorf("缺少页码分隔：\n%s", res.Text)
	}
}

// ---------- pdf ----------

func TestExtractPDF(t *testing.T) {
	raw := makePDF("BT /F1 20 Tf 20 120 Td (Hello EnvKit PDF) Tj ET")
	res, err := aiExtractBytes(raw)
	if err != nil {
		t.Fatalf("pdf extract: %v", err)
	}
	if res.Kind != "pdf" {
		t.Fatalf("kind=%q", res.Kind)
	}
	if res.Pages != 1 {
		t.Errorf("pages=%d want 1", res.Pages)
	}
	if !strings.Contains(res.Text, "Hello EnvKit PDF") {
		t.Errorf("正文没抽出来：%q", res.Text)
	}
}

// 空页（有页面、没文字层）：必须报"没有文字层"，不能返回空串。
// 返回空串的话，前端会把它当成"读到了 0 个字"塞进输入框，
// 用户看到的是一个空引用块，模型只能瞎猜。
func TestExtractPDFNoTextLayer(t *testing.T) {
	raw := makePDF("BT ET")
	_, err := aiExtractBytes(raw)
	if err == nil {
		t.Fatal("空文字层应当报错")
	}
	if !strings.Contains(err.Error(), "文字层") {
		t.Errorf("错误信息要说清是扫描件/没有文字层：%v", err)
	}
}

// ---------- 编码 ----------

func TestExtractTextEncoding(t *testing.T) {
	gbk, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte("中文日志一行\n第二行"))
	if err != nil {
		t.Fatalf("gb18030 encode: %v", err)
	}
	u := utf16.Encode([]rune("UTF-16 中文"))
	le := make([]byte, 0, 2+len(u)*2)
	le = append(le, 0xFF, 0xFE)
	for _, v := range u {
		le = binary.LittleEndian.AppendUint16(le, v)
	}
	cases := []struct {
		name    string
		raw     []byte
		wantEnc string
		wantHas string
	}{
		{"utf8", []byte("纯中文 UTF-8"), "utf-8", "纯中文"},
		{"utf8-bom", append([]byte{0xEF, 0xBB, 0xBF}, []byte("带BOM")...), "utf-8", "带BOM"},
		{"gb18030", gbk, "gb18030", "第二行"},
		{"utf16le", le, "utf-16le", "UTF-16 中文"},
	}
	for _, c := range cases {
		res, err := aiExtractBytes(c.raw)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if res.Enc != c.wantEnc {
			t.Errorf("%s: enc=%q want %q", c.name, res.Enc, c.wantEnc)
		}
		if !strings.Contains(res.Text, c.wantHas) {
			t.Errorf("%s: text=%q 不含 %q", c.name, res.Text, c.wantHas)
		}
		// BOM 不能留在正文里
		if strings.HasPrefix(res.Text, "\ufeff") {
			t.Errorf("%s: BOM 没剥掉", c.name)
		}
	}
}

// ---------- 拒绝路径 ----------

func TestExtractRejects(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		want string
	}{
		{"ole2", append(append([]byte{}, aiOLE2Magic...), make([]byte, 64)...), "另存为"},
		{"binary", []byte("MZ\x00\x00binary"), "二进制"},
		// 普通压缩包：能解开但不是 OOXML，必须说"请解压后再拖"，而不是"读不出来"
		{"plain-zip", makeZip(t, zf("a.txt", "hi")), "解压"},
		// 有 document.xml 但里面没有文字
		{"empty-docx", makeZip(t, zf("word/document.xml", "<w:document><w:body/></w:document>")), "没有可提取的文字"},
	}
	for _, c := range cases {
		_, err := aiExtractBytes(c.raw)
		if err == nil {
			t.Errorf("%s: 应当报错", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: 错误信息要包含 %q，实际：%v", c.name, c.want, err)
		}
	}
}

// ---------- 截断 ----------

func TestExtractCut(t *testing.T) {
	// 切在行边界：不能把一行切成半句
	body := strings.Repeat("一行内容\n", 200)
	got := aiExtractCut(body, 100)
	if len(got) > 100 {
		t.Fatalf("超限：%d", len(got))
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("应当切在行边界，实际结尾 %q", got[len(got)-8:])
	}
	// 多字节字符不能被切成半个
	cn := strings.Repeat("中", 100)
	got = aiExtractCut(cn, 51)
	if !utf8ValidString(got) {
		t.Errorf("切出了非法 UTF-8")
	}
	if len([]rune(got)) != 17 {
		t.Errorf("rune 数=%d want 17（51 字节 / 3）", len([]rune(got)))
	}
}

func utf8ValidString(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

// ---------- 控制符 ----------

func TestStripCtrl(t *testing.T) {
	in := "正常\x01文本\x1b[32m绿色\x1b[0m\n\t制表"
	got := aiStripCtrl(in)
	if strings.ContainsAny(got, "\x01\x1b") {
		t.Errorf("控制符没清干净：%q", got)
	}
	if !strings.Contains(got, "\n\t") {
		t.Errorf("换行与制表要保留：%q", got)
	}
	if !strings.Contains(got, "正常文本") || !strings.Contains(got, "绿色") {
		t.Errorf("正文字符丢了：%q", got)
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
