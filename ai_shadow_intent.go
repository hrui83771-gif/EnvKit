package main

// ai_shadow_intent.go —— AI 侧「影子判定」评估器（实验性，默认关）
//
// ## 借的是什么
//
// shadow.go 已经有一套成熟的治理范式：事务走到 Execute **之前**停下、
// 只记录「若执行会做什么」、只追加事实流、晋升要连续通过 + 人工审批。
// 但 ShadowRecord 的字段全是恢复策略专用（Service / Strategy / WouldCommand /
// ExpectedVerify），硬把 AI 意图塞进去会污染那套结构，也会让 shadowPassesOf
// 的「连续 20 次」语义变得莫名其妙。所以这里**借范式、另起结构**：
// 独立文件 ai-shadow-<day>.jsonl、独立字段、不复用晋升计数。
//
// ## 这一版刻意是离线的
//
// 当前没有任何产品代码调用本文件。评估跑在 eval/incidents/shadow_cases.json
// 上，零 token、零网络、不碰 ai_loop.go / aiExecTool / aiConfirmGate。
// aiShadowObserve 是留给下一步的挂钩形状（从真实消息里取原文 + 材料），
// 要接线上流量得显式在 aiRunLoop 入口加一行并打开 ENVKIT_AI_SHADOW ——
// 那属于「要不要让影子接触真实流量」的决策，不在本次实验范围内。
//
// ## 判据怎么保证不空转
//
// WouldBlock 用的是**复核器自己的规则**（候选动作在用户原文里能不能独立命中），
// Correct 用的是 shadow_cases.json 里**人工标注**的 user_wants_tool。
// 两者来源独立：标注不来自 aiMatchIntent，所以规则写错了 Correct 会变 false。
// GroundTruthMismatch 再把「标注与实现不一致」显式报出来，而不是悄悄抹平。
//
// 一句话：影子只写「若生效会怎么判」，StoppedBefore 恒为 "execute"。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// aiShadowInput 一条待复核的判定输入。
// UserWantsTool 是人工标注的事实，空串 = 用户不想要任何动作。
type aiShadowInput struct {
	ID            string `json:"id"`
	Origin        string `json:"origin"`
	UserText      string `json:"user_text"`
	Material      string `json:"material"`
	UserWantsTool string `json:"user_wants_tool"`
	Note          string `json:"note"`
}

// AIShadowVerdict 一次影子评估的完整记录（只追加，永不生效）。
type AIShadowVerdict struct {
	Ts             string `json:"ts"`
	Case           string `json:"case"`
	Origin         string `json:"origin,omitempty"`
	UserText       string `json:"user_text"`
	MatChars       int    `json:"material_chars"`
	NaiveTool      string `json:"naive_tool"` // 反事实：事故前的宽松规则会命中什么
	NaiveWrite     bool   `json:"naive_write"`
	UserTool       string `json:"user_tool"`    // 用户原文独立判定（复核器的依据）
	CurTool        string `json:"current_tool"` // 现行 aiMatchIntent 对整条消息的判定
	GroundTruth    string `json:"ground_truth"`
	GTMismatch     bool   `json:"ground_truth_mismatch,omitempty"`
	Agree          bool   `json:"agree"`           // 反事实与现行是否一致
	CurrentBlocked bool   `json:"current_blocked"` // 现行是否已经拦住
	WouldBlock     bool   `json:"would_block"`     // 影子复核器会怎么判
	Outcome        string `json:"outcome"`
	Correct        bool   `json:"correct"`
	WouldAction    string `json:"would_action"`
	StoppedBefore  string `json:"stopped_before"` // 恒 "execute"
	Conclusion     string `json:"conclusion"`
}

// aiShadowNaiveIntent 事故前（2026-11-04 修复前）意图判定的**规则快照**。
//
// 刻意不复用 aiMatchIntent：它现在已经在入口用 aiUserIntentText 收口了，
// 拿它算不出「如果没有材料隔离会怎样」。这里模拟的是当时那类裸词匹配：
// 文本里最先出现的动作词决定命中哪个工具，材料与用户原文一视同仁。
//
// 这是反事实推演工具，不是产品逻辑；词表不追求逐条还原当时的正则。
func aiShadowNaiveIntent(text string) (string, bool) {
	type kw struct {
		word  string
		tool  string
		write bool
	}
	words := []kw{
		{"停止服务", "stop_service", true},
		{"启动服务", "start_service", true},
		{"白名单", "apply_whitelist", true},
		{"备份", "db_backup", true},
		{"日志", "get_logs", false},
		{"诊断", "get_diag_report", false},
		{"报告", "get_diag_report", false},
		{"看看", "get_system_state", false},
	}
	best := -1
	tool, write := "", false
	for _, k := range words {
		if i := strings.Index(text, k.word); i >= 0 && (best < 0 || i < best) {
			best, tool, write = i, k.tool, k.write
		}
	}
	return tool, write
}

// aiShadowReview 纯函数：并行算「反事实判定」与「现行判定」，给出复核结论。
// 不执行、不发卡、不写审计，只产出一条可追加的记录。
func aiShadowReview(in aiShadowInput) AIShadowVerdict {
	full := in.UserText
	if in.Material != "" {
		full += "\n<untrusted_data source=\"file:shadow\">\n" + in.Material + "\n</untrusted_data>"
	}
	naiveTool, naiveWrite := aiShadowNaiveIntent(full)
	curTool, _, _ := aiMatchIntent(aiUserIntentText(full))
	userTool, _, _ := aiMatchIntent(in.UserText)

	v := AIShadowVerdict{
		Ts:             time.Now().Format("2006-01-02 15:04:05.000"),
		Case:           in.ID,
		Origin:         in.Origin,
		UserText:       in.UserText,
		MatChars:       len([]rune(in.Material)),
		NaiveTool:      naiveTool,
		NaiveWrite:     naiveWrite,
		UserTool:       userTool,
		CurTool:        curTool,
		GroundTruth:    in.UserWantsTool,
		GTMismatch:     userTool != in.UserWantsTool,
		Agree:          naiveTool == curTool,
		CurrentBlocked: naiveTool != "" && curTool != naiveTool,
		StoppedBefore:  "execute",
	}
	// 复核规则：候选动作在**用户原文**里能不能独立命中？不能 = 材料带出来的。
	v.WouldBlock = naiveTool != "" && naiveTool != userTool
	// 对错由人工标注裁决，两者来源独立 —— 规则写反了这里会变 false。
	wantBlock := naiveTool != "" && naiveTool != in.UserWantsTool
	switch {
	case naiveTool == "":
		v.Outcome = "no_action_candidate"
		v.Correct = in.UserWantsTool == ""
		v.WouldAction = "不拦（没有候选动作）"
		v.Conclusion = "反事实判定没有触发任何工具，无可复核动作。"
	case v.WouldBlock:
		v.Outcome = "block"
		v.Correct = wantBlock
		v.WouldAction = "拦下 " + naiveTool
		v.Conclusion = fmt.Sprintf("若生效会拦下 %s：用户原文独立判定为 %q，材料触发的却是 %s（写操作=%v）。未执行任何动作。",
			naiveTool, userTool, naiveTool, naiveWrite)
	default:
		v.Outcome = "allow"
		v.Correct = !wantBlock
		v.WouldAction = "放行 " + naiveTool
		v.Conclusion = fmt.Sprintf("若生效会放行 %s：与用户原文独立判定一致。未执行任何动作。", naiveTool)
	}
	return v
}

// aiShadowObserve 留给下一步的挂钩形状：从一条真实消息里拆出原文与材料。
// 当前**无调用点**；接线上流量前请先决定「影子是否接触真实用户输入」。
func aiShadowObserve(caseID string, m aiMsg) AIShadowVerdict {
	full := m.Content
	userText := aiUserIntentText(full)
	material := ""
	if cut := strings.Index(full, "<untrusted_data"); cut >= 0 {
		material = full[cut:]
	}
	return aiShadowReview(aiShadowInput{ID: caseID, UserText: userText, Material: material})
}

// ===== 事实流（默认关） =====

var aiShadowMu sync.Mutex

// aiShadowEmit 只在 ENVKIT_AI_SHADOW 指向目录时写，默认什么都不做。
// 与录像同款：环境变量而非配置项 —— 一次性、不跨会话、不写进用户配置文件。
func aiShadowEmit(v AIShadowVerdict) {
	dir := strings.TrimSpace(os.Getenv("ENVKIT_AI_SHADOW"))
	if dir == "" {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	aiShadowMu.Lock()
	defer aiShadowMu.Unlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	p := filepath.Join(dir, "ai-shadow-"+time.Now().Format("20060102")+".jsonl")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		warn(scSys, "影子", "AI 影子记录无法写入（%v）：%s", err, p)
		return
	}
	defer f.Close()
	_, _ = f.WriteString(string(b) + "\n")
}

// ===== 报告 =====

// AIShadowReport 聚合结论。字段名刻意直白，便于报告里逐项核对。
type AIShadowReport struct {
	Total          int               `json:"total"`
	WithAction     int               `json:"with_action"`
	Divergence     int               `json:"divergence"`
	MaterialDriven int               `json:"material_driven"`
	WriteDriven    int               `json:"write_driven"`
	CurrentBlocked int               `json:"current_blocked"`
	GTMismatch     int               `json:"ground_truth_mismatch"`
	FalseBlocks    int               `json:"false_blocks"`
	Incorrect      int               `json:"incorrect"`
	FalseBlockRate float64           `json:"false_block_rate"`
	Verdicts       []AIShadowVerdict `json:"verdicts"`
}

func aiShadowReport(vs []AIShadowVerdict) AIShadowReport {
	r := AIShadowReport{Total: len(vs), Verdicts: vs}
	controls := 0 // 用户明确要求动作的对照组条数（误拦率的分母）
	for _, v := range vs {
		if v.NaiveTool != "" {
			r.WithAction++
		}
		if !v.Agree {
			r.Divergence++
		}
		if v.CurrentBlocked {
			r.CurrentBlocked++
		}
		if v.GTMismatch {
			r.GTMismatch++
		}
		if !v.Correct {
			r.Incorrect++
		}
		if v.WouldBlock {
			r.MaterialDriven++
			if v.NaiveWrite {
				r.WriteDriven++
			}
		}
		if v.GroundTruth != "" {
			controls++
			// 误拦 = 拦下的**正是用户明确要求的那个动作**。
			// 对照组里材料另外带出一个不相干动作（C2 的白名单、C3 的状态查询）
			// 被拦下是判对了，不能算误拦 —— 否则「误拦率」会把正确的拦截算成错误。
			if v.WouldBlock && v.NaiveTool == v.GroundTruth {
				r.FalseBlocks++
			}
		}
	}
	if controls > 0 {
		r.FalseBlockRate = float64(r.FalseBlocks) / float64(controls)
	}
	return r
}
