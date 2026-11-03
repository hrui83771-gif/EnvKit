package main

// ai_doc.go —— 拖进对话区的**文档**（docx/xlsx/pptx/pdf/文本）：抽出的正文落盘到
// <exeDir>/aidoc/，消息里只留一个几十字节的引用（id/name/kind/chars）。
//
// ## 为什么正文不再进输入框
//
// 拖文件的本意是"把这份材料给你看"，不是"把这份材料贴进聊天"。旧做法把整段
// <untrusted_data> 直接写进 textarea，于是：输入框被几千字撑满、用户没法继续写自己的
// 问题；气泡回看时一屏全是文件正文，对话变日志；正文还随消息进了 localStorage（5MB 配额）。
//
// ## 为什么不干脆放前端内存
//
// 历史要在刷新后继续可用（模型下一轮还得看到那份材料），而正文进 localStorage 会写爆
// 配额 —— QuotaExceededError 被 aiHistSave 的 try/catch 吞掉，表现是"历史莫名丢了"。
// 所以和图片附件走完全同构的路：**存盘 + 引用**，每轮发给上游前才读盘拼进 prompt。
//
// ## 与图片附件的差别
//
// 图片是"像素"，必须走 content 块数组；文档是"文字"，直接拼进 content 就够了 ——
// 不必换消息形状（换一次形状 = 所有请求的字节前缀都变 = 上下文缓存整体失效）。
//
// 保留期与图片一致（24 小时）。过期后 hydrate 会往正文里补一句"原本附了某文件但已不可读"，
// 让模型知道"这里曾经有材料"，而不是当成用户什么都没给。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// 目录保留份数：兜底防"一天之内狂拖"。正文单份已由 explore_max_kb 截断。
	aiDocMaxCount = 200
	// 与图片附件同一个 TTL 口径：文档也只是"这一轮对话的输入"。
	aiDocTTL = 24 * time.Hour
	// 扫描间隔远小于 TTL：TTL 是"文件年龄"判据，扫描频率只决定滞后多久才删。
	aiDocSweepEvery = time.Hour
)

// aiDocRef 是 messages 里对一份文档的引用。
//
// 正文**不在**这里：它只在服务端拼上游请求前从盘上读出来拼进 Content，
// 绝不回传前端 —— 回传就等于又把几千字塞回浏览器（历史一存就写爆配额）。
type aiDocRef struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Kind  string `json:"kind,omitempty"`  // docx / xlsx / pptx / pdf / text
	Chars int    `json:"chars,omitempty"` // 截断后的字符数，仅用于界面显示"读进来多少"
}

// aiDocDirFn 是间接层：测试要往临时目录写，不能碰用户 exe 旁边那份。
var aiDocDirFn = func() string { return filepath.Join(exeDir(), "aidoc") }

func aiDocDir() string {
	d := aiDocDirFn()
	_ = os.MkdirAll(d, 0o755)
	return d
}

// aiDocPath 的文件名就是 32 位 hex 的 id：形状白名单复用 aiAttachIDOK，
// 它是防目录穿越的那道闸（id 直接当路径用）。
func aiDocPath(id string) string { return filepath.Join(aiDocDir(), id) }

// aiDocStore 把抽取后的正文落盘，返回引用 id。
func aiDocStore(text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("正文为空")
	}
	id := newToken()
	if err := os.WriteFile(aiDocPath(id), []byte(text), 0o644); err != nil {
		return "", err
	}
	aiDocGC()
	return id, nil
}

// aiDocLoad 读回正文。
func aiDocLoad(id string) (string, error) {
	if !aiAttachIDOK(id) {
		return "", fmt.Errorf("文档 ID 形状不对")
	}
	raw, err := os.ReadFile(aiDocPath(id))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// aiDocSafeName 处理文件名里能提前闭合包裹标签的字符。
//
// 文件名来自用户（甚至来自别的系统的导出名），一个 `">` 就能让
// <untrusted_data source="file:..."> 提前结束 —— 那等于让文件内容自己决定边界。
// 名字只用于人看和溯源，不需要保留这些字符。
func aiDocSafeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.NewReplacer("<", "_", ">", "_", "&", "_", `"`, "_", "\r", " ", "\n", " ").Replace(name)
	if name == "" {
		return "文件"
	}
	return name
}

// aiDocBlock 是一份文档注入 prompt 的形状。外部文件内容一律当**不可信数据**包裹，
// 让其中的指令不生效（与前端原来的包裹格式逐字节一致，便于长对话前缀缓存命中）。
func aiDocBlock(name, text string) string {
	return "<untrusted_data source=\"file:" + aiDocSafeName(name) + "\">\n" + text + "\n</untrusted_data>"
}

// aiDocHydrate 把 messages 里的文档引用读盘、拼回正文。
//
// 拼在用户那句话**后面**：先说要问什么、再给材料，对模型更自然（与图片块的顺序同一个理由）。
// 读完把 Docs 清空 —— 上游从没见过这个键，留着只会让严格实现的兼容层报 400。
func aiDocHydrate(msgs []aiMsg) []aiMsg {
	for i, m := range msgs {
		if m.Role != "user" || len(m.Docs) == 0 {
			continue
		}
		var blocks, gone []string
		for _, d := range m.Docs {
			text, err := aiDocLoad(d.ID)
			if err != nil {
				// 读不到就说清楚是哪一份、为什么，并让模型提醒用户重拖 ——
				// 静默丢掉的话，模型会对着"用户只说了半句话"的上下文瞎猜。
				warn(scSys, "AI", "文档附件读取失败（%s）：%v —— 该文件不随本轮上下文发送", aiDocLabel(d), err)
				gone = append(gone, aiDocLabel(d))
				continue
			}
			blocks = append(blocks, aiDocBlock(d.Name, text))
		}
		body := strings.TrimRight(m.Content, " \t\r\n")
		extra := strings.Join(blocks, "\n")
		switch {
		case body != "" && extra != "":
			m.Content = body + "\n" + extra
		case extra != "":
			m.Content = extra
		}
		if len(gone) > 0 {
			m.Content = strings.TrimRight(m.Content, " \t\r\n") +
				"\n（此处原本附有文件，但已不可读（附件只保留 24 小时）：" + strings.Join(gone, "、") +
				"。这是文件缺失，不是用户没提供材料；若仍需查看，请让用户重新拖入。）"
		}
		m.Docs = nil
		msgs[i] = m
	}
	return msgs
}

func aiDocLabel(d aiDocRef) string {
	if n := strings.TrimSpace(d.Name); n != "" {
		return n
	}
	if d.ID != "" {
		return "文件 " + d.ID[:6]
	}
	return "文件"
}

// aiDocGC 按 TTL + 份数上限清理（与图片附件同一套规则，见 attachSweep）。
func aiDocGC() {
	aiDocGCMu.Lock()
	defer aiDocGCMu.Unlock()
	removed, freed := attachSweep(aiDocDir(), aiDocMaxCount, aiDocTTL)
	if removed > 0 {
		info(scSys, "AI", "文档附件清理：删除 %d 个（%.1f MB），保留最近 %d 份 / %s",
			removed, float64(freed)/(1<<20), aiDocMaxCount, aiDocTTL)
	}
}

var (
	aiDocGCMu    sync.Mutex
	aiDocGCStart sync.Once
)

// aiDocStartGC 启动时清一次，之后每小时一次（理由同 aiAttachStartGC）。
func aiDocStartGC() {
	aiDocGCStart.Do(func() {
		aiDocGC()
		go func() {
			t := time.NewTicker(aiDocSweepEvery)
			defer t.Stop()
			for range t.C {
				aiDocGC()
			}
		}()
	})
}

// 排序只为了让 GC 的"保留最近 N 份"有确定语义（见 attachSweep）。
