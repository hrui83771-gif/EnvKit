package main

// procstats_test.go —— 进程资源采样的三条硬约束：
//   1. 采的是"这一轮真正在跑的东西"（自己必须在列表里，PID 必须对得上）；
//   2. 增长判据是可复算的（窗口不够就不下判断，小进程抖动不算异常）；
//   3. 历史窗口有上限（否则常驻几天后 sparkline 数组会一直长）。

import (
	"os"
	"runtime"
	"testing"
	"time"
)

func findProc(t *testing.T, key string) procStat {
	t.Helper()
	for _, p := range procStatsSnapshot().Procs {
		if p.Key == key {
			return p
		}
	}
	t.Fatalf("快照里找不到 %q，实际有：%v", key, procStatsSnapshot().Procs)
	return procStat{}
}

func TestProcStatsSamplesSelf(t *testing.T) {
	procStatsStart() // 幂等；即便别的测试先启动过也无害
	procStatsCollect()

	snap := procStatsSnapshot()
	if snap.CPUs != runtime.NumCPU() {
		t.Fatalf("核数应为 %d，得到 %d", runtime.NumCPU(), snap.CPUs)
	}
	self := findProc(t, "self")
	if int(self.PID) != os.Getpid() {
		t.Fatalf("self 的 PID 应为 %d，得到 %d", os.Getpid(), self.PID)
	}
	if !self.Alive {
		t.Fatal("self 应标记为 alive")
	}
	// 关键断言：RSS 必须是真读出来的数。若 MemoryInfo 失败又被填 0，
	// 界面上就会显示"EnvKit 占 0MB"——比不显示更坏。
	if self.RSSMB <= 0 {
		t.Fatalf("自身 RSS 应 > 0，得到 %v（说明 MemoryInfo 没取到却被当成 0 上报）", self.RSSMB)
	}
	if self.Threads <= 0 {
		t.Fatalf("自身线程数应 > 0，得到 %d", self.Threads)
	}
}

func TestProcStatsHistBounded(t *testing.T) {
	tr := &procTracker{}
	for i := 0; i < procStatsHistLen*3; i++ {
		tr.pushHist(float64(i))
	}
	if len(tr.hist) != procStatsHistLen {
		t.Fatalf("历史长度应被限制在 %d，得到 %d", procStatsHistLen, len(tr.hist))
	}
	// 保留的必须是**最近**的 N 个点，而不是最早的 N 个
	if got, want := tr.hist[len(tr.hist)-1], float64(procStatsHistLen*3-1); got != want {
		t.Fatalf("最后一个点应为 %v，得到 %v", want, got)
	}
}

func TestProcGrowthHint(t *testing.T) {
	cases := []struct {
		name     string
		hist     []float64
		span     time.Duration
		wantGrow bool
		wantMB   float64
	}{
		{"窗口太短只给增量不下判断", []float64{100, 500}, 10 * time.Second, false, 400},
		{"涨得快", []float64{100, 100, 200}, 2 * time.Minute, true, 100},
		{"小进程正常抖动不算", []float64{30, 45}, 2 * time.Minute, false, 15},
		{"下降不是增长", []float64{300, 120}, 2 * time.Minute, false, -180},
		{"只有一个点", []float64{100}, 2 * time.Minute, false, 0},
		{"刚好到阈值不算（要大于）", []float64{100, 150}, 2 * time.Minute, false, 50},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mb, grow := procGrowthHint(c.hist, c.span)
			if grow != c.wantGrow {
				t.Fatalf("Growing 应为 %v，得到 %v（增量 %v）", c.wantGrow, grow, mb)
			}
			if mb != c.wantMB {
				t.Fatalf("增量应为 %v，得到 %v", c.wantMB, mb)
			}
		})
	}
}

// 采样两次之后，旧快照里的 hist 长度不许被后来的采样改掉
// （采样是原地 append，快照必须持有副本，否则前端画出来的曲线会跳变）。
func TestProcStatsSnapshotHistIsCopy(t *testing.T) {
	procStatsCollect()
	first := findProc(t, "self")
	n1 := len(first.Hist)
	procStatsCollect()
	if len(first.Hist) != n1 {
		t.Fatalf("旧快照的 hist 长度被后续采样改动了：%d → %d", n1, len(first.Hist))
	}
	if n2 := len(findProc(t, "self").Hist); n2 <= n1 {
		t.Fatalf("新快照应比旧快照多一个点：%d → %d", n1, n2)
	}
}
