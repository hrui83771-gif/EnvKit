package main

// explore_read_test.go —— read_file 的分页语义。
//
// ## 这里守的是什么
//
// 旧实现是 io.LimitReader(前 N KB) 再在这段文本里切行，于是 offset 只能落在
// "前 N KB 之内"。一份 5MB 的日志，第 10000 行**永远读不到**，而返回里还写着
// "当前显示第 X-Y 行"，看起来像是能翻页。用户看到的就是"选个大文件只读了个开头"。
//
// 所以这些用例的核心断言不是"返回了多少"，而是**offset 能不能越过预算继续往后读**，
// 以及连续翻页能不能不漏不重地覆盖全文（后者能同时抓住"重复返回同一段"这种死循环）。
//
// ## 为什么抽纯函数测
//
// 构造"刚好跨过分页边界"的真实文件要写几百 KB 到磁盘，还要保证行宽与预算精确对上；
// 而 aiReadLines 只需要一个 io.Reader，strings.NewReader 就能把边界造得干净。

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// numberedLines 造 n 行等宽文本，每行以行号开头，便于断言"读到第几行"。
func numberedLines(n, width int) (string, []string) {
	lines := make([]string, n)
	for i := range lines {
		tag := fmt.Sprintf("L%04d", i+1)
		pad := width - len(tag)
		if pad < 0 {
			pad = 0
		}
		lines[i] = tag + strings.Repeat("-", pad)
	}
	return strings.Join(lines, "\n"), lines
}

// 核心回归：offset 落在"单次预算之外"时，必须仍然读得到。
func TestReadLinesOffsetReachesBeyondBudget(t *testing.T) {
	text, want := numberedLines(400, 100) // 约 40KB
	const budget = 2000                   // 只够二十来行

	if _, first := aiReadLines(strings.NewReader(text), 1, 1000, budget); first.Next <= 0 {
		t.Fatalf("第一段应还有后续，实际 Next=%d", first.Next)
	}
	target := 300 // 远在 2000 字节之外
	body, st := aiReadLines(strings.NewReader(text), target, 5, budget)
	if st.First != target {
		t.Fatalf("offset=%d 没生效（First=%d）—— offset 被单次预算卡住了", target, st.First)
	}
	if !strings.Contains(body, want[target-1]) {
		t.Fatalf("没读到第 %d 行：%q", target, body)
	}
}

// 连续翻页必须不漏不重地覆盖全文。
// 这条同时抓两类错：Next 指回已读位置（重复 → 拼起来比原文长）、
// 或者跳过一段（漏行 → 比原文短）。
//
// lines 故意传得远大于实际需要，让**预算**成为分页的唯一原因：
// 行数先到时走的是另一条分支，而真正容易错的是"字节用完了该从哪接着读"。
func TestReadLinesWalkCoversWholeFile(t *testing.T) {
	text, want := numberedLines(180, 60)
	const budget = 1000
	var got []string
	offset := 1
	for i := 0; ; i++ {
		if i > 500 {
			t.Fatal("翻页没有终止：Next 不收敛（模型照着提示会陷入死循环）")
		}
		body, st := aiReadLines(strings.NewReader(text), offset, 1000, budget)
		if st.First == 0 {
			t.Fatalf("offset=%d 意外没有内容（Total=%d Next=%d）", offset, st.Total, st.Next)
		}
		got = append(got, strings.Split(body, "\n")...)
		if st.Next == 0 {
			if st.Total != len(want) {
				t.Fatalf("到末尾时总行数应为 %d，实际 %d", len(want), st.Total)
			}
			break
		}
		offset = st.Next
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("连续翻页拼起来与原文不一致：拿到 %d 行，期望 %d 行", len(got), len(want))
	}
}

// 行数触发的那条分支也要不漏不重 —— 它与预算分支是两条独立路径，
// 只测其中一条会漏掉另一条的 Next 算错。
func TestReadLinesWalkByLineLimit(t *testing.T) {
	text, want := numberedLines(50, 10)
	var got []string
	offset := 1
	for i := 0; ; i++ {
		if i > 100 {
			t.Fatal("翻页没有终止")
		}
		body, st := aiReadLines(strings.NewReader(text), offset, 6, 1<<20)
		got = append(got, strings.Split(body, "\n")...)
		if st.Next == 0 {
			break
		}
		offset = st.Next
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("按行数翻页拼起来与原文不一致：拿到 %d 行，期望 %d 行", len(got), len(want))
	}
}

func TestReadLinesOffsetBeyondEOF(t *testing.T) {
	text, _ := numberedLines(10, 20)
	body, st := aiReadLines(strings.NewReader(text), 99, 10, 4096)
	if body != "" || st.First != 0 {
		t.Fatalf("越过末尾应返回空，实际 body=%q First=%d", body, st.First)
	}
	if st.Total != 10 || st.Next != 0 {
		t.Fatalf("Total/Next 不对：%+v", st)
	}
}

// 单行超预算（minified 文件）：给前 N 字节，且**必须跳到下一行**，
// 否则模型按提示 offset 重读同一行，会一直拿到一模一样的开头。
func TestReadLinesLongLineTruncatedAndSkipped(t *testing.T) {
	text := strings.Repeat("x", 5000) + "\nnext-line"
	body, st := aiReadLines(strings.NewReader(text), 1, 10, 500)
	if len(body) != 500 {
		t.Fatalf("单行应按预算截断到 500 字节，实际 %d", len(body))
	}
	if st.LongLine != 1 {
		t.Fatalf("应标记第 1 行被截断，实际 LongLine=%d", st.LongLine)
	}
	if st.Next != 2 {
		t.Fatalf("截断后应从第 2 行继续（否则会死循环在同一行），实际 Next=%d", st.Next)
	}
	body2, st2 := aiReadLines(strings.NewReader(text), st.Next, 10, 500)
	if !strings.Contains(body2, "next-line") {
		t.Fatalf("没能读到下一行：%q", body2)
	}
	if st2.Next != 0 || st2.Total != 2 {
		t.Fatalf("第 2 行就是末尾，实际 %+v", st2)
	}
}

// 参数传 0/负数不能崩，也不能返回空。
func TestReadLinesToleratesZeroArgs(t *testing.T) {
	text, _ := numberedLines(3, 10)
	body, st := aiReadLines(strings.NewReader(text), 0, 0, 0)
	if body == "" {
		t.Fatal("参数兜底后仍应返回内容")
	}
	if st.First != 1 {
		t.Fatalf("offset<1 应视为 1，实际 First=%d", st.First)
	}
}

// 空文件：不是错误，是"什么都没有"。
func TestReadLinesEmptyFile(t *testing.T) {
	body, st := aiReadLines(strings.NewReader(""), 1, 10, 4096)
	if body != "" || st.First != 0 || st.Total != 0 || st.Err != "" {
		t.Fatalf("空文件应干净返回空：body=%q %+v", body, st)
	}
}

// 端到端：真文件 + 真 exploreRead，offset 必须能翻过默认 64KB 预算。
// 这条是最贴近用户抱怨的那一句"选个大文件只读了个开头"。
func TestReadFilePagesBeyondDefaultBudget(t *testing.T) {
	root := withProjectRoot(t)
	text, want := numberedLines(3000, 100) // 约 300KB，默认预算 64KB ≈ 前 650 行
	writeF(t, filepath.Join(root, "big.log"), text)

	out := exploreRead(map[string]any{"path": "big.log", "offset": 2500, "lines": 5})
	if !strings.Contains(out, want[2499]) {
		t.Fatalf("第 2500 行落在 64KB 之外就读不到 —— 大文件只能看开头。输出：%s", out)
	}
	if !strings.Contains(out, "offset=2505") {
		t.Fatalf("应告诉模型下一段从哪开始：%s", out)
	}
	if strings.Contains(out, "仅载入前") {
		t.Fatalf("不该再出现\"仅载入前 N KB\"这种读不下去的说法：%s", out)
	}
}

// 读到末尾时要说清楚"已到文件末尾"，否则模型会一直问下一段。
func TestReadFileSaysWhenAtEOF(t *testing.T) {
	root := withProjectRoot(t)
	writeF(t, filepath.Join(root, "small.txt"), "a\nb\nc\n")
	out := exploreRead(map[string]any{"path": "small.txt"})
	if !strings.Contains(out, "已到文件末尾") {
		t.Fatalf("末尾未说明：%s", out)
	}
	// 旧实现用 strings.Split 数行，文件末尾的换行会多算出一个空行（3 行报成 4 行）
	if !strings.Contains(out, "共 3 行") {
		t.Fatalf("总行数不对（末尾换行不该多算一行）：%s", out)
	}
}
