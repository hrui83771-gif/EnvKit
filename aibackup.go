package main

// aibackup.go —— v2.5：备份的只读查询能力
//
// ## 为什么加这个工具
//
// v2.4 的故障恢复率评测抓到一个真实缺陷：
// 用户问「检查一下最近的备份文件，看看能不能用」，
// AI **一次工具都没调**就要执行 mysqldump。
//
// 根因有两层，**只修一层不够**：
//
//  1. `ai_intent.go` 的预检把「含"备份"二字」一律判成创建（已修，见该文件）
//  2. **更根本的是：AI 根本没有"查看现有备份"的工具。**
//     `get_system_state` 不含备份信息，`get_diag_report` 也没有。
//     即使预检路由修对了，模型想查也没东西可查——
//     它只能二选一：要么直接创建，要么什么都不做。
//
// 这就是 v2.0 那条老教训的又一次出现：
// **缺能力时模型会拿最像的动作凑**，而那个动作恰好是有副作用的那个。
//
// ## 工具边界：只读
//
// 本工具**只读不写**：
//   - 只列目录、只算校验和、只读文件头做静态检查
//   - 不创建备份、不删除、不触发演练
// 「该不该重新备份」是用户的决策，不是本工具该替他做的。
// 评测里这一条叫 `Safe Handling`——**正确处理备份问题的前提是不越权**。

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// backupEntry 一份备份的只读摘要。
type backupEntry struct {
	Name string
	// SizeBytes 存字节而非 KB。KB 是展示单位，不是存储单位——
	// 用 KB 存储会让 <1KB 的文件（**截断的备份正是这一类**）
	// 一律显示为 0，模型据此跳过内容检查。
	SizeBytes int64
	AgeMin    int64  // 多少分钟前
	SHAOk     bool   // sha256 复算是否一致（**只有复算过才可能是 true**）
	SHAState  string // 校验和状态：一致 / 不一致 / 缺失 / 未复算（超限）
	Static    string // 静态检查结论摘要
	Truncate  bool   // 文件被截断（尾部标记缺失）
}

// 超过这个大小就不做 sha256 复算。
//
// 不是懒，是**如实说明做不到**比假装做到好：
// 几百 MB 的 dump 复算要几百毫秒，list_backups 一次列 5 份，
// 每次提问都烧一遍不划算。所以明说"未复算"，
// 让模型知道这个字段缺证据，而不是给它一个假的"一致"。
const shaRecalcMaxBytes = 64 * 1024 * 1024

// humanSize 把字节数格式化成带单位的字符串。
// 小于 1KB 时给**字节**而不是「0 KB」——
// 「0 KB」会被读成"空文件"，而真相是"很小但非空"，那正是最需要看的内容。
func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d 字节", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

// listBackups 列出备份目录里的 .sql 并给出只读体检结果。
//
// 刻意不返回文件内容——只返回**状态**。
// 返回内容会让表数据（可能含个人信息）进上下文，
// 而"能不能用"这个问题只需要状态。
func listBackups(db string) []backupEntry {
	dir := backupDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	prefix := strings.TrimSpace(db) + "-"
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".sql") {
			continue
		}
		// prefix == "-" 表示不过滤库名（db 为空时）
		if prefix != "-" && !strings.HasPrefix(n, prefix) {
			continue
		}
		names = append(names, n)
	}
	// 注入的测试产物（*-injected.sql）**照常列出**，不排除。
	//
	// ## 这里原来写着"去掉注入的测试产物：它们不是真实备份"
	//
	// 那个理由在人工手动跑注入器时成立（不想看到测试文件干扰判断）。
	// 但**评测必须看到自己注入的故障**——否则会出现一个很隐蔽的错位：
	// 注入器往备份目录写了损坏文件，AI 调 list_backups 却看不到它，
	// 于是报告里"AI 检查了备份"这句话与AI 实际看到的东西无关。
	//
	// 实测踩到过：v2.5 前两轮跑出「目录里只有 1 份，0 KB」，
	// 而那份其实是**别的残留文件**，被注入的损坏备份根本没进列表。
	// AI 的分析再细致也是在分析一个不相干的文件。
	//
	// 判据：**装置注入的东西必须能被被测对象看到**，
	// 否则测的就不是我们以为在测的东西。
	sort.Strings(names)

	now := time.Now()
	out := make([]backupEntry, 0, len(names))
	for _, n := range names {
		p := filepath.Join(dir, n)
		be := backupEntry{Name: n}
		if st, err := os.Stat(p); err == nil {
			// **存字节，不存 KB。**
			//
			// 原来这里写 st.Size() / 1024（整除），于是任何小于 1KB 的文件
			// 都被报成「0 KB」。评测实测踩到：注入的损坏备份只有 200 多字节，
			// AI 读到的却是「大小 0 KB，文件是空的」——
			// **它对文件内容的判断全部建立在一个错误的数字上**，
			// 后面整段分析（"导入后肯定不会还原"）虽然结论碰巧对，
			// 理由却是错的。
			//
			// 小文件恰恰是**最该被认真检查**的那类（截断的备份就是这样），
			// 报成 0 KB 会让模型直接跳过内容检查。
			be.SizeBytes = st.Size()
			be.AgeMin = int64(now.Sub(st.ModTime()).Minutes())
		}
		// 校验和：**真的复算文件内容的 sha256**再与旁挂比对。
		//
		// ## 这里原来只校验旁挂文件的十六进制形态（v2.6 修正）
		//
		// 第一版是 `verifyShaSidecar`：读 .sha256，
		// 判断它是不是 64 位十六进制、且不含 g-z 字母，然后返回 true。
		//
		// **那个判断与文件内容毫无关系。** 实测（v26_probe_test.go）证实：
		// 注入器写一份内容被篡改的备份 + 一个 64 个 '0' 的旁挂，
		// `SHAOk` 返回 true，输出里印「校验和旁挂：一致」。
		// 而真实摘要是 `0ef93fff…`——**内容明明对不上**。
		//
		// 后果不是"报得保守了"，是**报了一条与事实相反的信号**：
		// 模型读到「一致」就可能认为文件没被动过。
		// 这正是本项目从 v2.0 起就在防的那类错——
		// **把弱检查说成强校验**。
		//
		// 真实的 mysqldump 产物几十 MB 起，sha256 是流式的，
		// 复算一份几十 MB 的文件几十毫秒，完全可接受。
		// 真正需要跳过的只有超大文件，而那种情况**必须明说未复算**。
		be.SHAOk, be.SHAState = verifyShaSidecar(p, be.SizeBytes)
		// 静态检查：只关心"有没有缺东西"，不关心具体缺什么
		_, gaps := backupStaticCheck(p)
		if len(gaps) == 0 {
			be.Static = "关键对象齐全"
		} else {
			be.Static = "缺：" + firstLines(strings.Join(gaps, "；"), 120)
		}
		// 截断检测读**文件末尾**。
		//
		// ## v2.6 修正：原来读的是前 4MB
		//
		// `readHead(p, 4MB)` 里找 "dump completed"——
		// 而该标记位于 mysqldump 输出的**末尾**。
		// 于是任何超过 4MB 的正常备份，头部 4MB 都不含它，
		// **每一份大文件备份都被报成"疑似截断"**。
		//
		// 实测（v26_probe_test.go）：造一份 5MB、末尾带完整标记的备份，
		// `Truncate` 返回 true。
		//
		// 后果比假绿更危险：模型会据此劝用户"你的备份被截断了，重做一份"，
		// 而真相是它完好。**一个诚实的工具不该反复制造假警报。**
		//
		// 小文件（<4MB）整读，所以旧行为对它也是对的；
		// readTail 对小文件同样返回全部内容，不存在回归。
		be.Truncate = !strings.Contains(strings.ToLower(readTail(p, 64*1024)),
			"dump completed")
		out = append(out, be)
	}
	// 新的在前：用户问"最近那一份"时第一个就是答案
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// verifyShaSidecar 复算备份内容的 sha256，与 .sha256 旁挂里的值比对。
//
// 返回 (是否一致, 状态文字)。**四个状态必须分开报，不能压成布尔**：
//
//	一致       —— 复算过，且与旁挂一致
//	不一致     —— 复算过，且对不上（文件被改动或损坏）
//	缺失       —— 旁挂文件不存在，无法验证
//	未复算     —— 文件超过上限，如实说明没算（**不能报"一致"**）
//
// ## 为什么从"校验旁挂形态"改成"复算内容"（v2.6）
//
// 第一版读旁挂文件，只判断它是不是 64 位十六进制且不含 g-z。
// 那与文件内容**毫无关系**，却输出了「校验和旁挂：一致」。
// 实测：内容被篡改 + 旁挂写 64 个 '0' → 报"一致"，而真实摘要对不上。
//
// **一个只检查了元数据的检查，却输出了关于内容的结论。**
// 模型没有能力分辨这个差别，它只会读走标签。
//
// ## 超大文件为什么跳过而不是硬算
//
// 几百 MB 的 dump 复算要几百毫秒，一次列 5 份就是几秒。
// 但**跳过必须明说**——"未复算"和"一致"是两个完全不同的证据强度，
// 混起来就是把没检查说成检查了。
func verifyShaSidecar(sqlPath string, size int64) (bool, string) {
	sumPath := sqlPath + ".sha256"
	if !fileExists(sumPath) {
		return false, "缺失（没有 .sha256 旁挂文件，无法验证内容是否被改动）"
	}
	if size > shaRecalcMaxBytes {
		return false, fmt.Sprintf("未复算（文件 %.1f MB 超过 %d MB 上限，"+
			"旁挂存在但本工具没有核对内容）",
			float64(size)/1024/1024, shaRecalcMaxBytes/(1024*1024))
	}
	b, err := os.ReadFile(sumPath)
	if err != nil {
		return false, "缺失（旁挂文件无法读取）"
	}
	stored := strings.TrimSpace(string(b))
	// 旁挂格式常见两种：纯摘要，或 "<hash>  <filename>"
	if i := strings.IndexAny(stored, " \t"); i > 0 {
		stored = stored[:i]
	}
	if len(stored) != 64 {
		return false, "缺失（旁挂内容不是 64 位摘要，格式异常）"
	}
	actual, err := fileSHA256(sqlPath)
	if err != nil {
		return false, "缺失（复算失败：" + err.Error() + "）"
	}
	if !strings.EqualFold(actual, stored) {
		return false, fmt.Sprintf("不一致（旁挂记 %s…，实际复算 %s… —— "+
			"文件已被改动或损坏）", shortSum(stored), shortSum(actual))
	}
	return true, "一致（已复算文件内容 sha256 并与旁挂比对）"
}

// backupsBrief 给 AI 看的一句话结论。
func backupsBrief(db string) string {
	lst := listBackups(db)
	if len(lst) == 0 {
		return fmt.Sprintf("备份目录里没有 %s 的备份文件。用户问的是「检查备份」时"+
			"**不要用 db_backup 顶替**——那会创建新文件并可能覆盖问题现场。"+
			"如实说明没有备份，问用户是否要新建。", strings.TrimSpace(db))
	}
	var sb strings.Builder
	dir := backupDir()
	sb.WriteString(fmt.Sprintf("共 %d 份备份（目录 %s），最近一份在前：\n", len(lst), dir))
	limit := len(lst)
	if limit > 5 {
		limit = 5
	}
	for _, b := range lst[:limit] {
		sb.WriteString(fmt.Sprintf("· %s（%s，%d 分钟前）\n", b.Name, humanSize(b.SizeBytes), b.AgeMin))
		sb.WriteString("    校验和：" + b.SHAState + "\n")
		if b.Truncate {
			sb.WriteString("    ⚠ 文件末尾未见 mysqldump 结束标记 —— 文件可能被截断\n")
		}
		if b.Static != "关键对象齐全" {
			sb.WriteString("    静态检查：" + b.Static + "\n")
		}
	}
	if len(lst) > limit {
		sb.WriteString(fmt.Sprintf("（另有 %d 份较旧的未列出）\n", len(lst)-limit))
	}
	sb.WriteString("\n")
	sb.WriteString("**以上校验和只证明「文件内容与生成时一致」，不证明「能还原」。**" +
		"判断「能不能还原」需要真实演练（导入临时库实测），" +
		"静态检查只能查出内容缺损。是否新建备份由用户决定，**不要自行调用 db_backup 顶替检查**。")
	return sb.String()
}

func init() {
	aiToolRegistry["list_backups"] = aiTool{
		Desc: "**只读**列出数据库备份目录里的备份文件，并给出每一份的状态：" +
			"大小、生成时间、**sha256 是否已复算并与旁挂一致**（不一致说明文件被改动或损坏）、" +
			"文件末尾有没有 mysqldump 结束标记（没有则可能被截断）、" +
			"静态检查有没有发现内容缺损（缺触发器 / 缺视图 / 缺字符集声明）。" +
			"用户问「最近的备份能不能用吗」「备份完整吗」「有哪些备份」时用它。" +
			"**注意区分「检查备份」与「创建备份」**：检查用本工具（只读），" +
			"创建才用 db_backup。用户问「能不能用」时不要用 db_backup 顶替——" +
			"那会生成新文件并覆盖问题现场。" +
			"**边界要说清**：本工具验的是「文件没被改动 + 内容缺没缺损」，" +
			"**不等于「能还原」**——后者要真建临时库导入才知道，耗时几十秒。" +
			"用户问「这个备份真能用吗」时要说明这个边界，" +
			"并建议他点「程序配置 → 恢复演练」实测。",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"database": map[string]any{"type": "string",
				"description": "库名。留空则列备份目录里所有库"},
		}, "required": []string{}},
		Execute: func(args map[string]any) (string, error) {
			db, _ := args["database"].(string)
			if strings.TrimSpace(db) == "" {
				db = cfg.Projects.DBName
			}
			return backupsBrief(db), nil
		},
	}
}
