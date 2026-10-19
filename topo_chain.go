// topo_chain.go —— SSHChain 适配器（v3.0 第二阶段 T2.5）：远程链端 + WeBASE
//
// 收编的现有能力（plan §4.1 的 SSHChain 适配器条目）：
//
//	Observe ← chainReach + sshLocalPortOpen（chain.go）
//	Execute ← runChainStart（chain.go，远程/本机两路 + 就绪轮询）
//	Verify  ← verifyChain（verify.go，端口 + 节点进程 + 共识推进）
//
// **链端专属逻辑全部收在本文件内**。这是 R5（plan §六）的前置条件：
// 恢复事务核心文件里不允许出现拓扑名字符串，而那些字符串总得有个家——
// 就是这个文件。T2.9 的静态检查会逐个文件核对。
//
// 与另外两个适配器的差别在于 Observe 多一层"共同原因"分辨：
// SSH 不可达时**不能**判"链节点宕机"。旧代码在这里踩过
// （`nodeUp, _ := sshLocalPortOpen(...)` 把 err 丢了，连接失败时 nodeUp
// 是零值 false），于是"虚拟机 IP 变了"被当成"节点宕机"，每 30~300 秒
// 重启一次 + 弹一次通知，无限循环。恢复命令本身就靠 SSH 送达：
// 连不上时重启毫无意义。
package main

import (
	"fmt"
	"strings"
	"time"
)

// SSHChainAdapter 远程链端适配器。无状态。
type SSHChainAdapter struct{}

// sshChainTargets 本适配器接管的 service id。
var sshChainTargets = map[string]bool{"chain": true, "webase": true}

// Observe 采集链端健康事实。
//
// 四种结局（ErrKind 供上层分辨，混任何一个都会造成误恢复）：
//
//	unreachable   SSH 主机不可达——**共同原因，不是本服务故障**。
//	              上层应记"受影响"，不发起恢复（plan §4.3）。
//	auth_failed   端口通但握手失败——凭据/主机密钥问题，机器是活的。
//	not_listening 主机可达但目标端口没在听 → 这才该触发恢复。
//	ok            端口在听。
func (SSHChainAdapter) Observe(svc Service) Observation {
	obs := Observation{Service: svc.ID, At: time.Now()}
	if !sshChainTargets[svc.ID] {
		obs.ErrKind = errKindBadConfig
		obs.Evidence = []string{"SSHChain 适配器只接管 chain/webase，当前 id=" + svc.ID}
		return obs
	}
	c := chainCfg()
	st := chainReach(c)
	if st != reachOK {
		obs.ErrKind = reachErrKind(st)
		obs.Evidence = []string{"ssh=" + c.SSHHost, "reach=" + st.String(),
			"hint=" + reachHint(c, st)}
		return obs
	}
	port := svc.Probe.Port
	if port <= 0 {
		// 端口未配置：不猜。链端端口填错会让"探测"去探一个无关服务，
		// 然后把结论安到链上——那是编造健康事实。
		obs.ErrKind = "probe_no_clue"
		obs.Evidence = []string{"ssh=" + c.SSHHost, "reach=ok", "probe.port 未配置"}
		return obs
	}
	up, err := sshLocalPortOpen(port)
	if err != nil {
		// 主机可达但远程命令执行失败：不是"端口没监听"，不能判宕机
		obs.ErrKind = "probe_no_clue"
		obs.Evidence = []string{"ssh=" + c.SSHHost, "port=" + fmt.Sprint(port),
			"远程探测命令失败：" + firstLine(strings.TrimSpace(err.Error()))}
		return obs
	}
	if !up {
		obs.ErrKind = "not_listening"
		obs.Evidence = []string{"ssh=" + c.SSHHost, "port=" + fmt.Sprint(port),
			"远程 127.0.0.1 未监听"}
		return obs
	}
	obs.Healthy = true
	obs.Evidence = []string{"ssh=" + c.SSHHost, "port=" + fmt.Sprint(port) + " LISTENING(remote)"}
	return obs
}

// reachErrKind 把可达性状态映射成 ErrKind。
// unreachable 与 auth_failed 分开：前者是"机器没了/地址失效"，
// 后者是"机器在，是凭据问题"——两者的后续动作完全不同。
func reachErrKind(st reachState) string {
	switch st {
	case reachDown:
		return "unreachable"
	case reachAuth:
		return "auth_failed"
	}
	return "unreachable"
}

// Plan 规划链端启动/重启。
//
// R1：命令模板来自配置（chain.chain_start / chain.webase_start），
// 空模板直接拒绝而不是"帮你猜一条"。
func (SSHChainAdapter) Plan(svc Service, a Action) (Plan, error) {
	if !sshChainTargets[svc.ID] {
		return Plan{}, fmt.Errorf("SSHChain 适配器只接管 chain/webase，当前 id=%s", svc.ID)
	}
	c := chainCfg()
	kind := strings.TrimSpace(a.Kind)
	if kind == "" {
		kind = "restart"
	}
	if kind != "start" && kind != "restart" {
		return Plan{}, fmt.Errorf("SSHChain 适配器不支持的动作 %q（只支持 start/restart）", kind)
	}
	var dir, script string
	switch svc.ID {
	case "chain":
		dir, script = c.ChainDir, c.ChainStart
	case "webase":
		dir, script = c.WebaseDir, c.WebaseStart
	}
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(script) == "" {
		return Plan{}, fmt.Errorf("未配置 %s 的目录或启动脚本（dir=%q script=%q）",
			svc.ID, dir, script)
	}
	return Plan{
		Service:  svc.ID,
		Action:   kind,
		Command:  fmt.Sprintf("cd %s && %s", dir, script),
		RiskTier: svc.RiskTier,
	}, nil
}

// Execute 执行启动。直接走 runChainStart——它已经处理了远程/本机两路、
// 脱离会话、就绪轮询与日志分区。
//
// 注意这里**不返回 OpResult 而是 void**：runChainStart 的老签名如此，
// 复验由 Verify 独立完成（plan §2.1 的"执行与复验分离"）。
// 把 void 包成"一定成功"的 OpResult 会是撒谎，所以返回一个
// "已提交、待复验"的诚实态。
func (SSHChainAdapter) Execute(p Plan) OpResult {
	if !sshChainTargets[p.Service] {
		return opFail("start_service", p.Service, errKindBadConfig,
			"SSHChain 适配器只接管 chain/webase")
	}
	runChainStart(p.Service)
	return opOK("start_service", p.Service,
		"启动脚本已提交（进程已脱离会话），就绪与否由复验独立判定",
		"command="+p.Command)
}

// Verify 复验。直接走 verifyChain——端口 + 节点进程 + 共识推进，
// 它是链端恢复结论的唯一凭据（守护是无人值守的，没有第二个凭据）。
func (SSHChainAdapter) Verify(svc Service, spec VerifySpec) OpResult {
	growth := verifyChainGrowth
	if spec.WaitSecs > 0 {
		growth = time.Duration(spec.WaitSecs) * time.Second
	}
	return verifyChain(growth)
}
