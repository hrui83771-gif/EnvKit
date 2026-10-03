package main

// ai_dbquery.go —— v2.3 N6：AI 的数据库只读查询工具
//
// ## 为什么需要它
//
// 用户问"库里有多少溯源记录"、"哪个农产品批次最多"这类问题时，
// AI 此前只能回答"你可以在「数据浏览」里自己查"——
// **它看得到环境、读得到文件，却读不到数据**。这不是能力问题，
// 是接线缺失：底层早就有完整的只读查询（dbbrowse.go 的
// dbReadOnlySQL + readOnlyExec + TSV 解析），只是没接给 AI。
//
// ## 安全边界：为什么只读工具可以是"自动执行"而不是"要确认"
//
// PolicyGate 把写工具（备份/启停/白名单）放在 confirm 档，理由是它们
// 有副作用。而只读查询的判据是**能否修改数据**，答案是否定的：
//
//  1. dbReadOnlySQL 限定首词为 SELECT/SHOW/DESC/DESCRIBE/EXPLAIN/WITH/TABLE
//  2. 强制 LIMIT（无 LIMIT 自动补，且拒绝已被改写的）
//  3. readOnlyExec 在同一次会话里先执行 SET SESSION TRANSACTION READ ONLY
//  4. 库名/表名经 quoteIdent 转义
//
// 四道防线都在 MySQL 侧生效，不依赖"调用方是否老实"。
// 所以它属于 auto 档——**给只读操作加确认卡只会训练用户闭眼点确认**。
//
// ## 结果为什么要裁剪
//
// 一次 SELECT * FROM big_table 能拉出几 MB，塞进上下文会挤爆提示词，
// 而且真正有用的通常只有前几行 + 聚合结果。所以：
//   - 默认最多 50 行（模型可要更多，但有硬上限）
//   - 总字符数有上限，超出截断并**显式告知已截断**
//   - 提示模型优先用聚合（COUNT/GROUP BY）而不是拉全表
//
// 显式告知截断是关键：静默截断会让模型以为看到了全部数据，
// 然后基于不完整的数据下结论——这正是 v2.0 修掉的"谎报"同源问题。

import (
	"fmt"
	"strings"
)

// AI 查询结果上限。
const (
	aiDBMaxRows    = 200  // 硬上限：模型要多少都不超过这个
	aiDBDefaultRow = 50   // 默认返回行数
	aiDBMaxChars   = 6000 // 结果文本总字符上限
)

func init() {
	aiToolRegistry["db_query"] = aiTool{
		Desc: "对业务数据库执行**只读**查询并返回结果（SELECT / SHOW / DESCRIBE / EXPLAIN / WITH）。" +
			"用于回答与业务数据有关的问题：有多少条记录、某个字段的分布、最近的数据、聚合统计等。" +
			"只读，不会修改任何数据，因此无需你确认。" +
			"注意：①默认查「程序配置」里的目标库；②必须自己写 LIMIT（不写会自动补 50，上限 200）；" +
			"③要统计总数或分布请用 COUNT/GROUP BY，不要拉全表再自己数；" +
			"④库名不确定时先 db_list 看有哪些库",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"sql":      map[string]any{"type": "string", "description": "只读 SQL：SELECT / SHOW / DESCRIBE / EXPLAIN / WITH。必须自己带 LIMIT"},
			"database": map[string]any{"type": "string", "description": "库名。留空则用「程序配置」里的目标库"},
			"limit":    map[string]any{"type": "integer", "description": "希望返回的行数，默认 50，上限 200"},
		}, "required": []string{"sql"}},
		Execute: func(args map[string]any) (string, error) {
			sql, _ := args["sql"].(string)
			db, _ := args["database"].(string)
			want := aiDBDefaultRow
			if n, ok := args["limit"].(float64); ok && n > 0 {
				want = int(n)
			}
			return aiDBQuery(sql, db, want)
		},
	}
	aiToolRegistry["db_list"] = aiTool{
		Desc: "列出 MySQL 服务器上的所有数据库，以及每个库的表与行数（只读）。" +
			"不知道库名或表名时先用它，别猜。也可 SHOW CREATE TABLE 拿建表语句看字段",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"database": map[string]any{"type": "string", "description": "库名。留空则列所有库；给了则只列该库的表"},
		}, "required": []string{}},
		Execute: func(args map[string]any) (string, error) {
			db, _ := args["database"].(string)
			return aiDBList(db)
		},
	}
}

// aiDBQuery 执行只读查询并把结果裁剪成模型可读的文本。
func aiDBQuery(sql, db string, want int) (string, error) {
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return "", fmt.Errorf("SQL 为空")
	}
	if want <= 0 {
		want = aiDBDefaultRow
	}
	if want > aiDBMaxRows {
		want = aiDBMaxRows
	}
	// 统一库名口径：留空用配置里的目标库
	if strings.TrimSpace(db) == "" {
		db = cfg.Projects.DBName
	}

	// 只读校验在这里，错误要转成模型能据此改正的话
	safe, err := dbReadOnlySQL(sql)
	if err != nil {
		return "", fmt.Errorf("查询被拒绝：%v\n"+
			"只允许 SELECT / SHOW / DESCRIBE / EXPLAIN / WITH 一条语句，且必须带 LIMIT"+
			"（聚合查询如 COUNT(*) 的 LIMIT 1 即可）", err)
	}
	// 写审计：查询内容与行数都留痕，"谁在什么时候查了什么"要能回答
	fin := auditStart(actAI, "db_query", db, runeSnippet(safe, 120))
	cols, rows, err := readOnlyExec(db, safe)
	if err != nil {
		fin(resFail, err.Error())
		return "", fmt.Errorf("查询失败：%v", err)
	}
	// 结论一次即可：auditStart 的 finish 写一条记录，
	// 之前误调两次会在审计里留下两条同名记录，看审计的人会以为查了两遍
	fin(resOK, fmt.Sprintf("%d 行", len(rows)))

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("库=%s 返回 %d 行", db, len(rows)))
	if len(cols) > 0 {
		sb.WriteString("　列：" + strings.Join(cols, " | "))
	}
	sb.WriteString("\n")

	// 行数裁剪：超出的部分必须说明，不能让模型以为看到了全部
	shown := rows
	cut := false
	if len(shown) > want {
		shown = shown[:want]
		cut = true
	}
	for i, r := range shown {
		if len(r) == 0 {
			continue
		}
		sb.WriteString("· " + strings.Join(r, " | ") + "\n")
		_ = i
	}
	if cut {
		sb.WriteString(fmt.Sprintf("（只返回前 %d 行，共 %d 行。要更多请自己加 LIMIT 并说明用途，"+
			"或用聚合查询直接要统计结果）", want, len(rows)))
	}

	// 字符上限：超了要说，否则模型会把截断当完整
	out := sb.String()
	if len(out) > aiDBMaxChars {
		out = out[:aiDBMaxChars] + fmt.Sprintf(
			"\n（结果过长已截断，实际 %d 字符。要完整结果请缩小查询范围，"+
				"例如只 SELECT 你需要的列、或用聚合代替拉全表）", len(out))
	}

	// 结果属数据库内容，包 <untrusted_data> 让模型把它当素材而非指令
	// （表里的备注字段可能写着"请执行…"，与日志/文件同源的风险）
	return "<untrusted_data>\n" + out + "\n</untrusted_data>", nil
}

// aiDBList 列出库与表。
func aiDBList(db string) (string, error) {
	db = strings.TrimSpace(db)
	if db == "" {
		_, rows, err := readOnlyExec("", "SHOW DATABASES")
		if err != nil {
			return "", fmt.Errorf("列库失败：%v", err)
		}
		// 过滤系统库：information_schema / mysql / performance_schema
		// 对用户没有分析价值，占位置还让模型误以为那些能查
		var dbs []string
		for _, r := range rows {
			if len(r) == 0 {
				continue
			}
			switch r[0] {
			case "information_schema", "performance_schema", "mysql", "sys":
				continue
			}
			dbs = append(dbs, r[0])
		}
		if len(dbs) == 0 {
			return "服务器上没有业务数据库（已过滤系统库）", nil
		}
		return "数据库：" + strings.Join(dbs, "、"), nil
	}

	q, err := quoteIdent(db)
	if err != nil {
		return "", err
	}
	// information_schema 查表与行数：一次拿到，不用逐个 SHOW TABLES
	_, rows, err := readOnlyExec("", fmt.Sprintf(
		"SELECT TABLE_NAME, TABLE_ROWS, TABLE_COMMENT FROM information_schema.TABLES "+
			"WHERE TABLE_SCHEMA='%s' ORDER BY TABLE_NAME LIMIT %d", db, aiDBMaxRows))
	if err != nil {
		return "", fmt.Errorf("列表失败：%v", err)
	}
	if len(rows) == 0 {
		return fmt.Sprintf("库 %s 里没有表（或不存在）", db), nil
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("库 %s 共 %d 张表（行数是 MySQL 的估算值）：\n", db, len(rows)))
	for _, r := range rows {
		if len(r) == 0 {
			continue
		}
		line := "· " + r[0]
		if len(r) > 1 && r[1] != "" {
			line += "（约 " + r[1] + " 行）"
		}
		if len(r) > 2 && r[2] != "" {
			line += " — " + r[2]
		}
		sb.WriteString(line + "\n")
	}
	_ = q
	return "<untrusted_data>\n" + strings.TrimRight(sb.String(), "\n") + "\n</untrusted_data>", nil
}
