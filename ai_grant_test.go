package main

// ai_grant_test.go —— 批量授权的边界（v3.2）
//
// 这块一旦判错，就等于"用户点了一次头，系统替他点了很多次"，
// 所以把每条边界都写成断言：范围、次数、有效期、工具族。

import (
	"testing"
	"time"
)

func TestGrantFamilyOnlyServiceControl(t *testing.T) {
	for _, tool := range []string{"start_service", "stop_service", "restart_service"} {
		if aiGrantFamily(tool) != "service" {
			t.Fatalf("%s 应当属于可批量的 service 族", tool)
		}
	}
	// 数据类 / 系统类 / 探索类一律不可批量：每一步都值得单独看一眼
	for _, tool := range []string{"db_backup", "db_restore", "apply_sql", "kill_process",
		"apply_whitelist", "manage_memories", "read_file", "db_query", "unknown"} {
		if aiGrantFamily(tool) != "" {
			t.Fatalf("%s 不得进入可批量族", tool)
		}
	}
}

func TestGrantAllowsThenExhausts(t *testing.T) {
	aiGrantReset()
	if aiGrantAllows("start_service") {
		t.Fatal("未开启时不得放行")
	}
	left, ok := aiGrantOpen("start_service")
	if !ok || left != aiGrantMaxUses {
		t.Fatalf("开启后应剩 %d 次，实际 ok=%v left=%d", aiGrantMaxUses, ok, left)
	}
	if !aiGrantAllows("restart_service") {
		t.Fatal("同族工具应当共用授权")
	}
	if aiGrantAllows("db_backup") {
		t.Fatal("异族工具绝不能被批量授权放行")
	}
	// 用两次就该耗尽
	for i := 0; i < aiGrantMaxUses; i++ {
		if !aiGrantAllows("start_service") {
			t.Fatalf("第 %d 次仍应放行", i+1)
		}
		if n := aiGrantTake("start_service"); n != aiGrantMaxUses-i-1 {
			t.Fatalf("第 %d 次消耗后剩余应为 %d，实际 %d", i+1, aiGrantMaxUses-i-1, n)
		}
	}
	if aiGrantAllows("start_service") {
		t.Fatal("次数用尽后必须重新逐次确认")
	}
}

func TestGrantExpires(t *testing.T) {
	aiGrantReset()
	aiGrantOpen("start_service")
	aiGrantMu.Lock()
	aiGrant.until = time.Now().Add(-time.Second)
	aiGrantMu.Unlock()
	if aiGrantAllows("start_service") {
		t.Fatal("过期后必须重新确认")
	}
}

func TestGrantResetClears(t *testing.T) {
	aiGrantOpen("start_service")
	aiGrantReset()
	if aiGrantAllows("start_service") {
		t.Fatal("aiGrantReset 之后必须回到逐次确认（授权绝不跨请求）")
	}
}

func TestGrantOpenRejectsUnbatchable(t *testing.T) {
	aiGrantReset()
	if _, ok := aiGrantOpen("db_backup"); ok {
		t.Fatal("不可批量的工具不得开启授权")
	}
	if aiGrantAllows("db_backup") {
		t.Fatal("开启失败后也不得放行")
	}
}
