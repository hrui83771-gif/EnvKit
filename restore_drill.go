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

// backupStaticCheck 从 dump 文本确认关键内容是否存在。
//
// 返回"发现"与"缺口"两类，**不判失败**——
// "缺触发器"是警告级（可能是项目本来就不用），
// "一张表都没有"才是失败级。判定交给调用方分级。
func backupStaticCheck(path string) (finds []string, gaps []string) {
	head := readHead(path, 4*1024*1024) // 4MB 足够覆盖绝大多数项目的建表段
	if head == "" {
		return nil, []string{"无法读取备份内容（文件可能已损坏或被占用）"}
	}
	low := strings.ToLower(head)

	tables := countSubstr(low, "create table")
	finds = append(finds, fmt.Sprintf("表定义 %d 个", tables))
	if tables == 0 {
		gaps = append(gaps, "未发现任何 CREATE TABLE（空库，或产物根本不是有效 dump）")
	}

	// 触发器：mysqldump 默认包含，但 --skip-triggers 会静默丢掉。
	// 这是"文件完整但内容缺失"最典型的形态，值得单独提醒。
	trig := countSubstr(low, "create trigger") + countSubstr(low, "create definer")
	if trig == 0 {
		gaps = append(gaps,
			"未发现触发器定义：若项目靠触发器维护业务逻辑，还原后它们会缺失"+
				"（备份时若用了 --skip-triggers 就是这种情况）")
	} else {
		finds = append(finds, fmt.Sprintf("触发器 %d 个", trig))
	}

	if v := countSubstr(low, "create view"); v > 0 {
		finds = append(finds, fmt.Sprintf("视图 %d 个", v))
	} else {
		gaps = append(gaps, "未见视图定义（若项目用视图，还原后会缺失）")
	}
	if p := countSubstr(low, "create procedure") + countSubstr(low, "create function"); p > 0 {
		finds = append(finds, fmt.Sprintf("存储过程/函数 %d 个", p))
	} else {
		gaps = append(gaps, "未见存储过程/函数（若项目用到，还原后会缺失）")
	}

	if strings.Contains(low, "set names") || strings.Contains(low, "charset=") {
		finds = append(finds, "含字符集声明")
	} else {
		gaps = append(gaps, "未见字符集声明（还原后中文可能乱码，建议备份时带 --default-character-set）")
	}

	return finds, gaps
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
	finds, gaps := backupStaticCheck(r.Artifact)
	ev := append([]string{}, r.Evidence...)
	for _, f := range finds {
		ev = append(ev, "static:"+f)
	}

	// 无缺口：仍然算复验通过，但要如实说明"只验了完整性，没验可还原性"
	if len(gaps) == 0 {
		ev = append(ev, "hint=静态检查未发现缺失项；可点「恢复演练」实测能否真的还原")
		return opVerified("verify_backup", cfg.Projects.DBName,
			"备份已复验通过：产物非空、校验和一致、内容完整、关键对象齐全"+
				"（未做真实还原演练）", ev...).withArtifact(r.Artifact)
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
