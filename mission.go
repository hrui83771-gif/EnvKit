package main

// mission.go —— Agent 闭环 Phase 1：Mission（对外叫「行动」）实体与状态机。
//
// 设计文档：docs/DESIGN-agent-loop.md（2026-10-26 评审定案）。
//
// 与 TaskHandle（state.go）的分工：
//   - TaskHandle = 一次脚本执行的**内存**句柄，秒级，生命周期 = 一次调用；
//   - Mission = 交给助手的一个目标，分钟~小时级，跨多个 AI 回合，可持久化、
//     可断点续作。两者没有交集，不要互相塞字段。
//
// 存储独立于 config.json（同 memory.go 的理由）：高频状态变更的数据混进
// 配置会被表单自动保存的 merge 坑牵连。
//
// 红线（设计文档第一节，改动前先读）：
//  1. 成败只认 verify_result —— 没有复验证据的终态一律 failed，不发假徽章；
//  2. 写操作永远过确认（本文件只做状态管理，不绕过 PolicyGate）；
//  3. 动作类主动能力默认关（配置面见 MissionConfig）；
//  4. 全程 trace_id；
//  5. 后台执行与前台对话共用 aiChatMu。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ===== 行动状态机 =====

// 行动状态。转移表见 missionTransitions —— 单测按表逐格验证，
// 加状态或改转移必须同步改表（设计验收 5）。
const (
	msPending         = "pending"          // 已创建，待执行/待人批
	msAwaitingConfirm = "awaiting_confirm" // 等用户确认写操作
	msRunning         = "running"          // 正在执行（绑定一个 HTTP 回合）
	msPaused          = "paused"           // 暂停/断流/重启降级，待用户继续
	msDone            = "done"             // 终态：复验通过（红线 1）
	msFailed          = "failed"           // 终态：复验未过/预算耗尽/无复验
	msCanceled        = "canceled"         // 终态：用户取消
	msExpired         = "expired"          // 终态：超过截止时间未收口
)

// missionTransitions 允许的状态转移（表驱动测试的唯一依据）。
// 终态（done/failed/canceled/expired）不在表里 = 不可再转移。
var missionTransitions = map[string][]string{
	msPending:         {msRunning, msAwaitingConfirm, msPaused, msFailed, msCanceled, msExpired},
	msAwaitingConfirm: {msRunning, msPaused, msFailed, msCanceled, msExpired},
	msRunning:         {msAwaitingConfirm, msPaused, msDone, msFailed, msCanceled, msExpired},
	msPaused:          {msRunning, msAwaitingConfirm, msFailed, msCanceled, msExpired},
}

func missionTerminal(s string) bool {
	return s == msDone || s == msFailed || s == msCanceled || s == msExpired
}

// missionGo 执行一次状态转移。非法转移返回错误且不改状态。
func missionGo(m *Mission, to string) error {
	if m == nil {
		return fmt.Errorf("行动为空")
	}
	if m.State == to {
		return nil
	}
	for _, ok := range missionTransitions[m.State] {
		if ok == to {
			m.State = to
			m.UpdatedAt = time.Now()
			return nil
		}
	}
	return fmt.Errorf("行动 %s 不允许从 %s 转到 %s", m.ID, m.State, to)
}

// missionFinish 收口终态（红线 1 的落点）。
//
// verified=false（没有复验证据、或复验未通过）一律落 failed —— 宁可错杀，
// 不发假徽章。只有 verify_result 判过的成功才配 done。
func missionFinish(m *Mission, verified bool, msg string) error {
	to := msFailed
	if verified {
		to = msDone
	}
	if m.Result == nil {
		m.Result = &OpResult{Action: "mission", Target: m.ID}
	}
	m.Result.Verified = verified
	m.Result.Ok = verified
	m.Result.Msg = truncateRunes(msg, 500)
	m.Pending = nil // 终态不留断点
	return missionGo(m, to)
}

// ===== 实体 =====

// Trigger 行动的来源。
type Trigger struct {
	Kind   string `json:"kind"`             // manual | patrol | schedule | webhook | guard
	Detail string `json:"detail,omitempty"` // 触发摘要（谁/为什么）
}

// PlanStepRecord 一个已完成步骤的落地记录（断点续作的回放点）。
//
// 与 plan.go planStep 的区别：planStep 是**回合内**的内存展示视图；
// 本结构是落盘的执行记录，续作时要靠它把关键结果全量回传给模型
// （设计定案 3：摘要 + 关键结果全量）。
type PlanStepRecord struct {
	Seq      int    `json:"seq"`
	Tool     string `json:"tool"`
	Target   string `json:"target,omitempty"`
	Why      string `json:"why,omitempty"`
	OK       bool   `json:"ok"`
	Evidence string `json:"evidence,omitempty"` // 工具结论/复验证据摘要
	At       string `json:"at"`
}

// PendingStep 断点：待确认/待执行的下一步。续作从这里接着跑。
type PendingStep struct {
	Kind string `json:"kind"` // confirm（待确认）| exec（待执行）
	Tool string `json:"tool,omitempty"`
	Note string `json:"note,omitempty"` // 给用户看的一句话
	At   string `json:"at"`
}

// OpResult 复用 opresult.go 的操作结果契约（Ok/Verified 三态语义）。
// 不在这里重新定义：红线 1 说「成败只认 verify_result」，而 verify 事件
// 落盘用的就是这个结构，Mission 与工具结果必须是同一套判据载体。

// Mission 一个跨回合、可持久化、可恢复的行动。
type Mission struct {
	ID        string           `json:"id"`    // m-<时间>-<短随机>
	Goal      string           `json:"goal"`  // 自然语言目标（巡检类放触发摘要）
	State     string           `json:"state"` // 见 ms* 常量
	Trigger   Trigger          `json:"trigger"`
	TraceID   string           `json:"trace_id,omitempty"`  // 关联审计轨迹（trace.go）
	Plan      []PlanStepRecord `json:"plan,omitempty"`      // 已完成步骤
	Pending   *PendingStep     `json:"pending,omitempty"`   // 断点
	Playbook  string           `json:"playbook,omitempty"`  // 套用的预案 ID（Phase 2 起）
	Result    *OpResult        `json:"result,omitempty"`    // 终态结论
	PauseWhy  string           `json:"pause_why,omitempty"` // 落 paused 的原因（断流/重启/用户）
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
	ExpiresAt time.Time        `json:"expires_at"` // 截止时间：超期未收口 → expired
}

// 行动的时间参数（超期收口 / 终态保留）从 v3.4 起由 dev 段配置
// （mission_deadline_days / mission_keep_days，默认都是 30 天），
// 这里保留零值兜底语义：dev 段缺失或手写 0 时仍是原来的 30 天。
//
// msDeadline 创建后多久没走完就自动收成 expired —— 实际任务远用不到这么久，
// 这只是防僵尸行动永远挂着。
func msDeadline() time.Duration { return devSnap().missionDeadline() }

// msKeep 终态行动保留期，过期清理。
func msKeep() time.Duration { return devSnap().missionKeep() }

const (
	// msMaxCount 文件里最多保留的行动数（防无限增长；淘汰最老的**终态**）。
	msMaxCount = 200
	// msConfirmTimeout 等确认超时降级为 paused 的时长。
	// 对齐 aiConfirmWaitMax（10 分钟）+ 余量；卡片登记 TTL 本来就是 30 分钟。
	msConfirmTimeout = 30 * time.Minute
)

// missionFile 行动文件路径。抽成变量供单测替换（同 memoryFile 手法）。
var missionFile = func() string { return filepath.Join(exeDir(), "missions.json") }

var msMu sync.Mutex

// MissionConfig 行动配置（config.json 的 "missions" 段）。
//
// 定案（2026-10-26 评审）：**巡检类默认开、动作类默认关**——
// 只读巡检/晨报只产出报告、不写任何状态，开箱即用争议最小；
// 会写状态的动作无论来源一律过确认（红线 2），本配置不豁免。
type MissionConfig struct {
	// Enabled 定时巡检/晨报总开关。nil = 开（零值即默认开，与本库
	// 「零值 = 内置默认」的惯例一致，用指针区分「没设」与「显式关」）。
	Enabled *bool `json:"enabled,omitempty"`
	// PatrolIntervalMin 巡检间隔（分钟），0 = 默认 60。
	PatrolIntervalMin int `json:"patrol_interval_min"`
	// InboundHook 入站触发端点 POST /api/trigger 的开关，**默认关**
	//（红线 3：主动能力 opt-in）。
	InboundHook bool `json:"inbound_hook"`
	// BriefingAt 晨报时刻 "HH:MM"，"" = 默认 "08:30"，"off" = 关晨报。
	BriefingAt string `json:"briefing_at"`
}

// on 定时产出（巡检/晨报）是否开启。
func (c MissionConfig) on() bool { return c.Enabled == nil || *c.Enabled }

// inboundOn 入站触发端点是否开启（默认关）。
func (c MissionConfig) inboundOn() bool { return c.InboundHook }

// ===== 持久化 =====

// loadMissions 读行动文件。坏条跳过而不是整体失败——文件常被手工编辑。
func loadMissions() []Mission {
	b, err := os.ReadFile(missionFile())
	if err != nil {
		return []Mission{}
	}
	var out []Mission
	if err := json.Unmarshal(b, &out); err != nil {
		// 兼容手工编辑破坏的文件：不返回半截数据，留 .bak 兜底
		return []Mission{}
	}
	if out == nil {
		return []Mission{}
	}
	return out
}

// saveMissions 原子写 + 备份（与 memories.json 同级保护）。
func saveMissions(ms []Mission) error {
	b, err := json.MarshalIndent(ms, "", " ")
	if err != nil {
		return err
	}
	p := missionFile()
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	if _, err := os.Stat(p); err == nil {
		_ = os.Remove(p + ".bak")
		_ = os.Rename(p, p+".bak")
	}
	return os.Rename(tmp, p)
}

// ===== 查询与变更（全部持 msMu：Lock → load → 改 → save → Unlock）=====

// missionNew 创建一条行动（pending 态）。
func missionNew(goal string, tg Trigger) (Mission, error) {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return Mission{}, fmt.Errorf("行动目标为空")
	}
	now := time.Now()
	m := Mission{
		ID:        "m-" + now.Format("20060102-150405") + "-" + traceRand(),
		Goal:      truncateRunes(goal, 500),
		State:     msPending,
		Trigger:   tg,
		CreatedAt: now,
		UpdatedAt: now,
		ExpiresAt: now.Add(msDeadline()),
	}
	msMu.Lock()
	defer msMu.Unlock()
	all, _ := missionSweep(loadMissions(), now)
	all = append(all, m)
	if err := missionTrim(&all); err != nil {
		return Mission{}, err
	}
	if err := saveMissions(all); err != nil {
		return Mission{}, err
	}
	return m, nil
}

// missionGet 按 ID 取行动。
func missionGet(id string) (Mission, bool) {
	msMu.Lock()
	defer msMu.Unlock()
	for _, m := range missionLoadSweep(false) {
		if m.ID == id {
			return m, true
		}
	}
	return Mission{}, false
}

// missionList 返回行动列表，新的在前。
func missionList() []Mission {
	msMu.Lock()
	defer msMu.Unlock()
	all := missionLoadSweep(false)
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].UpdatedAt.After(all[j].UpdatedAt)
	})
	return all
}

// missionUpdate 按 ID 变更一条行动（fn 内直接改 m），落盘。
func missionUpdate(id string, fn func(*Mission) error) (Mission, error) {
	msMu.Lock()
	defer msMu.Unlock()
	all, _ := missionSweep(loadMissions(), time.Now())
	for i := range all {
		if all[i].ID != id {
			continue
		}
		m := &all[i]
		if err := fn(m); err != nil {
			return Mission{}, err
		}
		m.UpdatedAt = time.Now()
		if err := missionTrim(&all); err != nil {
			return Mission{}, err
		}
		if err := saveMissions(all); err != nil {
			return Mission{}, err
		}
		return *m, nil
	}
	return Mission{}, fmt.Errorf("行动不存在：%s", id)
}

// missionTrim 控制总量：终态优先淘汰（最老的终态先走），不淘汰未收口的。
func missionTrim(all *[]Mission) error {
	for len(*all) > msMaxCount {
		idx := -1
		for i, m := range *all {
			if missionTerminal(m.State) && (idx < 0 || m.UpdatedAt.Before((*all)[idx].UpdatedAt)) {
				idx = i
			}
		}
		if idx < 0 {
			return fmt.Errorf("行动数超上限 %d 且无可淘汰的终态行动", msMaxCount)
		}
		*all = append((*all)[:idx], (*all)[idx+1:]...)
	}
	return nil
}

// ===== 漫游清理（sweep）=====

// missionSweep 对读到的列表做确定性清理/降级，返回处理后的列表与是否发生变更。
//
// runningIsStale=true 只在启动时用：执行绑定 HTTP 回合，进程重启后
// 旧的 running 必然是尸体——一律降级 paused（绝不自动续跑：隔夜重启后
// 无人值守执行写操作违背红线 2）。
//
// 注意：UpdatedAt 只在状态真的变了才刷新——否则每次读都刷新时钟，
// awaiting_confirm 的超时永远触发不了。
func missionSweep(ms []Mission, now time.Time, runningIsStale ...bool) ([]Mission, bool) {
	stale := len(runningIsStale) > 0 && runningIsStale[0]
	changed := false
	out := make([]Mission, 0, len(ms))
	for _, m := range ms {
		if !missionTerminal(m.State) {
			to := ""
			why := ""
			switch {
			case stale && m.State == msRunning:
				to, why = msPaused, "EnvKit 重启，行动已暂停（不会自动继续）"
			case m.State == msAwaitingConfirm && now.Sub(m.UpdatedAt) > msConfirmTimeout:
				// 等确认超时不作废：降级 paused，用户回来还能点继续
				to, why = msPaused, "确认超时，已暂停"
			case now.After(m.ExpiresAt):
				to = msExpired
			}
			if to != "" {
				m.State = to
				if why != "" {
					m.PauseWhy = why
				}
				m.UpdatedAt = now
				changed = true
			}
		} else if now.Sub(m.UpdatedAt) > msKeep() {
			// 终态过保留期：删
			changed = true
			continue
		}
		out = append(out, m)
	}
	return out, changed
}

// missionLoadSweep 读 + 清理；有变更就落盘。所有读路径统一走这里。
// 调用方必须已持 msMu。
func missionLoadSweep(stale bool) []Mission {
	all, changed := missionSweep(loadMissions(), time.Now(), stale)
	if changed {
		if err := saveMissions(all); err != nil {
			warn(scSys, "行动", "行动清理落盘失败：%v", err)
		}
	}
	return all
}

// missionStartupSweep 启动时的全量降级 + 过期清理。main.go 启动链调用。
func missionStartupSweep() {
	msMu.Lock()
	defer msMu.Unlock()
	all := missionLoadSweep(true)
	info(scSys, "行动", "已加载 %d 条行动记录", len(all))
}

// truncateRunes 按 rune 截断（i18n/中文安全，同 ai_dbquery 的教训）。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
