package main

// ai_attach.go —— AI 图片附件：拖进对话区 / 粘进输入框 / 拖到桌宠的图片，
// 落盘到 <exeDir>/aiattach/，对话消息里只留一个几十字节的引用。
//
// ## 为什么不把图片直接内联进 messages
//
// 三条都是硬约束，不是"顺手优化"：
//
//  1. `/api/ai/chat` 的请求体上限是 16MB（见 handleAIChat 开头）。一张手机照片
//     8MB，base64 之后 11MB，两张就撞墙 —— 而撞墙发生在用户已经写完问题之后，
//     他只会看到"请求体过大"，图片和问题一起白写。
//  2. 对话历史每一轮都会整体重发。图片内联在 messages 里，等于**每一轮都把同一张图
//     重新上传一遍**，上行流量与请求体大小随时长线性膨胀，而 DeepSeek 单图最多
//     计 1024 token，重复发送纯属付费买同一个前缀。
//  3. 前端要把历史存进 localStorage（`ek-ai-hist`，5MB 配额）。存 base64 图必然写爆，
//     而 QuotaExceededError 被 aiHistSave 的 try/catch 吞掉 —— 表现是"历史莫名丢了"，
//     比报错难查得多。
//
// 落盘之后：id 32 字符、刷新不丢、历史可回看，发给上游前才读盘转 data URL。
//
// ## 与"路径引用"（ai-pick / read_file）的分工
//
// 那条路是给**文本文件**用的：只传路径，读取由 AI 的 read_file 在沙箱内完成。
// 图片走这条路行不通 —— read_file 返回的是文本，模型看不到像素。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// 单张上限：前端已把长边压到 1600、JPEG q0.9 上下，正常 200~600KB。
	// 12MB 是硬闸，防的是"绕过前端直接打接口"和 GIF 原图。
	aiAttachMaxBytes = 12 << 20
	// 目录保留张数：兜底，防"一天之内狂传"。正常一天传不到 400 张。
	aiAttachMaxCount = 400
	// 保留期 24 小时。图片只是"这一轮对话的输入"，用户几乎不会在第二天
	// 还回头问"刚才那张截图"，留着纯属占盘（一张截图 300KB，一个月几百 MB）。
	aiAttachTTL = 24 * time.Hour
	// 扫描间隔刻意远小于 TTL：TTL 是"文件年龄"判据，扫描频率只决定**滞后多久才删**。
	// 若按 24h 间隔扫，一张图最长能活 47.9 小时（写入 1 秒后过期，但要等下次扫）。
	// 空目录扫一次就是一次 ReadDir，成本可以忽略，所以每小时扫、超期立刻清。
	aiAttachSweepEvery = time.Hour
	// 历史里最多几条消息仍真的携带图片。更早的降级成文字占位：
	// 每轮重发一张图 = 每轮 1024 token，聊到第 10 轮还在为第 1 轮那张图付费。
	aiAttachKeepMsgs = 3
)

// aiImage 是 messages 里对一张图片的引用。
//
// DataURL 用 json:"-"：它只在服务端拼上游请求前填充，绝不回传前端 ——
// 一旦回传，历史里每条都带一份 base64，落盘的全部意义就没了。
type aiImage struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	MIME string `json:"mime,omitempty"`

	DataURL string `json:"-"`
}

// aiAttachDirFn 是间接层：测试要往临时目录写，不能碰用户 exe 旁边那份。
var aiAttachDirFn = func() string { return filepath.Join(exeDir(), "aiattach") }

func aiAttachDir() string {
	d := aiAttachDirFn()
	_ = os.MkdirAll(d, 0o755)
	return d
}

// aiImageMIME 按**文件内容**判定类型：既不信前端声明的 MIME，也不看扩展名。
//
// 两个理由：
//   - 安全：不判内容的话，这个端点就是个"任意文件落盘 × base64 转发给上游"的通道；
//   - 一致：DeepSeek 官方文档明确写"格式按文件内容识别，不看扩展名或声明的 MIME"，
//     我们在这里先用同一套判据挡住不属于这四种的东西，用户就不用等上游报一句看不懂的 400。
func aiImageMIME(raw []byte) string {
	switch http.DetectContentType(raw) {
	case "image/jpeg":
		return "image/jpeg"
	case "image/png":
		return "image/png"
	case "image/gif":
		return "image/gif"
	case "image/webp":
		return "image/webp"
	}
	return ""
}

// aiAttachIDOK 校验附件 ID 形状。它同时是防目录穿越的那道闸（ID 直接当文件名用），
// 所以必须是白名单：32 位小写 hex（newToken 的输出）。
func aiAttachIDOK(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func aiAttachPath(id string) string { return filepath.Join(aiAttachDir(), id) }

// handleAIAttach 两个动作：
//
//	POST /api/ai/attach  {name, mime, content(base64 可带 data: 前缀)} → {id,name,mime,bytes}
//	GET  /api/ai/attach?id=<32hex>                                    → 图片原始字节
//
// GET 是给历史缩略图用的（<img src="…?id=…&t=token">）：浏览器加载 img 不能带自定义头，
// 所以走 main.go 已有的查询参数令牌。
func handleAIAttach(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		aiAttachServe(w, r)
	case http.MethodPost:
		aiAttachCreate(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func aiAttachCreate(w http.ResponseWriter, r *http.Request) {
	// base64 后体积约涨 1/3，再留 JSON 包装余量
	r.Body = http.MaxBytesReader(w, r.Body, aiAttachMaxBytes*2)
	var body struct {
		Name    string `json:"name"`
		MIME    string `json:"mime"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "请求体过大或格式错误（图片上限 12MB）", http.StatusBadRequest)
		return
	}
	b64 := body.Content
	if strings.HasPrefix(b64, "data:") {
		if i := strings.Index(b64, ","); i >= 0 {
			b64 = b64[i+1:]
		}
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) == 0 {
		http.Error(w, "图片内容解码失败或为空", http.StatusBadRequest)
		return
	}
	if len(raw) > aiAttachMaxBytes {
		http.Error(w, fmt.Sprintf("图片过大（%d 字节，上限 %d）", len(raw), aiAttachMaxBytes), http.StatusRequestEntityTooLarge)
		return
	}
	mime := aiImageMIME(raw)
	if mime == "" {
		http.Error(w, "只接受 PNG / JPEG / GIF / WebP 图片（按文件内容判定）", http.StatusBadRequest)
		return
	}
	// 文件名只作展示用：剥掉路径分隔符，避免它被当成"路径"读（前端也会显示它）。
	name := filepath.Base(strings.TrimSpace(body.Name))
	if name == "" || name == "." {
		name = "图片"
	}
	id := newToken()
	if err := os.WriteFile(aiAttachPath(id), raw, 0o644); err != nil {
		warn(scSys, "AI", "图片附件写入失败：%v", err)
		http.Error(w, "写入附件失败："+err.Error(), http.StatusInternalServerError)
		return
	}
	aiAttachGC()
	info(scSys, "AI", "已接收图片附件 %s（%s，%d 字节）", name, mime, len(raw))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok": true, "id": id, "name": name, "mime": mime, "bytes": len(raw),
	})
}

func aiAttachServe(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if !aiAttachIDOK(id) {
		http.Error(w, "附件 ID 不合法", http.StatusBadRequest)
		return
	}
	raw, err := os.ReadFile(aiAttachPath(id))
	if err != nil {
		// 说清"为什么没了"：附件目录 24 小时自动清理，用户看到"可能已被清理"
		// 会以为是毛病，看到"只保留 24 小时"就知道该重新拖一次。
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "图片已被自动清理（附件只保留 24 小时）", http.StatusNotFound)
		return
	}
	mime := aiImageMIME(raw)
	if mime == "" {
		http.Error(w, "附件不是受支持的图片", http.StatusUnsupportedMediaType)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, max-age=86400") // 内容寻址式 ID，不会变
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(raw)
}

// aiAttachGC 按 TTL + 张数上限清理。
//
// 两个调用入口：写入后（立刻回收刚过期的）、以及 aiAttachStartGC 里的小时级定时器。
// 定时器那条才是主力 —— 只在写入后跑的话，用户传完图就关掉 EnvKit，
// 一周后重启才发现目录里躺着一堆早该删的东西。
func aiAttachGC() {
	// 串行化：写入路径和定时器可能同时进来，两遍并发删同一批文件虽然无害
	// （os.Remove 失败只是跳过），但日志会重复打，排查时容易误判成"删了两遍"。
	aiAttachGCMu.Lock()
	defer aiAttachGCMu.Unlock()

	ents, err := os.ReadDir(aiAttachDir())
	if err != nil || len(ents) == 0 {
		return
	}
	type fi struct {
		name string
		mod  time.Time
		size int64
	}
	list := make([]fi, 0, len(ents))
	deadline := time.Now().Add(-aiAttachTTL)
	for _, e := range ents {
		if e.IsDir() || !aiAttachIDOK(e.Name()) {
			continue
		}
		st, err := e.Info()
		if err != nil {
			continue
		}
		list = append(list, fi{name: e.Name(), mod: st.ModTime(), size: st.Size()})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].mod.After(list[j].mod) })
	removed, freed := 0, int64(0)
	for i, f := range list {
		if i < aiAttachMaxCount && f.mod.After(deadline) {
			continue
		}
		// 删除失败不阻断（可能正被另一个读到），下次再清
		if err := os.Remove(aiAttachPath(f.name)); err == nil {
			removed++
			freed += f.size
		}
	}
	if removed > 0 {
		info(scSys, "AI", "图片附件清理：删除 %d 个（%.1f MB），保留最近 %d 张 / %s",
			removed, float64(freed)/(1<<20), aiAttachMaxCount, aiAttachTTL)
	}
}

var (
	aiAttachGCMu    sync.Mutex
	aiAttachGCStart sync.Once
)

// aiAttachStartGC 启动时清一次，之后每小时一次。
//
// 用 sync.Once 而不是在 main 里裸起 goroutine：这个函数将来可能被别的初始化
// 路径再调一次（比如热重载配置），重复起 ticker 会让清理频率变成 N 倍。
//
// goroutine 不设退出通道：它只持有一个 ticker、被调时才读目录，
// 进程退出时随进程一起消失。为一个自用工具再加一套 stop/join，
// 多出来的时序复杂度比它省下的那点资源贵得多。
func aiAttachStartGC() {
	aiAttachGCStart.Do(func() {
		aiAttachGC() // 启动先扫一遍：关掉 EnvKit 的那些天里没人扫
		go func() {
			t := time.NewTicker(aiAttachSweepEvery)
			defer t.Stop()
			for range t.C {
				aiAttachGC()
			}
		}()
	})
}

// aiAttachLoad 把一条引用读盘成 data URL。
func aiAttachLoad(im aiImage) (string, error) {
	if !aiAttachIDOK(im.ID) {
		return "", fmt.Errorf("附件 ID 形状不对")
	}
	raw, err := os.ReadFile(aiAttachPath(im.ID))
	if err != nil {
		return "", err
	}
	if len(raw) > aiAttachMaxBytes {
		return "", fmt.Errorf("附件超出单张上限")
	}
	mime := aiImageMIME(raw)
	if mime == "" {
		return "", fmt.Errorf("附件内容不是受支持的图片")
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw), nil
}

// aiAttachHydrate 把 messages 里的图片引用读盘成 data URL，并做两件事：
//
//  1. **只让最近 aiAttachKeepMsgs 条带图消息真的携带图片**，更早的换成文字占位。
//  2. 读不到文件时**降级成文字占位并记 WARN**，不静默当成"没这张图"——
//     后者会让模型对着空气回答，用户还以为它看过截图。
//
// 占位文字写进正文而不是丢掉引用：模型必须知道"这里原本有张图"，否则它会
// 把"我看不到图"理解成"用户没给图"，然后开始瞎猜。
func aiAttachHydrate(msgs []aiMsg) []aiMsg {
	var idx []int
	for i, m := range msgs {
		if m.Role == "user" && len(m.Images) > 0 {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return msgs
	}
	keepFrom := 0
	if len(idx) > aiAttachKeepMsgs {
		keepFrom = len(idx) - aiAttachKeepMsgs
	}
	kept := map[int]bool{}
	for _, i := range idx[keepFrom:] {
		kept[i] = true
	}
	for _, i := range idx {
		m := msgs[i]
		out := make([]aiImage, 0, len(m.Images))
		var aged, gone []string
		for _, im := range m.Images {
			if !kept[i] {
				aged = append(aged, aiAttachLabel(im))
				continue
			}
			data, err := aiAttachLoad(im)
			if err != nil {
				warn(scSys, "AI", "图片附件读取失败（%s）：%v —— 该图不随本轮上下文发送", aiAttachLabel(im), err)
				gone = append(gone, aiAttachLabel(im))
				continue
			}
			im.DataURL = data
			out = append(out, im)
		}
		// 两种"不发图"要分开说，否则模型会把"太旧被降级"和"文件没了"混为一谈：
		//   aged  = 超出最近几条的限制，主动降级（图还在盘上，只是不值得每轮重发）
		//   gone  = 读不到文件，多半是过了 24 小时保留期
		if len(aged) > 0 {
			m.Content = strings.TrimSpace(m.Content) +
				"\n（此处原本附有图片，因超出近期上下文窗口已不再发送：" + strings.Join(aged, "、") + "。）"
		}
		if len(gone) > 0 {
			m.Content = strings.TrimSpace(m.Content) +
				"\n（此处原本附有图片，但已不可读（附件只保留 24 小时）：" + strings.Join(gone, "、") +
				"。若仍需查看，请让用户重新拖入。）"
		}
		m.Images = out
		msgs[i] = m
	}
	return msgs
}

func aiAttachLabel(im aiImage) string {
	if strings.TrimSpace(im.Name) != "" {
		return im.Name
	}
	if im.ID != "" {
		return "图片 " + im.ID[:6]
	}
	return "图片"
}

// aiIsImageUnsupported 判断上游拒绝是不是"这个模型不吃图片"。
//
// 各家的措辞不一样（DeepSeek 是 "This model does not support image"，
// 也有实现报 "image input is not supported" / "does not support image_url"），
// 所以按"同时出现了 image 和否定词"来判，而不是钉死一整句 ——
// 钉死句子的匹配会在换一个服务商时静默失效，然后用户又只看到英文 400。
func aiIsImageUnsupported(msg string) bool {
	l := strings.ToLower(msg)
	// 中文服务商（以及被本地化的错误文案）不一定出现 "image" 这个词，
	// 所以中英两套关键词都要认，否则英文界面翻好了、中文报错又漏掉。
	if !strings.Contains(l, "image") && !strings.Contains(l, "图片") && !strings.Contains(l, "图像") {
		return false
	}
	for _, k := range []string{"does not support", "not supported", "unsupported", "不支持", "no support"} {
		if strings.Contains(l, k) {
			return true
		}
	}
	return false
}
