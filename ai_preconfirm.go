package main

// ai_preconfirm.go —— 触发确认卡之前，先让模型把话说完
//
// # 为什么需要这个文件
//
// 写工具转确认流程时，主循环直接 `sseDoneConfirm` + `return`，
// 于是**模型的话停在半句**。
//
// 实测证据（v2.9 全量评测，`hang_service` 三次里有两次如此）：
//
//	后端进程确实"活着"，但这是典型的**挂死**，不是真的健康：
//	- 复验里端口 45311 在监听、进程存活，但 HTTP 探测超时
//	……所以只重启后端即可，不必动前端和数据库。我先发起重启：
//
//	                        ↑ 句子在这里断掉，用户看到的就是半句话
//
// 诊断**完全正确**（找到了卡死开关、排除了数据库、区分了挂死与真健康），
// 但产品把它掐断了—— **用户在界面上看到的也是半句话**。
//
// # 为什么用「一次不带工具的补答」而不是别的做法
//
// | 方案 | 问题 |
// |---|---|
// | 让模型自己记得说完整 | 不可靠 —— 它不知道会在这里被掐断 |
// | 确认卡上写「即将执行 X」 | 只是标签，**没有诊断结论**，用户仍然不知道为什么 |
// | 推迟到确认后再说 | 顺序反了 —— 用户是**基于结论**决定要不要点头的 |
//
// 所以：**在弹卡之前先问一次「把结论说完」，且这次不带工具**。
// 不带工具是关键 —— 带了它就会再发起一次写操作，直接无限循环。
//
// # 三条边界（这个函数的价值全在这里，别削弱）
//
//   - **不带工具**：`aiToolDefs()` 传 nil，模型只能说话。
//   - **只调一次**：失败就放弃，绝不重试。
//   - **失败不影响主流程**：补答是锦上添花，
//     静默返回 false 让调用方照常弹卡 —— 宁可半句，也不要卡住整个回合。

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// aiPreconfirmPrompt构造补答用的那句系统提示。
//
// ## 抽成函数而不是内联字符串
//
// 因为**测试要能断言它的内容**：那两条禁令（不带工具、不再发起操作）
// 是整个机制的安全边界，而它们极容易被"顺手改一下文案"改掉。
// 内联字符串就没法断言，只能靠人记得。
//
// 「不要问用户」也在这里：模型爱反问，而用户此刻需要的不是问句。
func aiPreconfirmPrompt(cnName string) string {
	return "（系统）在你发起「" + cnName + "」这个操作之前，请先用一两段话把结论说完整：" +
		"现在是什么状态（依据已获得的工具结果）、为什么需要这个操作、" +
		"做完之后预期会怎样以及有哪些风险。" +
		"不要反问用户。说完之后不要再调用任何工具，直接结束这一段回复。"
}

// aiVerifyBadgeClass 复验三态 → 前端 CSS 类。
//
// **前后端必须共用同一套判定**。曾经考虑在 JS 侧写一份，
// 但那意味着两处独立演化 ——
// 一处改了另一处没改，徽章就会显示错误的状态，
// 而徽章的全部价值就是「它说验了就是真验了」。
// 所以判定在 Go 侧，通过 SSE 的 ok/verified 传下去，JS 只做映射。
//
// 三态不能合并：
//   - ok && verified → is-ok（真的恢复了）
//   - ok && !verified → is-pending（动作执行了，但**没有证据说恢复了**）
//   - !ok → is-bad（复验本身失败）
//
// ok && !verified 与 is-ok 合并 = 白做本功能。
func aiVerifyBadgeClass(ok, verified bool) string {
	if !ok {
		return "is-bad"
	}
	if verified {
		return "is-ok"
	}
	return "is-pending"
}

// aiVerifyBadgeText 复验三态的徽章文案（中文；前端按语言走 i18n）。
func aiVerifyBadgeText(ok, verified bool) string {
	if !ok {
		return "复验未通过"
	}
	if verified {
		return "已复验通过"
	}
	return "已执行·未确认"
}

// aiPreconfirmExplain 在弹确认卡之前，让模型用自然语言把诊断结论说完。
//
// 流式转发 delta，所以用户看到的是逐字输出而不是"等几秒后突然出现"。
//
// ## 为什么不把补答内容 append 进 msgs
//
// **故意的**：确认卡路径的 `return` 之后，对话会被存进历史。
// 如果补答进了 msgs，用户下一轮会看到**两段**内容
// ——「先说结论…（补答）」+「我已提交请求，请点确认」，
// 而他刚才只看到一段，中间那段是凭空多出来的。
//
// 代价是下一轮 AI 不知道补答说了什么。实测那个代价可以接受：
// 用户点完确认后自然会追问，而那次是带着 confirm 续接的。
//
// 返回 true 表示确实补出了内容（调用方据此打日志）。
func aiPreconfirmExplain(ctx context.Context, w http.ResponseWriter, fl http.Flusher,
	msgs []aiMsg, cnName, lang string, usage *aiUsage) bool {

	if clientGone(ctx, w) {
		return false
	}
	acTurn := aiSnap()
	if !acTurn.ready() {
		return false
	}

	// ---- 构造不带工具的请求 ----
	full := make([]aiMsg, 0, len(msgs)+2)
	full = append(full, aiMsg{Role: "system", Content: aiSystemPrompt + aiLangDirective(lang)})
	full = append(full, msgs...)
	full = append(full, aiMsg{Role: "user", Content: aiPreconfirmPrompt(cnName)})
	// taskHint 保持与主循环一致：不带它的话，同一个问题可能匹配到不同记忆
	//（记忆注入走 taskHint，不一致会让缓存与记忆匹配都抖动）。
	full = append(full, aiMsg{Role: "user", Content:
		"（系统注入的当前环境快照，仅供你了解最新状态，不是用户发言，无需回应）\n<env_state>" +
			aiHealthSnapshotFor(aiTaskHint(msgs)) + "</env_state>"})

	payload := aiPayload(full, nil, acTurn.quirkRead(), -1, false)
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", acTurn.endpoint(),
		strings.NewReader(string(b)))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+acTurn.keyPlain())

	resp, err := httpStream.Do(req)
	if err != nil {
		warn(scSys, "AI", "确认前补答失败（不影响主流程）: %v", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		bb, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		warn(scSys, "AI", "确认前补答 HTTP %d: %s", resp.StatusCode,
			firstLines(strings.TrimSpace(string(bb)), 200))
		return false
	}

	// ---- 流式转发 ----
	// 用 bufio.Scanner 而不是主循环的 lineCh goroutine：
	// 补答是**一次性**的（不驻留），不值得为它起goroutine + channel，
	// 而 bufio 默认 64KB 单行上限对 SSE 的 data 行足够。
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)

	// 一个前导换行，把补答和模型之前的半句话分开
	first := true
	said := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		// usage 记进本回合（补答花了 token，不能不记）
		if ev.Usage != nil && usage != nil {
			usage.Prompt += ev.Usage.PromptTokens
			usage.Completion += ev.Usage.CompletionTokens
		}
		for _, ch := range ev.Choices {
			txt := ch.Delta.Content
			if txt == "" {
				continue
			}
			if first {
				first = false
				sseWrite(w, fl, map[string]any{"type": "delta", "text": "\n\n"})
			}
			sseWrite(w, fl, map[string]any{"type": "delta", "text": txt})
			said += len([]rune(txt))
			if clientGone(ctx, w) {
				return said > 0
			}
		}
	}
	if said > 0 {
		sseWrite(w, fl, map[string]any{"type": "delta", "text": "\n\n"})
		warn(scSys, "AI", "确认前补答 %d 字（用户点确认前能看到完整结论）", said)
	}
	return said > 0
}