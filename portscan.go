package main

// 端口占用一键诊断：netstat 找出谁占着端口（PID + 进程名），可选结束进程。
// 只读扫描无副作用；结束进程是破坏性操作，走 428 二次确认 + 审计。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// defaultScanPorts 默认诊断端口：前端 devServer / 本地后端
var defaultScanPorts = []int{8080, 8888}

type portOcc struct {
	Port  int    `json:"port"`
	State string `json:"state"` // LISTENING / FREE
	PID   int    `json:"pid,omitempty"`
	Name  string `json:"name,omitempty"`
}

// scanPortsOnce 执行一次端口扫描（同步返回结果，供 handler 与 AI 工具复用）。
func scanPortsOnce(ports []int) []portOcc {
	res := make([]portOcc, 0, len(ports))
	if len(ports) == 0 {
		return res
	}
	occ := map[int][]int{} // port -> pids
	ctx, cancel := cmdContext(20 * time.Second)
	// 不带 -p：netstat -ano 同时给出 IPv4（TCP）与 IPv6（TCPv6 也标 "TCP"）
	// 的监听行。带 -p tcp 只看 IPv4——Node 17+ / vite 默认绑 [::1]，
	// 只扫 v4 会把"明明 ready 了"的前端判成端口未监听（真实缺陷，已踩）。
	out, err := exec.CommandContext(ctx, "netstat", "-ano").CombinedOutput()
	cancel()
	if err == nil {
		occ = parseNetstatListeners(string(out))
	}
	names := pidNames()
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
	out, err := exec.CommandContext(ctx, "tasklist", "/fo", "csv", "/nh").CombinedOutput()
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
	_, _ = w.Write([]byte(mustJSON(map[string]any{"ports": scanPortsOnce(ports)})))
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
