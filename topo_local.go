// topo_local.go —— LocalProc 适配器（v3.0 第二阶段 T2.3）：本机进程（web / backend）
//
// 收编的现有能力（plan §4.1 的 LocalProc 适配器条目）：
//
//	Observe ← svcAlive + firstListeningPortExcept + portOwner（verify.go）
//	Verify  ← verifyService（verify.go）
//	Execute ← opStartWeb / opStartBackend（program.go，内部过 launch.go 安全规则）
//
// **适配器不重写采集与复验逻辑**，只是把它们接到拓扑接口上。
// 重写一遍就等于凭空多出一条可能与原逻辑漂移的实现——
// 而漂移的方向永远是"新实现悄悄变松"。
package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// LocalProcAdapter 本机进程适配器。无状态，可共享一个实例。
type LocalProcAdapter struct{}

// localProcTargets 本适配器接管的 service id。
var localProcTargets = map[string]bool{"web": true, "backend": true}

// Observe 采集本机服务的健康事实。
//
// 三种结局必须分开（ErrKind 就是给上层做这个分辨的）：
//
//	ok              进程活着且端口在听、owner 符合预期
//	not_listening   进程活着但端口没听（起慢了/起失败了）
//	owner_mismatch  端口在听但占用者不是本服务（第三方占用/僵尸监听者）
//	probe_no_clue   没有候选端口线索——**这不是"服务坏了"，是"判断不了"**。
//	                 混在一起会让上层对健康服务发起恢复，正是误恢复的来源。
//
// 判定表在 classifyLocalProc（纯函数）里，Observe 只负责取事实——
// 这样整张决策表能在无环境依赖下单测，而不是只能靠真机。
func (LocalProcAdapter) Observe(svc Service) Observation {
	obs := Observation{Service: svc.ID, At: time.Now()}
	if !localProcTargets[svc.ID] {
		obs.ErrKind = errKindBadConfig
		obs.Evidence = []string{"LocalProc 适配器只接管 web/backend，当前 id=" + svc.ID}
		return obs
	}
	alive, pid := svcAlive(svc.ID)
	ports := localProcPorts(svc)
	port := firstListeningPortExcept(ports, nil)
	ownerOK := false
	owner := ""
	if port > 0 {
		owner = portOwner(port)
		ownerOK = svcOwnerAcceptable(owner, svcExpectProcs(svc.ID))
	}
	healthy, kind, ev := classifyLocalProc(alive, pid, ports, port, owner, ownerOK)
	obs.Healthy = healthy
	obs.ErrKind = kind
	obs.Evidence = ev
	return obs
}

// classifyLocalProc 本机服务健康判定表（纯函数，无 IO）。
//
// 顺序即优先级：**进程不在 → 端口没线索 → 端口没听 → owner 不符 → 健康**。
// 这个顺序不能调换：进程都死了还去查端口线索是白查；
// 而"没有线索"必须排在"端口没听"前面——前者是判断不了，后者是确定坏了，
// 把前者并进后者会让无法探测的服务被当成故障服务。
func classifyLocalProc(alive bool, pid int, ports []int, port int,
	owner string, ownerOK bool) (bool, string, []string) {
	ev := []string{"pid=" + strconv.Itoa(pid), "alive=" + strconv.FormatBool(alive)}
	if !alive {
		return false, "not_listening", append(ev, "进程已退出")
	}
	if len(ports) == 0 {
		return false, "probe_no_clue",
			append(ev, "没有候选端口线索（scan_ports / 项目画像 / 内置默认均为空）")
	}
	if port == 0 {
		return false, "not_listening",
			append(ev, fmt.Sprintf("候选端口 %v 均未 LISTENING", ports))
	}
	ev = append(ev, "port="+strconv.Itoa(port)+" LISTENING")
	if owner != "" {
		ev = append(ev, "owner="+owner)
	}
	if !ownerOK {
		// 端口被"别人的"进程占着：这是第三方占用，**不是本服务故障**。
		// 恢复策略必须能分辨这两者——对第三方占用发起重启是白费，
		// 而杀掉第三方进程是越权（plan §3.2.2 端口冲突安全处理）。
		return false, "owner_mismatch",
			append(ev, fmt.Sprintf("占用者 %s 不属于本服务", owner))
	}
	return true, "", ev
}

// localProcPorts 候选端口：Service 显式配了用配的，否则走既有的推断链
// （svcPortsFn 是可替换点，单测靠它隔离环境）。
// **0 = 未配置**，不是"端口 0"。
func localProcPorts(svc Service) []int {
	if svc.Probe.Kind == ProbePort && svc.Probe.Port != 0 {
		return []int{svc.Probe.Port}
	}
	return svcPortsFn(svc.ID)
}

// Plan 把启动/停止意图变成可核查的计划。
//
// R1：命令不是调用方传进来的字符串，而是"适配器 + 配置模板 + 安全规则"
// 三者共同决定的结果。svc.Start 配了也要过 launchScriptAllowed——
// 配置是用户填的，但用户也可能手滑填成 migrate。
func (LocalProcAdapter) Plan(svc Service, a Action) (Plan, error) {
	if !localProcTargets[svc.ID] {
		return Plan{}, fmt.Errorf("LocalProc 适配器只接管 web/backend，当前 id=%s", svc.ID)
	}
	kind := strings.TrimSpace(a.Kind)
	if kind == "" {
		kind = "start"
	}
	if kind != "start" && kind != "stop" && kind != "restart" {
		return Plan{}, fmt.Errorf("不支持的动作 %q（只支持 start/stop/restart）", kind)
	}
	p := Plan{
		Service:  svc.ID,
		Action:   kind,
		RiskTier: svc.RiskTier,
	}
	switch svc.ID {
	case "web":
		script := strings.TrimSpace(svc.Start)
		if script != "" && !launchScriptAllowed(script) {
			// 命中黑名单或不在白名单：拒绝，不退化成"帮你挑一个"。
			// 退化就是用一个用户没批准过的动作替代他被拒绝的动作。
			return Plan{}, fmt.Errorf("配置的启动脚本 %q 未通过安全规则（白名单∩黑名单），已拒绝", script)
		}
		p.Command = fmt.Sprintf("npm run %s（目录 %s）", scriptOr(script, "<自动推断>"), cfg.Projects.FrontendDir)
	case "backend":
		entry := strings.TrimSpace(svc.Start)
		if entry != "" && !launchEntryInDir(cfg.Projects.BackendDir, entry) {
			return Plan{}, fmt.Errorf("配置的后端入口 %q 不在后端目录内或含越界路径，已拒绝", entry)
		}
		p.Command = fmt.Sprintf("启动后端入口 %s（目录 %s）", entryOr(entry, "<自动推断>"), cfg.Projects.BackendDir)
	}
	return p, nil
}

// Execute 执行计划。内部走到既有的 opStartWeb/opStartBackend，
// 那些函数已经带组锁、看门狗、安全规则与审计——适配器不复制这套。
func (LocalProcAdapter) Execute(p Plan) OpResult {
	switch p.Service {
	case "web":
		return opStartWeb(cfg.Projects.FrontendDir, "", actGuard)
	case "backend":
		return opStartBackend(cfg.Projects.BackendDir, actGuard)
	}
	return opFail("start_service", p.Service, errKindBadConfig,
		"LocalProc 适配器只接管 web/backend")
}

// Verify 复验。直接走 verifyService——它是 v3.0 判据改造（T1.7）的
// 系统记录来源，适配器必须复用而不是另写一套，否则复验结论会有两个口径。
func (LocalProcAdapter) Verify(svc Service, spec VerifySpec) OpResult {
	wait := verifyWaitBackend()
	if svc.ID == "web" {
		wait = verifyWaitWeb()
	}
	if spec.WaitSecs > 0 {
		wait = time.Duration(spec.WaitSecs) * time.Second
	}
	return verifyService(svc.ID, wait)
}

func scriptOr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func entryOr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
