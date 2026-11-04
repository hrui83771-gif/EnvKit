package main

// ai_extract.go —— 拖进 AI 对话区的**文档**抽取：docx / xlsx / pptx / pdf。
//
// ## 为什么这件事必须搬到后端
//
// 之前这条路径在前端用 `File.text()`，两个问题都无解：
//
//  1. 挡不住二进制。拖一个 .docx 进去，`text()` 会把 zip 里的压缩流按 UTF-8
//     强行解码，得到一坨乱码塞进输入框 —— 用户以为"文件读进来了"，
//     模型看到的是几千个 U+FFFD，白烧 token 还答不出东西。
//  2. 修不了编码。`File` 没有"按什么编码解码"的参数，浏览器一律按 UTF-8。
//     国内常见的 GBK 日志、老 CSV 拖进来就是一片问号。
//
// 后端能同时解决这两个：文件头判类型 + 编码探测，都是用标准库就能做的事。
//
// ## 类型一律按**魔数**判定，不看扩展名
//
// 和 aiImageMIME() 同一个理由：扩展名是用户随手改的，内容不是。
// 一个 `.txt` 里装着 PDF 字节，按扩展名走就会抽出乱码。
//
// ## 为什么 PDF 用第三方库
//
// 标准库没有 PDF 解码。手写要处理 xref 表 / 对象流 / FlateDecode / 字体
// ToUnicode CMap 四层，任何一层错一个字节就是整份文档读不出来 ——
// 而这个库（ledongthuc/pdf）是纯 Go、无 cgo，正合"单个 exe 到处跑"的约束。
//
// ## 老 .doc/.xls/.ppt 明确拒绝
//
// OLE2 复合文档要解析扇区分配表 + WordDocument 流 + 片段表（piece table），
// 实现量比上面全部加起来还大，而且极易在异常文件上崩。收益（读十几年前的
// 老文档）远小于代价。所以给一句能照做的提示：另存为 .docx。

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
	"golang.org/x/text/encoding/simplifiedchinese"
)

const (
	// aiExtractMaxBytes 原始文件上限。16MB 与 /api/ai/chat 的请求体上限一致；
	// 一份 300 页带图的 PDF 大概 10~20MB，再大就该先拆。
	aiExtractMaxBytes = 16 << 20
	// aiExtractMaxPages PDF 页数上限：不是能力限制，是防"拖一个 5000 页的手册进来
	// 把整轮对话的上下文一次性烧光"。真需要整本读，用户可以分段拖。
	aiExtractMaxPages = 400
)

// aiExtractResult 是抽取结果。Text 已经过编码统一与截断，可直接塞进 prompt。
type aiExtractResult struct {
	Kind  string // docx / xlsx / pptx / pdf / text
	Enc   string // 实际采用的编码：utf-8 / gb18030 / utf-16le / utf-16be
	Text  string
	Chars int  // 截断后的字符数（rune 数，不是字节数）
	Pages int  // 仅 pdf
	Trunc bool // 是否因超上限被截断
	Note  string
}

// handleAIExtract 做两件事：
//
//	POST /api/ai/extract  {name, mime, content(base64 可带 data: 前缀)}
//	 → {ok, id, kind, enc, chars, pages, truncated, note}
//
// 抽出来的正文**不回传前端**（只给 id）：前端拿到几千字只能往输入框里塞，
// 而用户要的是"一个文件附件"，不是"把文档贴进聊天"。正文落盘为 aidoc/<id>，
// 每轮发给上游前由 aiDocHydrate 读盘拼进 content（见 ai_doc.go）。
//
// 和 /api/ai/attach 的分工：图片是像素，必须按 content 块发送，所以要 GET 回来做缩略图；
// 文档是文字，只需要一个引用 + 服务端读盘，没有"回显"需求。
func handleAIExtract(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// base64 后体积约涨 1/3，再留 JSON 包装余量
	r.Body = http.MaxBytesReader(w, r.Body, aiExtractMaxBytes*2)
	var body struct {
		Name    string `json:"name"`
		MIME    string `json:"mime"`
		Content string `json:"content"`
		// Path：「+号」文件选择器点中的磁盘路径。
		// 与 Content 二选一 —— 拖拽时字节由浏览器送来，而选择器手里只有一个路径，
		// 内容只能由服务端自己读。两条路在 aiExtractBytes 汇合，之后完全一致。
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, fmt.Sprintf("请求体过大或格式错误（文件上限 %d MB）", aiExtractMaxBytes>>20), http.StatusBadRequest)
		return
	}
	var (
		raw  []byte
		name string
		err  error
	)
	if p := strings.TrimSpace(body.Path); p != "" {
		raw, name, err = aiExtractReadPath(p)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		b64 := body.Content
		if strings.HasPrefix(b64, "data:") {
			if i := strings.Index(b64, ","); i >= 0 {
				b64 = b64[i+1:]
			}
		}
		raw, err = base64.StdEncoding.DecodeString(b64)
		if err != nil || len(raw) == 0 {
			http.Error(w, "文件内容解码失败或为空", http.StatusBadRequest)
			return
		}
		if len(raw) > aiExtractMaxBytes {
			http.Error(w, fmt.Sprintf("文件过大（%.1f MB，上限 %d MB）",
				float64(len(raw))/(1<<20), aiExtractMaxBytes>>20), http.StatusRequestEntityTooLarge)
			return
		}
		// 文件名只作展示与日志用：剥掉路径分隔符，避免它被当成"路径"读
		name = filepath.Base(strings.TrimSpace(body.Name))
	}
	if name == "" || name == "." {
		name = "文件"
	}

	res, err := aiExtractBytes(raw)
	if err != nil {
		// 走到这里的是"读不了"，理由都已经写成人话，直接透给前端显示
		http.Error(w, err.Error(), http.StatusUnsupportedMediaType)
		return
	}
	// 统一清掉控制符：PDF 文本流里常见 0x01/0x02 这类内部标记，
	// 终端日志里还有 ANSI 色码（\x1b[32m）—— 它们不是内容，
	// 混进 prompt 只会让模型把十六进制转义当成正文的一部分。
	res.Text = aiStripCtrl(res.Text)
	// **不再按 64KB 截断**：全文落盘，注入时只拼第一段，剩下的由模型调用
	// read_doc 分页取（见 ai_doc.go）。早先在这里砍掉，就等于把"300 页 PDF 的第
	// 200 页"永久丢弃 —— 而 note 还写着"请分段拖入"，用户根本没法把一份 PDF 分段。
	// 这里只保留一个"荒谬大小"的兜底：一份被误标成文本的巨型文件不该写满磁盘。
	if len(res.Text) > aiDocMaxTextBytes {
		res.Text = aiExtractCut(res.Text, aiDocMaxTextBytes)
		res.Trunc = true
		res.Note = strings.TrimSpace(res.Note + fmt.Sprintf(
			" 正文超过 %d MB，只保留了前 %d 字符。", aiDocMaxTextBytes>>20, len([]rune(res.Text))))
	}
	res.Chars = len([]rune(res.Text))
	id, err := aiDocStore(res.Text)
	if err != nil {
		warn(scSys, "AI", "文档正文暂存失败（%s）：%v", name, err)
		http.Error(w, "文档已读出，但暂存失败："+err.Error(), http.StatusInternalServerError)
		return
	}
	info(scSys, "AI", "已抽取文档 %s（%s/%s，%d 字符%s）",
		name, res.Kind, res.Enc, res.Chars, map[bool]string{true: "，已截断"}[res.Trunc])

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok": true, "id": id, "kind": res.Kind, "enc": res.Enc,
		"chars": res.Chars, "pages": res.Pages, "truncated": res.Trunc, "note": res.Note,
	})
}

// aiExtractReadPath 读「+号 文件选择器」点中的那个文件。
//
// 和拖拽的唯一差别是**字节从哪来**：拖拽由浏览器读成 base64 送上来，
// 选择器手里只有一个路径、拿不到 File 对象，内容只能由服务端自己读。
// 读出来之后完全同一条流水线（判魔数 → 抽文本 → 落盘 → 只回引用）。
//
// 为什么仍然要过沙箱（aiSafePath）：
//   - `read_file` 是**模型自己决定**读哪条路径，所以必须圈在项目目录内；
//   - 这里是**用户在 UI 上点选**，看上去有授权，但这个端点一旦不校验，
//     就等于多了一个"后端可读全盘任意文件"的接口 —— 边界一破，以后没人说得清
//     "到底能不能读 .ssh"。所以两条路的边界必须一致。
//
// 区别在**失败时的说法**：拖拽没有路径限制，所以直接告诉用户"拖进来就行"。
func aiExtractReadPath(p string) ([]byte, string, error) {
	abs, ok := aiSafePath(p)
	if !ok {
		if len(projectRoots()) == 0 {
			return nil, "", fmt.Errorf("还没有配置项目目录，按路径读文件用不了。直接把这个文件拖进对话区就行。")
		}
		return nil, "", fmt.Errorf("这个文件不在项目目录内，我不能按路径读。" +
			"想让我看它，直接把它拖进对话区 —— 拖拽不受路径限制。")
	}
	if exploreSecretPath(abs) {
		return nil, "", fmt.Errorf("这是凭据/密钥类文件（%s），按安全策略不读取。", filepath.Base(abs))
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, "", fmt.Errorf("文件不存在或不可读：%s", abs)
	}
	if fi.IsDir() {
		return nil, "", fmt.Errorf("这是一个目录，不是文件：%s", abs)
	}
	if fi.Size() > aiExtractMaxBytes {
		return nil, "", fmt.Errorf("文件过大（%.1f MB，上限 %d MB），先拆分或压缩再选。",
			float64(fi.Size())/(1<<20), aiExtractMaxBytes>>20)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, "", fmt.Errorf("读取失败：%v", err)
	}
	if len(raw) == 0 {
		return nil, "", fmt.Errorf("文件是空的：%s", filepath.Base(abs))
	}
	return raw, filepath.Base(abs), nil
}

// ---------- 类型判定 ----------

// OLE2 复合文档的魔数（.doc / .xls / .ppt / .msg 都是它）
var aiOLE2Magic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// aiExtractSniff 只看文件头，返回 pdf / zip / ole2 / bin / text。
//
// 判定顺序有讲究：PDF 与 OLE2 的魔数足够长，先判；zip 最后判文本 ——
// 反过来会把"以 PK 开头的普通文本"误判成压缩包（虽然罕见，但 log 里真有）。
func aiExtractSniff(raw []byte) string {
	switch {
	case bytes.HasPrefix(raw, []byte("%PDF-")):
		return "pdf"
	case bytes.HasPrefix(raw, []byte("PK\x03\x04")), bytes.HasPrefix(raw, []byte("PK\x05\x06")):
		return "zip"
	case bytes.HasPrefix(raw, aiOLE2Magic):
		return "ole2"
	}
	// 前 8KB 里出现 NUL 字节 —— 文本文件不会有（UTF-16 除外，那个先按 BOM 走）
	head := raw
	if len(head) > 8192 {
		head = head[:8192]
	}
	if bytes.IndexByte(head, 0) >= 0 && !aiHasUTF16BOM(raw) {
		return "bin"
	}
	return "text"
}

func aiHasUTF16BOM(raw []byte) bool {
	return bytes.HasPrefix(raw, []byte{0xFF, 0xFE}) || bytes.HasPrefix(raw, []byte{0xFE, 0xFF})
}

// aiExtractBytes 是抽取的唯一入口：判类型 → 分派。
func aiExtractBytes(raw []byte) (aiExtractResult, error) {
	switch aiExtractSniff(raw) {
	case "pdf":
		return aiExtractPDF(raw)
	case "zip":
		return aiExtractZipDoc(raw)
	case "ole2":
		return aiExtractResult{}, fmt.Errorf(
			"这是旧版 Office 格式（.doc / .xls / .ppt，OLE2 复合文档），我不支持直接读。" +
				"请在 Office / WPS 里「另存为」.docx / .xlsx / .pptx 再拖进来。")
	case "bin":
		return aiExtractResult{}, fmt.Errorf(
			"这是一个二进制文件（不是文档或文本），读不出文字内容。" +
				"如果里面是表格数据，请先导出成 .xlsx 或 .csv。")
	}
	text, enc := aiTextDecode(raw)
	if strings.TrimSpace(text) == "" {
		return aiExtractResult{}, fmt.Errorf("文件是空的，没有可读内容。")
	}
	return aiExtractResult{Kind: "text", Enc: enc, Text: text}, nil
}

// ---------- 编码 ----------

// aiTextDecode 把字节解成 UTF-8 字符串，返回 (文本, 实际编码)。
//
// 判据很朴素：**合法 UTF-8 就是 UTF-8**。GBK/GB18030 编码的中文恰好构成
// 合法 UTF-8 的概率极低（首字节与后续字节的位模式对不上），所以不需要
// 打分式探测 —— 那种"猜编码"的启发式在短文本上经常猜错。
func aiTextDecode(raw []byte) (string, string) {
	switch {
	case bytes.HasPrefix(raw, []byte{0xFF, 0xFE}):
		return aiDecodeUTF16(raw[2:], binary.LittleEndian), "utf-16le"
	case bytes.HasPrefix(raw, []byte{0xFE, 0xFF}):
		return aiDecodeUTF16(raw[2:], binary.BigEndian), "utf-16be"
	case bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}):
		return string(raw[3:]), "utf-8"
	}
	if utf8.Valid(raw) {
		return string(raw), "utf-8"
	}
	if out, err := simplifiedchinese.GB18030.NewDecoder().Bytes(raw); err == nil {
		return string(out), "gb18030"
	}
	// 两种都不成立：保留能认的部分，把坏字节换成 U+FFFD 而不是丢掉整份文件
	return strings.ToValidUTF8(string(raw), "\uFFFD"), "unknown"
}

func aiDecodeUTF16(b []byte, order binary.ByteOrder) string {
	n := len(b) / 2
	u := make([]uint16, n)
	for i := 0; i < n; i++ {
		u[i] = order.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u))
}

// aiExtractCut 按字节上限截断，且**退到行边界**。
// 切在行中间会让模型看到半句话，而半句话经常被理解成"这里就是结尾"。
func aiExtractCut(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := s[:limit]
	// 先退到合法 UTF-8 边界（多字节字符被切断会变成 U+FFFD）
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	// 退到最后一个换行**之后**（i+1：把换行本身留下）。
	// 少这个 +1 就会把行尾那个 \n 一起切掉，于是"最后一行"和下一段粘在一起。
	// 只在离末尾足够近时才退，否则一行超长的 JSON 会把整份内容砍成几行。
	if i := strings.LastIndexByte(cut, '\n'); i > limit/2 {
		cut = cut[:i+1]
	}
	return cut
}

// ---------- zip 家族（docx / xlsx / pptx） ----------

// aiExtractZipDoc 打开 zip 容器，按内部结构分辨三种 OOXML 文档。
//
// 不看扩展名：一个 .docx 改名成 .zip 也照样能读，反过来一个 .txt
// 里装着 docx 字节也能读 —— 判据是「里面有什么文件」，不是「外面叫什么」。
func aiExtractZipDoc(raw []byte) (aiExtractResult, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return aiExtractResult{}, fmt.Errorf("这是压缩包但无法解开（可能已损坏或被加密）：%v", err)
	}
	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[f.Name] = f
	}
	switch {
	case files["word/document.xml"] != nil:
		text, err := aiDOCXText(files["word/document.xml"])
		if err != nil {
			return aiExtractResult{}, err
		}
		return aiFinishOOXML("docx", text)
	case files["xl/workbook.xml"] != nil:
		text, err := aiXLSXText(zr)
		if err != nil {
			return aiExtractResult{}, err
		}
		return aiFinishOOXML("xlsx", text)
	case files["ppt/presentation.xml"] != nil:
		text, err := aiPPTXText(zr)
		if err != nil {
			return aiExtractResult{}, err
		}
		return aiFinishOOXML("pptx", text)
	}
	return aiExtractResult{}, fmt.Errorf(
		"这是一个普通压缩包，不是 Word / Excel / PPT 文档。请先解压，再把里面的具体文件拖进来。")
}

// aiFinishOOXML 收尾：内容是纯文本（XML 里带 UTF-8 声明），统一标 utf-8。
func aiFinishOOXML(kind, text string) (aiExtractResult, error) {
	text = aiSqueezeBlank(text)
	if strings.TrimSpace(text) == "" {
		return aiExtractResult{}, fmt.Errorf(
			"这个 %s 里没有可提取的文字（可能整篇都是图片）。可以截个图拖给我，我能直接看。", kind)
	}
	return aiExtractResult{Kind: kind, Enc: "utf-8", Text: text}, nil
}

// aiDOCXText 抽 word/document.xml 里的文字。
//
// 只认三种元素，其余（样式、批注、域代码）一概忽略：
//   - <w:t>   文本片段（可能是行内分片的，必须按顺序拼）
//   - <w:tab> 制表符
//   - <w:br>  手动换行
//
// 表格的处理：<w:tc> 之间用 tab、<w:tr> 结束换行 —— 输出成 TSV。
// 不这么做的话，表格会变成一个单元格一行，行列关系全丢，模型读到的是
// 一堆并列的短句，根本看不出哪几个数是一行的。
func aiDOCXText(f *zip.File) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", fmt.Errorf("无法读取文档正文：%v", err)
	}
	defer rc.Close()

	dec := xml.NewDecoder(rc)
	dec.Strict = false // 真实 docx 里常有未声明实体，严格模式会直接失败

	var out, para strings.Builder
	inText := false
	tblDepth := 0
	flushPara := func() {
		s := strings.TrimRight(para.String(), " \t")
		if strings.TrimSpace(s) != "" {
			out.WriteString(s)
			out.WriteString("\n")
		}
		para.Reset()
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			// 半截 XML：把已经抽到的交出去，比整份丢掉有用
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				para.WriteString("\t")
			case "br", "cr":
				para.WriteString("\n")
			case "tbl":
				tblDepth++
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				if tblDepth > 0 {
					// 表格内的段落 = 一个单元格，用 tab 分隔，不换行
					para.WriteString("\t")
				} else {
					flushPara()
				}
			case "tc":
				// 空单元格（只有 <w:p/>）：补一个 tab，保持列对齐
				if tblDepth > 0 && !strings.HasSuffix(para.String(), "\t") {
					para.WriteString("\t")
				}
			case "tr":
				flushPara()
			case "tbl":
				if tblDepth > 0 {
					tblDepth--
				}
				flushPara()
			}
		case xml.CharData:
			if inText {
				para.WriteString(string(t))
			}
		}
	}
	flushPara()
	return out.String(), nil
}

// aiXLSXText 抽所有工作表。共享字符串表在 xl/sharedStrings.xml，
// 单元格里 t="s" 的 <v> 是**索引**，不是值 —— 直接当数字读会得到一串下标。
//
// 表名和顺序取自 xl/workbook.xml + rels，而不是 sheetN.xml 这类文件名：
//   - 用户起的名字（"加工运输"/"质量报告"）才是模型能对上号的语义，
//     输出 "### sheet1" 等于把这一层信息白扔了；
//   - 删表/挪表后 sheet 文件编号不会重排，按文件名排序会把顺序读反。
//
// 两处都读不到时回退到"按文件名尾号排序"，残缺文件仍要能读。
func aiXLSXText(zr *zip.Reader) (string, error) {
	var shared []string
	for _, f := range zr.File {
		if f.Name == "xl/sharedStrings.xml" {
			s, err := aiXLSXSharedStrings(f)
			if err != nil {
				return "", err
			}
			shared = s
			break
		}
	}
	sheets := aiXLSXSheets(zr)

	var out strings.Builder
	for _, sh := range sheets {
		body, err := aiXLSXSheet(sh.file, shared)
		if err != nil {
			continue // 单个工作表坏了不该毁掉整本簿
		}
		if strings.TrimSpace(body) == "" {
			continue
		}
		if len(sheets) > 1 {
			out.WriteString("### " + sh.title + "\n")
		}
		out.WriteString(body)
		out.WriteString("\n")
	}
	return out.String(), nil
}

type aiXLSXSheetRef struct {
	file  *zip.File
	title string
}

func aiXLSXSheets(zr *zip.Reader) []aiXLSXSheetRef {
	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[f.Name] = f
	}
	paths, titles := aiOOXMLPartOrder(zr, "xl/workbook.xml", "xl/_rels/workbook.xml.rels", "xl/", "sheet")
	if len(paths) == 0 {
		// 回退：sheet10 不能按字典序排到 sheet2 前面
		var fallback []*zip.File
		for _, f := range zr.File {
			if strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml") {
				fallback = append(fallback, f)
			}
		}
		sort.Slice(fallback, func(i, j int) bool {
			return aiTrailingNum(fallback[i].Name) < aiTrailingNum(fallback[j].Name)
		})
		paths = paths[:0]
		for _, f := range fallback {
			paths = append(paths, f.Name)
		}
		titles = make([]string, len(paths))
	}
	var out []aiXLSXSheetRef
	for i, p := range paths {
		f := files[p]
		if f == nil {
			continue
		}
		title := ""
		if i < len(titles) {
			title = strings.TrimSpace(titles[i])
		}
		if title == "" {
			title = strings.TrimSuffix(filepath.Base(p), ".xml")
		}
		out = append(out, aiXLSXSheetRef{file: f, title: aiOneLine(title)})
	}
	return out
}

func aiXLSXSharedStrings(f *zip.File) ([]string, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("无法读取共享字符串表：%v", err)
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	dec.Strict = false
	var out []string
	var cur strings.Builder
	inT := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				inT = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "si":
				out = append(out, cur.String())
				cur.Reset()
			}
		case xml.CharData:
			if inT {
				cur.WriteString(string(t))
			}
		}
	}
	return out, nil
}

// aiXLSXSheet 把一张工作表输出成 TSV。公式只取缓存的 <v>（不重算）——
// 重算要一个表格引擎，而用户拖进来通常就是要看"里面写了什么"。
func aiXLSXSheet(f *zip.File, shared []string) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	dec.Strict = false

	var out, row strings.Builder
	var cell strings.Builder
	cellType := ""
	inV, inT := false, false
	// nextCol 是"下一个单元格该落在第几列"（1 基）。xlsx 里**空单元格不写进 XML**，
	// 只靠 <c r="D3"> 里的列引用标明位置。按顺序 append 的话，一行 A/B/D 有值
	// 会被读成"A 列是 A、B 列是 B、C 列是 D 的内容" —— 表头写着 5 列，
	// 数据却整体左移一列，模型据此下的结论全是错的。
	nextCol, colRef := 1, 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "c":
				cellType = ""
				colRef = 0
				cell.Reset()
				inV, inT = false, false
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "t":
						cellType = a.Value
					case "r":
						colRef = aiXLSXColNum(a.Value)
					}
				}
			case "v":
				inV = true
			case "t":
				inT = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "t":
				inT = false
			case "c":
				val := cell.String()
				if cellType == "s" {
					// t="s" 的 <v> 是共享字符串**索引**。索引越界说明这个单元格
					// 引用了不存在的字符串（删除行后常见的残留），此时必须输出空，
					// 不能把索引数字本身当内容 —— 那等于给表格里凭空加一个 "9"。
					val = ""
					if i, err := parseIntLoose(strings.TrimSpace(cell.String())); err == nil &&
						i >= 0 && i < len(shared) {
						val = shared[i]
					}
				}
				// 只往后补空列：遇到乱序/畸形引用时不倒退，免得把已经写出的值挤掉
				for colRef > nextCol && nextCol < aiXLSXMaxCols {
					row.WriteString("\t")
					nextCol++
				}
				row.WriteString(aiOneLine(val))
				row.WriteString("\t")
				nextCol++
			case "row":
				s := strings.TrimRight(row.String(), "\t")
				if strings.TrimSpace(s) != "" {
					out.WriteString(s)
					out.WriteString("\n")
				}
				row.Reset()
				nextCol = 1
			}
		case xml.CharData:
			if inV || inT {
				cell.WriteString(string(t))
			}
		}
	}
	return out.String(), nil
}

// aiPPTXText 抽每页的文字。按**放映顺序**排列，页与页之间留分隔，
// 让模型能把"某页的标题"和"某页的要点"对上号。
//
// 放映顺序只在 presentation.xml 的 <p:sldIdLst> 里，slideN.xml 的 N 是
// "创建时的编号"：删页/拖动重排后两者会分叉，按文件名排就会把整份 PPT 读反。
func aiPPTXText(zr *zip.Reader) (string, error) {
	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[f.Name] = f
	}
	paths, _ := aiOOXMLPartOrder(zr, "ppt/presentation.xml", "ppt/_rels/presentation.xml.rels", "ppt/", "sldId")
	if len(paths) == 0 {
		var fallback []*zip.File
		for _, f := range zr.File {
			if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
				fallback = append(fallback, f)
			}
		}
		sort.Slice(fallback, func(i, j int) bool {
			return aiTrailingNum(fallback[i].Name) < aiTrailingNum(fallback[j].Name)
		})
		paths = paths[:0]
		for _, f := range fallback {
			paths = append(paths, f.Name)
		}
	}
	var slides []*zip.File
	for _, p := range paths {
		if f := files[p]; f != nil {
			slides = append(slides, f)
		}
	}

	var out strings.Builder
	for i, sl := range slides {
		rc, err := sl.Open()
		if err != nil {
			continue
		}
		dec := xml.NewDecoder(rc)
		dec.Strict = false
		var page, para strings.Builder
		inT := false
		for {
			tok, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "t" {
					inT = true
				}
			case xml.EndElement:
				switch t.Name.Local {
				case "t":
					inT = false
				case "p":
					s := strings.TrimSpace(para.String())
					if s != "" {
						page.WriteString(s)
						page.WriteString("\n")
					}
					para.Reset()
				}
			case xml.CharData:
				if inT {
					para.WriteString(string(t))
				}
			}
		}
		rc.Close()
		body := strings.TrimSpace(page.String())
		if body == "" {
			continue
		}
		out.WriteString(fmt.Sprintf("### 第 %d 页\n%s\n\n", i+1, body))
	}
	return out.String(), nil
}

// ---------- PDF ----------

// aiExtractPDF 逐页抽文字层。
//
// 抽不出内容时**必须说清是哪种情况**，不能返回空字符串让用户猜：
//   - 扫描件（整页是图片）根本没有文字层，任何文本抽取都拿不到；
//   - 加密 PDF 要先解密。
//
// 这两句提示的价值高于"支持 PDF"本身 —— 用户知道下一步该干什么
// （截图拖进来 / 先解密），而不是觉得工具坏了。
func aiExtractPDF(raw []byte) (aiExtractResult, error) {
	r, err := pdf.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "encrypt") {
			return aiExtractResult{}, fmt.Errorf(
				"这个 PDF 已加密，需要密码才能读。请先用阅读器另存一份不加密的副本。")
		}
		return aiExtractResult{}, fmt.Errorf("PDF 无法解析（文件可能已损坏）：%v", err)
	}
	total := r.NumPage()
	if total == 0 {
		return aiExtractResult{}, fmt.Errorf("这个 PDF 里没有页面。")
	}
	pages := total
	if pages > aiExtractMaxPages {
		pages = aiExtractMaxPages
	}
	var out strings.Builder
	empty := 0
	for i := 1; i <= pages; i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			empty++
			continue
		}
		s, perr := p.GetPlainText(nil)
		if perr != nil || strings.TrimSpace(s) == "" {
			empty++
			continue
		}
		// Word/WPS 导出的 PDF 会把一行字切成一堆 Tj 片段，库给每个片段加 \n，
		// 不重排的话"学号：2021304900"会变成四行单字碎片（见 aiPDFReflow）。
		s = aiPDFReflow(aiPDFNormalizeSpace(s))
		out.WriteString(fmt.Sprintf("### 第 %d 页\n", i))
		out.WriteString(strings.TrimRight(s, "\n"))
		out.WriteString("\n\n")
	}
	text := aiSqueezeBlank(out.String())
	var notes []string
	if total > pages {
		notes = append(notes, fmt.Sprintf("全文 %d 页，只读了前 %d 页。", total, pages))
	}
	if strings.TrimSpace(text) == "" {
		return aiExtractResult{}, fmt.Errorf(
			"这个 PDF 读不出文字 —— %d 页全都没有文字层，几乎可以确定是**扫描件或整页图片**。"+
				"文字抽取对这种文件无效；请改把页面截图拖给我，我能直接看图片内容。", total)
	}
	if empty > 0 {
		notes = append(notes, fmt.Sprintf("其中 %d 页没有文字层（多为图片/表格截图），已跳过。", empty))
	}
	return aiExtractResult{
		Kind: "pdf", Enc: "utf-8", Text: text, Pages: total,
		Note: strings.Join(notes, ""),
	}, nil
}

// ---------- 小工具 ----------

// aiXLSXMaxCols 是补空列的硬上限：畸形文件里一个 r="ZZZ999" 不该让输出
// 凭空长出上万个 tab。真实表格远到不了这里。
const aiXLSXMaxCols = 1024

// aiXLSXColNum 把 "C3" / "AA12" 转成 1 基列号；没有列字母就返回 0（= 未知，
// 退化为"按出现顺序 append"，与没有 r 属性的老文件兼容）。
func aiXLSXColNum(ref string) int {
	n := 0
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		switch {
		case c >= 'A' && c <= 'Z':
			n = n*26 + int(c-'A'+1)
		case c >= 'a' && c <= 'z':
			n = n*26 + int(c-'a'+1)
		default:
			if n == 0 || n > aiXLSXMaxCols {
				return 0
			}
			return n
		}
		if n > aiXLSXMaxCols {
			return 0
		}
	}
	return n
}

// aiOOXMLPartOrder 按 OOXML 主 part 里的 r:id 顺序给出实际文件路径。
//
// workbook.xml 的 <sheet> / presentation.xml 的 <p:sldId> 才是"用户看到的顺序"，
// r:id 通过同目录的 .rels 映射到具体 xml。文件名里的数字只是创建时的编号，
// 删表、挪页之后不会重排 —— 按文件名排序就会读反。
// 返回 (paths, titles)；titles 只有 xlsx 的 <sheet name> 有，pptx 为空串。
// 任何一步缺失都返回空切片，由调用方回退到文件名排序。
func aiOOXMLPartOrder(zr *zip.Reader, mainPart, relsPart, base, tag string) ([]string, []string) {
	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[f.Name] = f
	}
	var rids, titles []string
	aiOOXMLScan(files[mainPart], func(st xml.StartElement) {
		if st.Name.Local != tag {
			return
		}
		rid, title := "", ""
		for _, a := range st.Attr {
			switch a.Name.Local {
			case "id": // r:id；sheetId 的 Local 是 "sheetId"，不会撞上
				rid = a.Value
			case "name":
				title = a.Value
			}
		}
		rids = append(rids, rid)
		titles = append(titles, title)
	})
	targets := make(map[string]string)
	aiOOXMLScan(files[relsPart], func(st xml.StartElement) {
		if st.Name.Local != "Relationship" {
			return
		}
		id, target := "", ""
		for _, a := range st.Attr {
			switch a.Name.Local {
			case "Id":
				id = a.Value
			case "Target":
				target = a.Value
			}
		}
		if id != "" && target != "" {
			targets[id] = aiOOXMLResolvePart(base, target)
		}
	})
	var paths, names []string
	for i, rid := range rids {
		p, ok := targets[rid]
		if !ok || p == "" || files[p] == nil {
			continue
		}
		paths = append(paths, p)
		names = append(names, titles[i])
	}
	return paths, names
}

// aiOOXMLResolvePart 把 rels 的 Target 变成 zip 内的完整路径。
// 常见两种写法："worksheets/sheet1.xml"（相对 base）与 "/xl/worksheets/sheet1.xml"。
func aiOOXMLResolvePart(base, target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(target, "/")
	}
	p := path.Clean(base + target)
	if strings.HasPrefix(p, "..") {
		return ""
	}
	return p
}

// aiOOXMLScan 只遍历 StartElement。rels / workbook 都极小，用不上流式状态机；
// Strict=false 是因为真实文件里常有未声明的命名空间前缀。
func aiOOXMLScan(f *zip.File, fn func(xml.StartElement)) {
	if f == nil {
		return
	}
	rc, err := f.Open()
	if err != nil {
		return
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		if st, ok := tok.(xml.StartElement); ok {
			fn(st)
		}
	}
}

// aiPDFNormalizeSpace 把 PDF 里的排版空格归一。
// 期刊 PDF 常用 U+00A0（不换行空格）做对齐，一段里连着四五个；
// 中文里还有 U+3000（全角空格）。它们不是内容，留着只会让模型把
// "摘　要:" 当成跟 "摘要:" 不同的词。
func aiPDFNormalizeSpace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		switch r {
		case '\u00a0', '\u3000':
			r = ' '
		case '\u200b', '\ufeff':
			continue
		}
		if r == ' ' {
			if space {
				continue
			}
			space = true
		} else {
			space = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// aiPDFReflow 把"每个字一行"的 PDF 文本重排成能读的段落。
//
// Word/WPS 导出的 PDF 常把一行文字切成许多 Tj 片段（中文按字、数字按位），
// ledongthuc/pdf 的 GetPlainText 给**每个片段**后面加 \n，于是
// "学号：2021304900" 会变成 "学号：\n2\n021304900"。这类输出里，真正的
// 段落边界是一个空白行（库输出 " \n"），片段与片段之间没有。
//
// 但排版系统生成的 PDF（期刊 / LaTeX）每行就是一个完整 Tj，行与行之间只有
// 一个 \n —— 对它们做合并会把整页粘成一行。所以先判"碎片化"：非空行里
// ≤3 字符的短行至少 5 行且占 1/4 以上，才启用重排；其余原样返回。
//
// 拼接片段时不额外插空格：库在需要分词的地方已经留了尾空格
// （"ETH Beijing "），而中文片段之间加空格反而会把 "2023年4月" 拆散。
func aiPDFReflow(s string) string {
	lines := strings.Split(s, "\n")
	nonEmpty, short := 0, 0
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		nonEmpty++
		if len([]rune(trimmed)) <= 3 {
			short++
		}
	}
	if nonEmpty == 0 || short < 5 || short*4 < nonEmpty {
		return s
	}
	var out, buf strings.Builder
	flush := func() {
		v := strings.TrimSpace(buf.String())
		if v != "" {
			out.WriteString(v)
			out.WriteString("\n")
		}
		buf.Reset()
	}
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			flush()
			continue
		}
		buf.WriteString(l)
	}
	flush()
	return out.String()
}

// aiStripCtrl 去掉 C0 控制符，只保留换行与制表。
//
// 两个来源：PDF 文本流里的 0x01（表/段落标记）、终端日志里的 ANSI 色码。
// 它们对阅读理解没有价值，但会实打实地占 token，还会让模型把 `\x1b[0m`
// 当成正文的一部分去解释。
func aiStripCtrl(s string) string {
	has := strings.ContainsFunc(s, func(r rune) bool {
		return r < 0x20 && r != '\n' && r != '\t'
	})
	if !has {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// aiSqueezeBlank 把连续空行压成一个。PDF 与 OOXML 抽出来常有大量空行，
// 它们不携带信息，只占 token（每轮整体重发时尤其明显）。
func aiSqueezeBlank(s string) string {
	var out strings.Builder
	blank := 0
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(line) == "" {
			blank++
			if blank > 1 {
				continue
			}
			out.WriteString("\n")
			continue
		}
		blank = 0
		out.WriteString(line)
		out.WriteString("\n")
	}
	return strings.TrimSpace(out.String()) + "\n"
}

// aiTrailingNum 取文件名里最后一个数字用来排序（slide1.xml → 1）。
//
// **必须先去掉扩展名**：不去的话循环看的是末尾那个 'l'，一个数字都取不到，
// 于是 slide1/slide2 全返回 0，排序退化成"zip 里的原始顺序"——
// 而那顺序来自 map 遍历，每次跑都不一样。表现是"这次 PPT 页序对、下次乱"。
// 没有数字就返回 0，让它们排在前面（比 panic 好）。
func aiTrailingNum(name string) int {
	base := filepath.Base(name)
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	i := len(base)
	for i > 0 && base[i-1] >= '0' && base[i-1] <= '9' {
		i--
	}
	n, err := parseIntLoose(base[i:])
	if err != nil {
		return 0
	}
	return n
}

func parseIntLoose(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int(c-'0')
		if n > 1<<30 {
			return 0, fmt.Errorf("too large")
		}
	}
	return n, nil
}

// aiOneLine 单元格值里不能有 tab/换行，否则整行的列数就错了
// （模型会以为后面几列是新的行）。
func aiOneLine(s string) string {
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}
