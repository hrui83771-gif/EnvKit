package main

// ---------- config.toml 链地址一致性（v3.1） ----------
//
// 场景：链端（WeBASE/节点）实际 IP 会变——用户主用本地虚拟机 NAT，DHCP
// 换个网段 IP 就跟着变；此时 backend 目录的 config.toml（FISCO Go SDK
// 配置）里的 NodeURL 还指着旧 IP，后端连不上链。
//
// 设计裁定（2026-10-23 与用户对齐）：
//   · 检测走产品（客观比对，不用 AI）——「能用客观事实判就别用措辞判」；
//   · 修复给 AI 一个**无参数**写工具 sync_chain_ip：值由服务端从权威源
//     （EnvKit 链端路由配置 ChainHost:ChainPort）读取，AI 只能"触发同步"，
//     不能提供任何 IP/路径——它是个开关，不是一支笔；
//   · 路径写死（服务端解析 backend 目录），字段写死（只动未注释的
//     NodeURL 行，地址整体对齐权威源 host:port），其它字节一概不碰；
//   · 改前备份、原子写、改后重读复验（OpResult 三态）、全程审计。

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const chainIPTomlName = "config.toml"

// nodeURLRe 匹配一行 NodeURL="host:port"（含前导空白与行尾注释）。
// 是否被注释由调用方用 trimLeft 判断——正则本身允许两种形态，
// 这样注释行也能被**读出来**（报告里说明"注释行不动"），但不会被改写。
var nodeURLRe = regexp.MustCompile(`^(\s*)NodeURL\s*=\s*"([^"]*)"(.*)$`)

type nodeURLLine struct {
	idx    int    // 行号（0 起）
	indent string // 原样保留的前导空白
	val    string // 引号里的原始值（host:port）
	host   string // 拆出来的 host（拆不动时为空，改写时跳过）
	port   string // 拆出来的 port（可能为空）
}

// backendConfigTomlPath 服务端写死的唯一目标文件——
// 从 EnvKit 自身配置解析，AI 任何情况下都无法指定别的路径。
func backendConfigTomlPath() string {
	return filepath.Join(cfg.Projects.BackendDir, chainIPTomlName)
}

// chainWantAddr 权威源：链端路由当前配置的 host:port。
func chainWantAddr() string {
	c := chainCfg()
	return net.JoinHostPort(c.ChainHost, fmt.Sprintf("%d", c.ChainPort))
}

// parseNodeURLLines 找出所有未注释的 NodeURL 行。
// 注释行（# [[Network.Connection]] 下的备用配置）刻意**不进结果**——
// 备用配置是用户故意留的，同步不该替用户做决定。
func parseNodeURLLines(content string) []nodeURLLine {
	var out []nodeURLLine
	for i, line := range strings.Split(content, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "#") || strings.HasPrefix(trim, "//") {
			continue
		}
		m := nodeURLRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n := nodeURLLine{idx: i, indent: m[1], val: m[2]}
		if h, p, err := net.SplitHostPort(m[2]); err == nil {
			n.host, n.port = h, p
		} else {
			// 没有端口（纯 host）也算——port 留空，改写时保持无端口形态
			n.host = m[2]
		}
		out = append(out, n)
	}
	return out
}

// chainIPCheck 一次比对的结构化结论。n_a（没有可比对象）单列，
// 与判据设计同构：不是问题，不该被报成缺陷。
type chainIPCheck struct {
	Status    string `json:"status"`     // ok | mismatch | n_a
	Why       string `json:"why"`        // 给人看的依据
	Path      string `json:"path"`       // 检查的文件（绝对路径）
	Want      string `json:"want"`       // 权威源地址
	Current   string `json:"current"`    // 文件里的当前值（多个用逗号连）
	Mismatch  int    `json:"mismatch"`   // 不一致的行数
	Total     int    `json:"total"`      // 未注释 NodeURL 行数
	OldLive   bool   `json:"old_live"`   // 旧地址端口是否可达（mismatch 时才有意义）
	OldProbed bool   `json:"old_probed"` // 是否做过可达性探测
}

// checkBackendChainIP 比对 config.toml 的 NodeURL 与权威源。
//
// ⚠️ n_a（文件不存在 / 没有未注释的 NodeURL）**不是缺陷**：
// backend 可能根本不是链项目（比如纯 Web 项目），把"没这个文件"
// 报成"不一致"会让人去修不存在的问题——与 crash_on_next 误分类同一教训。
func checkBackendChainIP() chainIPCheck {
	out := chainIPCheck{Status: "n_a", Want: chainWantAddr(),
		Path: backendConfigTomlPath()}
	if strings.TrimSpace(cfg.Projects.BackendDir) == "" ||
		strings.TrimSpace(cfg.Chain.ChainHost) == "" {
		out.Why = "未配置后端目录或链端地址，无从比对"
		return out
	}
	b, err := os.ReadFile(out.Path)
	if err != nil {
		out.Why = "后端目录下没有 config.toml（可能不是链项目）——不视为问题"
		return out
	}
	lines := parseNodeURLLines(string(b))
	out.Total = len(lines)
	if len(lines) == 0 {
		out.Why = "config.toml 里没有未注释的 NodeURL 配置——不视为问题"
		return out
	}
	wantHost := cfg.Chain.ChainHost
	wantPort := fmt.Sprintf("%d", chainCfg().ChainPort)
	var cur []string
	for _, n := range lines {
		cur = append(cur, n.val)
		if nodeURLDiffers(n, wantHost, wantPort) {
			out.Mismatch++
		}
	}
	out.Current = strings.Join(cur, ", ")
	if out.Mismatch == 0 {
		out.Status = "ok"
		out.Why = fmt.Sprintf("NodeURL 与链端配置一致（%s）", out.Want)
		return out
	}
	out.Status = "mismatch"
	out.Why = fmt.Sprintf("%d/%d 行 NodeURL 指向旧地址；权威源（链端路由）=%s",
		out.Mismatch, out.Total, out.Want)
	// 可达性探测只对"不一致"的场景做：区分「IP 变了且旧 IP 已死」
	// 与「只是写法不同但两台都能通」。探测失败不改变结论——
	// 「不一致」本身就够触发修复，「不可达」只是加重语气。
	var lastHost, lastPort string
	for _, n := range lines {
		if nodeURLDiffers(n, wantHost, wantPort) && n.host != "" {
			lastHost, lastPort = n.host, n.port
			break
		}
	}
	if lastHost != "" {
		addr := net.JoinHostPort(lastHost, orDefaultPort(lastPort, "20200"))
		if c, err := net.DialTimeout("tcp", addr, 2*time.Second); err == nil {
			out.OldLive = true
			c.Close()
		}
		out.OldProbed = true
	}
	return out
}

func orDefaultPort(p, def string) string {
	if strings.TrimSpace(p) == "" {
		return def
	}
	return p
}

// nodeURLDiffers 判断一行 NodeURL 是否与权威源不一致。
// **host 与端口都比对**——权威源是 ChainHost:ChainPort 整体，
// 只比 host 的话，链端换了端口会出现"检查说一致、实际连不上"的谎报。
// 文件里没写端口的行只比 host（没有可比的端口就不冤枉它）。
func nodeURLDiffers(n nodeURLLine, wantHost, wantPort string) bool {
	if n.host != wantHost {
		return true
	}
	return n.port != "" && n.port != wantPort
}

// chainIPCheckReq 包装成环境符合性检查项（进 check_env_req 与快照注入）。
// n_a 时不装样子——如实说"没有可比对象"，不报问题。
func chainIPCheckReq() EnvReq {
	c := checkBackendChainIP()
	r := EnvReq{Component: "config.toml 链地址", Required: c.Want, Actual: c.Current}
	switch c.Status {
	case "ok":
		r.Status = reqOK
		r.Why = c.Why
	case "mismatch":
		r.Status = reqMismatch
		r.Why = c.Why
		if c.OldProbed && !c.OldLive {
			r.Why += "；旧地址已不可达（后端现在应该连不上链）"
		}
		r.Action = "用 sync_chain_ip 同步（或手动改 config.toml 的 NodeURL）"
	default:
		r.Status = reqNA
		r.Why = c.Why
	}
	return r
}

// syncBackendChainIP 把 config.toml 里所有未注释 NodeURL 的 host 同步为权威源。
//
// 安全边界（缺一不可）：
//
//	· 路径写死（backendConfigTomlPath），AI 传不进来；
//	· 只替换 host 部分，端口/缩进/引号/行尾注释/注释行全部原样保留；
//	· 改前备份 config.toml.bak-<时间戳>（失败即整体放弃，不动原文件）；
//	· 原子写（tmp + rename，与 saveExternalConfig 同款）；
//	· 改后**重读磁盘**复验（OpResult 三态：verified / fail），
//	  复验是客观证据，不是"我改了所以应该好了"；
//	· 已一致时幂等短路——不产生第二个备份，也不做一次假"修改"。
func syncBackendChainIP(actor string) OpResult {
	const action, target = "chain_ip_sync", "backend_config_toml"
	path := backendConfigTomlPath()
	fin := auditStart(actor, action, target, path)
	finish := func(result, detail string, r OpResult) OpResult {
		fin(result, detail)
		return r
	}
	if strings.TrimSpace(cfg.Projects.BackendDir) == "" ||
		strings.TrimSpace(cfg.Chain.ChainHost) == "" {
		return finish("error", "未配置后端目录或链端地址", opFail(action, target,
			"not_configured", "未配置后端目录或链端地址，无从同步"))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return finish("error", fmt.Sprintf("读 config.toml 失败: %v", err),
			opFail(action, target, "read_failed",
				"读 "+path+" 失败："+err.Error()+"（可能不是链项目）"))
	}
	content := string(raw)
	lines := parseNodeURLLines(content)
	if len(lines) == 0 {
		return finish("error", "没有未注释的 NodeURL",
			opFail(action, target, "no_entry",
				"config.toml 里没有未注释的 NodeURL 配置，无从同步"))
	}
	wantHost := cfg.Chain.ChainHost
	wantPort := fmt.Sprintf("%d", chainCfg().ChainPort)
	want := chainWantAddr()
	if mismatchCount(lines, wantHost, wantPort) == 0 {
		return finish("ok", "已一致，无需修改",
			opVerified(action, target,
				fmt.Sprintf("NodeURL 已与链端配置一致（%s），未做任何修改", want),
				"重读比对：0 行不一致"))
	}
	// 改前备份：失败即放弃——没有退路的写操作不该发生。
	bak := path + ".bak-" + nowFilestamp()
	if err := os.WriteFile(bak, raw, 0o644); err != nil {
		return finish("error", fmt.Sprintf("备份失败: %v", err),
			opFail(action, target, "backup_failed",
				"备份到 "+bak+" 失败："+err.Error()+"（已放弃修改，原文件未动）"))
	}
	// 逐行改写：只动未注释 NodeURL 行，其余字节原样保留。
	var changed []string
	outLines := strings.Split(content, "\n")
	for _, n := range lines {
		if !nodeURLDiffers(n, wantHost, wantPort) {
			continue
		}
		changed = append(changed, n.val+" → "+want)
		outLines[n.idx] = rebuildNodeURLLine(outLines[n.idx], wantHost, wantPort)
	}
	newContent := strings.Join(outLines, "\n")
	// 原子写
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(newContent), 0o644); err != nil {
		return finish("error", fmt.Sprintf("写临时文件失败: %v", err),
			opFail(action, target, "write_failed",
				"写临时文件失败："+err.Error()+"（原文件未动，备份在 "+bak+"）"))
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return finish("error", fmt.Sprintf("原子替换失败: %v", err),
			opFail(action, target, "rename_failed",
				"原子替换失败："+err.Error()+"（原文件未动，备份在 "+bak+"）"))
	}
	// 复验：**重读磁盘**比对，不是拿内存里的 newContent 自说自话。
	again, err := os.ReadFile(path)
	if err != nil {
		return finish("error", fmt.Sprintf("复验读失败: %v", err),
			opFail(action, target, "verify_read_failed",
				"已写入但复验读取失败："+err.Error()+"（备份在 "+bak+"）"))
	}
	reLines := parseNodeURLLines(string(again))
	if mismatchCount(reLines, wantHost, wantPort) != 0 {
		return finish("fail", "复验仍不一致",
			opFail(action, target, "verify_failed",
				"已写入但重读后仍存在不一致行（备份在 "+bak+"，可手动还原）"))
	}
	return finish("ok", "同步完成: "+strings.Join(changed, "; "),
		opVerified(action, target,
			fmt.Sprintf("已同步 %d 行 NodeURL → %s（备份：%s）", len(changed), want, bak),
			"重读比对：0 行不一致，共 "+fmt.Sprint(len(reLines))+" 行"))
}

func mismatchCount(lines []nodeURLLine, wantHost, wantPort string) int {
	n := 0
	for _, l := range lines {
		if nodeURLDiffers(l, wantHost, wantPort) {
			n++
		}
	}
	return n
}

// rebuildNodeURLLine 只替换行内 NodeURL 的地址部分（对齐权威源 host:port），
// 缩进/引号/行尾注释原样保留。
// 用 submatch 拼装，**不整行重写**——行尾可能有 `# 注释`，
// 整行重写会把注释吃掉（那是对用户文件的无声破坏）。
func rebuildNodeURLLine(line, wantHost, wantPort string) string {
	m := nodeURLRe.FindStringSubmatch(line)
	if m == nil {
		return line
	}
	if _, _, err := net.SplitHostPort(m[2]); err == nil {
		// 原行带端口 → 写全地址（对齐权威源的 host 与端口）
		return m[1] + `NodeURL="` + net.JoinHostPort(wantHost, wantPort) + `"` + m[3]
	}
	// 原行只有 host（无端口形态）→ 保持无端口形态，只对齐 host
	return m[1] + `NodeURL="` + wantHost + `"` + m[3]
}

// nowFilestamp 文件名安全的时间戳（冒号在 Windows 文件名里非法）。
func nowFilestamp() string {
	return time.Now().Format("20060102_150405")
}

// ---------- AI 工具注册 ----------
//
// sync_chain_ip 是**无参数**写工具：值由服务端从权威源读取，AI 只能
// "触发同步"，不能提供任何 IP / 路径——它是个开关，不是一支笔。
// 这是「工具 schema 即权限边界」的具体落地：越权面为零。
// Write: true → 走确认卡（用户不点头就不动手），与 db_backup 同款流程。

func init() {
	aiToolRegistry["sync_chain_ip"] = aiTool{
		Desc: "把后端 config.toml 的链节点地址（NodeURL）同步为 EnvKit 链端路由当前配置的地址。" +
			"适用场景：链端机器换了 IP（如虚拟机 NAT 重新分配），后端因地址过期连不上链。" +
			"**你不需要也不能提供任何 IP**——目标地址由系统从链端配置读取；" +
			"只修改 config.toml 中未注释的 NodeURL 的 host 部分（端口/注释/其它内容原样保留），" +
			"改前自动备份，改后自动重读复验。" +
			"注意：若用户想改成某个新地址，正确做法是先让用户在**链端路由**里更新地址配置，" +
			"再调用本工具同步——本工具不能被用来把地址改成任意的值。",
		Schema: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}},
		Write:  true,
		Execute: func(map[string]any) (string, error) {
			r := syncBackendChainIP(actAI)
			if !r.Ok {
				return "", aiOpErr("链地址同步", r)
			}
			return aiOpResultText("链地址同步", r), nil
		},
	}
}
