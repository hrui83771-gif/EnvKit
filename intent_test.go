package main

import "testing"

// ---------- 意图识别（预检快通道的地基：它错一次，用户就会看到一次"没反应"） ----------

func TestAiMatchIntent(t *testing.T) {
	cases := []struct {
		in      string
		tool    string
		isWrite bool
	}{
		{"查看日志", "get_logs", false},
		{"看看系统日志", "get_logs", false},
		{"启动后端", "start_service", true},
		{"把前端跑起来", "start_service", true},
		{"起服务", "start_service", true},
		{"停止前端", "stop_service", true},
		{"关掉后端服务", "stop_service", true},
		{"检查数据库连接", "db_check", false},
		{"备份一下数据库", "db_backup", true},
		{"清理后台进程", "cleanup_processes", true},
		{"生成诊断报告", "get_diag_report", false},
		{"加入白名单", "apply_whitelist", true},
		{"环境状态怎么样", "get_system_state", false},
		{"你好", "", false},
		{"今天天气不错", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		tool, _, isWrite := aiMatchIntent(c.in)
		if tool != c.tool || isWrite != c.isWrite {
			t.Errorf("aiMatchIntent(%q)=(%q,%v)，期望 (%q,%v)", c.in, tool, isWrite, c.tool, c.isWrite)
		}
	}
}

func TestAiMatchIntentLogScope(t *testing.T) {
	cases := []struct{ in, scope string }{
		{"查看日志", "all"},
		{"看看系统日志", "sys"},
		{"安装日志", "install"},
		{"链端日志", "chain"},
	}
	for _, c := range cases {
		_, args, _ := aiMatchIntent(c.in)
		if got, _ := args["scope"].(string); got != c.scope {
			t.Errorf("aiMatchIntent(%q) scope=%q，期望 %q", c.in, got, c.scope)
		}
	}
}

// 一次"起服务"必须只发起一次确认（确认两次会被用户当成"反复弹窗"）
func TestAiMatchIntentStartServiceIsSingleCall(t *testing.T) {
	for _, in := range []string{"起服务", "启动服务", "把服务起来", "一键启动"} {
		tool, args, _ := aiMatchIntent(in)
		if tool != "start_service" || args["service"] != "all" {
			t.Errorf("aiMatchIntent(%q)=(%q,%v)，期望 start_service{service:all}", in, tool, args)
		}
	}
}

// ---------- 菜单式无效回复识别 ----------

func TestAiLooksLikeMenu(t *testing.T) {
	menu := []string{
		"需要我帮你做什么？",
		"我是 EnvKit 助手，你可以让我：\n1. 查看日志\n2. 启动服务\n3. 检查数据库",
		"1. 查看日志\n2. 启动服务\n3. 检查数据库",
		"请告诉我你想做什么",
		"直接说需求即可",
	}
	for _, m := range menu {
		if !aiLooksLikeMenu(m) {
			t.Errorf("应判定为菜单式回复：%q", m)
		}
	}
	good := []string{
		"",
		"数据库连接正常，MySQL 8.0.40，farm 库已存在。",
		"后端已启动（PID 1234），前端正在编译，请稍候。",
		"日志里最后一条是 21:43 的「清理项目遗留进程 3 个」。",
		"1. 后端未启动\n2. 前端未启动", // 只有 2 项，不算菜单
	}
	for _, g := range good {
		if aiLooksLikeMenu(g) {
			t.Errorf("不应判定为菜单式回复：%q", g)
		}
	}
}

func TestAiLooksLikeMenuPrefix(t *testing.T) {
	if !aiLooksLikeMenuPrefix("你好，我是 EnvKit 运维助手") {
		t.Error("开场白式回复应被识别")
	}
	if aiLooksLikeMenuPrefix("MySQL 8.0.40 连接成功") {
		t.Error("正常结论不应被识别为开场白")
	}
}

// ---------- 序号反查 ----------

func TestAiResolveNumberInput(t *testing.T) {
	msgs := []aiMsg{
		{Role: "user", Content: "怎么办"},
		{Role: "assistant", Content: "1. 查看日志\n2. 启动服务\n3. 检查数据库"},
	}
	cases := []struct{ in, want string }{
		{"2", "启动服务"},
		{"3", "检查数据库"},
		{"1", "查看日志"},
		{"4", ""},
		{"abc", ""},
	}
	for _, c := range cases {
		if got := aiResolveNumberInput(c.in, msgs); got != c.want {
			t.Errorf("aiResolveNumberInput(%q)=%q，期望 %q", c.in, got, c.want)
		}
	}
	// 没有助手消息时不应误判
	if got := aiResolveNumberInput("2", []aiMsg{{Role: "user", Content: "2"}}); got != "" {
		t.Errorf("无助手消息时应返回空，实际 %q", got)
	}
}

// ---------- 越权拦截（模型不得主动发起写/敏感操作） ----------

func TestAiIsOptInTool(t *testing.T) {
	for _, tool := range []string{"apply_whitelist", "db_backup"} {
		if !aiIsOptInTool(tool) {
			t.Errorf("%s 应属于「需用户明确要求」的工具", tool)
		}
	}
	// stop_service 不再 opt-in（v1.9.7）：它是写工具，走确认卡片即可；
	// opt-in 关键词匹配不到用户的口语说法（"停一下""停了"）会整轮拦死并引发模型重试刷屏
	if aiIsOptInTool("stop_service") {
		t.Error("stop_service 不应 opt-in（写工具已有确认卡片兜底）")
	}
	for _, tool := range []string{"get_logs", "get_system_state", "db_check", "run_detection", "start_service"} {
		if aiIsOptInTool(tool) {
			t.Errorf("%s 不应被拦截", tool)
		}
	}
}

func TestAiUserAskedFor(t *testing.T) {
	if !aiUserAskedFor("db_backup", []aiMsg{{Role: "user", Content: "帮我把数据库备份一下"}}) {
		t.Error("用户提到备份时应放行")
	}
	if aiUserAskedFor("db_backup", []aiMsg{{Role: "user", Content: "查看日志"}}) {
		t.Error("用户没提备份时应拦截")
	}
	// 只看最近 3 条用户消息：更早的诉求不再算数
	msgs := []aiMsg{
		{Role: "user", Content: "备份数据库"},
		{Role: "assistant", Content: "好的"},
		{Role: "user", Content: "1"},
		{Role: "assistant", Content: "2"},
		{Role: "user", Content: "3"},
		{Role: "assistant", Content: "4"},
		{Role: "user", Content: "5"},
	}
	if aiUserAskedFor("db_backup", msgs) {
		t.Error("超出最近 3 条用户消息后不应再放行")
	}
	// 不在名单里的工具默认放行
	if !aiUserAskedFor("get_logs", nil) {
		t.Error("非敏感工具应默认放行")
	}
}

// ---------- 参数自适应 ----------

func TestAiFixParamError(t *testing.T) {
	q, changed, hint := aiFixParamError("invalid temperature: only 1 is allowed for this model")
	if !changed || q.TempMode != "one" || hint == "" {
		t.Errorf("temperature 被拒应降级为 1：q=%+v changed=%v hint=%q", q, changed, hint)
	}

	q, changed, _ = aiFixParamError("max_tokens is not supported for this model")
	if !changed || q.MaxTokensField != "max_completion_tokens" {
		t.Errorf("max_tokens 被拒应改用 max_completion_tokens：%+v", q)
	}

	q, changed, _ = aiFixParamError("Tools are not supported by this model")
	if !changed || !q.NoTools {
		t.Errorf("模型不支持工具调用应降级：%+v", q)
	}

	if _, changed, _ := aiFixParamError("invalid api key"); changed {
		t.Error("无关错误不应触发参数降级")
	}
}

// ---------- 工具中文名 ----------

func TestAiToolCnName(t *testing.T) {
	cases := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"start_service", map[string]any{"service": "all"}, "启动前后端服务（先后端、再前端）"},
		{"stop_service", map[string]any{"service": "backend"}, "停止后端服务"},
		{"db_check", map[string]any{}, "检测数据库连接"},
		{"unknown_tool", map[string]any{}, "unknown_tool"},
	}
	for _, c := range cases {
		if got := aiToolCnName(c.tool, c.args); got != c.want {
			t.Errorf("aiToolCnName(%q)=%q，期望 %q", c.tool, got, c.want)
		}
	}
}
