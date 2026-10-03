package main

// ai_history.go —— 对话历史管理：协议合规裁剪、滚动摘要、工具结果转消息
//
// 从 ai_loop.go 拆出。三件事共同的约束是"**历史不能无限增长**
// 且**协议必须合规**"，而它们互不相干：
//
//   - 悬挂 tool_calls 会让上游报 400，必须剥掉
//   - 历史变长后要把早期内容压成要点备忘（带前缀缓存，不重复生成）
//   - 工具结果一律走 user 消息（合成 assistant+tool 会触发 thinking 模型 400）

import (
	"fmt"
	"hash/fnv"
	"strings"
)

// aiTrimDanglingToolCalls 去掉历史中所有"没有紧跟 tool 结果"的 assistant(tool_calls) 消息。
// 场景：确认卡片弹出后用户不理会，直接发新消息 → 前端历史里留下悬挂的 tool_calls，
// 位置在中间（aiTrimLastToolTurn 只裁末尾），上游 OpenAI 协议直接 400。
// 非确认续接路径（prompt 分支）也调用它兜底。
func aiTrimDanglingToolCalls(msgs []aiMsg) []aiMsg {
	out := msgs[:0:0]
	for i, m := range msgs {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			if i+1 >= len(msgs) || msgs[i+1].Role != "tool" {
				continue // 悬挂：丢弃
			}
		}
		out = append(out, m)
	}
	return out
}
func aiTrimLastToolTurn(msgs []aiMsg) []aiMsg {
	i := len(msgs)
	for i > 0 && msgs[i-1].Role == "tool" {
		i--
	}
	if i > 0 && i < len(msgs) && msgs[i-1].Role == "assistant" && len(msgs[i-1].ToolCalls) > 0 {
		i--
	}
	if i < len(msgs) {
		return msgs[:i]
	}
	return msgs
}

// aiSummaryCut 返回摘要切分点：<=0 表示无需摘要。
// 边界量化是缓存的关键：cut 随消息增长每回合 +1~2，若直接做切分点，
// 缓存永远不命中、每回合多一次上游调用；对 aiSumQuantum 取整后，
// 只有累计满 6 条新消息边界才前移一次。
func aiSummaryCut(n int) int {
	if n <= aiSummarizeAfter {
		return 0
	}
	cut := n - aiSummarizeKeep
	return cut - cut%aiSumQuantum
}

// aiHistoryKey 历史前缀指纹（FNV-1a）：头部内容不变 → 摘要缓存命中。
func aiHistoryKey(head []aiMsg) string {
	h := fnv.New32a()
	for _, m := range head {
		h.Write([]byte(m.Role))
		h.Write([]byte{0})
		h.Write([]byte(m.Content))
		h.Write([]byte{0})
	}
	return fmt.Sprint(h.Sum32())
}

// aiMaybeSummarize 把 msgs 的较早部分替换成一条摘要消息；摘要失败时静默降级为原始历史
// （宁可上下文长一点，也不能因为摘要挂掉而且回合）。
func aiMaybeSummarize(msgs []aiMsg) []aiMsg {
	cut := aiSummaryCut(len(msgs))
	if cut <= 0 {
		return msgs
	}
	head := msgs[:cut]
	key := aiHistoryKey(head)
	aiSumMu.Lock()
	cached := aiSumCache.key == key
	text := aiSumCache.text
	aiSumMu.Unlock()
	if !cached {
		var err error
		text, err = aiSummarizeHistory(head)
		if err != nil {
			warn(scSys, "AI", "对话摘要生成失败，本回合按原始历史继续：%v", err)
			return msgs
		}
		aiSumMu.Lock()
		aiSumCache.key, aiSumCache.text = key, text
		aiSumMu.Unlock()
		info(scSys, "AI", "已生成对话摘要（压缩 %d 条 → %d 字），长对话上下文已续上", cut, len([]rune(text)))
	}
	out := make([]aiMsg, 0, len(msgs)-cut+1)
	out = append(out, aiMsg{Role: "user", Content: "（系统注入的此前对话要点备忘，不是用户发言，无需回应）\n<untrusted_data>" + text + "</untrusted_data>"})
	out = append(out, msgs[cut:]...)
	return out
}

// aiSummarizeHistory 用当前配置的模型把历史头部压缩成要点备忘（非流式，一次调用）。
func aiSummarizeHistory(head []aiMsg) (string, error) {
	var b strings.Builder
	b.WriteString("以下是 EnvKit 助手与用户的对话历史。请压缩成不超过 400 字的要点备忘，必须保留：\n" +
		"1) 用户的目标与明确要求；\n" +
		"2) 已执行/已确认/被用户拒绝的操作及其结果（保留关键文件名、路径、版本号、报错关键词）；\n" +
		"3) 助手尚未兑现的承诺或未完成的待办。\n" +
		"直接输出要点列表，不要评论，不要提问。\n\n<untrusted_data>\n")
	n := 0
	for _, m := range head {
		c := strings.TrimSpace(m.Content)
		if c == "" || (m.Role != "user" && m.Role != "assistant") {
			continue
		}
		if m.Role == "user" {
			b.WriteString("用户：")
		} else {
			b.WriteString("助手：")
		}
		b.WriteString(firstLines(c, 400))
		b.WriteString("\n")
		n++
		if n >= 40 {
			break
		}
	}
	b.WriteString("\n</untrusted_data>")
	out, err := aiCallLLM([]aiMsg{{Role: "user", Content: b.String()}}, nil)
	if err != nil {
		return "", err
	}
	return firstLines(strings.TrimSpace(out), 1200), nil
}

// aiToolResultAsUser 把工具结果包装成一条用户消息（供模型基于结果作答）
func aiToolResultAsUser(tool, result string) aiMsg {
	return aiMsg{Role: "user", Content: "[工具 " + tool + " 已执行，结果如下]\n" + result +
		"\n\n请基于以上结果用简体中文简要回答用户（不要罗列选项菜单，不要重复问我需要什么）。"}
}

// aiSleepCtx 可被客户端断开打断的退避等待；返回 false 表示上下文已取消
// aiLastUserText 取最后一条真实用户消息（跳过工具结果与系统注入），作为轨迹目标描述。
func aiLastUserText(msgs []aiMsg) string {
	for i := len(msgs) - 1; i >= 0 && i >= len(msgs)-6; i-- {
		c := strings.TrimSpace(msgs[i].Content)
		if c == "" || msgs[i].Role != "user" {
			continue
		}
		if strings.HasPrefix(c, "[工具 ") || strings.HasPrefix(c, "（系统") || strings.HasPrefix(c, "（系统注入") {
			continue
		}
		return c
	}
	return ""
}
