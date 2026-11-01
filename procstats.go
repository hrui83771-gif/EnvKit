// procstats.go —— 进程资源采样（gopsutil）
//
// 解决的问题：EnvKit 对托管服务的认知原本只有两态——「进程在」和「端口通」。
// 服务吃了 1.2G 内存、CPU 打满、句柄涨到几万，界面上一片绿。这里补上第三个维度：
// 真实占用。
//
// 三条设计约束：
//  1. 只读、只报事实。判据只输出「相对窗口起点涨了多少 MB」这种可复算的数字，
//     不输出「疑似内存泄漏」这种结论——结论该由人（或 AI）看着数字下。
//  2. HTTP 不采样。采样在后台 ticker 里跑，快照走 atomic.Value；
//     请求进来只做一次原子读，绝不阻塞在 gopsutil 上（它有系统调用，慢的时候是几十毫秒）。
//  3. 采样对象是「这一轮真正在跑的东西」：EnvKit 自身 + svcState 里 Running 的条目。
//     不按进程名全局扫——那会把用户手起的同名进程也算进来，口径就错了。
package main

import (
	"math"
	"net/http"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

const (
	procStatsEvery   = 2 * time.Second // 采样间隔
	procStatsHistLen = 60              // 每个进程保留多少点（2s × 60 = 2 分钟走势）
	procStatsMinSpan = 60 * time.Second
	procStatsGrowMB  = 50.0 // 增长判据下限（MB）
	procStatsGrowPct = 0.30 // 增长判据比例（相对窗口起点）
)

// procStat 单个进程的一帧画像（对外 JSON）。
type procStat struct {
	Key     string  `json:"key"`   // svc key（web/backend…）或 "self"
	Label   string  `json:"label"` // 展示名（进程名或中文名）
	PID     int32   `json:"pid"`
	Alive   bool    `json:"alive"`
	CPUPct  float64 `json:"cpu_pct"` // 占单核百分比（多核可 >100，前端显示时按核数折算）
	RSSMB   float64 `json:"rss_mb"`
	Threads int32   `json:"threads,omitempty"`
	Handles int32   `json:"handles,omitempty"`
	Uptime  int64   `json:"uptime_sec,omitempty"`
	// GrowthMB 窗口内的内存增量（正数=涨）。窗口不足 procStatsMinSpan 时为 0。
	GrowthMB float64 `json:"growth_mb"`
	// Growing 仅表示「涨得快」，是判据不是结论。
	Growing bool `json:"growing"`
	// Hist 最近 N 点的 RSS（MB），供前端画 sparkline。
	Hist []float64 `json:"hist,omitempty"`
}

// procStatsSnap 一次完整快照。
type procStatsSnap struct {
	TS    int64      `json:"ts"`
	CPUs  int        `json:"cpus"`
	Procs []procStat `json:"procs"`
}

// procTracker 单个 PID 的采样状态。
type procTracker struct {
	proc   *process.Process
	key    string
	label  string
	hist   []float64 // RSS MB，环形追加
	firstT time.Time
}

var (
	procStatsOnce sync.Once
	procStatsVal  atomic.Value // 存 *procStatsSnap（只写指针，读完即丢）
	procStatsMu   sync.Mutex   // 保护 procTrack
	procTrack     = map[int32]*procTracker{}
)

// procStatsStart 启动后台采样（幂等）。
func procStatsStart() {
	procStatsOnce.Do(func() {
		procStatsVal.Store(&procStatsSnap{TS: time.Now().Unix(), CPUs: runtime.NumCPU()})
		go procStatsLoop()
	})
}

func procStatsLoop() {
	t := time.NewTicker(procStatsEvery)
	defer t.Stop()
	procStatsCollect() // 立刻采一次：界面首次打开就有数，不用等 2 秒
	for range t.C {
		procStatsCollect()
	}
}

// procStatsTargets 列出本轮要采的 (pid, key, label)。
func procStatsTargets() []struct {
	PID   int32
	Key   string
	Label string
} {
	out := []struct {
		PID   int32
		Key   string
		Label string
	}{{int32(os.Getpid()), "self", "EnvKit"}}
	svcMu.Lock()
	for k, s := range svcState {
		if s != nil && s.Running && s.PID > 0 {
			out = append(out, struct {
				PID   int32
				Key   string
				Label string
			}{int32(s.PID), k, k})
		}
	}
	svcMu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// pushHist 追加一个 RSS 采样点并保持长度上限。
// 单独成方法是为了让"窗口不会无限长"这条能被单测直接钉住（而不是在采集函数里埋着）。
func (t *procTracker) pushHist(mb float64) {
	t.hist = append(t.hist, mb)
	if len(t.hist) > procStatsHistLen {
		t.hist = t.hist[len(t.hist)-procStatsHistLen:]
	}
}

// procStatsCollect 采一帧并原子发布。
func procStatsCollect() {
	tg := procStatsTargets()
	keep := make(map[int32]bool, len(tg))
	rows := make([]procStat, 0, len(tg))

	procStatsMu.Lock()
	for _, t := range tg {
		keep[t.PID] = true
		tr := procTrack[t.PID]
		if tr == nil {
			p, err := process.NewProcess(t.PID)
			if err != nil {
				continue // 进程已经不在了，直接跳过（下一轮列表里也会消失）
			}
			tr = &procTracker{proc: p, key: t.Key, label: t.Label, firstT: time.Now()}
			procTrack[t.PID] = tr
		}
		// key/label 可能因为重启换了（PID 变了就是新 tracker，这里只防同 PID 复用）
		tr.key, tr.label = t.Key, t.Label

		row := procStat{Key: t.Key, Label: t.Label, PID: t.PID, Alive: true}

		// v4 的 CPUPercent 无参：用"上次调用时间"自己算差异，立即返回不阻塞。
		// 首次调用没有基线，返回 0 —— 如实报 0，不猜。
		if v, err := tr.proc.CPUPercent(); err == nil && !math.IsNaN(v) {
			row.CPUPct = math.Round(v*10) / 10
		}
		if mi, err := tr.proc.MemoryInfo(); err == nil && mi != nil {
			mb := float64(mi.RSS) / (1 << 20)
			row.RSSMB = math.Round(mb*10) / 10
			tr.pushHist(row.RSSMB)
		} else {
			row.Alive = false // 进程在采样间隙退出了：如实说，不填 0 冒充实测
		}
		if n, err := tr.proc.NumThreads(); err == nil {
			row.Threads = n
		}
		// Windows 上 gopsutil 的 NumFDs 走 GetProcessHandleCount，语义就是句柄数。
		if n, err := tr.proc.NumFDs(); err == nil {
			row.Handles = n
		}
		if ms, err := tr.proc.CreateTime(); err == nil && ms > 0 {
			if up := time.Since(time.UnixMilli(ms)); up > 0 {
				row.Uptime = int64(up.Seconds())
			}
		}
		row.GrowthMB, row.Growing = procGrowthHint(tr.hist, time.Since(tr.firstT))
		row.Hist = append([]float64(nil), tr.hist...) // 拷贝：快照发出去之后 hist 还会增长
		rows = append(rows, row)
	}
	// 清掉已经不在目标里的 PID（服务停了/重启了），否则 hist 会一直占着
	for pid := range procTrack {
		if !keep[pid] {
			delete(procTrack, pid)
		}
	}
	procStatsMu.Unlock()

	procStatsVal.Store(&procStatsSnap{TS: time.Now().Unix(), CPUs: runtime.NumCPU(), Procs: rows})
}

// procGrowthHint 从 RSS 历史里算「涨了多少、涨得快不快」。
//
// 判据写死成可复算的形式：窗口跨度够（≥ procStatsMinSpan）且
// 增量 > max(50MB, 起点 30%) 才置 Growing。两条同时满足才算，
// 避免小进程正常抖动（30MB→45MB）被报成异常。
func procGrowthHint(hist []float64, span time.Duration) (grownMB float64, growing bool) {
	if len(hist) < 2 {
		return 0, false
	}
	first, last := hist[0], hist[len(hist)-1]
	delta := math.Round((last-first)*10) / 10
	if span < procStatsMinSpan {
		return delta, false // 窗口太短：给出增量但不下判断
	}
	threshold := math.Max(procStatsGrowMB, first*procStatsGrowPct)
	return delta, delta > threshold
}

// procStatsSnapshot 原子读当前快照（没有采样过则返回空快照，不现采）。
func procStatsSnapshot() *procStatsSnap {
	if v := procStatsVal.Load(); v != nil {
		if s, ok := v.(*procStatsSnap); ok && s != nil {
			return s
		}
	}
	return &procStatsSnap{TS: time.Now().Unix(), CPUs: runtime.NumCPU()}
}

func handleProcStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(mustJSON(procStatsSnapshot())))
}
