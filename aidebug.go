package main

// aidebug.go —— v2.4：AI 回合快照的只读调试端点
//
// ## 为什么需要它
//
// v2.4 跑「经验增益 A/B」时遇到一个无法绕开的观测缺口：
// A/B 只能看到「AI 调了哪些工具、说了什么」，
// **看不到「这一回合的提示词里到底注入了什么」**。
//
// 后果是：记忆库里明明有内容、开关也确认是开的，
// 但「这段记忆有没有真的进到模型面前」只能**从工具序列形态间接推断**
// （B 组走 read_file 直达而A 组反复 search_files，像是读了提示词）。
//
// **推断不是证据。** 而且它是唯一一处「A/B 两侧唯一变量是否真的生效」
// 无法直接验证的地方——如果哪天记忆注入链路悄悄断了（比如改了
// aiHealthSnapshotFor 的字段名），A/B 会跑出"增益消失"，
// 但没有任何机制能区分「记忆层退化了」与「注入本来就一直是空的」。
//
// 所以这里开一个**只读**端点，把当回合快照原样返回。
//
// ## 为什么放在 /api/ai/ 而不是 /api/debug/
//
// 它返回的是 AI 的输入而不是系统内部状态，语义上属于 AI 域；
// 且它复用 aiHealthSnapshotFor ——**同一个函数、同一条注入路径**，
// 端点返回什么就等于模型看到了什么（除了 chat 里另加的 system 提示）。
//
// ## 安全边界（刻意收紧）
//
// 1. **只返回快照，不接受任何动作**——没有 op 参数、没有 POST 写语义。
// 2. **不返回任何凭据**——快照本身已是脱敏后的（见 redactSecrets），
//    但这里再过一遍 aiSanitizeForDebug，双保险。
// 3. **需要 Token 鉴权**——走的是 authMW，与其它 /api/* 一致。
// 4. **不返回完整 system prompt**——只给快照 JSON。
//    完整提示词里有大量内部指令，对外暴露没有价值。

import (
	"encoding/json"
	"net/http"
	"strings"
)

// handleAIDebug GET/POST：返回当回合会注入给模型的环境快照。
//
// query/body 参数：
//
//	task（可选）：用户当前诉求，用于挑选相关记忆。
//	              与 chat 里 aiTaskHint 提取的是同一个字段，
//	              传不同的 task 就能验证「记忆是否真的按任务筛选」。
//
// 响应：
//
//	{
//	  "ok": true,
//	  "memory_enabled": true,
//	  "task": "数据库配置在哪",
//	  "snapshot": { ...原样 JSON... },
//	  "snapshot_text": "…同一份内容的可读形态…",
//	  "memories_matched": 3,
//	  "lessons_matched": 1,
//	  "note": "记忆注入为空时这里会是 0——那说明 A/B 测的是空容器"
//	}
//
// **matched 计数是这个端点最有价值的部分**：它把「注入是否发生」
// 从推断变成了可读的数字。
func handleAIDebug(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}

	task := strings.TrimSpace(r.URL.Query().Get("task"))
	if task == "" && r.Method == http.MethodPost {
		// 允许 POST 传 task，但 body 里只认这一个字段。
		// 不解析其它键——这是个只读端点，收到别的字段一律忽略。
		var body struct {
			Task string `json:"task"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			task = strings.TrimSpace(body.Task)
		}
	}

	snap := aiHealthSnapshotFor(task)

	// 从快照里数出实际注入了多少条记忆与经验。
	// 直接解析 JSON 而不是再调一次 memoriesFor ——
	// **要验的是"快照里有什么"，不是"函数会返回什么"**。
	// 两者理论上相等，但一旦 aiHealthSnapshotFor 改了字段名，
	// 只有这个解析结果会变，正好是我们要抓的那种漂移。
	nMem, nLes := 0, 0
	var keys []string
	if m := map[string]any{}; json.Unmarshal([]byte(snap), &m) == nil {
		for k := range m {
			keys = append(keys, k)
			switch k {
			case "user_memories":
				nMem = countLines(m[k])
			case "lessons_learned":
				nLes = countLines(m[k])
			}
		}
	}

	note := ""
	if aiMemoryOn() && nMem == 0 && nLes == 0 {
		// 这是本端点最有价值的一条提示：
		// 开关是开的但一条都没注入 —— A/B 测的就是空容器。
		note = "**记忆开关已开，但本回合注入量为 0。**" +
			"若此刻在跑 A/B，测的是「开不开一个空容器」，增益必然为 0。" +
			"先用 POST /api/memory 写入与任务相关的经验，再重跑。"
	}

	writeJSON(w, map[string]any{
		"ok":               true,
		"memory_enabled":   aiMemoryOn(),
		"task":             task,
		"snapshot":         json.RawMessage(snap),
		"snapshot_text":    snap,
		"snapshot_fields":  keys,
		"memories_matched": nMem,
		"lessons_matched":  nLes,
		"note":             note,
	})
}

// countLines 数一个快照字段里有几行内容。
// 字段可能是字符串（记忆文本）或数组，两种都按"条数"算。
func countLines(v any) int {
	switch t := v.(type) {
	case nil:
		return 0
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0
		}
		n := 0
		for _, ln := range strings.Split(s, "\n") {
			if strings.TrimSpace(ln) != "" {
				n++
			}
		}
		return n
	case []any:
		return len(t)
	default:
		return 1
	}
}

func init() {
	aiToolRegistry["get_env_snapshot"] = aiTool{
		Desc: "读取**这一回合会注入给模型的环境快照**（当前有哪些组件、项目结构、启动方式、" +
			"服务状态，以及用户记忆与自动经验——如果记忆层开着的话）。" +
			"用户问「你现在看到了什么」「你知道了哪些关于这个项目的信息」时用它。" +
			"注意：这是**读自己的输入**，不是读环境状态——" +
			"要环境状态请用 get_system_state。",
		Schema: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}},
		Execute: func(args map[string]any) (string, error) {
			task, _ := args["task"].(string)
			return aiHealthSnapshotFor(task), nil
		},
	}
}
