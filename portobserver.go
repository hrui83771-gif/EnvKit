package main

// portobserver.go —— 端口观察器（只读）
//
// 解决三个此前不存在的事实：
//  1. 用户在 EnvKit 之外启动了服务，右侧栏仍显示"未启动"；
//  2. 用户在外部起了服务后再点启动，EnvKit 会重复派生并撞端口；
//  3. 端口候选是前后端共享的一锅数字，无法判断"这个 8080 到底是谁"。
//
// 本文件只做**观察**：netstat、tasklist、gopsutil 命令行/父链、HTTP 指纹。
// 不结束任何进程，不写 svcState，不自动纳管。停止权限仍只属于 EnvKit 自己
// 派生的 activeProcs（见 procops.go 红线）。

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

type observedService struct {
	Service        string   `json:"service"`
	State          string   `json:"state"` // external | unknown | foreign
	Port           int      `json:"port,omitempty"`
	PID            int      `json:"pid,omitempty"`
	OwnerName      string   `json:"owner_name,omitempty"`
	Health         string   `json:"health,omitempty"` // ok | auth | fail | unknown
	PortSource     string   `json:"port_source,omitempty"`
	PortConfidence int      `json:"port_confidence,omitempty"`
	Since          string   `json:"since,omitempty"`
	ObservedAt     string   `json:"observed_at,omitempty"`
	Evidence       []string `json:"evidence,omitempty"`
}

type observedInternal struct {
	observedService
	misses int
}

var (
	observedOnce  sync.Once
	observedMu    sync.RWMutex
	observedState = map[string]observedInternal{}
	observedKick  = make(chan struct{}, 1)

	lastUIPoll atomic.Int64
)

func markUIPoll() {
	lastUIPoll.Store(time.Now().UnixNano())
}

func uiRecentlyActive() bool {
	t := lastUIPoll.Load()
	if t <= 0 {
		return false
	}
	return time.Since(time.Unix(0, t)) < 20*time.Second
}

func startPortObserver() {
	observedOnce.Do(func() {
		go portObserverLoop()
	})
}

func kickPortObserver() {
	select {
	case observedKick <- struct{}{}:
	default:
	}
}

func portObserverLoop() {
	interval := func() time.Duration {
		if uiRecentlyActive() {
			return 5 * time.Second
		}
		return 15 * time.Second
	}
	timer := time.NewTimer(interval())
	defer timer.Stop()
	observePortsNow()
	for {
		select {
		case <-timer.C:
		case <-observedKick:
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		}
		observePortsNow()
		timer.Reset(interval())
	}
}

// observeSnapshot 返回某服务当前观察结论；没有结论时 ok=false。
func observeSnapshot(target string) (observedService, bool) {
	observedMu.RLock()
	defer observedMu.RUnlock()
	v, ok := observedState[target]
	if !ok || v.State == "" {
		// 防御：state 为空但记录还在，视为无结论。
		return observedService{}, false
	}
	return v.observedService, true
}

func observePortsNow() {
	p := currentProjectProfile()
	targets := []string{"web", "backend"}
	occ, names := scanAllListeners()

	prev := map[string]observedInternal{}
	observedMu.RLock()
	for _, t := range targets {
		if v, ok := observedState[t]; ok {
			prev[t] = v
		}
	}
	observedMu.RUnlock()

	next := map[string]observedInternal{}
	for _, target := range targets {
		svc := p.service(target)
		if strings.TrimSpace(svc.Dir) == "" {
			continue
		}
		best := observedService{}
		bestScore := -1
		managedSeen := false
		tested := 0
		candidates := svcPortHintList(target)
		if len(candidates) == 0 {
			continue
		}
		expect := svcExpectProcs(target)
		for _, hint := range candidates {
			pids, ok := occ[hint.Port]
			if !ok || len(pids) == 0 {
				continue
			}
			sort.Ints(pids)
			pid := pids[0]
			if isManagedPortPID(target, pid) {
				// EnvKit 自己的服务：交给 svcState/复验，不在这里重复认领。
				managedSeen = true
				continue
			}
			if tested >= 4 {
				break
			}
			tested++
			ob, score := classifyOccupant(target, hint, pid, names[pid], expect, svc)
			if hint.Confidence < 60 && ob.State != "external" {
				// 低置信兜底端口上的"别人/身份不明"不当作本项目状态，
				// 否则同一个 Vite 会把后端染成 unknown，8080 上任何应用都会染红。
				// 只要 external（有项目目录/框架强指纹）仍保留——外部实例可能正好跑在默认端口。
				continue
			}
			if score > bestScore {
				best, bestScore = ob, score
			}
		}

		if managedSeen {
			// 当前由 EnvKit 管理：清掉此前记录的外部实例，避免切换后残留。
			continue
		}

		// 端口漂移：已经确认的外部 PID 换了端口，仍按 PID 跟过去。
		if best.State == "" {
			if old, ok := prev[target]; ok && old.State == "external" && old.PID > 0 {
				if port := firstPortForPID(occ, old.PID); port > 0 {
					best = old.observedService
					best.Port = port
					best.ObservedAt = nowText()
					best.Evidence = append(best.Evidence, fmt.Sprintf("port_drift=%d→%d（同一 PID 换端口）", old.Port, port))
					probe, code := probeServicePort(port, svc.HealthPaths)
					best.Health = healthFromProbe(probe, code, target)
				}
			}
		}

		if best.State == "" {
			// 没看到：保留一轮，连续两轮消失才清。避免重启时红绿闪烁。
			if old, ok := prev[target]; ok {
				old.misses++
				if old.misses < 2 {
					next[target] = old
				}
			}
			continue
		}
		best.ObservedAt = nowText()
		next[target] = observedInternal{observedService: best}
	}

	observedMu.Lock()
	observedState = next
	observedMu.Unlock()
}

func nowText() string { return time.Now().Format("2006-01-02 15:04:05") }

func firstPortForPID(occ map[int][]int, pid int) int {
	var ports []int
	for port, pids := range occ {
		for _, p := range pids {
			if p == pid {
				ports = append(ports, port)
				break
			}
		}
	}
	if len(ports) == 0 {
		return 0
	}
	sort.Ints(ports)
	return ports[0]
}

func isManagedPortPID(target string, pid int) bool {
	svcMu.Lock()
	var running bool
	var root int
	if s := svcState[target]; s != nil {
		running, root = s.Running, s.PID
	}
	svcMu.Unlock()
	if !running || root <= 0 {
		return false
	}
	if pid == root {
		return true
	}
	// 端口占用者通常是子进程（node/java/main），沿父链找 EnvKit 记录的根。
	cur := pid
	for i := 0; i < 8 && cur > 0; i++ {
		meta := procMeta(cur)
		if meta.Parent <= 0 {
			break
		}
		cur = meta.Parent
		if cur == root {
			return true
		}
	}
	return false
}

// ---------- 进程信息与分类 ----------

type procDetails struct {
	Name    string
	Cmdline string
	Exe     string
	Parent  int
	Create  int64
	At      time.Time
}

var (
	procCacheMu sync.Mutex
	procCache   = map[int]procDetails{}
)

func procMeta(pid int) procDetails {
	procCacheMu.Lock()
	if v, ok := procCache[pid]; ok && time.Since(v.At) < 30*time.Second {
		procCacheMu.Unlock()
		return v
	}
	procCacheMu.Unlock()

	v := procDetails{At: time.Now()}
	if pid <= 0 {
		return v
	}
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		procCacheMu.Lock()
		procCache[pid] = v
		procCacheMu.Unlock()
		return v
	}
	if n, err := p.Name(); err == nil {
		v.Name = n
	}
	if c, err := p.Cmdline(); err == nil {
		v.Cmdline = c
	}
	if e, err := p.Exe(); err == nil {
		v.Exe = e
	}
	if pp, err := p.Parent(); err == nil && pp != nil {
		v.Parent = int(pp.Pid)
	}
	if ct, err := p.CreateTime(); err == nil && ct > 0 {
		v.Create = ct
	}

	procCacheMu.Lock()
	procCache[pid] = v
	procCacheMu.Unlock()
	return v
}

func procChain(pid int) []procDetails {
	var out []procDetails
	cur := pid
	for i := 0; i < 6 && cur > 0; i++ {
		m := procMeta(cur)
		if m.Name == "" && m.Cmdline == "" && m.Exe == "" {
			break
		}
		out = append(out, m)
		if m.Parent <= 0 || m.Parent == cur {
			break
		}
		cur = m.Parent
	}
	return out
}

func classifyOccupant(target string, hint svcPortHint, pid int, name string, expect []string, svc projectServiceProfile) (observedService, int) {
	meta := procMeta(pid)
	if name == "" {
		name = meta.Name
	}
	chain := procChain(pid)
	cmdAll := strings.ToLower(strings.Join(func() []string {
		var s []string
		for _, m := range chain {
			s = append(s, m.Cmdline, m.Exe, m.Name)
		}
		return s
	}(), " "))

	family := matchProcName(name, expect) || matchProcName(meta.Name, expect)
	dir := strings.ToLower(strings.ReplaceAll(normPath(svc.Dir), "/", `\`))
	cmdNorm := strings.ToLower(strings.ReplaceAll(cmdAll, "/", `\`))
	dirMatch := dir != "" && strings.Contains(cmdNorm, dir)
	marker := occupantMarker(target, cmdAll, svc)

	probe, code := "", 0
	if family || dirMatch || marker {
		probe, code = probeServicePort(hint.Port, svc.HealthPaths)
	}
	return classifyEvidence(target, hint, pid, name, meta, family, dirMatch, marker, svc, probe, code)
}

// classifyEvidence 是纯判定：进程命令行、父链、HTTP 指纹进来，状态与分数出去。
// 抽出来是为了让"同名 java/node 到底是不是本项目"能被单测覆盖，不用真的起进程。
func classifyEvidence(target string, hint svcPortHint, pid int, name string, meta procDetails, family, dirMatch, marker bool, svc projectServiceProfile, probe string, code int) (observedService, int) {
	health := healthFromProbe(probe, code, target)

	score := 0
	if dirMatch {
		score += 40
	}
	if marker {
		score += 25
	}
	if family {
		score += 20
	}
	if hint.Confidence >= 75 {
		score += 20
	} else if hint.Confidence >= 60 {
		score += 8
	}
	switch health {
	case "ok":
		score += 10
	case "auth":
		score += 5
	}

	state := "foreign"
	switch {
	case score >= 60:
		state = "external"
	case score >= 35:
		state = "unknown"
	}

	ev := []string{
		fmt.Sprintf("port=%d LISTENING", hint.Port),
		fmt.Sprintf("pid=%d owner=%s", pid, nonEmptyStr(name, "未知")),
		"port_source=" + hint.Source + "(" + strconv.Itoa(hint.Confidence) + ")",
	}
	if dirMatch {
		ev = append(ev, "fingerprint=进程命令行含项目目录")
	}
	if marker {
		ev = append(ev, "fingerprint=框架标记")
	}
	if family {
		ev = append(ev, "fingerprint=进程族匹配")
	}
	if probe != "" {
		ev = append(ev, "health="+probe)
	}
	if meta.Cmdline != "" {
		ev = append(ev, "cmd="+firstChars(meta.Cmdline, 220))
	}
	since := nowText()
	if meta.Create > 0 {
		since = time.UnixMilli(meta.Create).Format("15:04:05")
	}

	return observedService{
		Service:        target,
		State:          state,
		Port:           hint.Port,
		PID:            pid,
		OwnerName:      name,
		Health:         health,
		PortSource:     hint.Source,
		PortConfidence: hint.Confidence,
		Since:          since,
		ObservedAt:     nowText(),
		Evidence:       ev,
	}, score
}

func occupantMarker(target, cmdAll string, svc projectServiceProfile) bool {
	switch target {
	case "web":
		return strings.Contains(cmdAll, "vite") || strings.Contains(cmdAll, "vue-cli-service") ||
			strings.Contains(cmdAll, "next") || strings.Contains(cmdAll, "nuxt") ||
			strings.Contains(cmdAll, "node_modules")
	case "backend":
		switch svc.Kind {
		case "maven":
			artifact := strings.ToLower(svc.ProjectLabel)
			return strings.Contains(cmdAll, `target\classes`) || strings.Contains(cmdAll, "target/classes") ||
				strings.Contains(cmdAll, `target\`) || (artifact != "" && strings.Contains(cmdAll, artifact))
		default:
			return strings.Contains(cmdAll, "go-build") || strings.Contains(cmdAll, "main.exe") ||
				strings.Contains(cmdAll, "go.exe")
		}
	}
	return false
}

func firstChars(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---------- HTTP 指纹 ----------

var obsHTTP = &http.Client{
	Timeout: 1200 * time.Millisecond,
	Transport: &http.Transport{
		Proxy:           nil,
		MaxIdleConns:    4,
		IdleConnTimeout: 20 * time.Second,
	},
}

func probeServicePort(port int, paths []string) (string, int) {
	if port <= 0 {
		return "", 0
	}
	if len(paths) == 0 {
		paths = []string{"/"}
	}
	for _, path := range paths {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		for _, host := range []string{"127.0.0.1", "::1"} {
			url := "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + path
			resp, err := obsHTTP.Get(url)
			if err != nil {
				continue
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
			_ = resp.Body.Close()
			snippet := strings.ReplaceAll(strings.ReplaceAll(string(body), "\r", ""), "\n", " ")
			return fmt.Sprintf("HTTP %d · %s", resp.StatusCode, firstChars(snippet, 120)), resp.StatusCode
		}
	}
	return "", 0
}

func healthFromProbe(probe string, code int, target string) string {
	if code == 0 {
		return "unknown"
	}
	if code >= 500 {
		return "fail"
	}
	if code == 401 || code == 403 {
		return "auth"
	}
	if code >= 200 && code < 500 {
		return "ok"
	}
	return "unknown"
}
