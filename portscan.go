package main

// 端口占用一键诊断：netstat 找出谁占着端口（PID + 进程名），可选结束进程。
// 只读扫描无副作用；结束进程是破坏性操作，走 428 二次确认 + 审计。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// systemExecutable 取 Windows 系统命令的绝对路径。
// 为什么需要它：EnvKit 可能被从受限 PowerShell / 安装器 / 计划任务拉起，
// 继承到的 PATH 里没有 System32，`exec.Command("netstat")` 会直接失败，
// 观察器于是"什么都看不见"——这不是观察逻辑错，是环境缺 PATH。
// 与 mysql.exe 的服务 ImagePath 回退同源：不能假设 PATH 完整。
func systemExecutable(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	if root := os.Getenv("SystemRoot"); root != "" {
		if filepath.Ext(name) == "" {
			name += ".exe"
		}
		p := filepath.Join(root, "System32", name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return name
}

// defaultScanPorts 默认诊断端口：常见前端 devServer + 本地后端。
// 前端 Vite（5173/4173）与 Next/CRA（3000）不在旧表里，导致"外部起在前端"
// 长期不可见；这里补成一张覆盖默认框架的常用表。真正的按项目候选由
// projectprofile.go 的 ProjectProfile 提供，这张表只用于"没有项目画像时"的兜底。
var defaultScanPorts = []int{8080, 8888, 5173, 4173, 3000}

type portOcc struct {
	Port  int    `json:"port"`
	State string `json:"state"` // LISTENING / FREE
	PID   int    `json:"pid,omitempty"`
	Name  string `json:"name,omitempty"`
	// 归属只做展示：managed / external / unknown / foreign。
	// 端口诊断与右侧栏看的是同一份观察结果，用户不必在两个页面对照 PID。
	Source  string `json:"source,omitempty"`
	Service string `json:"service,omitempty"`
}

// scanPortsOnce 执行一次端口扫描（同步返回结果，供 handler 与 AI 工具复用）。
func scanPortsOnce(ports []int) []portOcc {
	res := make([]portOcc, 0, len(ports))
	if len(ports) == 0 {
		return res
	}
	occ, names := scanAllListeners()
	for _, p := range ports {
		row := portOcc{Port: p, State: "FREE"}
		if pids, ok := occ[p]; ok && len(pids) > 0 {
			sort.Ints(pids)
			row.State = "LISTENING"
			row.PID = pids[0]
			row.Name = names[pids[0]]
		}
		res = append(res, row)
	}
	return res
}

// scanAllListeners 一次 netstat + 一次 tasklist，返回全部 TCP LISTENING 端口。
// 观察器每轮只调一次它，避免为每个候选端口重复起两个子进程。
func scanAllListeners() (map[int][]int, map[int]string) {
	occ := map[int][]int{}
	ctx, cancel := cmdContext(20 * time.Second)
	// 不带 -p：netstat -ano 同时给出 IPv4（TCP）与 IPv6（TCPv6 也标 "TCP"）
	// 的监听行。带 -p tcp 只看 IPv4——Node 17+ / vite 默认绑 [::1]，
	// 只扫 v4 会把"明明 ready 了"的前端判成端口未监听（真实缺陷，已踩）。
	out, err := exec.CommandContext(ctx, systemExecutable("netstat"), "-ano").CombinedOutput()
	cancel()
	if err == nil {
		occ = parseNetstatListeners(string(out))
	}
	return occ, pidNames()
}

// parseNetstatListeners 解析 netstat -ano 输出里的 TCP LISTENING 行，
// 返回 port -> pids。纯函数：单测可直接喂合成输出，不依赖真实机器状态。
//
// IPv4 行：TCP    0.0.0.0:3306    0.0.0.0:0    LISTENING    3308
// IPv6 行：TCP    [::1]:5173      [::]:0       LISTENING    24700
// 两种形态端口都在最后一个冒号之后（LastIndexByte 对 [::x]:port 同样成立）；
// UDP 行（4 列、协议名不是 TCP）与非 LISTENING 行一律跳过。
func parseNetstatListeners(out string) map[int][]int {
	occ := map[int][]int{}
	for _, ln := range strings.Split(out, "\n") {
		f := strings.Fields(strings.TrimSpace(ln))
		if len(f) < 5 || !strings.EqualFold(f[0], "TCP") || !strings.EqualFold(f[3], "LISTENING") {
			continue
		}
		idx := strings.LastIndexByte(f[1], ':')
		if idx < 0 {
			continue
		}
		p, err1 := strconv.Atoi(f[1][idx+1:])
		pid, err2 := strconv.Atoi(f[4])
		if err1 != nil || err2 != nil {
			continue
		}
		occ[p] = append(occ[p], pid)
	}
	return occ
}

// pidNames 一次 tasklist 拉全表，返回 pid -> 进程名。
func pidNames() map[int]string {
	m := map[int]string{}
	ctx, cancel := cmdContext(20 * time.Second)
	out, err := exec.CommandContext(ctx, systemExecutable("tasklist"), "/fo", "csv", "/nh").CombinedOutput()
	cancel()
	if err != nil {
		return m
	}
	for _, ln := range strings.Split(string(out), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || !strings.HasPrefix(ln, `"`) {
			continue
		}
		var rec []string
		if err := json.Unmarshal([]byte("["+ln+"]"), &rec); err != nil || len(rec) < 2 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(rec[1]))
		if err != nil {
			continue
		}
		m[pid] = rec[0]
	}
	return m
}

func handlePortScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	ports := cfg.Projects.ScanPorts
	if q := strings.TrimSpace(r.URL.Query().Get("ports")); q != "" {
		ports = nil
		for _, s := range strings.Split(q, ",") {
			if p, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && p >= 1 && p <= 65535 {
				ports = append(ports, p)
			}
		}
	}
	if len(ports) == 0 {
		ports = defaultScanPorts
	}
	if len(ports) > 32 {
		ports = ports[:32]
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	rows := scanPortsOnce(ports)
	annotatePortOwners(rows)
	_, _ = w.Write([]byte(mustJSON(map[string]any{"ports": rows})))
}

// annotatePortOwners 给端口扫描结果补上「这是谁」。
//
// 端口诊断原本只有 PID/进程名，用户还得回右侧栏对照；外部实例在服务卡上
// 显示「运行中 · 外部启动」，到这里却只剩一个 node.exe——两个面板各说各话。
// 这里复用唯一状态源的结论（managed）与观察器快照（external/unknown/foreign），
// 不重新判定身份，也不改停止权限。
func annotatePortOwners(rows []portOcc) {
	for i := range rows {
		if rows[i].State != "LISTENING" || rows[i].PID == 0 {
			continue
		}
		for _, key := range []string{"web", "backend"} {
			st := runtimeService(key)
			if st.Port != rows[i].Port || st.PID != rows[i].PID || st.Source == srcNone {
				continue
			}
			rows[i].Source = string(st.Source)
			rows[i].Service = key
			break
		}
	}
}

// portKillTask 结束占用端口的进程（供按钮复用）；同步等待。
func portKillTask(pid, port int) error {
	h, granted := beginTaskH("结束进程", true)
	if !granted {
		return fmt.Errorf("有任务正在执行，请稍候")
	}
	done := make(chan struct{})
	go func() {
		defer h.Done()
		defer close(done)
		fin := auditStart(actUser, "kill_process", fmt.Sprintf("port:%d pid:%d", port, pid), "")
		ctx, cancel := cmdContext(20 * time.Second)
		out, err := exec.CommandContext(ctx, "taskkill", "/PID", strconv.Itoa(pid), "/F").CombinedOutput()
		cancel()
		if err != nil {
			fail(scStart, "端口诊断", "结束 PID %d 失败：%v（%s）", pid, err, strings.TrimSpace(string(out)))
			fin(resFail, err.Error())
		} else {
			ok(scStart, "结束进程", "已结束 PID %d（端口 %d）", pid, port)
			fin(resOK, "")
		}
	}()
	<-done
	return nil
}

func handlePortKill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		PID     int    `json:"pid"`
		Port    int    `json:"port"`
		Confirm bool   `json:"confirm"`
		Lang    string `json:"lang"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	pid, port := body.PID, body.Port
	if pid <= 0 {
		http.Error(w, "缺少 PID", 400)
		return
	}
	// 安全护栏：不杀系统级进程（0 空闲 / 4 System），也不杀 EnvKit 自己
	if pid <= 4 {
		http.Error(w, "拒绝结束系统进程", 400)
		return
	}
	if pid == os.Getpid() {
		http.Error(w, "不能结束 EnvKit 自身", 400)
		return
	}
	if !body.Confirm {
		name := ""
		for _, row := range scanPortsOnce([]int{port}) {
			if row.PID == pid {
				name = row.Name
			}
		}
		auditNow(actUser, "kill_process", fmt.Sprintf("port:%d pid:%d", port, pid), "", resDenied, "等待二次确认")
		needConfirm(w, "kill_process", fmt.Sprintf("PID %d", pid), confirmReason(body.Lang,
			fmt.Sprintf("即将强制结束进程 %s（PID %d，占用端口 %d），该进程会被直接终止，未保存的数据可能丢失。请确认它不是系统关键进程。",
				nameOrUnknown(name), pid, port),
			fmt.Sprintf("About to force-kill process %s (PID %d, holding port %d). Unsaved data may be lost. "+
				"Make sure it is not a critical system process.", nameOrUnknown(name), pid, port)))
		return
	}
	if err := portKillTask(pid, port); err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"killed":true}`))
}

func nameOrUnknown(n string) string {
	if strings.TrimSpace(n) == "" {
		return "未知进程"
	}
	return n
}
