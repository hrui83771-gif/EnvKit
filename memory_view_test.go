package main

// memory_view_test.go —— 记忆层 v1（"写了为什么不生效"）的回归。
//
// 这一版修的三件事都在这里钉住：
//  1. 作用域失效必须**可见**（旧实现只在内存里标一下，API 从不返回）；
//  2. always 超过上限时保留**最新**的，落选的要有原因（旧实现升序截断，丢新的）；
//  3. 条数到顶要报错，不能静默截掉刚写入的那条。

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

func writeMemFile(t *testing.T, ms []Memory) {
	t.Helper()
	var sb strings.Builder
	for _, m := range ms {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("序列化失败：%v", err)
		}
		sb.Write(b)
		sb.WriteString("\n")
	}
	if err := os.WriteFile(memoryFile(), []byte(sb.String()), 0600); err != nil {
		t.Fatalf("写记忆文件失败：%v", err)
	}
}

func TestMemoriesViewStaleIsVisible(t *testing.T) {
	defer withMemoryEnv(t)()
	writeMemFile(t, []Memory{{
		ID: "m1", Text: "上一个项目的规矩", ScopeFP: "another-project",
		Created: "2026-10-01 10:00:00", Source: "manual",
	}})

	vs := memoriesView("随便什么任务", 12)
	if len(vs) != 1 {
		t.Fatalf("应有 1 条视图，实际 %d", len(vs))
	}
	if !vs[0].Stale || vs[0].StaleWhy == "" {
		t.Fatalf("作用域不匹配必须标 stale 并给理由，实际 %+v", vs[0])
	}
	if vs[0].Injected {
		t.Fatal("失效的记忆不能被注入")
	}
	if !strings.Contains(vs[0].Reason, "已失效") {
		t.Fatalf("失效原因要能直接显示给用户，实际 %q", vs[0].Reason)
	}
	if got := memoriesFor("", 12); len(got) != 0 {
		t.Fatalf("失效记忆不该进注入列表，实际 %d 条", len(got))
	}
}

func TestMemoriesViewCapKeepsNewest(t *testing.T) {
	defer withMemoryEnv(t)()
	writeMemFile(t, []Memory{
		{ID: "a", Text: "老规矩", Always: true, Created: "2026-10-01 10:00:00"},
		{ID: "b", Text: "中间规矩", Always: true, Created: "2026-10-02 10:00:00"},
		{ID: "c", Text: "新规矩", Always: true, Created: "2026-10-03 10:00:00"},
	})

	vs := memoriesView("", 2)
	got := map[string]MemoryView{}
	for _, v := range vs {
		got[v.ID] = v
	}
	if !got["c"].Injected || !got["b"].Injected {
		t.Fatalf("超过上限时应保留最新的两条，实际 %+v", vs)
	}
	if got["a"].Injected {
		t.Fatal("最老的一条应落选")
	}
	if !strings.Contains(got["a"].Reason, "上限") {
		t.Fatalf("落选必须给出上限原因，实际 %q", got["a"].Reason)
	}
}

func TestMemoriesViewIrrelevantHasReason(t *testing.T) {
	defer withMemoryEnv(t)()
	writeMemFile(t, []Memory{{
		ID: "m1", Text: "部署前先备份数据库", Created: "2026-10-01 10:00:00",
	}})

	vs := memoriesView("帮我启动前端", 12)
	if len(vs) != 1 || vs[0].Injected {
		t.Fatalf("不相关的非 always 记忆不该注入：%+v", vs)
	}
	if !strings.Contains(vs[0].Reason, "不相关") {
		t.Fatalf("没注入要说明原因，实际 %q", vs[0].Reason)
	}

	// 相关性达到阈值时应注入（正文命中两个以上关键词）
	vs2 := memoriesView("帮我做一次数据库备份", 12)
	if len(vs2) != 1 || !vs2[0].Injected {
		t.Fatalf("相关任务应注入，实际 %+v", vs2)
	}
}

func TestMemoriesSaveRejectsOverflow(t *testing.T) {
	defer withMemoryEnv(t)()
	ms := make([]Memory, 0, memMaxCount)
	for i := 0; i < memMaxCount; i++ {
		ms = append(ms, Memory{ID: fmt.Sprintf("m%d", i), Text: "规矩" + strconv.Itoa(i), Created: "2026-10-01 10:00:00"})
	}
	writeMemFile(t, ms)

	_, isNew, err := memUpsert("第 101 条规矩", "", false, "manual")
	if err == nil {
		t.Fatal("超过上限必须报错，不能静默丢弃")
	}
	if !strings.Contains(err.Error(), "上限") {
		t.Fatalf("错误信息要说清是上限问题，实际 %v", err)
	}
	if isNew {
		t.Fatal("失败时不能报告成新增成功")
	}
	if n := len(loadMemories()); n != memMaxCount {
		t.Fatalf("失败后文件里应仍是 %d 条，实际 %d", memMaxCount, n)
	}
}
