package main

// restore_drill.go —— v2.3 N3：备份可还原性验证
//
// ## 先说清楚：这里补的是已有实现缺的什么
//
// v1.x 就有"恢复演练"（dbDrillTask：备份 → 建临时库 → 导入 → 比对表数 → 删库），
// 它已经做得不错——含看门狗、中途失败的清理、空库场景的如实说明。
// **本项不重写它**，那属于"预防性重构"，只会撞碎已有的测试网。
//
// 真正的缺口是另两件事：
//
// **① 静态检查（零风险、每次备份后自动跑）**
// 演练要花几十秒且要真动数据库，所以不能每次备份都跑。但"文件完整"
// 与"该有的东西在里面"是两件事，而后者只需读文本就能查：
//
//   - mysqldump 带了 --skip-triggers：文件完整、校验和一致、能导进去，
//     但所有触发器都没了 —— 还原后业务逻辑静默失效
//   - 缺字符集声明：还原后中文可能乱码
//   - 视图 / 存储过程 / 函数缺失：这三类最容易在"看起来完整"的备份里悄悄丢失
//
// 演练能查出这些（导进去比对），但只有人主动点才查得到；
// 静态检查让它在每次备份后立刻可见，代价是毫秒级。
//
// **② 归类分开：文件坏了 vs 备份不完整**
// 现有 verifyBackup 的校验和/建表/结尾三项全绿时返回"已复验通过"，
// 但如果 dump 缺了触发器，用户以为"备份没问题"，实际不是。
// 引入 errKindRestoreFail 让"备份产物本身是好的，但内容不完整/不能还原"
// 与"文件损坏"区分开——两者的处置完全不同。

import (
	"fmt"
	"strings"
)

// drillMaxCount 计数上限。只用于统计，设个上限防病态输入。
const drillMaxCount = 5000

// backupStaticCheck 兼容包装：两值返回，供 AI 备份列表与既有调用方使用。
// 要区分「确凿缺失」与「没核对源库」，用 backupStaticCheckEx。
func backupStaticCheck(path string) (finds []string, gaps []string) {
	f, g, _ := backupStaticCheckEx(path)
	return f, g
}

// backupStaticCheckEx 读备份时落盘的源库对象清单，再交给纯判据。
// 没有清单（老备份 / 采样失败）时 unknown=true：文案改为"未核对"，
// 调用方不得把它当成确凿缺失、更不得据此判失败。
func backupStaticCheckEx(path string) (finds []string, gaps []string, unknown bool) {
	inv, ok := readObjectInventory(inventoryPath(path))
	if !ok || !inv.Verified {
		return backupStaticCheckWithBaseline(path, nil)
	}
	return backupStaticCheckWithBaseline(path, &inv)
}

// backupStaticCheckWithBaseline 纯判据：inv 为 nil 或 Verified=false 时按"未核实"处理。
//
// 三类对象只有一条判据入口 checkObj，避免"源库有没有"这个事实在不同分支里
// 被算成不同口径。表定义与字符集不依赖源库清单，判据保持原样。
func backupStaticCheckWithBaseline(path string, inv *dbObjectInventory) (finds []string, gaps []string, unknown bool) {
	unknown = inv == nil || !inv.Verified
	head := readHead(path, 4*1024*1024) // 4MB 足够覆盖绝大多数项目的建表段
	if head == "" {
		return nil, []string{"无法读取备份内容（文件可能已损坏或被占用）"}, unknown
	}
	low := strings.ToLower(head)

	tables := countSubstr(low, "create table")
	finds = append(finds, fmt.Sprintf("表定义 %d 个", tables))
	if tables == 0 {
		gaps = append(gaps, "未发现任何 CREATE TABLE（空库，或产物根本不是有效 dump）")
	}

	var sTrig, sView, sRout int
	if !unknown {
		sTrig, sView, sRout = inv.Triggers, inv.Views, inv.Routines
	}
	checkObj := func(label string, inDump, inSource int, tip string) {
		if inDump > 0 {
			finds = append(finds, fmt.Sprintf("%s %d 个", label, inDump))
			return
		}
		if unknown {
			// "没发现"不等于"丢了"：没有源库清单时只能标未核实，不能下缺失结论。
			gaps = append(gaps, fmt.Sprintf(
				"未核对源库：备份里未见%s定义（不一定是缺失；%s；请核对源库或点「恢复演练」）",
				label, tip))
			return
		}
		if inSource == 0 {
			finds = append(finds, fmt.Sprintf("源库无%s（非缺失）", label))
			return
		}
		gaps = append(gaps, fmt.Sprintf("源库有 %d 个%s，但备份里一个都没有：%s", inSource, label, tip))
	}

	// 触发器：mysqldump 默认包含，但 --skip-triggers 会静默丢掉。
	trig := countSubstr(low, "create trigger") + countSubstr(low, "create definer")
	checkObj("触发器", trig, sTrig, "备份时若用了 --skip-triggers 就是这种情况")
	checkObj("视图", countSubstr(low, "create view"), sView, "若项目用视图，还原后会缺失")
	checkObj("存储过程/函数",
		countSubstr(low, "create procedure")+countSubstr(low, "create function"),
		sRout, "若项目用到，还原后会缺失")

	if strings.Contains(low, "set names") || strings.Contains(low, "charset=") {
		finds = append(finds, "含字符集声明")
	} else {
		gaps = append(gaps, "未见字符集声明（还原后中文可能乱码，建议备份时带 --default-character-set）")
	}

	return finds, gaps, unknown
}

// countSubstr 统计子串出现次数。
func countSubstr(s, sub string) int {
	if sub == "" {
		return 0
	}
	n, idx := 0, 0
	for {
		i := strings.Index(s[idx:], sub)
		if i < 0 {
			return n
		}
		n++
		idx += i + len(sub)
		if n >= drillMaxCount {
			return n
		}
	}
}

// verifyBackupStatic 在现有 verifyBackup 之上补静态检查。
//
// **不改变 verifyBackup 的行为**，只是把静态检查的结论作为额外证据
// 附加进去，并在有缺口时把结论从"已复验通过"降级为"未复验"——
// 因为"文件没坏"已经不等于"备份能用了"。
//
// drillDBStatic 为 true 时额外提示可做真实演练（不自动跑，见下方说明）。
func verifyBackupStatic(path string) OpResult {
	r := verifyBackup(path)
	if !r.Ok || r.Artifact == "" {
		// 基础验证都没过，静态检查没有意义
		return r
	}
	inv := backupBaseline(r.Artifact)
	finds, gaps, unknown := backupStaticCheckWithBaseline(r.Artifact, &inv)
	ev := append([]string{}, r.Evidence...)
	ev = append(ev, "baseline:"+inv.String())
	for _, f := range finds {
		ev = append(ev, "static:"+f)
	}

	// 源库清单没核实：既不能说"齐全"，也不能把"不知有没有"当成"丢了"。
	// 结论落在 Ok=true / Verified=false —— 动作已执行，但没证明能还原。
	if unknown {
		for _, g := range gaps {
			ev = append(ev, "unverified:"+g)
		}
		ev = append(ev, "hint=源库对象清单未核实；请点「恢复演练」实测能否真的还原")
		return opOK("verify_backup", cfg.Projects.DBName,
			"备份已生成且文件校验通过；源库对象清单未核实，静态检查不能据此判定缺失"+
				"，也未证明能还原（未做真实还原演练）", ev...).withArtifact(r.Artifact)
	}

	// 清单已核实且无缺口：仍只证明"文件没坏 + 该有的都在"，不证明能还原。
	if len(gaps) == 0 {
		ev = append(ev, "hint=静态检查按源库清单核对未发现缺失项；可点「恢复演练」实测能否真的还原")
		return opVerified("verify_backup", cfg.Projects.DBName,
			"备份已复验通过：产物非空、校验和一致、内容完整、关键对象与源库清单一致"+
				"（源库本来就没有的不算缺失；未做真实还原演练）", ev...).withArtifact(r.Artifact)
	}

	// 有缺口：降级为"未复验"，并把缺口说清楚。
	// **关键**：必须区分"文件坏了"和"内容缺斤少两"——
	// 前者要重新备份，后者要改备份命令（如去掉 --skip-triggers）。
	for _, g := range gaps {
		ev = append(ev, "gap:"+g)
	}
	ev = append(ev, "hint=可点「恢复演练」实测能否真的还原")
	return opFail("verify_backup", cfg.Projects.DBName, errKindRestoreFail,
		"备份文件本身完整，但内容可能有缺失（这不等于文件损坏）："+strings.Join(gaps, "；"),
		ev...).withArtifact(r.Artifact)
}
