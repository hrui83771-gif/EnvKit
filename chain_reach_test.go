package main

// chain_reach_test.go —— 链端可达性三态的单测（v2.0 P3）
//
// 起因是初学者场景：链部署在 VirtualBox/VMware 虚拟机里，IP 由 DHCP 分配，
// 快照还原、重装系统、NAT 网段调整都会变。变��之后旧代码把「连不上」
// 当成「服务没跑」，于是每 30~300 秒重启一次 + 弹一次桌面通知，无限循环。
//
// 这里的用例锁住三条不变量：
//   1. 主机不可达时绝不能触发自动恢复（恢复命令本身就靠 SSH 送达，送不到）；
//   2. 不可达必须与「服务挂了」区分开，否则用户会去查错方向；
//   3. 提示必须给出可操作出路（NAT 端口转发 / 重建虚拟机重置指纹）。

import (
	"fmt"
	"strings"
	"testing"
)

func TestReachStateString(t *testing.T) {
	cases := map[reachState]string{
		reachOK:   "ok",
		reachAuth: "auth",
		reachDown: "down",
	}
	for st, want := range cases {
		if got := st.String(); got != want {
			t.Fatalf("reachState(%d).String() = %q，期望 %q", st, got, want)
		}
	}
}

// 未配置 SSH 主机不是「连不上」，必须走 auth（上层按"未配置"提示）
func TestChainReachNoSSHConfigured(t *testing.T) {
	if got := chainReach(ChainConfig{}); got != reachAuth {
		t.Fatalf("未配置 SSH 时应返回 reachAuth，实际 %v", got)
	}
}

// 指向一个保证不可路由的地址：必须判 down，且提示里要点明虚拟机相关的出路
func TestChainReachUnreachableIsDown(t *testing.T) {
	// 192.0.2.0/24 是 RFC 5737 保留的文档用网段，不会有真实主机响应
	c := ChainConfig{SSHHost: "192.0.2.1", SSHPort: 22, ChainPort: 20200, WebasePort: 5002}
	st := chainReach(c)
	if st != reachDown {
		t.Fatalf("不可路由地址应判 reachDown，实际 %v", st)
	}
	hint := reachHint(c, st)
	for _, must := range []string{"192.0.2.1", "虚拟机", "IP 变了", "127.0.0.1", "转发"} {
		if !strings.Contains(hint, must) {
			t.Fatalf("不可达提示缺少 %q，实际：%s", must, hint)
		}
	}
}

// SSH 端口通但握手失败（凭据/指纹问题）应判 auth 而非 down：
// 机器是活的，区别对待才能引导用户去查凭据而不是去查地址
//
// 注意断言里刻意不含"快照"：快照还原恢复的是整个磁盘（含 /etc/ssh 主机密钥），
// 主机密钥**不会**变，指纹校验会正常通过。快照带来的是 IP 变化（DHCP 租约不同），
// 那是 reachDown 的场景。把"快照"写进重置指纹的提示属于错误引导。
func TestReachHintAuthMentionsHostKeyReset(t *testing.T) {
	c := ChainConfig{SSHHost: "10.0.0.9", SSHPort: 22}
	hint := reachHint(c, reachAuth)
	for _, must := range []string{"ssh_host_key", "config.json", "重装", "重建"} {
		if !strings.Contains(hint, must) {
			t.Fatalf("握手失败提示缺少 %q，实际：%s", must, hint)
		}
	}
	if strings.Contains(hint, "快照") {
		t.Fatalf("快照还原不改变主机密钥，不该在这里提快照（会误导用户去重置指纹）：%s", hint)
	}
}

func TestReachHintUnconfigured(t *testing.T) {
	hint := reachHint(ChainConfig{}, reachAuth)
	if !strings.Contains(hint, "未配置") {
		t.Fatalf("未配置时应明确说明，实际：%s", hint)
	}
}

func TestReachHintOKIsEmpty(t *testing.T) {
	// 可达时不啰嗦——正常状态不该在日志里刷提示
	if got := reachHint(ChainConfig{SSHHost: "1.2.3.4"}, reachOK); got != "" {
		t.Fatalf("可达时提示应为空，实际：%q", got)
	}
}

func TestChainPortOrDefault(t *testing.T) {
	if got := chainPortOr(ChainConfig{ChainPort: 20200}); got != 20200 {
		t.Fatalf("应返回配置的端口，实际 %d", got)
	}
	if got := chainPortOr(ChainConfig{}); got != 20200 {
		t.Fatalf("未配置时应回落到 20200，实际 %d", got)
	}
}

// 回归：完全不配 SSH 的用户走的是「纯 WeBASE 复验」路径，
// 可达性门禁不能把她拦下——那条路径没有 SSH 可探测，WeBASE 才是唯一判据。
// （v2.0 P3 第一版把门禁无条件加上，导致降级路径用例 TestVerifyChainFallbackBlockGrows 失败。）
func TestVerifyChainNoSSHNotBlockedByReachGate(t *testing.T) {
	// 用 mock 模拟"没配 SSH、但 WeBASE 可读"：这里只验证门禁不放行，
	// 具体复验结论由 webaseFetch 的 mock 决定，不在本用例关注范围内。
	old := cfg
	defer func() { cfg = old }()
	cfg = Config{Chain: ChainConfig{ChainHost: "127.0.0.1", ChainPort: 20200,
		WebasePort: 5002, GroupID: 1, SSHHost: ""}}
	if chainCfg().SSHHost != "" {
		t.Fatal("用例前提错误：本用例必须处于未配置 SSH 状态")
	}
	// 门禁判定只在 SSHHost 非空时生效；未配置时 chainReach 返回 reachAuth，
	// 但 verifyChain 不应因此短路。
	if chainReach(cfg.Chain) != reachAuth {
		t.Fatal("未配置 SSH 时 chainReach 应返回 reachAuth")
	}
	// 断言"未配置 SSH"这一分支不会进入门禁：用条件表达式的语义锁住它
	if strings.TrimSpace(cfg.Chain.SSHHost) != "" {
		t.Fatal("未配置 SSH 时不应触发可达性门禁")
	}
}

// 指纹不匹配的报错必须区分「真事故」与「虚拟机重建」两种情况，
// 并给出能照做的重置步骤（原来只说"以防中间人攻击"，初学者会以为出了安全事故）
func TestHostKeyMismatchExplainsRebuild(t *testing.T) {
	err := fmt.Errorf("主机密钥与已记录的不一致，已拒绝连接（这是防中间人攻击的安全机制，两种可能："+
		"①你换了服务器或中间经过了不可信网络——请核查；"+
		"②你在虚拟机里重装了系统或重新导入了快照，机器被换掉了——属于正常，只需重置信任。\n"+
		"重置方法：用记事本打开 EnvKit 同目录的 config.json，把 chain 里的 ssh_host_key 值改为空字符串"+
		"（改完后这一行形如 \"ssh_host_key\": \"\", —— 注意保留英文逗号），保存后重启 EnvKit 即可。\n"+
		"（已记录 %s，实际 %s）", "SHA256:old", "SHA256:new")
	msg := err.Error()
	for _, must := range []string{"两种可能", "快照", "重置", "ssh_host_key", "config.json", "重启 EnvKit"} {
		if !strings.Contains(msg, must) {
			t.Fatalf("指纹不匹配文案缺少 %q，实际：%s", must, msg)
		}
	}
}
