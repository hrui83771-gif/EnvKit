package main

// 意图识别回归测试：用例全部来自真实翻车记录（用户说"启动前后端/起服务/启动/好"，
// 助手却反复复述状态、要求"给出明确指令"，始终不发起工具调用）。

import "testing"

func TestAIMatchIntent_StartPhrases(t *testing.T) {
	// (输入, 期望工具, 期望service参数)
	cases := []struct {
		in      string
		tool    string
		service string
	}{
		{"启动前后端", "start_service", "all"},
		{"起服务", "start_service", "all"},
		{"启动", "start_service", "all"},
		{"启动吧", "start_service", "all"},
		{"启动一下", "start_service", "all"},
		{"跑起来", "start_service", "all"},
		{"把服务起来", "start_service", "all"},
		{"全部启动", "start_service", "all"},
		{"前后端跑起来", "start_service", "all"},
		{"拉起整个项目", "start_service", "all"},
		{"启动一下前端", "start_service", "web"},
		{"启动前端", "start_service", "web"},
		{"启动后端", "start_service", "backend"},
		{"起后端", "start_service", "backend"},
	}
	for _, c := range cases {
		tool, args, isWrite := aiMatchIntent(c.in)
		if tool != c.tool {
			t.Errorf("aiMatchIntent(%q) tool = %q, want %q", c.in, tool, c.tool)
			continue
		}
		if !isWrite {
			t.Errorf("aiMatchIntent(%q) isWrite = false, want true", c.in)
		}
		if svc, _ := args["service"].(string); svc != c.service {
			t.Errorf("aiMatchIntent(%q) service = %q, want %q", c.in, svc, c.service)
		}
	}
}

func TestAIMatchIntent_OtherPhrases(t *testing.T) {
	if tool, _, _ := aiMatchIntent("停止前后端"); tool != "stop_service" {
		t.Errorf("停止前后端 → %q, want stop_service", tool)
	}
	if tool, _, _ := aiMatchIntent("停掉前端"); tool != "stop_service" {
		t.Errorf("停掉前端 → %q, want stop_service", tool)
	}
	if tool, _, _ := aiMatchIntent("备份数据库"); tool != "db_backup" {
		t.Errorf("备份数据库 → %q, want db_backup", tool)
	}
	if tool, _, _ := aiMatchIntent("重新检测组件"); tool != "run_detection" {
		t.Errorf("重新检测组件 → %q, want run_detection", tool)
	}
	if tool, _, _ := aiMatchIntent("检测数据库连接"); tool != "db_check" {
		t.Errorf("检测数据库连接 → %q, want db_check", tool)
	}
	if tool, args, _ := aiMatchIntent("看看启动日志"); tool != "get_logs" {
		t.Errorf("看看启动日志 → %q, want get_logs", tool)
	} else if args["scope"] != "start" {
		t.Errorf("看看启动日志 scope = %v, want start", args["scope"])
	}
	if tool, _, _ := aiMatchIntent("生成诊断报告"); tool != "get_diag_report" {
		t.Errorf("生成诊断报告 → %q, want get_diag_report", tool)
	}
	if tool, _, _ := aiMatchIntent("加入白名单"); tool != "apply_whitelist" {
		t.Errorf("加入白名单 → %q, want apply_whitelist", tool)
	}
}

func TestAIMatchIntent_Negative(t *testing.T) {
	// 普通问答不得被误判为操作指令
	for _, s := range []string{"你好", "什么是 DID", "这个项目是做什么的", "MySQL 8 有什么新特性", "hello"} {
		if tool, _, _ := aiMatchIntent(s); tool != "" {
			t.Errorf("aiMatchIntent(%q) = %q, want 空（普通问答不应触发操作）", s, tool)
		}
	}
}

func TestAIBareAffirm(t *testing.T) {
	for _, s := range []string{"好", "好的", "嗯", "可以", "确认", "行吧", "OK", "开始吧", "好。", "麻烦了",
		"需要", "需要呀", "需要吧", "要", "要吧", "当然", "当然可以", "走起", "就这么办"} {
		if !aiBareAffirm(s) {
			t.Errorf("aiBareAffirm(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"好痒", "不太好", "启动前后端", "可以停了吗", "确认一下备份在哪", "不用", "不用了", "不要了", "需要吗", "需要一下"} {
		if aiBareAffirm(s) {
			t.Errorf("aiBareAffirm(%q) = true, want false", s)
		}
	}
}

func TestAIPromisedAction(t *testing.T) {
	yes := []string{
		"只要你给出一句明确指令，我立即发起对应工具调用",
		"如需使用，点名启动即可 —— 一句「起服务」我就按先后端再前端的方式一次发起启动（需你点「确认执行」才真正运行）",
		"直接说一声我就执行",
	}
	for _, s := range yes {
		if !aiPromisedAction(s) {
			t.Errorf("aiPromisedAction(%q) = false, want true", s)
		}
	}
	no := []string{
		"已执行：启动前后端服务，后端 PID 1234，前端 http://localhost:8080",
		"备份完成，文件在 backups/farm-20260918.sql",
	}
	for _, s := range no {
		if aiPromisedAction(s) {
			t.Errorf("aiPromisedAction(%q) = true, want false（正常结果汇报不应被判定为承诺式空转）", s)
		}
	}
}

func TestAIMatchIntent_RestartPhrases(t *testing.T) {
	cases := []struct {
		in      string
		service string
	}{
		{"重启前后端", "all"},
		{"重启", "all"},
		{"重启服务", "all"},
		{"重新启动服务", "all"},
		{"重新启动", "all"},
		{"整个项目重启一下", "all"},
		{"重启前端", "web"},
		{"重启一下后端", "backend"},
		{"前端重启", "web"},
	}
	for _, c := range cases {
		tool, args, isWrite := aiMatchIntent(c.in)
		if tool != "restart_service" {
			t.Errorf("aiMatchIntent(%q) tool = %q, want restart_service", c.in, tool)
			continue
		}
		if !isWrite {
			t.Errorf("aiMatchIntent(%q) isWrite = false, want true", c.in)
		}
		if svc, _ := args["service"].(string); svc != c.service {
			t.Errorf("aiMatchIntent(%q) service = %q, want %q", c.in, svc, c.service)
		}
	}
	// "重启"不得被启动/停止语义抢先误判
	if tool, args, _ := aiMatchIntent("重新启动服务"); tool == "start_service" {
		t.Errorf("重新启动服务 被误判为 start_service(args=%v)", args)
	}
}

func TestAIBacktrackIntent(t *testing.T) {
	// 场景1（真实翻车）：用户说"启动"，助手承诺"给我一句指令我立即发起"，用户回"好"
	msgs1 := []aiMsg{
		{Role: "user", Content: "启动"},
		{Role: "assistant", Content: "需要把服务跑起来，回一句\"启动\"我就直接拉起（先后端再前端，一次确认完成整套启动）"},
		{Role: "user", Content: "好"},
	}
	if tool, args, isWrite := aiBacktrackIntent("好", msgs1); tool != "start_service" || args["service"] != "all" || !isWrite {
		t.Errorf("场景1 backtrack = (%q, %v, %v), want (start_service, all, true)", tool, args, isWrite)
	}

	// 场景2：助手主动提议"需要我帮你备份数据库吗？"，用户回"需要"
	msgs2 := []aiMsg{
		{Role: "user", Content: "数据库好像有点问题"},
		{Role: "assistant", Content: "数据库连接正常。需要我帮你备份数据库吗？"},
		{Role: "user", Content: "需要"},
	}
	if tool, _, isWrite := aiBacktrackIntent("需要", msgs2); tool != "db_backup" || !isWrite {
		t.Errorf("场景2 backtrack = (%q, %v), want (db_backup, true)", tool, isWrite)
	}

	// 场景3：用户拒绝提议，不得触发任何工具
	msgs3 := []aiMsg{
		{Role: "assistant", Content: "需要我帮你备份数据库吗？"},
		{Role: "user", Content: "不用了"},
	}
	if tool, _, _ := aiBacktrackIntent("不用了", msgs3); tool != "" {
		t.Errorf("场景3 backtrack = %q, want 空（拒绝不应触发操作）", tool)
	}

	// 场景4：普通问答 + 裸肯定但无上下文动作 → 不触发
	msgs4 := []aiMsg{
		{Role: "user", Content: "MySQL 8 有什么新特性"},
		{Role: "assistant", Content: "MySQL 8 引入了窗口函数、CTE 等特性。"},
		{Role: "user", Content: "好"},
	}
	if tool, _, _ := aiBacktrackIntent("好", msgs4); tool != "" {
		t.Errorf("场景4 backtrack = %q, want 空（无动作上下文不应触发）", tool)
	}
}

func TestAILooksLikeMenu(t *testing.T) {
	// 误杀回归：合法的编号总结、单一动作的提议问句，都不得判为菜单
	notMenu := []string{
		"1. Go 1.25 已安装 2. Node v22 已安装 3. MySQL 8.0 正常", // 纯编号总结
		"1. 后端已停止 2. 前端运行中 3. 数据库连接正常",                   // 单动作主题的编号总结
		"需要我帮你备份数据库吗？",                                   // 单一动作提议（用户回"需要"的上下文来源）
		"检测完成，组件齐全。是否需要重启服务？",                            // 单一动作提议
		"备份已完成，文件在 backups/farm.sql",
	}
	for _, s := range notMenu {
		if aiLooksLikeMenu(s) {
			t.Errorf("aiLooksLikeMenu(%q) = true, want false（合法回复被误杀）", s)
		}
	}
	// 真菜单：多动作主题罗列、要求用户选择
	isMenu := []string{
		"1. 启动服务 2. 备份数据库 3. 查看日志 4. 生成诊断报告", // 多动作编号菜单
		"你可以让我：启动服务、备份数据库、查看日志。需要哪个？",        // 多动作 + 反问
		"需要我帮你启动服务、备份数据库，还是查看日志？",            // 多动作问句不享受提议豁免
		"你好，我是 EnvKit 运维助手，直接说需求即可",          // 自我介绍式空转
	}
	for _, s := range isMenu {
		if !aiLooksLikeMenu(s) {
			t.Errorf("aiLooksLikeMenu(%q) = false, want true（菜单未拦截）", s)
		}
	}
}
