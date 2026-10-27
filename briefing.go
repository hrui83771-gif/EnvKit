package main

// briefing.go —— Agent 闭环 Phase 2：晨报（确定性事实报告）。
//
// 设计文档：docs/DESIGN-agent-loop.md 3.2 与 Phase 2 验收 4：
//   「晨报在配置时刻产出，内容全部来自系统记录（**无模型编造空间**）」。
//
// 因此本文件**不调用任何模型**，只做聚合：
//   - 每个服务：运行态、端口、连续失败、最近错误、复验结论（runtime.go 的现成视图）；
//   - 近 24 小时崩溃记录（svcstate.go 的 Crashes）；
//   - 磁盘剩余 + 日志占用（platform.DiskFreeBytes / loghub 的 logSinkDisk），
//     并与历史快照对比——**没有历史就写"首次记录"，不编趋势**；
//   - 未收口的行动（要用户继续的那些）；
//   - 组件：配置里的声明版本 vs 最近一次检测结果（不重新跑检测，晨报不该启动子进程）；
//   - 链端：chain.go 的最近一次快照（不发起网络请求）；版本：diag.go 的最近一次检查。
//
// 每条"需关注"必须带**判定依据**（例如"连续失败 3 次 ≥ 阈值 3"）。理由很简单：
// 不带依据的结论，用户无法反驳，也就无法信任；而机器最该做的恰恰是把依据摆出来。
//
// 一条明确的边界：晨报只读。它不改任何服务状态，也不触发动作建议的执行——
// 建议本身仍走确认（红线 2）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"envkit/internal/platform"
)

// briefingDirFn 报告目录。抽成变量供单测替换（同 playbookFile 手法）。
var briefingDirFn = func() string { return filepath.Join(exeDir(), "briefings") }

// 晨报的阈值（写死常量而不是配置：这些是"什么算异常"的判断口径，
// 随手可调会让人分不清"环境变了"和"标准变了"）。
const (
	brfKeepFiles    = 30        // 报告保留份数（对齐审计窗口 30 天）
	brfCrashWarn    = 3         // 24 小时内崩溃达到这个次数 → 关注
	brfRestartWarn  = 3         // 连续失败次数达到这个值 → 关注
	brfDiskLowByte  = 2 << 30   // 剩余空间低于 2GB → 关注
	brfLogWarnByte  = 200 << 20 // 当日日志超过 200MB → 关注
	brfHistKeepDays = 30
)

// briefingFacts 一次晨报的聚合结果（测试断言"报告里有几条关注项"用）。
type briefingFacts struct {
	Attn     []string // 需关注条目（含判定依据）
	Crash    int      // 近 24 小时崩溃条数
	Report   string   // 生成的文件名
	Free     int64    // 磁盘剩余（HasFree=false 时无意义）
	HasFree  bool
	LogBytes int64
}

// ---------- 对外入口 ----------

// briefingGenerate 生成一份晨报并落盘，返回文件名与聚合结果。
func briefingGenerate(now time.Time) (briefingFacts, error) {
	return briefingGenerateInto(briefingDirFn(), now, "")
}

// briefingGenerateInto 生成到指定目录（测试用）。skipID 是"别把自己算进未收口行动"的行动 ID。
func briefingGenerateInto(dir string, now time.Time, skipID string) (briefingFacts, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return briefingFacts{}, fmt.Errorf("创建报告目录失败：%v", err)
	}
	md, facts := briefingCompose(dir, now, skipID)
	name := now.Format("2006-01-02-1504") + ".md"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(md), 0o644); err != nil {
		return briefingFacts{}, fmt.Errorf("写报告失败：%v", err)
	}
	facts.Report = name
	briefingPrune(dir)
	// 历史快照用于次日的"较 N 天前"对比；拿不到磁盘就不记，免得留下假数据
	if facts.HasFree {
		briefingSaveHist(dir, now, facts.Free, facts.LogBytes)
	}
	return facts, nil
}

// briefingPrune 只保留最近 brfKeepFiles 份报告（按文件名倒序 = 按时间倒序）。
func briefingPrune(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") && len(e.Name()) == len("2006-01-02-1504.md") {
			names = append(names, e.Name())
		}
	}
	if len(names) <= brfKeepFiles {
		return
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, n := range names[brfKeepFiles:] {
		_ = os.Remove(filepath.Join(dir, n))
	}
}

// ---------- 组装 ----------

// briefingCompose 生成报告正文（纯函数式：只读全局快照与本地文件，不写状态）。
func briefingCompose(dir string, now time.Time, skipID string) (string, briefingFacts) {
	var facts briefingFacts
	var b strings.Builder
	host, _ := os.Hostname()

	fmt.Fprintf(&b, "# EnvKit 晨报 %s\n\n", now.Format("2006-01-02"))
	fmt.Fprintf(&b, "- 生成时间：%s\n- 版本：%s\n- 主机：%s\n", now.Format("2006-01-02 15:04:05"), appVersion, host)
	b.WriteString("- 判定口径：**全部来自系统记录，不含模型推断**；每条关注项都附判定依据。\n\n")

	// —— 服务 ——
	type svcRow struct {
		label, state string
	}
	var rows []svcRow
	svcs := briefServices()
	crashLines := []string{}
	crashCount := 0
	for _, svc := range svcs {
		st := runtimeService(svc.ID)
		state := "—"
		switch {
		case st.Running:
			state = "运行中"
			if st.Port > 0 {
				state += fmt.Sprintf("（端口 %d）", st.Port)
			}
			if st.UptimeSec > 0 {
				state += "，已运行 " + humanDuration(time.Duration(st.UptimeSec)*time.Second)
			}
		case svc.Adapter == "local":
			// 只有本机适配器才能说"没在跑"；链端是 SSH 远程，本地没进程是正常的
			state = "未运行"
		}
		rows = append(rows, svcRow{label: svc.Label, state: state})

		// 连续失败 / 熔断 / 复验
		if st.Restarts >= brfRestartWarn {
			facts.Attn = append(facts.Attn,
				fmt.Sprintf("%s 连续失败 %d 次（≥ 阈值 %d）", svc.Label, st.Restarts, brfRestartWarn))
		}
		if st.BreakerTripped {
			facts.Attn = append(facts.Attn, svc.Label+" 处于熔断态（自动重启已被拒绝，需人工复位）")
		}
		// 「未运行」只写进服务表，不算需关注：EnvKit 的用户经常**故意**不启服务，
		// 每天早上一句"后端未运行"会把这个字段训练成噪音。真崩过的话，
		// 它会以崩溃记录的形式出现在下面的 24h 段里 —— 那条才是异常。

		// 近 24h 崩溃：这里必须读 svcSnapshot 的原始记录 ——
		// runtimeService 只把 Restarts/断路器搬进状态视图，**不搬 Crashes**，
		// 拿它的 Crashes 永远是空数组（第一版就踩了这个）。
		rec := svcSnapshot(svc.ID)
		for _, c := range rec.Crashes {
			t, err := time.ParseInLocation("2006-01-02 15:04:05", c.At, time.Local)
			if err != nil || now.Sub(t) > 24*time.Hour || t.After(now) {
				continue
			}
			crashCount++
			crashLines = append(crashLines, fmt.Sprintf("- %s · %s · %s · %s",
				c.At, svc.Label, briefOr(c.ErrKind, "未知类别"), briefOr(c.Why, "（无说明）")))
		}
	}
	facts.Crash = crashCount
	if crashCount >= brfCrashWarn {
		facts.Attn = append(facts.Attn,
			fmt.Sprintf("近 24 小时崩溃 %d 次（≥ 阈值 %d）", crashCount, brfCrashWarn))
	}

	b.WriteString("## 服务\n\n")
	if len(rows) == 0 {
		b.WriteString("配置里没有服务清单（`services` 为空且未能从旧配置推导）。\n\n")
	} else {
		b.WriteString("| 服务 | 状态 |\n|---|---|\n")
		for _, r := range rows {
			fmt.Fprintf(&b, "| %s | %s |\n", r.label, r.state)
		}
		b.WriteString("\n")
	}

	// —— 崩溃 ——
	b.WriteString("## 近 24 小时崩溃\n\n")
	if len(crashLines) == 0 {
		b.WriteString("没有崩溃记录。\n\n")
	} else {
		b.WriteString(strings.Join(crashLines, "\n") + "\n\n")
	}

	// —— 磁盘与日志 ——
	b.WriteString("## 磁盘与日志\n\n")
	free, hasFree := platform.DiskFreeBytes()
	logBytes := int64(0)
	logFiles := 0
	day := now.Format("2006-01-02")
	for _, src := range []string{"", "web", "backend"} {
		f, n := logSinkDisk(logDir(), day, src)
		logFiles += f
		logBytes += n
	}
	if hasFree {
		facts.Free, facts.HasFree = int64(free), true
		facts.LogBytes = logBytes
		line := fmt.Sprintf("- exe 所在分区剩余：%s", humanBytes(int64(free)))
		if delta, days, ok := briefingDiskDelta(dir, now, int64(free)); ok {
			sign := "+"
			if delta < 0 {
				sign = ""
			}
			line += fmt.Sprintf("（%d 天前 %s，变化 %s%s）", days, humanBytes(int64(free)-delta),
				sign, humanBytes(delta))
		} else {
			line += "（首次记录，暂无历史对比）"
		}
		b.WriteString(line + "\n")
		if free < brfDiskLowByte {
			facts.Attn = append(facts.Attn,
				fmt.Sprintf("磁盘剩余 %s（< 阈值 %s）", humanBytes(int64(free)), humanBytes(brfDiskLowByte)))
		}
	} else {
		b.WriteString("- 磁盘剩余：本平台不采集（仅 Windows 支持）\n")
	}
	fmt.Fprintf(&b, "- 今日日志：%d 个文件 / %s\n", logFiles, humanBytes(logBytes))
	if logBytes > brfLogWarnByte {
		facts.Attn = append(facts.Attn,
			fmt.Sprintf("当日日志 %s（> 阈值 %s）", humanBytes(logBytes), humanBytes(brfLogWarnByte)))
	}
	if _, err := os.Stat(logDir()); err != nil {
		facts.Attn = append(facts.Attn, "日志目录不可读："+logDir())
	}
	b.WriteString("\n")

	// —— 未收口行动 ——
	b.WriteString("## 待你继续的行动\n\n")
	pend := 0
	for _, m := range missionList() {
		if m.ID == skipID || missionTerminal(m.State) {
			continue
		}
		pend++
		note := ""
		if m.PauseWhy != "" {
			note = " · " + m.PauseWhy
		}
		if m.Playbook != "" {
			if p, ok := pbkGet(m.Playbook); ok {
				note += " · 预案「" + p.Title + "」"
			}
		}
		fmt.Fprintf(&b, "- %s · %s（%s）%s\n",
			m.CreatedAt.Format("01-02 15:04"), m.Goal, missionStateCN(m.State), note)
	}
	if pend == 0 {
		b.WriteString("没有未收口的行动。\n")
	}
	b.WriteString("\n")

	// —— 组件 ——
	b.WriteString("## 组件\n\n")
	cfgs := briefComponents()
	if len(cfgs) == 0 {
		b.WriteString("配置里没有组件清单。\n\n")
	} else {
		resultsMu.Lock()
		snap := make(map[string]CheckResult, len(results))
		for k, v := range results {
			snap[k] = v
		}
		resultsMu.Unlock()
		b.WriteString("| 组件 | 声明版本 | 最近检测 |\n|---|---|---|\n")
		for _, c := range cfgs {
			got := "尚未检测"
			if r, ok := snap[c.Name]; ok {
				if r.Installed {
					got = briefOr(r.Version, "已安装（版本未知）")
				} else {
					got = "未安装"
					facts.Attn = append(facts.Attn, c.Name+" 未安装（最近一次检测）")
				}
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", c.Name, briefOr(c.Version, "—"), got)
			// 版本漂移：声明与实际不一致本身就是事实，直接列出来
			if r, ok := snap[c.Name]; ok && r.Installed && c.Version != "" && r.Version != "" &&
				!strings.Contains(r.Version, c.Version) {
				facts.Attn = append(facts.Attn,
					fmt.Sprintf("%s 版本漂移：配置 %s / 实测 %s", c.Name, c.Version, r.Version))
			}
		}
		b.WriteString("\n")
	}

	// —— 链端 ——
	b.WriteString("## 链端\n\n")
	chainMu.Lock()
	ci := chainInfo
	chainMu.Unlock()
	switch {
	case !ci.Checked:
		b.WriteString("尚未检测。\n\n")
	case ci.Reach == "down":
		b.WriteString("最近一次检测：" + briefOr(ci.ReachMsg, "主机不可达") + "\n\n")
		facts.Attn = append(facts.Attn, "链端主机不可达（最近一次检测："+ci.At+"）")
	default:
		node := "未知"
		if ci.Port20200 {
			node = "存活"
		} else {
			node = "不通"
			facts.Attn = append(facts.Attn, "链节点 20200 不通（最近一次检测："+ci.At+"）")
		}
		webase := "不通"
		if ci.Port5002 {
			webase = "存活"
		} else {
			facts.Attn = append(facts.Attn, "WeBASE-Front 5002 不通（最近一次检测："+ci.At+"）")
		}
		fmt.Fprintf(&b, "- 最近检测 %s：节点 20200 %s，WeBASE 5002 %s", ci.At, node, webase)
		if ci.BlockNumber != "" {
			fmt.Fprintf(&b, "，块高 %s", ci.BlockNumber)
		}
		if ci.RecCount > 0 {
			fmt.Fprintf(&b, "，累计自动恢复 %d 次（最近 %s）", ci.RecCount, briefOr(ci.LastRecover, "—"))
		}
		b.WriteString("\n\n")
	}

	// —— 版本 ——
	b.WriteString("## 版本\n\n")
	updMu.Lock()
	u := upd
	updMu.Unlock()
	switch {
	case !u.Checked:
		b.WriteString(fmt.Sprintf("当前 %s，尚未检查更新。\n\n", appVersion))
	case u.HasNew && !u.Ignored:
		fmt.Fprintf(&b, "当前 %s，最新 %s —— 有新版本可用（检查于 %s）。\n\n", appVersion, u.Latest, u.At)
		facts.Attn = append(facts.Attn, fmt.Sprintf("有新版本 %s（当前 %s）", u.Latest, appVersion))
	default:
		fmt.Fprintf(&b, "当前 %s，已是最新（检查于 %s）。\n\n", appVersion, u.At)
	}

	// —— 结论 ——
	head := "## 结论\n\n"
	if len(facts.Attn) == 0 {
		head += "**无需关注**：所有已采集项都在阈值内。\n\n"
	} else {
		head += fmt.Sprintf("**需关注 %d 项**：\n\n", len(facts.Attn))
		for i, a := range facts.Attn {
			head += fmt.Sprintf("%d. %s\n", i+1, a)
		}
		head += "\n"
	}
	// 结论放最前面（人要读的是这段），正文顺序不变
	return head + "\n" + b.String(), facts
}

// ---------- 数据源 ----------

// briefServices 服务清单：优先 cfg.Services，为空时按旧配置推导（与守护循环同源）。
func briefServices() []Service {
	cfgMu.Lock()
	c := cfg
	cfgMu.Unlock()
	if len(c.Services) > 0 {
		return c.Services
	}
	return servicesFromLegacy(c)
}

// briefComponents 组件清单（只读配置，不重新检测）。
func briefComponents() []Component {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return append([]Component(nil), cfg.Components...)
}

// ---------- 磁盘历史（趋势的唯一来源是"以前记过什么"）----------

type diskSnap struct {
	Day       string `json:"day"`
	At        string `json:"at"`
	FreeBytes int64  `json:"free_bytes"`
	LogBytes  int64  `json:"log_bytes"`
}

func diskHistPath(dir string) string { return filepath.Join(dir, "disk-history.jsonl") }

// briefingDiskDelta 与最近 7 天内最早的一条记录比较，返回(差值, 天数, 是否有历史)。
//
// 为什么是"最早的一条"而不是"昨天"：晨报可能不是每天都跑（机器关着），
// 拿不到昨天就说没有历史，会让趋势长期空缺；取窗口内最早一条更诚实。
// 差值 = 今天 - 那次（负数 = 变少了）。
func briefingDiskDelta(dir string, now time.Time, free int64) (int64, int, bool) {
	hist := loadDiskHist(dir)
	var old *diskSnap
	for i := range hist {
		h := hist[i]
		t, err := time.ParseInLocation("2006-01-02", h.Day, time.Local)
		if err != nil || t.After(now) || now.Sub(t) > 7*24*time.Hour {
			continue
		}
		if h.Day == now.Format("2006-01-02") {
			continue // 今天的记录不算"历史"
		}
		if old == nil || h.Day < old.Day {
			cp := h
			old = &cp
		}
	}
	if old == nil {
		return 0, 0, false
	}
	// 按**日历日**算天数，不按小时折算：早上 08:30 生成的晨报，与 3 天前
	// 午夜记的那条相差 3 天 8.5 小时，按小时四舍五入会变成「4 天前」——
	// 数字看起来没差多少，但它和用户日历上的天数对不上，属于不该有的错。
	loc := now.Location()
	dayStart := func(t time.Time) time.Time {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	}
	tOld, err := time.ParseInLocation("2006-01-02", old.Day, loc)
	if err != nil {
		return 0, 0, false
	}
	days := int(dayStart(now).Sub(dayStart(tOld)).Hours()/24 + 0.5)
	if days < 1 {
		days = 1
	}
	return free - old.FreeBytes, days, true
}

func loadDiskHist(dir string) []diskSnap {
	b, err := os.ReadFile(diskHistPath(dir))
	if err != nil {
		return nil
	}
	var out []diskSnap
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var s diskSnap
		if err := json.Unmarshal([]byte(line), &s); err == nil && s.Day != "" {
			out = append(out, s)
		}
	}
	return out
}

// briefingSaveHist 记下今天的快照（同一天只保留一条，最新的覆盖旧的）。
func briefingSaveHist(dir string, now time.Time, free int64, logBytes int64) {
	day := now.Format("2006-01-02")
	hist := loadDiskHist(dir)
	out := make([]diskSnap, 0, len(hist)+1)
	for _, h := range hist {
		if h.Day == day {
			continue
		}
		out = append(out, h)
	}
	out = append(out, diskSnap{Day: day, At: now.Format("2006-01-02 15:04:05"), FreeBytes: free, LogBytes: logBytes})
	if len(out) > brfHistKeepDays {
		out = out[len(out)-brfHistKeepDays:]
	}
	var b strings.Builder
	for _, h := range out {
		bs, err := json.Marshal(h)
		if err != nil {
			continue
		}
		b.Write(bs)
		b.WriteString("\n")
	}
	if err := os.WriteFile(diskHistPath(dir), []byte(b.String()), 0o644); err != nil {
		warn(scSys, "晨报", "磁盘历史写入失败：%v", err)
	}
}

// ---------- 挂到行动上 ----------

// briefingAttach 给刚产出的晨报行动生成报告、挂上文件名并收口。
//
// 为什么直接 done 而不是等用户点「继续」：晨报是**只读产出**，报告生成的
// 那一刻任务就完成了。让它挂在「待执行」等人点，会让"待你继续"永远有一项，
// 用户很快就学会无视那一栏 —— 狼来了。
//
// 术语说明：Result.Verified=true 在这里的语义是「产出已验证存在」，与
// ai_mission.go 对报告型行动（trigger=patrol|schedule）的收口口径一致：
// 报告型没有环境状态可复验，用动作判据卡它会永远 failed。
func briefingAttach(id string, now time.Time) {
	facts, err := briefingGenerate(now)
	if err != nil {
		warn(scSys, "晨报", "生成失败（行动 %s）：%v", id, err)
		// 落 failed 而不是留在 pending：一份"稍后就有"的晨报比明说失败更糟
		if _, e := missionUpdate(id, func(x *Mission) error {
			return missionFinish(x, false, "晨报生成失败："+err.Error())
		}); e != nil {
			warn(scSys, "晨报", "收口失败（行动 %s）：%v", id, e)
		}
		return
	}
	msg := fmt.Sprintf("晨报已生成：%d 项需关注（只读，未改动任何状态）", len(facts.Attn))
	if len(facts.Attn) == 0 {
		msg = "晨报已生成：无需关注（只读，未改动任何状态）"
	}
	if _, err := missionUpdate(id, func(x *Mission) error {
		x.Report = facts.Report
		x.Draft = nil // 晨报不套预案，起草步骤字段保持干净
		return missionFinish(x, true, msg)
	}); err != nil {
		warn(scSys, "晨报", "挂载报告失败（行动 %s）：%v", id, err)
		return
	}
	auditNow(actSys, "briefing", id, facts.Report, resOK,
		fmt.Sprintf("需关注 %d 项，近 24h 崩溃 %d 条", len(facts.Attn), facts.Crash))
	info(scSys, "晨报", "已生成 %s（需关注 %d 项）", facts.Report, len(facts.Attn))
	notifyMissions()
}

// ---------- 小工具 ----------

// missionStateCN 行动状态的中文名（报告里给人看，日志/接口仍用英文常量）。
func missionStateCN(s string) string {
	switch s {
	case msPending:
		return "待执行"
	case msAwaitingConfirm:
		return "待确认"
	case msRunning:
		return "执行中"
	case msPaused:
		return "待你继续"
	case msDone:
		return "完成"
	case msFailed:
		return "失败"
	case msCanceled:
		return "已取消"
	case msExpired:
		return "已过期"
	}
	return s
}

// briefOr 空串兜底：报告里宁可写"（无说明）"也不留空白格。
func briefOr(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// humanBytes 人类可读容量（报告里只用于展示，不参与判定）。
func humanBytes(n int64) string {
	neg := ""
	if n < 0 {
		neg, n = "-", -n
	}
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%s%.1f GB", neg, float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%s%.1f MB", neg, float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%s%.1f KB", neg, float64(n)/(1<<10))
	}
	return fmt.Sprintf("%s%d B", neg, n)
}

// humanDuration 运行时长（天/小时/分钟，够用即可）。
func humanDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%d 天 %d 小时", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%d 小时 %d 分", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%d 分钟", int(d.Minutes()))
}
