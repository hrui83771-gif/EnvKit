package main

// chainip_test.go —— config.toml 链地址一致性的单测
//
// 三条底线：
//   1. 注释行是用户故意留的备用配置，**永远不能被改写**；
//   2. 替换只动 host——缩进/端口/引号/行尾注释/换行符原样保留
//      （整行重写会把行尾注释吃掉，那是对用户文件的无声破坏）；
//   3. n_a（文件不存在/没有可比对象）不是缺陷，不能报成 mismatch。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleToml = "[Network]\n" +
	"Type=\"channel\"\n" +
	"[[Network.Connection]]\n" +
	"NodeURL=\"111.228.57.198:20200\"\n" +
	"GroupID=1\n" +
	"# [[Network.Connection]]\n" +
	"# NodeURL=\"127.0.0.1:20200\"\n" +
	"# GroupID=2\n" +
	"[Chain]\n" +
	"ChainID=1\n"

func withChainIPCfg(t *testing.T, beDir, chainHost string, chainPort int) func() {
	t.Helper()
	oldBE, oldHost, oldPort := cfg.Projects.BackendDir, cfg.Chain.ChainHost, cfg.Chain.ChainPort
	cfg.Projects.BackendDir = beDir
	cfg.Chain.ChainHost = chainHost
	cfg.Chain.ChainPort = chainPort
	return func() {
		cfg.Projects.BackendDir = oldBE
		cfg.Chain.ChainHost = oldHost
		cfg.Chain.ChainPort = oldPort
	}
}

func TestParseNodeURLSkipsComments(t *testing.T) {
	lines := parseNodeURLLines(sampleToml)
	if len(lines) != 1 {
		t.Fatalf("应只认出 1 行未注释 NodeURL，得到 %d：%+v", len(lines), lines)
	}
	if lines[0].host != "111.228.57.198" || lines[0].port != "20200" {
		t.Fatalf("host/port 拆错：%+v", lines[0])
	}
	// 缩进形态也要认
	lines2 := parseNodeURLLines("  NodeURL = \"10.0.0.1:20201\" # 行尾注释\n")
	if len(lines2) != 1 || lines2[0].indent != "  " || lines2[0].port != "20201" {
		t.Fatalf("缩进/行尾注释形态解析错：%+v", lines2)
	}
}

func TestRebuildNodeURLLinePreservesEverythingElse(t *testing.T) {
	// 行尾注释、缩进必须原样保留；地址整体对齐权威源（host:port）
	got := rebuildNodeURLLine(`  NodeURL="111.228.57.198:20200" # 备用说明`, "10.0.0.5", "20201")
	want := `  NodeURL="10.0.0.5:20201" # 备用说明`
	if got != want {
		t.Fatalf("改写破坏了行内其它内容：\n got=%q\nwant=%q", got, want)
	}
	// 无端口形态保持无端口，只对齐 host
	if got := rebuildNodeURLLine(`NodeURL="old.host"`, "10.0.0.5", "20201"); got != `NodeURL="10.0.0.5"` {
		t.Fatalf("无端口形态改写错：%q", got)
	}
	// 非 NodeURL 行必须原样返回
	if got := rebuildNodeURLLine(`GroupID=1`, "10.0.0.5", "20201"); got != `GroupID=1` {
		t.Fatalf("不该动的行被动了：%q", got)
	}
	// CRLF：\r 属于行尾部分，必须留在 m[3] 里原样带回去
	got = rebuildNodeURLLine("NodeURL=\"1.2.3.4:20200\"\r", "10.0.0.5", "20201")
	if got != "NodeURL=\"10.0.0.5:20201\"\r" {
		t.Fatalf("CRLF 被破坏：%q", got)
	}
}

func TestNodeURLDiffersPortToo(t *testing.T) {
	// host 同、端口不同 → 也算不一致（链端换端口时只比 host 会谎报"一致"）
	n := nodeURLLine{host: "10.0.0.5", port: "20200"}
	if !nodeURLDiffers(n, "10.0.0.5", "20201") {
		t.Fatal("host 同端口不同应判不一致")
	}
	// 文件里没写端口 → 只比 host，不冤枉
	n2 := nodeURLLine{host: "10.0.0.5", port: ""}
	if nodeURLDiffers(n2, "10.0.0.5", "20201") {
		t.Fatal("无端口行不应因端口不同被判不一致")
	}
}

func TestCheckBackendChainIPNA(t *testing.T) {
	// 没配置 → n_a，不报问题
	restore := withChainIPCfg(t, "", "", 0)
	c := checkBackendChainIP()
	if c.Status != "n_a" {
		t.Fatalf("未配置应 n_a，得到 %s", c.Status)
	}
	restore()
	// 后端目录存在但没有 config.toml → n_a（不是链项目），不是 mismatch
	be := t.TempDir()
	defer withChainIPCfg(t, be, "10.0.0.5", 20201)()
	if c := checkBackendChainIP(); c.Status != "n_a" {
		t.Fatalf("无 config.toml 应 n_a，得到 %s（%s）", c.Status, c.Why)
	}
}

func TestCheckBackendChainIPMismatch(t *testing.T) {
	be := t.TempDir()
	defer withChainIPCfg(t, be, "10.0.0.5", 20201)()
	// 旧地址用 127.0.0.1:1（快速拒绝），验证探测不改变 mismatch 结论
	writeFile(t, be, "config.toml",
		strings.Replace(sampleToml, "111.228.57.198:20200", "127.0.0.1:1", 1))
	c := checkBackendChainIP()
	if c.Status != "mismatch" {
		t.Fatalf("应 mismatch，得到 %s（%s）", c.Status, c.Why)
	}
	if c.Mismatch != 1 || c.Total != 1 || c.Want != "10.0.0.5:20201" {
		t.Fatalf("比对明细错：%+v", c)
	}
	if !c.OldProbed {
		t.Fatal("mismatch 时应做过旧地址可达性探测")
	}
	// 一致场景
	writeFile(t, be, "config.toml",
		strings.Replace(sampleToml, "111.228.57.198:20200", "10.0.0.5:20201", 1))
	if c := checkBackendChainIP(); c.Status != "ok" {
		t.Fatalf("一致应 ok，得到 %s（%s）", c.Status, c.Why)
	}
}

func TestSyncBackendChainIPFlow(t *testing.T) {
	be := t.TempDir()
	defer withChainIPCfg(t, be, "10.0.0.5", 20201)()
	toml := filepath.Join(be, "config.toml")
	writeFile(t, be, "config.toml", sampleToml)

	// 第一次：执行同步
	r := syncBackendChainIP(actAI)
	if !r.Ok || !r.Verified {
		t.Fatalf("同步应成功且复验通过：%+v", r)
	}
	got, err := os.ReadFile(toml)
	if err != nil {
		t.Fatal(err)
	}
	// 逐项校验：host 变了；注释行、GroupID、其它段**一个字节都不能动**
	want := strings.Replace(sampleToml, "111.228.57.198:20200", "10.0.0.5:20201", 1)
	if string(got) != want {
		t.Fatalf("改写结果与预期逐字节不符：\n got=%q\nwant=%q", string(got), want)
	}
	if !strings.Contains(string(got), `# NodeURL="127.0.0.1:20200"`) {
		t.Fatal("注释掉的备用 NodeURL 被动了——那是用户故意留的")
	}
	// 备份恰好一份，内容是原始文件
	baks, _ := filepath.Glob(toml + ".bak-*")
	if len(baks) != 1 {
		t.Fatalf("应恰好 1 份备份，得到 %d", len(baks))
	}
	if b, _ := os.ReadFile(baks[0]); string(b) != sampleToml {
		t.Fatal("备份内容不是原始文件")
	}

	// 第二次：已一致 → 幂等短路，不产生第二个备份，也不假报"修改了"
	r2 := syncBackendChainIP(actAI)
	if !r2.Ok || !r2.Verified {
		t.Fatalf("幂等重入应成功：%+v", r2)
	}
	if !strings.Contains(r2.Msg, "未做任何修改") {
		t.Fatalf("幂等重入应明确说没改：%s", r2.Msg)
	}
	baks2, _ := filepath.Glob(toml + ".bak-*")
	if len(baks2) != 1 {
		t.Fatalf("幂等重入不应新增备份，现在 %d 份", len(baks2))
	}
}

func TestSyncBackendChainIPNoFile(t *testing.T) {
	be := t.TempDir()
	defer withChainIPCfg(t, be, "10.0.0.5", 20201)()
	r := syncBackendChainIP(actAI)
	if r.Ok {
		t.Fatal("没有 config.toml 应失败")
	}
	if r.ErrKind != "read_failed" {
		t.Fatalf("err_kind 应为 read_failed，得到 %s", r.ErrKind)
	}
}

func TestChainIPCheckReqStatuses(t *testing.T) {
	// mismatch → reqMismatch + Action 指向 sync_chain_ip
	be := t.TempDir()
	restore := withChainIPCfg(t, be, "10.0.0.5", 20201)
	writeFile(t, be, "config.toml", sampleToml)
	r := chainIPCheckReq()
	restore()
	if r.Status != reqMismatch {
		t.Fatalf("应 reqMismatch，得到 %s", r.Status)
	}
	if !strings.Contains(r.Action, "sync_chain_ip") {
		t.Fatalf("Action 应指向 sync_chain_ip：%q", r.Action)
	}
	// n_a → reqNA，且不进 envReqBadBrief（不是问题就别稀释真问题）
	be2 := t.TempDir()
	defer withChainIPCfg(t, be2, "10.0.0.5", 20201)()
	if r := chainIPCheckReq(); r.Status != reqNA {
		t.Fatalf("无文件应 reqNA，得到 %s", r.Status)
	}
}
