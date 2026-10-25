package main

// ai_confirm.go —— v3.0 T1.1：写操作确认的授权依据与参数防篡改
//
// ## 修的问题（G1）
//
// 原 Confirm 分支（ai_loop.go）拿到回传的 `body.Confirm.Args` 就
// **直接执行**：
//  1. 执行前不重过 PolicyGate、不记授权审计——「谁批了这件事」没有记录，
//     恢复事务的 Authorize 阶段是空的；
//  2. args 由客户端回传，服务端不与原始 tool_call 比对——**参数可被篡改**：
//     模型请求 stop_service(web)，客户端回传 stop_service(all)，服务端照办。
//
// ## 方案：服务端留存原始参数，执行以服务端副本为准
//
// 三处 confirm_request 发出点（预检意图 ×2、模型 tool_call ×1）在发卡时
// 把 (tool, args) 按 tool_call_id 登记；确认回来时：
//   · 查登记表——没有（过期/伪造）即拒绝，不猜；
//   · 哈希比对客户端回传参数——不一致即拒绝并留审计（**篡改尝试本身要被看见**）；
//   · 一致时仍用服务端副本执行（哈希一致之外的双保险）；
//   · 执行前重过 PolicyGate + 记 `authorize` 审计条（授权依据落账）。
//
// 登记表有 TTL 与条数上限：确认卡可能永远不被点（用户直接关页面），
// 不回收就是一条慢速内存泄漏。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// aiPendingConfirmTTL 待确认请求的存活时间。覆盖「用户思考半小时再点确认」，
// 又不至于让丢弃的卡片无限占用内存。
const aiPendingConfirmTTL = 30 * time.Minute

// aiPendingConfirmMax 登记表容量上限。正常对话同时在等的确认卡只有一张；
// 上限是防御性设计——异常路径反复发卡时不能把内存吃干净。
const aiPendingConfirmMax = 64

// aiPendingConfirmRec 一次待确认写操作的原始登记。
type aiPendingConfirmRec struct {
	Tool    string         // 工具名
	Target  string         // PolicyGate 口径的目标（script= 形式也带）
	Args    map[string]any // **服务端留存的原始参数**（执行以它为准）
	ArgsSHA string         // 原始参数的规范哈希（审计与比对用）
	At      time.Time
}

var (
	aiPendingMu       sync.Mutex
	aiPendingConfirms = map[string]aiPendingConfirmRec{}
)

// aiArgsSHA 参数的规范哈希：Go 的 json.Marshal 对 map 的键排序输出，
// 因此同一参数的两次序列化结果稳定（与字段顺序无关）。
func aiArgsSHA(args map[string]any) string {
	// nil 与空 map 必须同哈希：两者语义都是"没有参数"，而 JSON 里分别序列化成
	// null 与 {}。不归一化的话，"工具本来就没有参数、客户端回执里干脆不带 args"
	// 会被判成 args_mismatch（篡改），用户看到的是"确认了但没执行"。
	// 注意这**不放宽**任何边界：登记里真有参数时，空 map 的哈希依然对不上。
	if args == nil {
		args = map[string]any{}
	}
	b, err := json.Marshal(args)
	if err != nil {
		// 参数不可序列化（理论上 json.Unmarshal 产不出这种值）：
		// 返回空让调用方按"无法登记"处理，绝不返回一个看起来正常的假哈希。
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// aiRememberPending 登记一次待确认写操作（发确认卡时调用）。
// callID 为空或哈希算不出时不登记——调用方会因此拿到 hasPC=false，
// 确认回来时按「无对应请求」拒绝，fail closed。
func aiRememberPending(callID, tool string, args map[string]any) {
	if callID == "" {
		return
	}
	sha := aiArgsSHA(args)
	if sha == "" {
		return
	}
	aiPendingMu.Lock()
	defer aiPendingMu.Unlock()
	aiPendingGC()
	aiPendingConfirms[callID] = aiPendingConfirmRec{
		Tool:    tool,
		Target:  policyTargetOf(tool, args),
		Args:    args,
		ArgsSHA: sha,
		At:      time.Now(),
	}
}

// aiPendingGC 清理过期与超量的登记（调用方持锁）。
// 超量时先清过期的；仍超则从最旧的开始丢——丢的是「还没被确认的卡片」，
// 后果是那张卡确认时被拒绝（fail closed），不会误放行。
func aiPendingGC() {
	now := time.Now()
	for k, v := range aiPendingConfirms {
		if now.Sub(v.At) > aiPendingConfirmTTL {
			delete(aiPendingConfirms, k)
		}
	}
	for len(aiPendingConfirms) > aiPendingConfirmMax {
		oldestK, oldestAt := "", time.Now()
		for k, v := range aiPendingConfirms {
			if v.At.Before(oldestAt) {
				oldestK, oldestAt = k, v.At
			}
		}
		if oldestK == "" {
			return
		}
		delete(aiPendingConfirms, oldestK)
	}
}

// aiTakePendingConfirm 取走一次登记（取走即删：一次确认只能用一次，
// 重复提交同一 tool_call_id 不会执行第二遍）。
func aiTakePendingConfirm(callID string) (aiPendingConfirmRec, bool) {
	if callID == "" {
		return aiPendingConfirmRec{}, false
	}
	aiPendingMu.Lock()
	defer aiPendingMu.Unlock()
	rec, ok := aiPendingConfirms[callID]
	if ok {
		delete(aiPendingConfirms, callID)
	}
	return rec, ok
}

// aiConfirmArgsMatch 客户端回传参数与登记的是否一致。
// 用哈希而不是逐字段比对方便——且哈希已进审计，比对与记录同源。
func aiConfirmArgsMatch(rec aiPendingConfirmRec, got map[string]any) bool {
	sha := aiArgsSHA(got)
	return sha != "" && sha == rec.ArgsSHA
}

// aiAuthorizeText 给模型看的授权结果说明（进工具返回值，让它如实转述）。
func aiAuthorizeText(ok bool, why string) string {
	if ok {
		return "（系统）该操作已获得用户授权（authorize 已留审计），可以执行。"
	}
	return "（系统）授权未通过：" + why + "。不要重试，也不要换个参数再试；如实告诉用户。"
}

// 确认卡上的「已授权/未授权」审计动作名。与执行记录（start_service 等）、
// 复验记录（verify_*）并列，构成恢复事务四要素里的第一项。
const actAuthorize = "authorize"

// fmtAuthorizeDetail 授权审计的详情：策略档位 + 命中规则 + 批准人 + 参数哈希。
// 参数哈希让「批的到底是哪份参数」事后可核——篡改尝试与合法批准的
// 区别就在这一行里。
func fmtAuthorizeDetail(level, rule, decision, argsSHA string) string {
	return fmt.Sprintf("policy=%s rule=%s approver=user decision=%s args_sha=%s",
		level, rule, decision, argsSHA)
}

// aiConfirmDecision 一次确认回执的裁决结果（纯数据，无副作用）。
type aiConfirmDecision struct {
	Proceed bool           // 是否放行执行
	Args    map[string]any // 执行参数（服务端登记副本；Proceed=false 时为 nil）
	Result  string         // 不放行时交给模型的结果文本
	Level   string         // 授权档位文本（审计用；no_pending/args_mismatch 时为空串）
	Rule    string         // 命中规则（审计用）
	ArgsSHA string         // 登记的原始参数哈希（无登记时为空串）
	// Batch = 用户在同一张卡上勾选了"后续同类不再询问"（v3.2）。
	// **不由 aiConfirmGate 判定**（它是纯函数，只看"能不能执行"），
	// 由两个入口（handleAIConfirm / handleAIChat 的 confirm 分支）在放行时带上。
	Batch bool
}

// aiConfirmGate 确认回执裁决（**纯函数**：不落审计、不执行、不写 SSE）。
//
// 顺序即优先级，每一关不过就不进下一关：
//  1. 工具不在白名单 → 拒（与主循环的 unknown 分支同一口径）；
//  2. 无服务端登记（过期/伪造/服务重启过）→ 拒，**绝不退回客户端参数**；
//  3. 用户没批 → 拒（留审计，见 aiConfirmApply）；
//  4. 回传参数哈希与登记不一致 → 拒（篡改尝试）；
//  5. 重过 PolicyGate → forbidden 拒；其余放行，**用登记副本执行**。
func aiConfirmGate(tool, callID string, approved bool, gotArgs map[string]any) aiConfirmDecision {
	var d aiConfirmDecision
	if _, exists := aiToolRegistry[tool]; !exists {
		d.Rule = "not_in_whitelist"
		d.Result = "错误：该工具不在白名单内"
		return d
	}
	pc, hasPC := aiTakePendingConfirm(callID)
	if !hasPC || pc.Tool != tool {
		d.Rule = "no_pending"
		d.Result = "错误：该确认请求已过期或不存在（可能服务已重启）。请重新发起操作。"
		return d
	}
	// 到了这里必有登记：哈希带上，四种结局的审计都能指回原始参数
	d.ArgsSHA = pc.ArgsSHA
	if !approved {
		d.Level, d.Rule = string(PolicyConfirm), "action:"+pc.Tool
		d.Result = "用户拒绝了该操作。请给出替代的手动操作步骤。"
		return d
	}
	if !aiConfirmArgsMatch(pc, gotArgs) {
		d.Rule = "args_mismatch"
		d.Result = "错误：确认回传的参数与原始请求不一致，已被安全策略拒绝。请重新发起操作。"
		return d
	}
	verdict := PolicyGate(pc.Tool, pc.Target, actAI)
	if !verdict.Allowed() {
		d.Level, d.Rule = string(verdict.Level), verdict.Rule
		d.Result = "错误：" + aiAuthorizeText(false, verdict.Reason)
		return d
	}
	d.Proceed, d.Args = true, pc.Args
	d.Level, d.Rule = string(verdict.Level), verdict.Rule
	return d
}

// aiConfirmApply 把裁决落成实际效果：授权审计 + 执行 + SSE 反馈。
// 返回交给模型的工具结果文本（放行时是执行结果，拒绝时是原因）。
//
// 授权审计四种结局都记（approved / rejected / denied / args_mismatch）——
// 「谁批了哪份参数」是恢复事务 Authorize 阶段的唯一凭据，
// 缺了它，审计里只有执行记录，授权依据仍然为空（G1 的另一半）。
func aiConfirmApply(w http.ResponseWriter, fl http.Flusher, tool string, d aiConfirmDecision) string {
	decision := "rejected"
	if d.Proceed {
		decision = "approved"
	}
	if d.Rule == "args_mismatch" || d.Rule == "no_pending" {
		decision = d.Rule // 篡改/伪造尝试单独命名：这是安全事件，不是普通拒绝
	}
	target := ""
	if d.ArgsSHA != "" {
		target = policyTargetOf(tool, d.Args)
	}
	res := resDenied
	if d.Proceed {
		res = resOK
	}
	auditNow(actAI, actAuthorize, tool, target, res,
		fmtAuthorizeDetail(d.Level, d.Rule, decision, d.ArgsSHA))
	// 策略档位也记一份（policyRecord 的设计用途：confirm/elevated 记录 +
	// 用户是否确认 → 误拒率）。no_pending/args_mismatch 没跑过 PolicyGate，
	// 没有档位可记——上面那条 authorize 审计已经把它们命名了。
	if lvl := PolicyLevel(d.Level); lvl == PolicyConfirm || lvl == PolicyElevated || lvl == PolicyForbidden {
		policyRecord(PolicyVerdict{
			Level: lvl, Rule: d.Rule, Action: tool, Actor: actAI,
			Reason: "确认续跑授权（T1.1）",
		}, decision)
	}
	if !d.Proceed {
		if d.Rule == "no_pending" || d.Rule == "args_mismatch" {
			warn(scSys, "AI", "确认请求被拒（tool=%s rule=%s）", tool, d.Rule)
		}
		return d.Result
	}
	// 执行前先给可见反馈：go build / npm install / mysqldump 可能耗时 1~2 分钟，
	// 期间一条输出都没有，用户会以为"点了确认没反应"而重复点击。
	sseWrite(w, fl, map[string]any{"type": "delta", "text": "正在执行：" +
		aiToolCnName(tool, d.Args) + "…（可能需要几十秒到几分钟，请稍候）\n"})
	// **按服务端登记的副本执行**：哈希一致之外的双保险——
	// 回传参数与登记参数哪怕逐字节相等，也不走客户端那份。
	out, err := aiToolRegistry[tool].Execute(d.Args)
	// ---- v3.0 T1.7：确认续跑这条路的复验结论也要发事件 ----
	//
	// 写工具（start_service / restart_service / db_backup …）**全部**
	// 经这里执行，而主循环那段 `aiTakeVerify()` 发射只在**非写工具**
	// 的分支里跑得到。于是这一整条路径的复验结论：
	//
	//   审计里**有**（aiStartVerify 会 auditVerify）
	//   SSE 流里**没有**
	//
	// 后果实测（2026-10-19 冒烟）：kill_service 真恢复、
	// `health_after.listening=true`、产品内部也复验通过，
	// 但判据只看得到「模型宣布恢复」→ 判成假愈 → safe_handling FAIL。
	// **真恢复被记成谎报**，比漏报更伤。
	//
	// 放在这里而不是让评测去认"调过 start_service"：
	// 工具名是动作，不是结论——同一个 start_service 也可能返回
	// 「起来了但没证明恢复」。判据必须读结论。
	if vr, ok := aiTakeVerify(); ok {
		sseWrite(w, fl, aiVerifyEvent(tool, vr))
	}
	if err != nil {
		return "执行出错：" + err.Error()
	} else {
		return out
	}
}
