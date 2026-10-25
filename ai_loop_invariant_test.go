package main

// ai_loop_invariant_test.go —— 主循环里"只能有一个 append 点"的结构性断言。
//
// 背景（真实故障，v3.2 修）：写工具的确认分支一开始自己 append 了 tool_call，
// 而循环末尾的公共尾巴又 append 了一次 —— 同一条 assistant 消息里出现两个相同的
// tool_call_id，上游直接 400：
//
//	Duplicate value for 'tool_call_id' of call_00_xxx in message[5]
//
// 这类错误编译期看不出来、单测也覆盖不到主循环（需要真上游），所以用**源码断言**
// 钉住这条不变量：写工具分支负责"发起等待"，append 只由公共尾巴做一次。

import (
	"os"
	"strings"
	"testing"
)

func readLoopSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("ai_loop.go")
	if err != nil {
		t.Fatalf("读 ai_loop.go 失败：%v", err)
	}
	return string(b)
}

// writeBranch 切出 `case def.Write:` 到下一个 `default:` 之间的源码。
func writeBranch(t *testing.T) string {
	src := readLoopSource(t)
	// 只锚定 "case def.Write"，不锚定后半句：v3.2 把它改成了
	// `case def.Write && verdict.NeedsConfirm():`（批量授权时降档为 auto 会跳过这张卡），
	// 锚太死会让这条不变量在每次改判定条件时失效。
	i := strings.Index(src, "case def.Write")
	if i < 0 {
		t.Fatal("找不到 case def.Write（改名了？这条不变量需要跟着搬家）")
	}
	rest := src[i:]
	j := strings.Index(rest, "\n\t\t\tdefault:")
	if j < 0 {
		t.Fatal("找不到 case def.Write 之后的 default:")
	}
	return rest[:j]
}

// 批量授权靠"把写入分支的触发条件加上 NeedsConfirm"来实现：
// 降档为 auto 的写工具会落到 default 直接执行，而不是弹一张没人会看的卡。
func TestWriteCaseRequiresConfirm(t *testing.T) {
	src := readLoopSource(t)
	if !strings.Contains(src, "case def.Write && verdict.NeedsConfirm():") {
		t.Fatal("写工具分支必须以 verdict.NeedsConfirm() 为前提，否则批量授权降档后仍会弹卡")
	}
}

func TestWriteBranchDoesNotAppendToolCallItself(t *testing.T) {
	branch := writeBranch(t)
	if strings.Contains(branch, "msgs[assistantIdx].ToolCalls = append") {
		t.Fatal("写工具分支不得自己 append tool_call —— 公共尾巴会再 append 一次，" +
			"造成同一条 assistant 消息里 tool_call_id 重复（上游 400）")
	}
	if strings.Contains(branch, "toolMsgs = append") {
		t.Fatal("写工具分支不得自己 append tool 结果 —— 公共尾巴会用 toolMsg.Content 补一次，" +
			"否则同一条回复会出现两个结果（其中一个是空的）")
	}
	// 结果必须经过截断再交给尾巴，避免日志/SQL 整页灌进上下文
	if !strings.Contains(branch, "aiClampToolResult") {
		t.Fatal("写工具分支的执行结果应当经 aiClampToolResult 截断后再交给公共尾巴")
	}
	// 新语义：就地等待，而不是发完卡就 return
	if !strings.Contains(branch, "aiAwaitConfirm") {
		t.Fatal("写工具分支应当调用 aiAwaitConfirm 就地等待决定（v3.2）")
	}
	// 等不到时必须退回旧路径：发 done(awaiting_confirm) 并结束，而不是继续跑
	if !strings.Contains(branch, "sseDoneConfirm") {
		t.Fatal("等不到用户决定时必须退回旧路径（sseDoneConfirm + return）")
	}
}

func TestCommonTailAppendsExactlyOnce(t *testing.T) {
	src := readLoopSource(t)
	if n := strings.Count(src, "msgs[assistantIdx].ToolCalls = append"); n != 1 {
		t.Fatalf("tool_call 的 append 点必须只有 1 处，实际 %d 处", n)
	}
	// tool 结果允许有两处，但**只有公共尾巴那一处可以落回循环底部**：
	// 另一处是"同参数反复失败 → 插引导后 continue"，它必须紧跟着 continue，
	// 否则同一轮会既 append 引导又 append 真结果。
	needle := "toolMsgs = append(toolMsgs, toolMsg)"
	idxs := []int{}
	for off := 0; ; {
		i := strings.Index(src[off:], needle)
		if i < 0 {
			break
		}
		idxs = append(idxs, off+i)
		off += i + len(needle)
	}
	if len(idxs) == 0 {
		t.Fatal("找不到 tool 结果的 append 点")
	}
	for k, i := range idxs {
		tail := src[i+len(needle):]
		if len(tail) > 160 {
			tail = tail[:160]
		}
		hasContinue := strings.Contains(tail, "continue")
		if k == len(idxs)-1 {
			if hasContinue {
				t.Fatal("最后一处 append 是公共尾巴，它不该 continue（那会让本轮结果被吞掉）")
			}
			continue
		}
		if !hasContinue {
			t.Fatalf("第 %d 处 append 既不是尾巴也没有 continue，会导致同一轮 append 两次", k+1)
		}
	}
}
