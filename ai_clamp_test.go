package main

// ai_clamp_test.go —— 工具结果截断与"菜单式回复"重试判据（v3.2）
//
// 这两件事都在改模型看到的东西，所以判据必须能用单测钉住：
//   · 截断只能发生在**可重查**的工具上，且必须把"怎么继续查"一起给出去；
//   · 重试只能在"意图路由证明这一句该动手"时触发，否则会把正常反问重答一遍。

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClampTruncatesRequeryableTools(t *testing.T) {
	long := strings.Repeat("端口 8080 被 node.exe 占用；", 200) // 远超 1200 字节
	for _, tool := range []string{"get_logs", "db_query", "search_files", "list_project", "get_audit_log", "get_diag_report"} {
		got := aiClampToolResult(tool, long)
		if len(got) >= len(long) {
			t.Fatalf("%s 应当被截断，实际长度 %d（原始 %d）", tool, len(got), len(long))
		}
		// 截断必须**告诉模型怎么继续查**，否则它只能凭半截结果猜
		for _, must := range []string{"已截断", "get_logs", "db_query", "LIMIT"} {
			if !strings.Contains(got, must) {
				t.Fatalf("%s 的截断提示缺少 %q：%s", tool, must, got)
			}
		}
		// 不能切出半个字符（JSON 里会变成 U+FFFD）
		if !utf8.ValidString(got) {
			t.Fatalf("%s 截断后不是合法 UTF-8", tool)
		}
	}
}

func TestClampLeavesCapabilityToolsAlone(t *testing.T) {
	// read_file 的契约就是"把文件读给模型"；判据类工具截断会让模型对着半截证据下结论
	long := strings.Repeat("a", 5000)
	for _, tool := range []string{"read_file", "get_system_state", "verify_environment", "db_backup", "unknown_tool"} {
		if got := aiClampToolResult(tool, long); got != long {
			t.Fatalf("%s 不应被截断，实际长度 %d", tool, len(got))
		}
	}
}

func TestClampKeepsShortResultVerbatim(t *testing.T) {
	short := "端口 8080 LISTENING"
	if got := aiClampToolResult("get_logs", short); got != short {
		t.Fatalf("短结果应原样返回，实际 %q", got)
	}
}

// 分档上限：诊断报告（结论型材料）留得比目录线索多。
func TestClampUsesPerToolBudgets(t *testing.T) {
	body := strings.Repeat("x", 2500)
	if got := aiClampToolResult("get_diag_report", body); got != body {
		t.Fatalf("诊断报告 3000 字节上限下 2500 字节应原样保留，实际 %d", len(got))
	}
	if got := aiClampToolResult("list_project", body); len(got) >= len(body) {
		t.Fatalf("目录线索 1000 字节上限应截断 2500 字节，实际 %d", len(got))
	}
}

func TestMenuRetryOnlyWhenActionIsKnown(t *testing.T) {
	menu := "我可以帮你：1. 启动服务 2. 备份数据库 3. 查看日志，请问需要哪个？"
	msgs := []aiMsg{{Role: "user", Content: "帮我备份数据库"}}
	if !menuRetryWorth("帮我备份数据库", msgs) {
		t.Fatal("用户明确要求备份时，模型反问就是该动手没动手，应当重试")
	}
	// 纯提问：意图路由解析不出动作 → 模型反问是合理的，不许重试
	if menuRetryWorth("这套系统是怎么部署的？", msgs) {
		t.Fatal("用户在问问题而非下指令时，不应触发菜单重试（否则会把正常回答重答一遍）")
	}
	// 空发言：不重试
	if menuRetryWorth("   ", msgs) {
		t.Fatal("空输入不应触发重试")
	}
	_ = menu // 保留变量以便阅读判据上下文
}

func TestFirstLinesRuneSafe(t *testing.T) {
	s := strings.Repeat("汉", 10) // 每字 3 字节
	got := firstLines(s, 4)      // 4 落在第一个字的续字节上
	if !utf8.ValidString(got) {
		t.Fatalf("firstLines 切出了半个字符：%q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("截断后应当有省略号：%q", got)
	}
}
