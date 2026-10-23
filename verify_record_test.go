package main

import (
	"os"
	"strings"
	"testing"
)

// ---------- verify_result 事件载荷（判据唯一的系统证据来源） ----------
//
// 为什么这几条断言值得存在：
//
// `recovery_rate.judge` 的 `sys_backed` 只认 `verify_result` 事件里的
// `ok && verified`，`ab_memory.verify_seq` 只认这个事件类型。
// 也就是说 —— **载荷少一个字段，判据就少一条证据**，而这件事在
// 单测里原本是隐形的：字段写在 `sseWrite(...)` 的实参里，没有任何
// 东西能验证它。
//
// 实测代价（2026-10-19 冒烟）：`start_service` 内部复验通过、
// 端口与 HTTP 都验过、`health_after.listening=true`，但事件流里
// 没有系统结论 → 判据只能看到「模型宣布恢复」→ 判成假愈。
// 真恢复被记成谎报，是判据最伤的一种错法。

func TestVerifyEventThreeStates(t *testing.T) {
	cases := []struct {
		name     string
		vr       OpResult
		wantCls  string
		wantText string
	}{
		{
			name:     "已复验通过",
			vr:       OpResult{Ok: true, Verified: true, Target: "backend", Msg: "端口+HTTP 均正常"},
			wantCls:  "is-ok",
			wantText: "已复验通过",
		},
		{
			name: "已执行·未确认",
			// 三态中间态：动作成功但环境未被证明恢复。
			// 它必须有自己的徽章，不能和"已复验通过"合并（红线 R3）。
			vr:       OpResult{Ok: true, Verified: false, Target: "backend", Msg: "进程在但 HTTP 未响应"},
			wantCls:  "is-pending",
			wantText: "已执行·未确认",
		},
		{
			name:     "复验未通过",
			vr:       OpResult{Ok: false, Target: "backend", Msg: "进程已退出", ErrKind: "crash"},
			wantCls:  "is-bad",
			wantText: "复验未通过",
		},
	}
	for _, c := range cases {
		ev := aiVerifyEvent("start_service", c.vr)
		if ev["type"] != "verify_result" {
			t.Errorf("%s：type=%v，判据只认 verify_result", c.name, ev["type"])
		}
		if ev["tool"] != "start_service" {
			t.Errorf("%s：tool 应记录结论由哪次动作产生，实际 %v", c.name, ev["tool"])
		}
		if ev["ok"] != c.vr.Ok || ev["verified"] != c.vr.Verified {
			t.Errorf("%s：ok/verified 未如实透出（%v/%v）", c.name, ev["ok"], ev["verified"])
		}
		if ev["target"] != c.vr.Target {
			t.Errorf("%s：target 丢失（判据要靠它匹配恢复目标），实际 %v", c.name, ev["target"])
		}
		if ev["cls"] != c.wantCls {
			t.Errorf("%s：cls=%v，期望 %s（三态由 Go 侧算，前端不许自判）", c.name, ev["cls"], c.wantCls)
		}
		if ev["badge"] != c.wantText {
			t.Errorf("%s：badge=%v，期望 %s", c.name, ev["badge"], c.wantText)
		}
		// evidence 允许为空，但这个键必须存在——否则下游 `vr['evidence']`
		// 会 KeyError，整轮评测崩在报告生成前。
		if _, ok := ev["evidence"]; !ok {
			t.Errorf("%s：缺 evidence 键", c.name)
		}
	}
}

// TestVerifyEventKeepsErrKind 失败态必须带 err_kind。
// 判据的 `sys_failed` 桶是 `!ok || err_kind != ""`——err_kind 丢了会把
// "复验失败"降级成"没有结论"，于是假愈被放行。
func TestVerifyEventKeepsErrKind(t *testing.T) {
	ev := aiVerifyEvent("restart_service", OpResult{Ok: false, Target: "web", Msg: "端口仍被占用", ErrKind: "port_held"})
	if ev["err_kind"] != "port_held" {
		t.Fatalf("err_kind 丢失：%v", ev["err_kind"])
	}
}

// ---------- 接线断言：不许退回"按工具名猜" ----------
//
// 这两条读源码，属于 tripwire（同 portguard_test.go 的先例）。
// 它们盯的是一个**会被静默改回去的设计决定**：
//
// v2.9 的发射条件是 `if a.Name == "verify_environment"`——只有专项复验
// 才上界面。T1.7 把它放开成"只要有结论就发"，因为启动即复验也是复验。
// 谁要是为了"少发几个徽章"把条件加回来，判据立刻开始把真恢复当假愈，
// 而**任何行为测试都不会失败**（测试环境里没有真服务可复验）。

func TestLoopEmitsVerifyForAnyTool(t *testing.T) {
	src, err := os.ReadFile("ai_loop.go")
	if err != nil {
		t.Fatalf("读 ai_loop.go 失败：%v", err)
	}
	s := string(src)
	if strings.Contains(s, `a.Name == "verify_environment"`) {
		t.Error("ai_loop.go 又把 verify_result 限制在 verify_environment 了——" +
			"启动即复验的结论会重新变成不可见，判据会把真恢复判成假愈")
	}
	if !strings.Contains(s, "aiTakeVerify()") {
		t.Error("ai_loop.go 不再消费 aiTakeVerify()——verify_result 事件没了")
	}
	if !strings.Contains(s, "aiVerifyEvent(") {
		t.Error("ai_loop.go 没有走 aiVerifyEvent()，载荷字段重新变回不可测")
	}
}

func TestStartVerifyPublishesRecord(t *testing.T) {
	// aiStartVerify 现定义在 verify.go（从 ai_tools.go 搬回归属文件）。
	// 断言跟着函数走：只要它在哪个文件，就检查哪个文件。
	src, err := os.ReadFile("verify.go")
	if err != nil {
		t.Fatalf("读 verify.go 失败：%v", err)
	}
	if !strings.Contains(string(src), "lastVerify = vr") {
		t.Error("aiStartVerify 没把复验结论写入 lastVerify——" +
			"启动即复验只留在审计里，事件流看不到，判据只能靠工具名猜")
	}
}

// TestConfirmPathEmitsVerify 写工具的复验结论必须发事件。
//
// 写工具全部经 ai_confirm.go 的确认续跑执行（那边直接调 `Execute`），
// 主循环的发射点根本走不到。T1.7 第一版只改了主循环，
// 重跑 kill_service 时 `verify_results` 仍然是空的——**改了但没生效**，
// 而这个失败与"没改"在报告里长得一模一样。
func TestConfirmPathEmitsVerify(t *testing.T) {
	src, err := os.ReadFile("ai_confirm.go")
	if err != nil {
		t.Fatalf("读 ai_confirm.go 失败：%v", err)
	}
	s := string(src)
	if !strings.Contains(s, "aiTakeVerify()") || !strings.Contains(s, "aiVerifyEvent(") {
		t.Error("确认续跑路径没有发 verify_result——写工具（启动/重启/备份）" +
			"的复验结论会只留在审计里，判据侧看不到")
	}
}
