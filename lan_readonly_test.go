package main

// lan_readonly_test.go —— 「手机端只读」这条承诺的执行层断言
//
// ## 为什么这组断言不可省
//
// 只读有两道闸：
//   1. **清单闸**：写工具不发给模型（aiToolDefsReadOnly）
//   2. **执行闸**：真要执行时再拦一次（aiExecTool）
//
// 第 1 道挡的是"模型看不到"，第 2 道挡的是"模型调得动"。
// 只做第 1 道是不够的 —— 模型可能凭记忆报出一个写工具名，
// 意图兜底那条路更是根本不过模型（关键词匹配），第 1 道对它完全无效。
//
// **曾经真的漏过**：写工具有四个执行点，当时只有一处加了判断。
// 漏掉的三处分别是"意图兜底""模型 tool_call""用户点确认后执行"——
// 手机页不显示确认卡，恰好堵死其中一条，另外两条畅通。
//
// 所以这组测试的判据不是"某处有判断"，而是：
// **任何一个写工具，在只读上下文里执行，都必须被 aiExecTool 拦下。**

import (
	"os"
	"strings"
	"testing"
)

// lanReadOnlyToolList 当前注册表里的写工具。
//
// 动态注册的工具（manage_memories / sync_chain_ip）在各自的 init 里加进来，
// 这个测试跑的时候它们已经在表里了（见 memory.go / chainip.go 的 init）。
func lanReadOnlyWriteTools(t *testing.T) []string {
	t.Helper()
	var out []string
	for name, tool := range aiToolRegistry {
		if tool.Write {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		t.Fatal("注册表里一个写工具都没有 —— 这组测试失去意义")
	}
	return out
}

func TestLanExecToolBlocksWriteToolsInReadOnly(t *testing.T) {
	aiReadOnly.Store(true)
	defer aiReadOnly.Store(false)

	for _, name := range lanReadOnlyWriteTools(t) {
		tool := aiToolRegistry[name]
		// 用一个假的 Execute 计数：证明"根本没被调用"，而不是"调用了但返回了错误"。
		// 这两者的区别很关键 —— 靠返回值断言的话，
		// 哪天有人把 Execute 改成"执行完再报错"，测试还会是绿的。
		called := false
		orig := tool.Execute
		tool.Execute = func(map[string]any) (string, error) {
			called = true
			return "", nil
		}
		aiToolRegistry[name] = tool

		out, err := aiExecTool(name, map[string]any{})

		// 还原
		tool.Execute = orig
		aiToolRegistry[name] = tool

		if called {
			t.Errorf("写工具「%s」在只读上下文里被真正执行了 —— 只读被绕过", name)
		}
		if err == nil {
			t.Errorf("写工具「%s」在只读上下文里应当报错", name)
			continue
		}
		if !strings.Contains(err.Error(), "只读") {
			t.Errorf("写工具「%s」的错误文案应说明是只读限制，实际：%v", name, err)
		}
		if out != "" {
			t.Errorf("写工具「%s」被拒时不应有输出，实际：%q", name, out)
		}
	}
}

func TestLanExecToolAllowsReadToolsInReadOnly(t *testing.T) {
	aiReadOnly.Store(true)
	defer aiReadOnly.Store(false)

	// 只读上下文里，只读工具必须照常能跑 —— 否则手机端的 AI 什么也查不了，
	// "只读"就变成了"什么都不许做"。
	ran := false
	tool := aiToolRegistry["get_system_state"]
	orig := tool.Execute
	tool.Execute = func(map[string]any) (string, error) {
		ran = true
		return "ok", nil
	}
	aiToolRegistry["get_system_state"] = tool
	defer func() {
		tool.Execute = orig
		aiToolRegistry["get_system_state"] = tool
	}()

	out, err := aiExecTool("get_system_state", map[string]any{})
	if err != nil {
		t.Fatalf("只读工具在只读上下文里不应报错：%v", err)
	}
	if !ran {
		t.Fatal("只读工具在只读上下文里没有被执行")
	}
	if out != "ok" {
		t.Fatalf("只读工具输出不对：%q", out)
	}
}

func TestLanExecToolBlocksWriteToolsInNormalContext(t *testing.T) {
	// 反向断言：正常上下文（桌面端）里，写工具必须照常能执行。
	// 没有这条，上面那条测试可能是"因为闸坏了所以全都失败"。
	aiReadOnly.Store(false)
	defer aiReadOnly.Store(false)

	ran := false
	tool := aiToolRegistry["start_service"]
	orig := tool.Execute
	tool.Execute = func(map[string]any) (string, error) {
		ran = true
		return "ok", nil
	}
	aiToolRegistry["start_service"] = tool
	defer func() {
		tool.Execute = orig
		aiToolRegistry["start_service"] = tool
	}()

	if _, err := aiExecTool("start_service", map[string]any{"service": "web"}); err != nil {
		t.Fatalf("桌面端不应被只读闸拦住：%v", err)
	}
	if !ran {
		t.Fatal("桌面端写工具没有被执行 —— 只读闸误伤了正常路径")
	}
}

func TestLanExecToolRejectsUnknownTool(t *testing.T) {
	aiReadOnly.Store(false)
	defer aiReadOnly.Store(false)
	out, err := aiExecTool("no_such_tool_xyz", map[string]any{})
	if err == nil {
		t.Fatal("未注册的工具必须报错")
	}
	if out != "" {
		t.Fatalf("未注册工具不应有输出：%q", out)
	}
}

// TestLanWriteToolExecSitesAllGuarded 源码契约：
// **所有**写工具的执行都必须经过 aiExecTool，不得直接调 registry[...].Execute。
//
// 这条是"以后新增执行路径"的安全网。写法是读源码文本 ——
// 看起来很土，但比任何运行时断言都可靠：运行时断言只能覆盖已知的调用点，
// 而这条能抓住"新加了一处直接 Execute"。
func TestLanWriteToolExecSitesAllGuarded(t *testing.T) {
	// 这些文件里不允许再出现 registry[...].Execute 的直调。
	// ai_confirm.go / ai_loop.go 之外的测试文件用 registry[...].Execute 是合理的
	// （单测直接调工具），所以只查生产代码。
	files := []string{"ai_loop.go", "ai_confirm.go", "ai_tools.go"}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读 %s 失败：%v", f, err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(line, "aiToolRegistry[") {
				continue
			}
			if !strings.Contains(line, ".Execute(") {
				continue
			}
			// 允许注释里提到（文档说明），只查真正的代码行
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			t.Errorf("%s 里出现了绕过 aiExecTool 的直接执行：%s", f, trimmed)
		}
	}
}

// TestLanWriteToolNeverEntersConfirmFlow 手机端写工具**不能进确认流程**。
//
// ## 这条为什么重要
//
// 写工具在主循环里走的是 `case def.Write && verdict.NeedsConfirm()`：
// 发一张确认卡，然后**就地等用户点按**（最长 10 分钟）。
//
// 手机页面上没有确认按钮 —— 于是模型一旦报了写工具（哪怕是被诱导的），
// 这一轮就会挂到超时。用户看到的是"手机端对话转了十分钟不出结果"。
//
// 更糟的是：手机端那条路**永远等不到确认**，所以执行层的闸根本轮不到执行。
// 只在 aiExecTool 里拦不够 —— 必须在进确认流程**之前**就拒掉。
//
// 判据是源码结构：只读分支必须排在写工具分支**之前**。
func TestLanWriteToolNeverEntersConfirmFlow(t *testing.T) {
	b, err := os.ReadFile("ai_loop.go")
	if err != nil {
		t.Fatalf("读不到 ai_loop.go：%v", err)
	}
	src := string(b)

	// 模型 tool_call 的 switch：只读分支
	roIdx := strings.Index(src, "case def.Write && aiReadOnlyCur():")
	if roIdx < 0 {
		t.Fatal("ai_loop.go 里没有写工具的只读分支：手机端模型报写工具时会走确认流程并挂死")
	}
	// 确认流程分支
	cfIdx := strings.Index(src, "case def.Write && verdict.NeedsConfirm():")
	if cfIdx < 0 {
		t.Fatal("ai_loop.go 里找不到写工具的确认分支 —— 结构变了，测试要重写")
	}
	if roIdx > cfIdx {
		t.Error("只读分支排在确认分支之后：手机端写工具会先进确认流程（挂到 10 分钟超时），" +
			"执行层的闸轮不到执行")
	}

	// 意图兜底路径：写操作也必须先判只读，不能直接进确认
	if !strings.Contains(src, "if isWrite && aiReadOnlyCur() {") {
		t.Error("意图兜底路径没有只读判定：用户在手机上打「重启后端」会走关键词匹配进确认流程并挂死")
	}
}
