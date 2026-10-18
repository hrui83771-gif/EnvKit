package main

// ai_confirm_test.go —— T1.1 单测：确认授权、参数防篡改、登记表卫生
//
// 锁四类行为：
//  1. 授权审计：确认执行前后 authorize 条目必须存在，四种结局都可区分；
//  2. 参数防篡改：回传参数与登记不一致 → 拒绝执行，且尝试本身进审计；
//  3. 无登记 fail closed：过期/伪造/重复提交都不执行；
//  4. 登记表卫生：TTL 与容量上限真的在回收。

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// aiConfirmFakeTool 临时注册一个只记参数的假写工具，返回 (执行记录切片指针, 清理函数)。
// **清理必须还原原条目**而不是无脑 delete——stop_service 这类真实工具被假条目
// 覆盖后，一 delete 就把真工具从注册表里删掉了，后续用例全判 not_in_whitelist。
func aiConfirmFakeTool(t *testing.T, name string) (*[]map[string]any, func()) {
	t.Helper()
	got := &[]map[string]any{}
	orig, had := aiToolRegistry[name]
	aiToolRegistry[name] = aiTool{
		Desc:  "测试用假工具",
		Write: true,
		Execute: func(args map[string]any) (string, error) {
			*got = append(*got, args)
			return "假工具已执行", nil
		},
	}
	return got, func() {
		if had {
			aiToolRegistry[name] = orig
		} else {
			delete(aiToolRegistry, name)
		}
	}
}

func TestAiConfirmGateApprove(t *testing.T) {
	_, cleanup := aiConfirmFakeTool(t, "stop_service")
	defer cleanup()

	orig := map[string]any{"service": "web"}
	aiRememberPending("call-1", "stop_service", orig)
	d := aiConfirmGate("stop_service", "call-1", true, map[string]any{"service": "web"})
	if !d.Proceed {
		t.Fatalf("参数一致的确认应放行，实际被拒：%s", d.Result)
	}
	if d.Args["service"] != "web" {
		t.Errorf("执行参数应取服务端登记副本，实际 %v", d.Args)
	}
	if d.Level != string(PolicyConfirm) {
		t.Errorf("stop_service 应判 confirm 档，实际 %q", d.Level)
	}
	if d.ArgsSHA == "" {
		t.Error("授权审计需要参数哈希，不能为空")
	}
}

func TestAiConfirmGateArgsTamper(t *testing.T) {
	_, cleanup := aiConfirmFakeTool(t, "stop_service")
	defer cleanup()

	// 模型请求停 web，客户端回传停 all —— 哈希对不上，必须拒
	aiRememberPending("call-2", "stop_service", map[string]any{"service": "web"})
	d := aiConfirmGate("stop_service", "call-2", true, map[string]any{"service": "all"})
	if d.Proceed {
		t.Fatal("回传参数与原始 tool_call 不一致时必须拒绝执行")
	}
	if d.Rule != "args_mismatch" {
		t.Errorf("拒绝原因应命名为 args_mismatch，实际 %q", d.Rule)
	}
	if d.ArgsSHA == "" {
		t.Error("篡改尝试的审计也要能指回原始参数哈希")
	}
}

func TestAiConfirmGateNoPending(t *testing.T) {
	// 没登记过的 call_id：伪造/过期/服务重启 —— 一律拒，不猜
	d := aiConfirmGate("stop_service", "call-never", true, map[string]any{"service": "web"})
	if d.Proceed {
		t.Fatal("无服务端登记的确认必须拒绝执行")
	}
	if d.Rule != "no_pending" {
		t.Errorf("拒绝原因应命名为 no_pending，实际 %q", d.Rule)
	}
	// tool 与登记不一致（张冠李戴）同样拒
	aiRememberPending("call-3", "stop_service", map[string]any{"service": "web"})
	d2 := aiConfirmGate("restart_service", "call-3", true, map[string]any{"service": "web"})
	if d2.Proceed || d2.Rule != "no_pending" {
		t.Fatalf("tool 与登记不一致应按 no_pending 拒，实际 proceed=%v rule=%q", d2.Proceed, d2.Rule)
	}
}

func TestAiConfirmGateRejectAndForbidden(t *testing.T) {
	_, cleanup := aiConfirmFakeTool(t, "stop_service")
	defer cleanup()

	aiRememberPending("call-4", "stop_service", map[string]any{"service": "web"})
	d := aiConfirmGate("stop_service", "call-4", false, map[string]any{"service": "web"})
	if d.Proceed || d.Level != string(PolicyConfirm) {
		t.Fatalf("用户拒绝应拒且档位为 confirm，实际 proceed=%v level=%q", d.Proceed, d.Level)
	}

	// forbidden 参数（危险脚本名）：即使登记过、用户批了，也拒
	aiRememberPending("call-5", "start_service", map[string]any{"service": "web", "script": "deploy"})
	d2 := aiConfirmGate("start_service", "call-5", true,
		map[string]any{"service": "web", "script": "deploy"})
	if d2.Proceed {
		t.Fatal("危险脚本参数必须被闸门拒（确认续跑不得绕过 R2）")
	}
	if d2.Level != string(PolicyForbidden) {
		t.Errorf("应判 forbidden 档，实际 %q", d2.Level)
	}
}

func TestAiConfirmGateUnknownTool(t *testing.T) {
	d := aiConfirmGate("no_such_tool", "call-6", true, nil)
	if d.Proceed || d.Rule != "not_in_whitelist" {
		t.Fatalf("白名单外工具应拒，实际 proceed=%v rule=%q", d.Proceed, d.Rule)
	}
}

func TestAiConfirmApplyAuditsFourOutcomes(t *testing.T) {
	_, cleanup := aiConfirmFakeTool(t, "stop_service")
	defer cleanup()
	withAuditDir(t, nil)

	// ① 放行：authorize=ok + policy_confirm=approved
	aiRememberPending("a-1", "stop_service", map[string]any{"service": "web"})
	rec := httptest.NewRecorder()
	out := aiConfirmApply(rec, rec, "stop_service",
		aiConfirmGate("stop_service", "a-1", true, map[string]any{"service": "web"}))
	if out != "假工具已执行" {
		t.Errorf("放行后应返回执行结果，实际 %q", out)
	}
	es := auditQuery("", "", 50)
	if !aiAuditHas(es, actAI, actAuthorize, "ok", "decision=approved") {
		t.Error("放行后缺少 authorize=ok/decision=approved 审计条")
	}
	if !aiAuditHas(es, actAI, "policy_confirm", "approved", "level=confirm") {
		t.Error("放行后缺少 policy_confirm 档位记录（误拒率数据源）")
	}

	// ② 用户拒绝：authorize=denied/decision=rejected
	aiRememberPending("a-2", "stop_service", map[string]any{"service": "web"})
	_ = aiConfirmApply(rec, rec, "stop_service",
		aiConfirmGate("stop_service", "a-2", false, map[string]any{"service": "web"}))
	es = auditQuery("", "", 50)
	if !aiAuditHas(es, actAI, actAuthorize, resDenied, "decision=rejected") {
		t.Error("用户拒绝也必须有授权审计（被拒绝≠没批过）")
	}

	// ③ 篡改：authorize=denied/decision=args_mismatch，且**不执行**
	aiRememberPending("a-3", "stop_service", map[string]any{"service": "web"})
	_ = aiConfirmApply(rec, rec, "stop_service",
		aiConfirmGate("stop_service", "a-3", true, map[string]any{"service": "all"}))
	es = auditQuery("", "", 50)
	if !aiAuditHas(es, actAI, actAuthorize, resDenied, "decision=args_mismatch") {
		t.Error("篡改尝试必须进审计且命名可辨")
	}

	// ④ 无登记：authorize=denied/decision=no_pending
	_ = aiConfirmApply(rec, rec, "stop_service",
		aiConfirmGate("stop_service", "a-never", true, map[string]any{"service": "web"}))
	es = auditQuery("", "", 50)
	if !aiAuditHas(es, actAI, actAuthorize, resDenied, "decision=no_pending") {
		t.Error("无登记确认必须进审计")
	}
}

func TestAiConfirmTakeIsOneShot(t *testing.T) {
	_, cleanup := aiConfirmFakeTool(t, "stop_service")
	defer cleanup()

	aiRememberPending("once-1", "stop_service", map[string]any{"service": "web"})
	if _, ok := aiTakePendingConfirm("once-1"); !ok {
		t.Fatal("登记后应能取到")
	}
	if _, ok := aiTakePendingConfirm("once-1"); ok {
		t.Error("同一 tool_call_id 只能确认一次（重复提交不得执行第二遍）")
	}
	// 取完之后再 confirm 应按 no_pending 拒
	d := aiConfirmGate("stop_service", "once-1", true, map[string]any{"service": "web"})
	if d.Proceed {
		t.Error("已取走的登记不得再次放行")
	}
}

func TestAiPendingConfirmGC(t *testing.T) {
	// 过期条目被回收
	aiPendingMu.Lock()
	aiPendingConfirms = map[string]aiPendingConfirmRec{
		"old": {Tool: "stop_service", At: time.Now().Add(-2 * aiPendingConfirmTTL)},
		"new": {Tool: "stop_service", At: time.Now()},
	}
	aiPendingGC()
	_, hasOld := aiPendingConfirms["old"]
	_, hasNew := aiPendingConfirms["new"]
	aiPendingMu.Unlock()
	if hasOld {
		t.Error("超过 TTL 的登记必须被回收")
	}
	if !hasNew {
		t.Error("未过期的登记不得被回收")
	}

	// 超量时从最旧的开始丢
	aiPendingMu.Lock()
	aiPendingConfirms = map[string]aiPendingConfirmRec{}
	base := time.Now()
	for i := 0; i < aiPendingConfirmMax+5; i++ {
		k := "k" + itoa(i)
		aiPendingConfirms[k] = aiPendingConfirmRec{Tool: "stop_service", At: base.Add(-time.Duration(i) * time.Second)}
	}
	aiPendingGC()
	n := len(aiPendingConfirms)
	// At = base - i 秒：k0 最新，k(max+4) 最旧。超量丢的必须是最旧那头
	_, hasNewest := aiPendingConfirms["k0"]
	_, hasOldest := aiPendingConfirms["k"+itoa(aiPendingConfirmMax+4)]
	aiPendingMu.Unlock()
	if n > aiPendingConfirmMax {
		t.Errorf("登记表超量（%d > %d）", n, aiPendingConfirmMax)
	}
	if hasOldest {
		t.Error("超量时应先丢最旧的")
	}
	if !hasNewest {
		t.Error("超量时不应丢最新的")
	}
}

func TestAiArgsSHACanonical(t *testing.T) {
	// 同参数不同插入顺序 → 同哈希（map 键排序序列化）
	a := aiArgsSHA(map[string]any{"service": "web", "script": "dev"})
	b := aiArgsSHA(map[string]any{"script": "dev", "service": "web"})
	if a == "" || a != b {
		t.Errorf("同参数应得同哈希，实际 %q vs %q", a, b)
	}
	if c := aiArgsSHA(map[string]any{"service": "all"}); c == a {
		t.Error("不同参数不得同哈希")
	}
	if aiConfirmArgsMatch(aiPendingConfirmRec{ArgsSHA: a}, nil) {
		t.Error("空回传参数不得与登记匹配")
	}
}

// aiAuditHas 审计里是否存在指定 (actor, action, result, detail子串) 的条目。
// 子串在 Detail 或 Params 里都算——policyRecord 把口径写进 Params，
// 而 authorize 写进 Detail，只查一个字段会漏。
func aiAuditHas(es []AuditEntry, actor, action, result, detailSub string) bool {
	for _, e := range es {
		if e.Actor == actor && e.Action == action && e.Result == result &&
			(detailSub == "" || strings.Contains(e.Detail, detailSub) ||
				strings.Contains(e.Params, detailSub)) {
			return true
		}
	}
	return false
}
