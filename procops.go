// procops.go —— 自有进程树停止 + 端口冲突安全处理（v3.0 T1.9，修 G7）
//
// 红线（plan §3.2.2，用户限定"自有进程树"）：
//   · 只停 EnvKit 自己派生的服务（activeProcs 内的 web/backend，经 stopByKey
//     走进程树 kill + Job Object 连带终止）；
//   · 第三方占用只报告（owner 进程名 + PID），绝不杀。
//
// 已删除（G7 级风险，plan 倾向删除）：
//   · 按项目目录 CommandLine 杀 node.exe/main.exe/go.exe 的 PowerShell 扫描——
//     命令行里出现项目目录路径，不等于这个进程是 EnvKit 派生的（用户自己开的
//     终端、别的 IDE、CI runner 都可能命中），误杀就是别人的数据丢失；
//   · cleanupByTasklist 按进程名杀 main.exe/go.exe——同理会误杀任何同名
//     第三方进程（go.exe 在开发机上太常见了）。
// 人工结束第三方进程的入口保留在端口诊断（portscan.go handlePortKill）：
// 有 PID 展示、有二次确认、有系统进程护栏、有审计——那才叫"人工处理"。

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// cleanupProcesses 停止 EnvKit 管理的服务（只动自有进程树），并报告端口上的
// 第三方占用（只报告，不杀）。返回 (停掉的服务数, 占用报告行, 错误)。
func cleanupProcesses() (int, []string, error) {
	stopped := 0
	for _, key := range []string{"web", "backend"} {
		procMu.Lock()
		_, running := activeProcs[key]
		procMu.Unlock()
		if running {
			stopByKey(scStart, key, key+"-start")
			stopped++
		}
	}
	// 候选端口：前后端各自一锅（内置 {8080, 8888} 共享），去重后一把查。
	ports := dedupInts(append(svcPortsFn("web"), svcPortsFn("backend")...))
	// 停旧之后等端口真正释放（TIME_WAIT：进程杀了不等于端口立即可监听）。
	// ownNames = 刚停掉的两类的进程名；keepNames 空——两个都停了，
	// 没有"理应继续占着"的自己人。
	own := append(svcExpectProcs("web"), svcExpectProcs("backend")...)
	_, foreign := waitPortsFree(ports, 3*time.Second, own, nil)
	var report []string
	for _, o := range foreign {
		report = append(report, fmt.Sprintf("端口 %d 被 %s（PID %d）占用", o.Port, nameOrUnknown(o.Name), o.PID))
	}
	return stopped, report, nil
}

func dedupInts(in []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, v := range in {
		if v > 0 && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// classifyOccupants 把候选端口上的 LISTENING 占用者分三类：
//   dying   —— 自家旧进程还没死透（ownNames 命中）：要等它释放；
//   keep    —— 另一个自有服务正活着占着（keepNames 命中）：共享候选端口里
//             的正常情况，不等也不报（否则 backend 在跑时重启 web，
//              web 候选里的 8888 会被当成"没释放"，重启永远超时）；
//   foreign —— 第三方：只报告，不杀。
// 名字查不到的占用者按"未知"放行——与 svcOwnerAcceptable 同一立场：
// 查不到就下结论，冤枉一个好进程比漏报一个坏进程严重。
func classifyOccupants(ports []int, ownNames, keepNames []string) (dying, foreign []portOcc) {
	for _, o := range scanPortsOnce(ports) {
		if o.State != "LISTENING" || strings.TrimSpace(o.Name) == "" {
			continue
		}
		switch {
		case matchProcName(o.Name, ownNames):
			dying = append(dying, o)
		case matchProcName(o.Name, keepNames):
			// 另一个自有服务：正常占着，不管
		default:
			foreign = append(foreign, o)
		}
	}
	return dying, foreign
}

// waitPortsFree 等端口真正释放（plan §3.2.2：等端口真释放，对齐 fault_inject
// 踩过的 TIME_WAIT 坑——进程杀了不等于端口立即可用，盲目 sleep 只会得到
// "新进程起在半死状态"，或者把第三方占用误判成自己没停干净）。
// 返回 (自家旧进程是否已释放, 第三方占用列表)。ports 为空 = 无需等待。
func waitPortsFree(ports []int, timeout time.Duration, ownNames, keepNames []string) (bool, []portOcc) {
	if len(ports) == 0 {
		return true, nil
	}
	deadline := time.Now().Add(timeout)
	for {
		dying, foreign := classifyOccupants(ports, ownNames, keepNames)
		if len(dying) == 0 {
			return true, foreign
		}
		if !time.Now().Before(deadline) {
			return false, foreign // 旧进程没停透：不猜，让调用方报告
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// restartWaitPorts 重启前等自家旧进程释放端口（plan §3.2.2：组锁内先停旧、
// 等端口真释放再起新）。返回空串 = 可以起新进程；非空 = 不起，并说清原因
// （旧进程没停透 / 第三方占用及 owner）。第三方占用只报告——绝不杀，
// 杀错进程比重启失败严重得多。
func restartWaitPorts(key string) string {
	other := "web"
	if key == "web" {
		other = "backend"
	}
	released, foreign := waitPortsFree(svcPortsFn(key), 10*time.Second, svcExpectProcs(key), svcExpectProcs(other))
	if !released {
		return "旧进程已停止但端口仍未释放（进程树可能没杀透，或端口处于 TIME_WAIT）：请稍候重试，或到「端口诊断」查看占用者"
	}
	if len(foreign) > 0 {
		parts := make([]string, 0, len(foreign))
		for _, o := range foreign {
			parts = append(parts, fmt.Sprintf("端口 %d 被 %s（PID %d）占用", o.Port, nameOrUnknown(o.Name), o.PID))
		}
		return "端口被其他进程占用：" + strings.Join(parts, "；") +
			"——第三方进程不自动结束，请人工确认后在「端口诊断」处理"
	}
	return ""
}

// aiCleanupProcesses 供 AI 工具与按钮共用的入口（带日志）。
// v3.0 T1.9：语义从"清理进程"改为"停止 EnvKit 管理的服务 + 报告端口占用"，
// 不再结束任何第三方进程——误杀比杀不掉严重得多（plan §3.2.2 红线）。
func aiCleanupProcesses() (string, error) {
	// v3.0 T1.2：清理动的是前后端两组资源，成组原子获取
	h, granted := beginTaskHG("停止前后端服务", []string{grpWeb, grpBackend}, true)
	if !granted {
		return "", fmt.Errorf("%s", busyReason("web+backend"))
	}
	defer h.Done()
	fin := auditStart(actUser, "cleanup_processes", "web/backend", "")
	info(scStart, "清理", "停止 EnvKit 管理的前后端服务，并检查端口占用...")
	stopped, occupants, err := cleanupProcesses()
	if err != nil {
		fail(scStart, "清理", "%v", err)
		fin(resFail, err.Error())
		return "", err
	}
	fin(resOK, fmt.Sprintf("停 %d 个服务，%d 个端口被占用", stopped, len(occupants)))
	msg := fmt.Sprintf("已停止 EnvKit 管理的前后端服务（%d 个在运行）", stopped)
	if len(occupants) == 0 {
		msg += "；候选端口均未被占用"
	} else {
		msg += "；" + strings.Join(occupants, "；") + "——第三方进程不自动结束，请人工确认后在「端口诊断」处理"
	}
	ok(scStart, "清理", "%s", msg)
	return msg, nil
}

func handleCleanupProcs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	go func() { _, _ = aiCleanupProcesses() }()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"started":true}`))
}

// handleBreakerReset POST：人工复位断路器（plan §3.2.3 的"人工复位"出口）。
// 刻意不做成 AI 工具：AI 被熔断拒绝时该做的是升级给人，而不是自己拆保险——
// 人工复位的入口只能是人点的按钮。服务名限定 web/backend（链端走
// svcBreakerReset("chain") 由守护侧同一状态机覆盖，这里不开放）。
func handleBreakerReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Service string `json:"service"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	svc := strings.TrimSpace(body.Service)
	if svc != "web" && svc != "backend" {
		http.Error(w, "service 必须是 web/backend", 400)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if tripped, _, _ := svcBreakerStatus(svc); !tripped {
		_, _ = w.Write([]byte(`{"ok":true,"reset":false,"msg":"当前不在熔断态，无需复位"}`))
		return
	}
	svcBreakerReset(svc)
	_, _ = w.Write([]byte(`{"ok":true,"reset":true}`))
}
