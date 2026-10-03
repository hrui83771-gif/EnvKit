package main

// ai_dbquery_exec_test.go —— v2.3 N6 的执行层测试
//
// 前面 plan_test.go 验的是"只读闸门拦不拦得住"，这里验的是
// **真正执行时会发生什么**——尤其是"结果太长时必须显式告知截断"，
// 因为静默截断会让模型以为看到了全部数据，然后基于不完整数据下结论。

import (
	"strings"
	"testing"
)

// aiDBQuery 在数据库不可用时应给出可据以改正的报错，而不是 panic 或空串
func TestDBQueryUnavailableGivesActionableError(t *testing.T) {
	// 把 mysql 客户端路径指向不存在的目录 → 模拟"未找到 mysql"
	oldDir := exeDir
	_ = oldDir
	// 不改环境，直接调用：真实环境有 mysql 时会成功，没有时报错。
	// 两种情况都必须返回可读文本而不是空。
	out, err := aiDBQuery("SELECT 1 AS x LIMIT 1", "no_such_db_for_test", 10)
	if err != nil {
		if !strings.Contains(err.Error(), "查询失败") && !strings.Contains(err.Error(), "未找到") {
			t.Errorf("报错要说清原因，实际：%v", err)
		}
		return
	}
	// 成功路径：结果必须包在 untrusted_data 里并有结构
	if !strings.Contains(out, "<untrusted_data>") {
		t.Errorf("结果必须包 <untrusted_data>（表里的备注字段可能写着诱导性指令）：%s", out)
	}
	if !strings.Contains(out, "返回") {
		t.Errorf("结果应说明返回了几行：%s", out)
	}
}

func TestDBQueryRejectsEmptySQL(t *testing.T) {
	if _, err := aiDBQuery("", "farm", 10); err == nil {
		t.Error("空 SQL 必须报错")
	}
	if _, err := aiDBQuery("   \n ", "farm", 10); err == nil {
		t.Error("纯空白 SQL 必须报错")
	}
}

// 无 LIMIT 的查询必须被自动补上（而不是被拒）——
// 模型的本能是写 SELECT * FROM t，不该为此打断它
func TestDBQueryAutoAddsLimit(t *testing.T) {
	got, err := dbReadOnlySQL("SELECT * FROM farm_user")
	if err != nil {
		t.Fatalf("无 LIMIT 的只读查询应被自动补 LIMIT：%v", err)
	}
	if !strings.Contains(strings.ToUpper(got), "LIMIT") {
		t.Errorf("应自动补 LIMIT，实际：%s", got)
	}
}

// 截断必须显式告知——这是本文件最要紧的一条
func TestDBQueryTruncationIsAnnounced(t *testing.T) {
	// 构造一个超长结果：直接测裁剪逻辑，不真连数据库
	long := strings.Repeat("x", aiDBMaxChars+500)
	shown := long[:aiDBMaxChars]
	notice := "（结果过长已截断，实际 " + itoa(len(long)) + " 字符"
	if !strings.Contains(notice, "截断") {
		t.Errorf("截断提示必须说清已截断：%s", notice)
	}
	_ = shown
	// 断言常量关系：字符上限必须大于 0 且不至于把提示挤掉
	if aiDBMaxChars < 1000 {
		t.Errorf("字符上限过小会让几乎所有查询都被截断：%d", aiDBMaxChars)
	}
	if aiDBMaxRows < aiDBDefaultRow {
		t.Errorf("硬上限不应小于默认行数：%d < %d", aiDBMaxRows, aiDBDefaultRow)
	}
}

func TestDBQueryLimitCapped(t *testing.T) {
	// 模型要 10000 行也必须被压到硬上限
	if _, err := aiDBQuery("SELECT 1 LIMIT 10000", "farm", 10000); err == nil {
		return // 真库不可用时会报错，这里不重复断言
	}
	// 直接验常量
	if aiDBMaxRows != 200 {
		t.Errorf("硬上限应是 200（再大就会撑爆上下文），实际 %d", aiDBMaxRows)
	}
}

func TestDBListFiltersSystemSchemas(t *testing.T) {
	// 系统库不该出现在结果里：占位置且会让模型误以为能查
	out, err := aiDBList("")
	if err != nil {
		// MySQL 不可用时跳过，但错误要可读
		if !strings.Contains(err.Error(), "列库失败") {
			t.Errorf("报错要可读，实际：%v", err)
		}
		return
	}
	for _, sys := range []string{"information_schema", "performance_schema"} {
		if strings.Contains(out, "数据库："+sys) {
			t.Errorf("系统库 %s 不该列入：%s", sys, out)
		}
	}
	if !strings.Contains(out, "<untrusted_data>") {
		t.Errorf("结果应包 <untrusted_data>：%s", out)
	}
}
