package main

// ai_audit_test.go —— AI 读审计工具（get_audit_log）的单测
//
// 背景：AI 助手原先只能看 EnvKit 自己的运行日志（get_logs），
// 而「操作审计」页的数据（auditQuery / auditDays）只服务于页面接口——
// 模型被问「守护自动恢复过吗」「最近谁做了什么」时无从查起。
// get_audit_log 补上这条路。这里钉住五件事：
//  1. 注册且只读（读审计不该进确认闸门）；
//  2. 输出一条一行、新在前，词汇与页面一致（result 用原始值 + 图例）；
//  3. day / actor 的别名折算——模型给「昨天」「守护」不能筛出空结果，
//     更不能把「查不到」误会成「没发生过」；
//  4. 坏输入给能改正的错误，而不是静默按「全部」返回；
//  5. 条数限量（默认 20、上限 100）真的生效。

import (
	"strings"
	"testing"
	"time"
)

func TestAuditToolRegistered(t *testing.T) {
	tool, ok := aiToolRegistry["get_audit_log"]
	if !ok {
		t.Fatal("get_audit_log 未注册")
	}
	if tool.Write {
		t.Fatal("读审计是只读操作，不应进二次确认闸门")
	}
	if !strings.Contains(tool.Desc, "操作审计") {
		t.Errorf("工具描述没说清读的是什么：%s", tool.Desc)
	}
	if tool.Schema == nil {
		t.Error("工具必须有参数 Schema")
	}
	// 注册 ≠ 模型看得见：aiToolDefs 才是真正发给上游的清单
	var inDefs bool
	for _, d := range aiToolDefs() {
		if d.Function.Name == "get_audit_log" {
			inDefs = true
			if d.Function.Description == "" {
				t.Error("发给上游的工具描述不能为空")
			}
		}
	}
	if !inDefs {
		t.Error("get_audit_log 不在 aiToolDefs() 里——模型根本看不到它")
	}
}

// TestAiAuditTextFormat 一条一行、新在前，且带图例与复验结论。
func TestAiAuditTextFormat(t *testing.T) {
	setupAuditFiles(t)
	out, cnt := aiAuditText("", "", 0)
	if cnt != 6 {
		t.Fatalf("应返回全部 6 条，实际 %d", cnt)
	}
	// 新在前：10-03 的 start_service 必须排在 10-01 的 apply_sql 之前
	if strings.Index(out, "start_service") > strings.Index(out, "apply_sql") {
		t.Errorf("顺序错了：最新的记录应排在前面：\n%s", out)
	}
	for _, want := range []string{
		"user=用户手动", "ok=成功", "denied=被拦下", // 图例
		"2026-10-03 11:30:00.000 | ai | start_service | web | ok",
		"已复验通过：共识在推进", // 复验结论要进去
		"2026-10-01 10:00:00.000 | user | apply_sql | farm | ok | 建表",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q：\n%s", want, out)
		}
	}
}

// TestAiAuditDayAlias 日期别名：模型爱写 2026-10-17 / 今天 / 昨天。
func TestAiAuditDayAlias(t *testing.T) {
	today := time.Now().Format("20060102")
	yday := time.Now().AddDate(0, 0, -1).Format("20060102")
	cases := []struct{ in, want string }{
		{"20261003", "20261003"},
		{"2026-10-03", "20261003"},
		{"2026/10/03", "20261003"},
		{"", ""}, // 空 = 跨天
		{"今天", today}, {"today", today},
		{"昨天", yday}, {"yesterday", yday},
	}
	for _, c := range cases {
		got, err := aiAuditDay(c.in)
		if err != nil || got != c.want {
			t.Errorf("aiAuditDay(%q) = %q, %v；期望 %q", c.in, got, err, c.want)
		}
	}
	// 认不出必须报错——静默按「全部」处理会让模型以为查过了
	for _, bad := range []string{"2026-13-01", "上个月", "10 月 3 号"} {
		if _, err := aiAuditDay(bad); err == nil {
			t.Errorf("aiAuditDay(%q) 应报错", bad)
		}
	}
}

// TestAiAuditExecute 走完整 Execute：别名折算 + 不可信定界符 + 坏输入报错。
func TestAiAuditExecute(t *testing.T) {
	setupAuditFiles(t)
	ex := aiToolRegistry["get_audit_log"].Execute

	// 主体别名：guard / 守护 都应筛到 chain_autorecover，且不含用户操作
	for _, in := range []string{"guard", "守护"} {
		out, err := ex(map[string]any{"actor": in, "lines": float64(10)})
		if err != nil {
			t.Fatalf("actor=%q 报错：%v", in, err)
		}
		if !strings.Contains(out, "chain_autorecover") {
			t.Errorf("actor=%q 没筛到守护记录：\n%s", in, out)
		}
		if strings.Contains(out, "apply_sql") {
			t.Errorf("actor=%q 混进了用户操作：\n%s", in, out)
		}
		if !strings.HasPrefix(out, "<untrusted_data>") {
			t.Error("结果应包不可信定界符（详情列可能带第三方输出）")
		}
	}

	// 按天筛选：10-01 只有那两条
	out, err := ex(map[string]any{"day": "2026-10-01", "lines": float64(50)})
	if err != nil {
		t.Fatalf("按天查报错：%v", err)
	}
	if !strings.Contains(out, "apply_sql") || strings.Contains(out, "config_save") {
		t.Errorf("按天筛选结果不对：\n%s", out)
	}

	// 坏输入要给能改正的错误
	if _, err := ex(map[string]any{"actor": "管理员"}); err == nil {
		t.Error("未知主体应报错")
	}
	if _, err := ex(map[string]any{"day": "上个月"}); err == nil {
		t.Error("无法识别的日期应报错")
	}
}

// TestAiAuditLinesLimit 条数限量：给 2 就 2 条，给 500 也不超过硬上限。
func TestAiAuditLinesLimit(t *testing.T) {
	setupAuditFiles(t)
	if _, cnt := aiAuditText("", "", 2); cnt != 2 {
		t.Errorf("要 2 条应返回 2 条，实际 %d", cnt)
	}
	if _, cnt := aiAuditText("", "", 500); cnt != 6 {
		t.Errorf("记录总共 6 条，要 500 也应只返回 6，实际 %d", cnt)
	}
}

// TestAiAuditTextEmpty 一条审计都没有时必须明确说出来，
// 不能返回空字符串让模型以为「查过了，什么都没有」。
func TestAiAuditTextEmpty(t *testing.T) {
	withAuditDir(t, nil)
	out, cnt := aiAuditText("", "", 0)
	if cnt != 0 || !strings.Contains(out, "没有任何审计记录") {
		t.Errorf("空库应给明确说明，实际 cnt=%d out=%q", cnt, out)
	}
}

// TestAiAuditPromptMentionsTool 系统提示词的意图映射里必须有这把工具——
// 不映射的话，用户说「看看审计页面」时模型不知道有工具可用。
func TestAiAuditPromptMentionsTool(t *testing.T) {
	if !strings.Contains(aiSystemPrompt, "get_audit_log") {
		t.Error("系统提示词的意图→工具映射里没有 get_audit_log")
	}
}
