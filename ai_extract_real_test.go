package main

// ai_extract_real_test.go —— 用**真实文档暴露出来的结构特征**做回归夹具。
//
// 这些形状不是凭空设计的，是从本机几份真文件里量出来的：
//   - Word/WPS 导出的 PDF：一行字被切成一堆 Tj，库给每个片段加 \n；
//   - 期刊 PDF：每行一个完整 Tj，行间只有一个 \n（不能被误合并）；
//   - 导出的 xlsx：空单元格不写进 XML，靠 <c r="D3"> 标位置；
//   - 用户给工作表起中文名，顺序写在 workbook.xml 的 <sheet r:id> 里；
//   - PPTX 放映顺序在 presentation.xml 的 <p:sldIdLst> 里，不是文件名。
//
// 夹具一律现造（zip + XML / 手拼 PDF），不放二进制样本：放进去只能证明
// "这一份能读"，改一行解析逻辑就回归不了。

import (
	"fmt"
	"strings"
	"testing"
)

func realCell(ref, val string) string {
	return `<c r="` + ref + `" t="inlineStr"><is><t>` + val + `</t></is></c>`
}

func realSheet(rows string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>` +
		rows + `</sheetData></worksheet>`
}

func realWorkbook(sheets string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>` +
		sheets + `</sheets></workbook>`
}

func realWorkbookRels(pairs string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		pairs + `</Relationships>`
}

func realSlide(body string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<p:sld xmlns:p="p" xmlns:a="a"><p:cSld><p:spTree><p:sp><p:txBody>` +
		body + `</p:txBody></p:sp></p:spTree></p:cSld></p:sld>`
}

// ---------- xlsx：空单元格必须占住自己的列 ----------

// 真实文件「奶粉溯源完整信息」sheet2：表头 A/B/C/D/E 五列，
// 数据行只有 A、B、D 有值（C 地址、E 交易哈希是空的，XML 里根本没有 <c>）。
// 按顺序 append 会把 D 的"明细：…"错放到 C（地址）列上。
func TestExtractXLSXSparseColumns(t *testing.T) {
	head := `<row r="1">` + realCell("A1", "类型") + realCell("B1", "时间") +
		realCell("C1", "地址") + realCell("D1", "详细信息") + realCell("E1", "交易哈希") + `</row>`
	data := `<row r="2">` + realCell("A2", "Transport") + realCell("B2", "2025/4/11") +
		realCell("D2", "明细：运输正常") + `</row>`
	raw := makeZip(t,
		zf("xl/workbook.xml", `<workbook/>`),
		zf("xl/worksheets/sheet1.xml", realSheet(head+data)),
	)
	res, err := aiExtractBytes(raw)
	if err != nil {
		t.Fatalf("xlsx extract: %v", err)
	}
	if res.Kind != "xlsx" {
		t.Fatalf("kind=%q want xlsx", res.Kind)
	}
	var header, row string
	for _, l := range nonEmptyLines(res.Text) {
		if strings.HasPrefix(l, "类型\t") {
			header = l
		}
		if strings.HasPrefix(l, "Transport\t") {
			row = l
		}
	}
	if header == "" || row == "" {
		t.Fatalf("没找到表头/数据行：\n%s", res.Text)
	}
	if n := len(strings.Split(header, "\t")); n != 5 {
		t.Errorf("表头 %d 列，want 5：%q", n, header)
	}
	f := strings.Split(row, "\t")
	// 末尾的空列被 TrimRight 掉是正常的：这里只要 4 列（A/B/C/D）
	if len(f) != 4 {
		t.Fatalf("数据行 %d 列，want 4（A/B/空C/D）：%q", len(f), row)
	}
	if f[2] != "" {
		t.Errorf("C 列（地址）应当是空的，实际是 %q —— 说明后面的值左移了", f[2])
	}
	if f[3] != "明细：运输正常" {
		t.Errorf("D 列（详细信息）值错位：%q", f[3])
	}
}

// ---------- xlsx：表名与顺序来自 workbook.xml，不是文件名 ----------

func TestExtractXLSXSheetNamesAndOrder(t *testing.T) {
	// workbook 顺序刻意是 sheet2 → sheet1，而 zip 写入顺序是 sheet1 → sheet2。
	// 按文件名排、或忽略 r:id，都会把"乙表内容"排到后面。
	wb := realWorkbook(
		`<sheet name="第二张" sheetId="2" r:id="rId2"/>` +
			`<sheet name="第一张" sheetId="1" r:id="rId1"/>`)
	rels := realWorkbookRels(
		`<Relationship Id="rId1" Target="worksheets/sheet1.xml"/>` +
			`<Relationship Id="rId2" Target="worksheets/sheet2.xml"/>`)
	raw := makeZip(t,
		zf("xl/workbook.xml", wb),
		zf("xl/_rels/workbook.xml.rels", rels),
		zf("xl/worksheets/sheet1.xml", realSheet(`<row r="1">`+realCell("A1", "甲表内容")+`</row>`)),
		zf("xl/worksheets/sheet2.xml", realSheet(`<row r="1">`+realCell("A1", "乙表内容")+`</row>`)),
	)
	res, err := aiExtractBytes(raw)
	if err != nil {
		t.Fatalf("xlsx extract: %v", err)
	}
	if !strings.Contains(res.Text, "### 第二张") || !strings.Contains(res.Text, "### 第一张") {
		t.Fatalf("中文工作表名丢了：\n%s", res.Text)
	}
	if strings.Contains(res.Text, "### sheet1") || strings.Contains(res.Text, "### sheet2") {
		t.Errorf("标题不该用文件名：\n%s", res.Text)
	}
	iSecond := strings.Index(res.Text, "### 第二张")
	iFirst := strings.Index(res.Text, "### 第一张")
	iYi := strings.Index(res.Text, "乙表内容")
	iJia := strings.Index(res.Text, "甲表内容")
	if !(iSecond < iYi && iYi < iFirst && iFirst < iJia) {
		t.Errorf("工作表顺序/内容配对错了 second=%d yi=%d first=%d jia=%d\n%s",
			iSecond, iYi, iFirst, iJia, res.Text)
	}
}

// ---------- pptx：放映顺序来自 sldIdLst ----------

func TestExtractPPTXSlideShowOrder(t *testing.T) {
	// 用户把第 2 页拖到了最前面：sldIdLst 是 rId2 → rId1，
	// 而文件名仍是 slide1 / slide2。按文件名排就会读反。
	pres := `<?xml version="1.0" encoding="UTF-8"?>
<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
		`<p:sldIdLst><p:sldId id="256" r:id="rId2"/><p:sldId id="257" r:id="rId1"/></p:sldIdLst></p:presentation>`
	rels := realWorkbookRels(
		`<Relationship Id="rId1" Target="slides/slide1.xml"/>` +
			`<Relationship Id="rId2" Target="slides/slide2.xml"/>`)
	raw := makeZip(t,
		zf("ppt/presentation.xml", pres),
		zf("ppt/_rels/presentation.xml.rels", rels),
		zf("ppt/slides/slide1.xml", realSlide(`<a:p><a:r><a:t>甲页内容</a:t></a:r></a:p>`)),
		zf("ppt/slides/slide2.xml", realSlide(`<a:p><a:r><a:t>乙页内容</a:t></a:r></a:p>`)),
	)
	res, err := aiExtractBytes(raw)
	if err != nil {
		t.Fatalf("pptx extract: %v", err)
	}
	if res.Kind != "pptx" {
		t.Fatalf("kind=%q want pptx", res.Kind)
	}
	if !strings.Contains(res.Text, "### 第 1 页\n乙页内容") {
		t.Errorf("第一张应当是 slide2（乙页）：\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "### 第 2 页\n甲页内容") {
		t.Errorf("第二张应当是 slide1（甲页）：\n%s", res.Text)
	}
}

// ---------- pdf：Word 导出的碎片要拼回来 ----------

// 这段是「请假申请—黄锐.pdf」第 1 页的原始输出形状（逐字抄下来的结构）：
// 库给每个 Tj 片段加 \n，真正的段落边界是一个空白行。
func TestPDFReflowWordFragments(t *testing.T) {
	in := "\n1.\n \n姓名：黄锐\n \n学号：\n2\n021304900\n \n" +
		"专业班级：区块链\n2\n1\n-\n1\n \n" +
		"请假时间：共四天，\n4\n.5\n（周三，清明），\n4\n.6\n（周四）\n \n"
	got := aiPDFReflow(aiPDFNormalizeSpace(in))
	for _, want := range []string{
		"学号：2021304900\n",
		"专业班级：区块链21-1\n",
		"请假时间：共四天，4.5（周三，清明），4.6（周四）\n",
		"姓名：黄锐\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("碎片没拼回来，缺 %q：\n%s", want, got)
		}
	}
	if strings.Contains(got, "\n2\n") || strings.Contains(got, "\n4\n") {
		t.Errorf("还残留单字碎片行：\n%s", got)
	}
}

// 期刊 / LaTeX 生成的 PDF 每行就是完整 Tj，行间只有一个 \n。
// 对它们做合并会把整页粘成一行 —— 这类误伤比碎片更糟。
func TestPDFReflowKeepsTypesetText(t *testing.T) {
	in := "基于Bregman散度和差分隐私的个性化联邦学习方法*\n" +
		"张少波1,    张激勇1,    朱更明1\n" +
		"摘 要: 联邦学习因能解决数据孤岛问题而被广泛关注, 但也存在用户隐私泄露风险.\n" +
		"关键词: 隐私保护; 个性化联邦学习; 差分隐私\n\n" +
		"1 引 言\n随着数据驱动智能应用的快速发展, 机器学习在自然语言处理中已被广泛应用.\n"
	if got := aiPDFReflow(in); got != in {
		t.Errorf("排版正常的文本不该被改动：\ngot  = %q\nwant = %q", got, in)
	}
}

func TestPDFNormalizeSpace(t *testing.T) {
	got := aiPDFNormalizeSpace("摘\u3000要:\u00a0\u00a0\u00a0联邦\u200b学习")
	if got != "摘 要: 联邦学习" {
		t.Errorf("got=%q want %q", got, "摘 要: 联邦学习")
	}
}

// 端到端：确认 aiExtractPDF 真的调用了重排（只测纯函数抓不住"调用被删"）。
// makePDF 里每个独立 BT/ET 块会被库输出成一行短片段，正好是碎片形状。
func TestExtractPDFReflowsFragmentedPage(t *testing.T) {
	var b strings.Builder
	for i, ch := range []string{"A", "B", "C", "D", "E", "F"} {
		fmt.Fprintf(&b, "BT /F1 12 Tf 20 %d Td (%s) Tj ET ", 120-i*10, ch)
	}
	res, err := aiExtractBytes(makePDF(b.String()))
	if err != nil {
		t.Fatalf("pdf extract: %v", err)
	}
	if !strings.Contains(res.Text, "ABCDEF") {
		t.Errorf("碎片没有拼成一行：\n%s", res.Text)
	}
	if strings.Contains(res.Text, "\nA\nB\nC\n") {
		t.Errorf("仍残留逐字碎片：\n%s", res.Text)
	}
}

// ---------- 列引用解析 ----------

func TestXLSXColNum(t *testing.T) {
	cases := []struct {
		ref  string
		want int
	}{
		{"A1", 1}, {"C3", 3}, {"D32", 4}, {"Z9", 26}, {"AA12", 27},
		{"", 0}, {"12", 0}, {"ZZZZ1", 0}, // 超上限：不许造出上万个 tab
	}
	for _, c := range cases {
		if got := aiXLSXColNum(c.ref); got != c.want {
			t.Errorf("%q: got %d want %d", c.ref, got, c.want)
		}
	}
}
