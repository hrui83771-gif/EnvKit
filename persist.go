package main

// persist.go —— 运行状态持久化（零依赖：JSON 文件）
//
// 之前所有状态（检测结果、任务结果、服务状态、链端信息、数据库健康）都在内存里：
// 重启即失忆，前端一进来就是"尚未检测"，也没法回答"上次是什么时候好的"。
// 这里把它们落到 exe 同级的 state.json：启动时恢复展示，运行中每 30s 与关键节点各存一次。
//
// 注意：不引入 SQLite 是为了保住"零依赖单文件"的分发优势——这点数据量 JSON 足够。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type persistedState struct {
	Version string                 `json:"version"`
	SavedAt string                 `json:"saved_at"`
	Results map[string]CheckResult `json:"results"`
	Prog    map[string]ProgStatus  `json:"prog"`
	Chain   ChainInfo              `json:"chain"`
	DB      DBHealth               `json:"db"`
	// 上次退出时仍在运行的服务（用于启动提示：它们已随 EnvKit 退出被终止）
	LastServices map[string]bool `json:"last_services"`

	// v3.9：结论的时效性必须跨刷新/重启。「改过配置但没复检」不是一次页面会话内的
	// 临时状态——刷新就抹掉，等于让旧结论重新以「已通过」的面貌出现。
	// 体检报告同理：重启后回 idle 会写「还没跑过体检」，把上一轮的证据弄丢。
	CfgStale   bool       `json:"cfg_stale"`
	CfgStaleAt string     `json:"cfg_stale_at,omitempty"`
	HCHistory  []HCReport `json:"hc_history,omitempty"`
}

const stateVersion = "1"

// UI 状态单独一把锁：setCfgStale 会在配置保存的热路径上调用，
// 不能顺手拿 cfgMu，更不能和 savePersistedState 的锁嵌套。
var (
	uiMu          sync.RWMutex
	cfgStaleState bool
	cfgStaleAt    string
)

func getCfgStale() (bool, string) {
	uiMu.RLock()
	defer uiMu.RUnlock()
	return cfgStaleState, cfgStaleAt
}

// setCfgStale 改完立刻落盘：30 秒的定时落盘太慢，用户保存后随手一刷新就丢了。
func setCfgStale(on bool) {
	uiMu.Lock()
	cfgStaleState = on
	if on {
		cfgStaleAt = time.Now().Format("2006-01-02 15:04:05")
	} else {
		cfgStaleAt = ""
	}
	uiMu.Unlock()
	savePersistedState()
}

// 抽成变量供单测替换：任何碰状态的测试都不该写坏用户真实的 state.json。
var statePath = func() string { return filepath.Join(exeDir(), "state.json") }

var stateSaveMu sync.Mutex

// savePersistedState 落盘当前状态（原子写）。
func savePersistedState() {
	stateSaveMu.Lock()
	defer stateSaveMu.Unlock()
	resultsMu.Lock()
	rs := make(map[string]CheckResult, len(results))
	for k, v := range results {
		rs[k] = v
	}
	resultsMu.Unlock()
	progMu.Lock()
	ps := make(map[string]ProgStatus, len(progState))
	for k, v := range progState {
		ps[k] = v
	}
	progMu.Unlock()
	chainMu.Lock()
	ci := chainInfo
	chainMu.Unlock()
	dbMu.Lock()
	dh := dbHealth
	dbMu.Unlock()
	svcMu.Lock()
	last := map[string]bool{}
	for k, v := range svcState {
		if v != nil {
			last[k] = v.Running
		}
	}
	svcMu.Unlock()

	uiMu.RLock()
	stale, staleAt := cfgStaleState, cfgStaleAt
	uiMu.RUnlock()
	hcMu.RLock()
	hist := append([]HCReport(nil), hcHistory...)
	hcMu.RUnlock()

	st := persistedState{
		Version:      stateVersion,
		SavedAt:      time.Now().Format("2006-01-02 15:04:05"),
		Results:      rs,
		Prog:         ps,
		Chain:        ci,
		DB:           dh,
		LastServices: last,
		CfgStale:     stale,
		CfgStaleAt:   staleAt,
		HCHistory:    hist,
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	path := statePath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// loadPersistedState 启动时恢复上次状态；服务状态一律按"已停止"处理并提示。
func loadPersistedState() {
	b, err := os.ReadFile(statePath())
	if err != nil {
		return
	}
	var st persistedState
	if json.Unmarshal(b, &st) != nil || st.Version != stateVersion {
		return
	}
	if len(st.Results) > 0 {
		resultsMu.Lock()
		results = st.Results
		resultsMu.Unlock()
	}
	if len(st.Prog) > 0 {
		progMu.Lock()
		progState = st.Prog
		progMu.Unlock()
	}
	chainMu.Lock()
	if st.Chain.Checked {
		chainInfo = st.Chain
	}
	chainMu.Unlock()
	dbMu.Lock()
	if st.DB.Msg != "" {
		dbHealth = st.DB
	}
	dbMu.Unlock()

	uiMu.Lock()
	cfgStaleState, cfgStaleAt = st.CfgStale, st.CfgStaleAt
	uiMu.Unlock()
	if len(st.HCHistory) > 0 {
		hcMu.Lock()
		hcHistory = append([]HCReport(nil), st.HCHistory...)
		// 恢复的报告必须标明它不是刚跑出来的：界面要写清「上次体检（时间）」，
		// 不能用一份旧证据冒充本次结论。
		hcCur = st.HCHistory[0]
		hcCur.Restored = true
		hcMu.Unlock()
	}
	// 服务状态不恢复为"运行中"：EnvKit 退出时 Job Object 已把它们带走，
	// 若照搬上次的 running 会显示一个并不存在的进程。
	running := map[string]bool{}
	for k, v := range st.LastServices {
		if v {
			running[k] = true
		}
	}
	if len(running) > 0 {
		names := make([]string, 0, len(running))
		for k := range running {
			names = append(names, k)
		}
		info(scSys, "状态", "已恢复上次的检测与操作结果（%s）。注意：上次运行中的服务（%v）已随 EnvKit 退出终止，需要重新启动",
			st.SavedAt, names)
	} else {
		info(scSys, "状态", "已恢复上次的检测与操作结果（%s）", st.SavedAt)
	}
}

// startStatePersister 后台定期落盘。
func startStatePersister() {
	go func() {
		for {
			time.Sleep(30 * time.Second)
			savePersistedState()
		}
	}()
}
