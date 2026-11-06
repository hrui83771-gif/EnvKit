package main

// backup_inventory.go —— 备份静态检查的 ground truth：源库对象清单
//
// ## 修的是什么
//
// 旧判据把「dump 里没找到 CREATE TRIGGER / VIEW / PROCEDURE」一律当缺口，
// 于是源库本来就没有这些对象时，一次完全成功的备份会被判成
// errKindRestoreFail，AI 只能如实转述成 verified=failed。
// 用户实测（blood 库 4 张 BASE TABLE、TRIGGERS 0 行、ROUTINES 0 行）：
// 文件 sha256 一致、末尾 Dump completed 齐全，却连收三个"缺失"。
//
// 「没找到」不等于「丢了」。要判丢，必须先有**独立于被检文件的**事实源：
// 源库当时到底有没有这些对象。这份清单在备份完成时采样并落成 sidecar，
// 复验时只把「源库有、dump 没有」算缺口；源库本身为 0 的不算。
//
// ## 三条边界
//
//  1. **未知 ≠ 0**：查不到源库（不可达 / 没权限 / 查询不完整）时 Verified=false，
//     判据退回"未核实"，既不假装齐全，也不把不知道说成缺失、更不判失败。
//  2. **不放松真缺损**：源库有 N 个、dump 里 0 个，仍然报 restore_fail。
//  3. **静态检查永远不能证明"能还原"**：文案始终注明未做真实演练。
//
// 只读 information_schema，复用既有 readOnlyExec（固定 SQL + quoteStr 转义），
// 不新增任意命令执行；权限闸门不受影响。

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// dbObjectInventory 源库里「本该出现在备份里的」三类对象数量。
// Verified=false 表示没查到，此时 Triggers/Views/Routines 的 0 没有事实含义。
type dbObjectInventory struct {
	Schema   string `json:"schema"`
	Triggers int    `json:"triggers"`
	Views    int    `json:"views"`
	Routines int    `json:"routines"`
	At       string `json:"at"`
	Verified bool   `json:"verified"`
}

func (inv dbObjectInventory) String() string {
	if !inv.Verified {
		return "源库对象清单=未核实"
	}
	return fmt.Sprintf("源库对象清单(%s)：触发器 %d / 视图 %d / 存储过程与函数 %d",
		inv.Schema, inv.Triggers, inv.Views, inv.Routines)
}

func inventoryPath(backup string) string { return backup + ".objects.json" }

// liveObjectInventory 是可替换的现场查询入口：单测换成确定值，不依赖本机 MySQL。
// 换掉的是**外部依赖**，不是被测判据本身。
var liveObjectInventory = dbObjectInventoryOf

// dbObjectInventoryOf 一次查出三类对象数。任一步不完整就返回 Verified=false，
// 绝不把「半截结果」当成「源库里 0 个」。
func dbObjectInventoryOf(db string) dbObjectInventory {
	inv := dbObjectInventory{Schema: db, At: time.Now().Format(time.RFC3339)}
	if db == "" {
		return inv
	}
	qs, err := quoteStr(db)
	if err != nil {
		return inv
	}
	sql := "SELECT 'triggers', COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA=" + qs +
		" UNION ALL SELECT 'views', COUNT(*) FROM information_schema.VIEWS WHERE TABLE_SCHEMA=" + qs +
		" UNION ALL SELECT 'routines', COUNT(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA=" + qs
	_, rows, err := readOnlyExec("", sql)
	if err != nil || len(rows) != 3 {
		return inv
	}
	got := map[string]bool{}
	for _, r := range rows {
		if len(r) < 2 {
			return inv
		}
		n, e := strconv.Atoi(strings.TrimSpace(r[1]))
		if e != nil || n < 0 {
			return inv
		}
		switch strings.TrimSpace(r[0]) {
		case "triggers":
			inv.Triggers, got["triggers"] = n, true
		case "views":
			inv.Views, got["views"] = n, true
		case "routines":
			inv.Routines, got["routines"] = n, true
		default:
			return inv
		}
	}
	if len(got) != 3 {
		return inv
	}
	inv.Verified = true
	return inv
}

// writeObjectInventory 在备份成功时把清单落盘。查不到就不写文件：
// 留一份 Verified=false 的 sidecar 会让后续复验把"未知"误读成"已核实为 0"。
func writeObjectInventory(dest string) {
	inv := liveObjectInventory(cfg.Projects.DBName)
	if !inv.Verified {
		return
	}
	b, err := json.Marshal(inv)
	if err != nil {
		return
	}
	if err := os.WriteFile(inventoryPath(dest), append(b, '\n'), 0644); err != nil {
		warn(scConfig, "备份", "源库对象清单落盘失败（复验将现场核对）：%v", err)
	}
}

func readObjectInventory(path string) (dbObjectInventory, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return dbObjectInventory{}, false
	}
	var inv dbObjectInventory
	if json.Unmarshal(b, &inv) != nil {
		return dbObjectInventory{}, false
	}
	return inv, true
}

// backupBaseline 优先用备份当时采样的清单；老备份没有 sidecar 就现场查一次源库。
// 现场查的是"现在"的库，所以对历史备份只作参考——真要证明能还原仍须演练。
func backupBaseline(path string) dbObjectInventory {
	if inv, ok := readObjectInventory(inventoryPath(path)); ok && inv.Verified {
		return inv
	}
	return liveObjectInventory(cfg.Projects.DBName)
}
