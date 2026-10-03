package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// ---------- 链端配置 ----------
type ChainConfig struct {
	SSHHost     string `json:"ssh_host"`
	SSHPort     int    `json:"ssh_port"`
	SSHUser     string `json:"ssh_user"`
	SSHPassword string `json:"ssh_password"`
	SSHKey      string `json:"ssh_key"`

	ChainHost  string `json:"chain_host"`
	ChainPort  int    `json:"chain_port"`
	ChainDir   string `json:"chain_dir"`
	ChainStart string `json:"chain_start"`

	WebaseDir   string `json:"webase_dir"`
	WebaseStart string `json:"webase_start"`
	WebasePort  int    `json:"webase_port"`
	GroupID     int    `json:"group_id"`

	ChainAutoRecover bool `json:"chain_autorecover"` // 一键检测时发现节点/WeBASE 宕机则自动尝试重启
	ChainGuard       bool `json:"chain_guard"`       // 后台守护：定时检测 + 宕机自动拉起（无人值守）
	ChainGuardSecs   int  `json:"chain_guard_secs"`  // 守护检测间隔（秒，最小 30）
	ChainNotify      bool `json:"chain_notify"`      // 宕机/恢复时弹 Windows 桌面通知

	// SSHHostKey 已信任的远程主机公钥指纹（SHA256:...）。
	// 空 = 尚未建立信任，首次连接按 TOFU 记录并在日志里明示；
	// 非空但与实际不符 = 可能遭遇中间人，直接拒绝连接。
	SSHHostKey string `json:"ssh_host_key"`

	// NodeCount 预期节点进程数（0=不校验具体数量，只要求至少一个 fisco-bcos 进程）。
	// 复验时用它判断"4 节点链是不是只活了 1 个"——端口只反映被探的那个节点。
	NodeCount int `json:"node_count"`
}

type ChainInfo struct {
	Checked        bool   `json:"checked"`
	At             string `json:"at"`
	SSHOK          bool   `json:"sshOk"`
	SSHMsg         string `json:"sshMsg"`
	Port20200      bool   `json:"port20200"`      // 节点是否存活（本地监听 或 经 WeBASE 确认）
	Lat20200       int64  `json:"lat20200"`       // 公网探测耗时
	ChainExtReach  bool   `json:"chainExtReach"`  // 公网 host:20200 是否可达
	ChainBindLocal bool   `json:"chainBindLocal"` // 节点仅本地监听（公网不可达，已加固）
	Port5002       bool   `json:"port5002"`
	Lat5002        int64  `json:"lat5002"`
	WebaseOK       bool   `json:"webaseOk"`
	WebaseURL      string `json:"webaseUrl"`
	BlockNumber    string `json:"blockNumber"`
	TxCount        string `json:"txCount"`
	ClientVer      string `json:"clientVer"`
	NodeInfo       string `json:"nodeInfo"`
	ChainStartOK   bool   `json:"chainStartOk"`
	WebaseStartOK  bool   `json:"webaseStartOk"`
	NodeProcs      int    `json:"nodeProcs"`   // 远程 fisco-bcos 进程数（-1=未知/未探测）
	LastRecover    string `json:"lastRecover"` // 最近一次自动恢复时间
	RecCount       int    `json:"recCount"`    // 自动恢复累计次数
	// Reach 主机可达性三态：ok / auth / down（v2.0 P3）。
	// down 时上面所有端口结论都不可信——「连不上」和「服务挂了」是两件事，
	// 前者尤其常见于虚拟机场景（IP 变化、NAT 下填了内网 IP、虚拟机没开机）。
	Reach    string `json:"reach"`
	ReachMsg string `json:"reachMsg"` // 不可达时的人话解释与可操作建议
}

// 自动恢复统计（运行期内存态）
var (
	recMu         sync.Mutex
	lastRecoverAt string
	recCount      int
)

func recordRecover() {
	recMu.Lock()
	lastRecoverAt = time.Now().Format("15:04:05")
	recCount++
	recMu.Unlock()
	metChainRecover()
}

func recoverStats() (string, int) {
	recMu.Lock()
	defer recMu.Unlock()
	return lastRecoverAt, recCount
}

var (
	chainMu   sync.Mutex
	chainInfo = ChainInfo{}
)

func chainCfg() ChainConfig {
	c := cfg.Chain
	if c.SSHPort == 0 {
		c.SSHPort = 22
	}
	if c.ChainPort == 0 {
		c.ChainPort = 20200
	}
	if c.WebasePort == 0 {
		c.WebasePort = 5002
	}
	if c.GroupID == 0 {
		c.GroupID = 1
	}
	return c
}

func webaseBaseURL() string {
	c := chainCfg()
	host := c.ChainHost
	if host == "" {
		host = c.SSHHost
	}
	return fmt.Sprintf("http://%s:%d/WeBASE-Front", host, c.WebasePort)
}

func setChain(f func(*ChainInfo)) {
	chainMu.Lock()
	f(&chainInfo)
	chainMu.Unlock()
}

// ---------- TCP 端口检测 ----------
func tcpCheck(host string, port int) (bool, int64) {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 3*time.Second)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		return false, ms
	}
	_ = conn.Close()
	return true, ms
}

// ---------- 链端可达性三态 ----------
//
// 「服务没跑」和「机器/地址联系不上」是两件事，混为一谈会让 EnvKit 对着一个
// 根本送不到命令的地址反复重试。初学者在本地虚拟机上尤其常见：
// 虚拟机 IP 由 DHCP 分配（快照还原、NAT 网段调整、重装系统都会变），
// 而 NAT 模式下宿主机填虚拟机内网 IP 又根本连不通。
//
// 判定顺序刻意如此：先看 TCP 能不能连上（轻量、无副作用），再谈认证。

type reachState int

const (
	reachOK   reachState = iota // SSH 端口通 → 机器在，探测结果可信
	reachAuth                   // SSH 端口通但握手/认证/主机指纹被拒 → 机器在，是凭据或安全问题
	reachDown                   // SSH 端口压根连不上 → 地址失效、虚拟机未启动或被拦
)

func (r reachState) String() string {
	switch r {
	case reachOK:
		return "ok"
	case reachAuth:
		return "auth"
	default:
		return "down"
	}
}

// chainReach 探测链端主机的可达性。只做连接与握手，不执行任何命令。
func chainReach(c ChainConfig) reachState {
	if strings.TrimSpace(c.SSHHost) == "" {
		return reachAuth // 没配 SSH：不是"连不上"，上层按"未配置"提示
	}
	port := c.SSHPort
	if port <= 0 {
		port = 22
	}
	if up, _ := tcpCheck(c.SSHHost, port); !up {
		return reachDown
	}
	if _, err := sshDial(); err != nil {
		return reachAuth // 端口通但握手失败 → 凭据 / 主机密钥问题，机器本身是活的
	}
	return reachOK
}

// reachHint 给出「为什么连不上」的人话解释，并针对初学者最常见的两个坑给出路。
// 虚拟机部署是本项目的主要使用场景之一，这里的话术直接影响用户能不能自查。
func reachHint(c ChainConfig, st reachState) string {
	host := strings.TrimSpace(c.SSHHost)
	if host == "" {
		return "未配置 SSH 主机，链端相关操作已跳过"
	}
	switch st {
	case reachDown:
		return fmt.Sprintf("连不上 %s:%d。常见原因：①虚拟机未启动；②虚拟机 IP 变了（快照还原、重装系统、NAT 网段调整都会变）；"+
			"③链在 NAT 虚拟机里、这里却填了虚拟机内网 IP（192.168.x.x）——NAT 下宿主机路由不到它。"+
			"解法：在虚拟机软件里把 %d 端口转发到宿主机，再把这里改成 127.0.0.1",
			host, chainPortOr(c), c.WebasePort)
	case reachAuth:
		return fmt.Sprintf("能连上 %s 但 SSH 握手失败：凭据不对，或主机密钥与已记录的不一致。"+
			"如果你刚重建过虚拟机（重装系统会重新生成主机密钥），这属于正常现象——"+
			"清空 config.json 里的 chain.ssh_host_key 即可重新建立信任", host)
	default:
		return ""
	}
}

func chainPortOr(c ChainConfig) int {
	if c.ChainPort > 0 {
		return c.ChainPort
	}
	return 20200
}

// 经 SSH 在远程主机的 127.0.0.1 上探测端口是否真正监听。
// 节点可能被加固为仅本地绑定（channel_listen_ip=127.0.0.1），此时从本机 TCP 探公网必然失败，
// 但节点其实在跑——必须到远程本机探才准确。优先用 ss，回退到 /dev/tcp。
func sshLocalPortOpen(port int) (bool, error) {
	cmd := fmt.Sprintf("ss -ltnH '( sport = :%d )' 2>/dev/null | grep -q . && echo OPEN || ( timeout 3 bash -c 'exec 3<>/dev/tcp/127.0.0.1/%d' 2>/dev/null && echo OPEN || echo CLOSED )", port, port)
	out, err := sshRunCapture(cmd)
	if err != nil {
		return false, err
	}
	return strings.Contains(strings.ToUpper(strings.TrimSpace(out)), "OPEN"), nil
}

// ---------- SSH ----------
func sshDial() (*ssh.Client, error) {
	c := chainCfg()
	if c.SSHHost == "" {
		return nil, fmt.Errorf("未配置 SSH 主机")
	}
	var auths []ssh.AuthMethod
	if c.SSHKey != "" {
		if key, err := os.ReadFile(c.SSHKey); err == nil {
			if signer, err := ssh.ParsePrivateKey(key); err == nil {
				auths = append(auths, ssh.PublicKeys(signer))
			}
		}
	}
	if c.SSHPassword != "" {
		auths = append(auths, ssh.Password(c.SSHPassword))
		auths = append(auths, ssh.KeyboardInteractive(func(user, instruction string, questions []string, echos []bool) ([]string, error) {
			ans := make([]string, len(questions))
			for i := range ans {
				ans[i] = c.SSHPassword
			}
			return ans, nil
		}))
	}
	if len(auths) == 0 {
		return nil, fmt.Errorf("未配置 SSH 密码或私钥")
	}
	sc := &ssh.ClientConfig{
		User:            c.SSHUser,
		Auth:            auths,
		HostKeyCallback: sshHostKeyCallback(),
		Timeout:         8 * time.Second,
	}
	return ssh.Dial("tcp", net.JoinHostPort(c.SSHHost, strconv.Itoa(c.SSHPort)), sc)
}

// sshHostKeyCallback 主机密钥校验（修复 InsecureIgnoreHostKey 带来的中间人风险）：
//
//	· 未记录过指纹：按 TOFU（首次使用即信任）记录并写入配置，同时在日志里明示指纹；
//	· 已记录且一致：放行；
//	· 已记录但不一致：拒绝连接——这是"服务器被替换/链路被劫持"的典型信号。
func sshHostKeyCallback() ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		fp := ssh.FingerprintSHA256(key)
		cfgMu.Lock()
		known := strings.TrimSpace(cfg.Chain.SSHHostKey)
		if known == "" {
			cfg.Chain.SSHHostKey = fp
			snapshot := cfg
			cfgMu.Unlock()
			saveExternalConfig(snapshot)
			warn(scChain, "安全", "首次连接 %s：已记录主机指纹 %s。若这台服务器不是你自己的，请立即断开并核查网络。"+
				"此后该指纹一旦变化将拒绝连接（可在 config.json 清空 ssh_host_key 重置）", hostname, fp)
			auditNow(actSys, "ssh_tofu", hostname, "", resOK, fp)
			return nil
		}
		cfgMu.Unlock()
		if !strings.EqualFold(known, fp) {
			auditNow(actSys, "ssh_hostkey_mismatch", hostname, "", resDenied, fp)
			// 措辞要照顾初学者最常见的真实场景：在 VirtualBox/VMware 里重装系统、
			// 重新导入快照，都会重新生成主机密钥。这时报"中间人攻击"会让人以为是事故，
			// 实际上只是换了一台机器。所以先说清"这是安全机制"，再给出可直接照做的重置步骤。
			return fmt.Errorf("主机密钥与已记录的不一致，已拒绝连接（这是防中间人攻击的安全机制，两种可能："+
				"①你换了服务器或中间经过了不可信网络——请核查；"+
				"②你在虚拟机里重装了系统或重新导入了快照，机器被换掉了——属于正常，只需重置信任。\n"+
				"重置方法：用记事本打开 EnvKit 同目录的 config.json，把 chain 里的 ssh_host_key 值改为空字符串"+
				"（改完后这一行形如 \"ssh_host_key\": \"\", —— 注意保留英文逗号），保存后重启 EnvKit 即可。\n"+
				"（已记录 %s，实际 %s）", known, fp)
		}
		return nil
	}
}

// 远程执行并流式输出到链端日志
func sshRunStream(step, cmd string) error {
	client, err := sshDial()
	if err != nil {
		return err
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	w := &lineWriter{scope: scChain, tag: step, svcKey: "chain"}
	sess.Stdout = w
	sess.Stderr = w
	err = sess.Run(cmd)
	w.flush()
	return err
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// 远程启动脚本：用登录 shell（bash -lc）执行，确保加载 /etc/profile（JAVA_HOME、PATH 等）；
// 再用 setsid + 重定向 stdin 使其完全脱离 SSH 会话，避免会话关闭时进程被 SIGHUP/SIGTERM 杀掉
// （WeBASE-Front 的 tomcat、fisco-bcos 都是后台进程，若不脱离会话会在 SSH 通道关闭后约 7s 被杀）。
func sshRunStartScript(step, dir, script string) error {
	inner := fmt.Sprintf("cd %s && %s", shQuote(dir), script)
	cmd := "setsid bash -lc " + shQuote(inner) + " </dev/null"
	return sshRunStream(step, cmd)
}

func sshRunCapture(cmd string) (string, error) {
	client, err := sshDial()
	if err != nil {
		return "", err
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	out, err := sess.CombinedOutput(cmd)
	return string(out), err
}

// ---------- WeBASE-Front REST ----------
// 返回原始响应文本（WeBASE-Front 的接口有的是纯文本、有的是 JSON 对象、有的带 code/data 包装）
func webaseText(path string) (string, error) {
	resp, err := httpFast.Get(webaseBaseURL() + path)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	s := strings.TrimSpace(string(body))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d：%s", resp.StatusCode, firstLines(s, 120))
	}
	// 带 code/data 包装的统一去壳
	if strings.HasPrefix(s, "{") {
		var m struct {
			Code    *int            `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		}
		if json.Unmarshal([]byte(s), &m) == nil && m.Code != nil {
			if *m.Code != 0 {
				return "", fmt.Errorf("code=%d %s", *m.Code, m.Message)
			}
			return strings.TrimSpace(string(m.Data)), nil
		}
	}
	return s, nil
}

func jsonNum(s string) string {
	var f float64
	if json.Unmarshal([]byte(s), &f) == nil {
		return strconv.FormatInt(int64(f), 10)
	}
	return strings.TrimSpace(s)
}

// ---------- 处理入口 ----------
func handleChainSSHTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	h, granted := beginTaskH("SSH 连通测试", false) // 只读：可与其它只读任务并发
	if !granted {
		http.Error(w, "有任务正在执行，请稍候", 409)
		return
	}
	go func() {
		defer h.Done()
		fin := auditStart(actUser, "ssh_test", cfg.Chain.SSHHost, "")
		c := chainCfg()
		info(scChain, "SSH", "连接 %s@%s:%d ...", c.SSHUser, c.SSHHost, c.SSHPort)
		out, err := sshRunCapture("echo ENVKIT_OK; uname -a 2>/dev/null || ver")
		if err != nil {
			msg := firstLines(strings.TrimSpace(out), 200)
			setChain(func(ci *ChainInfo) {
				ci.SSHOK = false
				ci.SSHMsg = "失败：" + err.Error()
				ci.At = time.Now().Format("15:04:05")
			})
			fail(scChain, "SSH", "连接失败：%v %s", err, msg)
			fin(resFail, err.Error())
			return
		}
		fin(resOK, c.SSHHost)
		line := firstLine(strings.TrimSpace(out))
		setChain(func(ci *ChainInfo) {
			ci.SSHOK = true
			ci.SSHMsg = "连接成功"
			ci.NodeInfo = firstLines(strings.TrimSpace(out), 200)
			ci.At = time.Now().Format("15:04:05")
		})
		ok(scChain, "SSH", "连接成功：%s", line)
	}()
	_, _ = w.Write([]byte(`{"started":true}`))
}

func handleChainExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Cmd string `json:"cmd"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if strings.TrimSpace(body.Cmd) == "" {
		http.Error(w, "命令为空", 400)
		return
	}
	// 远程命令属于写操作（可能启停链节点），必须独占；且要审计命令内容
	h, granted := beginTaskH("远程执行命令", true)
	if !granted {
		http.Error(w, "有任务正在执行，请稍候", 409)
		return
	}
	go func() {
		defer h.Done()
		fin := auditStart(actUser, "ssh_exec", cfg.Chain.SSHHost, body.Cmd)
		info(scChain, "执行", "$ %s", body.Cmd)
		if err := sshRunStream("执行", body.Cmd); err != nil {
			fail(scChain, "执行", "命令失败：%v", err)
			fin(resFail, err.Error())
		} else {
			ok(scChain, "执行", "命令完成")
			fin(resOK, body.Cmd)
		}
	}()
	_, _ = w.Write([]byte(`{"started":true}`))
}

// 打开本机系统 SSH 终端（真实可交互 shell，密码在该窗口内输入）
func handleChainTerminal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	c := chainCfg()
	if c.SSHHost == "" {
		http.Error(w, "未配置 SSH 主机", 400)
		return
	}
	target := c.SSHUser + "@" + c.SSHHost
	if err := exec.Command("cmd.exe", "/c", "start", "EnvKit SSH", "ssh", "-p", strconv.Itoa(c.SSHPort), target).Start(); err != nil {
		http.Error(w, "打开终端失败："+err.Error(), 500)
		return
	}
	info(scChain, "SSH", "已打开系统终端：ssh -p %d %s", c.SSHPort, target)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// 一键检测：SSH + 20200 + 5002 + 链信息
func handleChainCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	h, granted := beginTaskH("链端检测", false) // 只读：检测不应被排队等待（但要避开写任务）
	if !granted {
		http.Error(w, "有任务正在执行，请稍候", 409)
		return
	}
	go func() {
		defer h.Done()
		fin := auditStart(actUser, "chain_check", cfg.Chain.SSHHost, "")
		doChainCheck()
		fin(resOK, "")
	}()
	_, _ = w.Write([]byte(`{"started":true}`))
}

// doChainCheck 对外入口：允许自动恢复
func doChainCheck() {
	doChainCheckWithRecover(true)
}

// doChainCheckWithRecover 一键检测。allowRecover=true 且用户开启 chain_autorecover 时，
// 若发现节点或 WeBASE 宕机则自动拉起（仅触发一次，递归时传 false 避免死循环）。
func doChainCheckWithRecover(allowRecover bool) {
	c := chainCfg()
	host := c.ChainHost
	if host == "" {
		host = c.SSHHost
	}
	now := time.Now().Format("15:04:05")
	info(scChain, "检测", "目标主机 %s", host)

	// 0) 可达性先判定：地址失效时，后面所有端口结论都不可信（v2.0 P3）
	// 这一步是"虚拟机 IP 变了"与"链挂了"的分水岭——两者在端口探测上表现完全一样，
	// 但只有后者才值得自动重启。NAT 模式下填了虚拟机内网 IP 也归到这里。
	reach := chainReach(c)

	// 1) 区块链端口：公网可达性（从本机 TCP 探测 host:ChainPort）
	extOK, extMS := tcpCheck(host, c.ChainPort)

	// 2) 区块链端口：本地监听（经 SSH 在远程 127.0.0.1 探测，权威判断节点是否真在跑）
	localOK := extOK
	if c.SSHHost != "" {
		up, err := sshLocalPortOpen(c.ChainPort)
		if err != nil {
			// v2.0 P3：不再静默吞掉。SSH 不可用时公网探测只能作参考，必须说清。
			warn(scChain, "检测", "SSH 探测不可用（%s），链端口结论仅供参考：%s", reach, reachHint(c, reach))
		} else {
			localOK = up
		}
	}

	// 2.5) 节点进程数：统计远程 fisco-bcos 进程，任何一个节点挂了都能看见（不只 node0）
	nodeProcs := -1
	if c.SSHHost != "" {
		if out, err := sshRunCapture(`ps -ef | grep fisco-bcos | grep -v grep | wc -l`); err == nil {
			if n, e := strconv.Atoi(strings.TrimSpace(out)); e == nil && n >= 0 {
				nodeProcs = n
			}
		}
	}

	// 3) 端口 5002（WebBASE-Front，公网 TCP）
	ok2, ms2 := tcpCheck(host, c.WebasePort)
	switch {
	case ok2:
		ok(scChain, "检测", "WebBASE-Front 端口 %d 可达（%dms）", c.WebasePort, ms2)
	case reach != reachOK:
		// 地址就联系不上，别把锅甩给 WeBASE——它只是"连不到"而已
		warn(scChain, "检测", "WebBASE-Front 端口 %d 探测失败：链端主机不可达（%s），"+
			"这通常是地址/虚拟机状态问题，不是 WeBASE 自身故障", c.WebasePort, reach)
	default:
		fail(scChain, "检测", "WebBASE-Front 端口 %d 不通（主机可达，故障在服务侧）", c.WebasePort)
	}

	// 4) 链信息（经 webase-front REST，能取到即证明链通、节点存活）
	blockNo, txCnt, ver := "", "", ""
	webaseOK := false
	if ok2 {
		g := strconv.Itoa(c.GroupID)
		// 区块高度（纯文本数字）
		if v, err := webaseText("/" + g + "/web3/blockNumber"); err == nil {
			blockNo = jsonNum(v)
			webaseOK = true
		}
		// 交易数量（{"txSum":..,"blockNumber":..}）
		if v, err := webaseText("/" + g + "/web3/transaction-total"); err == nil {
			var m struct {
				TxSum float64 `json:"txSum"`
			}
			if json.Unmarshal([]byte(v), &m) == nil {
				txCnt = strconv.FormatInt(int64(m.TxSum), 10)
			} else {
				txCnt = jsonNum(v)
			}
		}
		// 客户端版本（{"FISCO-BCOS Version":"2.11.0",...}）
		if v, err := webaseText("/" + g + "/web3/clientVersion"); err == nil {
			var m map[string]interface{}
			if json.Unmarshal([]byte(v), &m) == nil {
				if x, ok := m["FISCO-BCOS Version"].(string); ok {
					ver = x
				} else {
					ver = firstLines(v, 80)
				}
			} else {
				ver = v
			}
		}
		if webaseOK {
			ok(scChain, "链信息", "区块高度 %s · 交易数量 %s · 客户端 %s", blockNo, txCnt, ver)
		} else {
			warn(scChain, "链信息", "WebBASE-Front 可达但取链信息失败（可能未连接节点）")
		}
	} else {
		warn(scChain, "链信息", "WebBASE-Front 不通，跳过链信息读取")
	}

	// 节点存活 = 本地监听 或 WeBASE 已成功读到链信息（二者任一成立即说明节点在跑）
	nodeAlive := localOK || webaseOK

	// 节点进程数汇报
	switch {
	case nodeProcs < 0:
		// SSH 不可用，未探测
	case nodeProcs == 0 && nodeAlive:
		warn(scChain, "检测", "链端口在监听但未发现 fisco-bcos 进程（节点可能由容器/其它方式托管）")
	case nodeProcs == 0:
		warn(scChain, "检测", "未发现 fisco-bcos 进程（节点未运行）")
	default:
		ok(scChain, "检测", "fisco-bcos 节点进程存活 %d 个", nodeProcs)
	}

	// 端口 20200 状态汇报：区分「公网不可达但节点存活」与「节点真挂了」
	if extOK {
		ok(scChain, "检测", "区块链端口 %d 可达（%dms）", c.ChainPort, extMS)
	} else if nodeAlive {
		warn(scChain, "检测", "区块链端口 %d 公网不可达，但节点在本地 127.0.0.1:%d 正常监听（仅本地绑定）；链经 WeBASE 确认正常", c.ChainPort, c.ChainPort)
	} else {
		fail(scChain, "检测", "区块链端口 %d 不通（节点未运行）", c.ChainPort)
	}

	// 自动恢复：检测发现宕机且用户开启 chain_autorecover，则自动拉起（仅一次，递归传 false 防死循环）
	//
	// v2.0 P3 前置条件：主机必须可达。恢复脚本要经 SSH 送到远端执行，
	// 地址失效时这条命令根本送不到，重试多少次都没用——反而会刷屏、污染审计、
	// 让用户以为链在反复崩溃。不可达时直接给出可操作提示，不做任何自动动作。
	if allowRecover && c.ChainAutoRecover && reach != reachOK {
		warn(scChain, "自动恢复", "链端主机不可达（%s），已跳过自动重启。%s", reach, reachHint(c, reach))
		auditNow(actGuard, "chain_unreachable", c.SSHHost, host, resFail,
			"自动恢复已跳过（主机不可达）："+reachHint(c, reach))
		if c.ChainNotify {
			notify("链端不可达，已跳过自动恢复", reachHint(c, reach))
		}
		setChain(func(ci *ChainInfo) {
			ci.Checked = true
			ci.At = now
			ci.Reach = reach.String()
		})
		return
	}
	if allowRecover && c.ChainAutoRecover {
		if !nodeAlive {
			warn(scChain, "自动恢复", "节点未运行，按配置自动重启链端：cd %s && %s", c.ChainDir, c.ChainStart)
			recordRecover()
			if c.ChainNotify {
				alertDispatch("链节点宕机", "检测到节点未运行，正在自动重启链端与 WeBASE...")
			}
			runChainStart("chain")
			// 节点宕机时 WeBASE 连接必断，一并干净重启（WeBASE 依赖节点）
			warn(scChain, "自动恢复", "节点已重启，WeBASE-Front 连接已断，一并重启：cd %s && %s", c.WebaseDir, c.WebaseStart)
			runChainStart("webase")
			// 复验：重启脚本跑完不等于链恢复了，共识是否重新转起来才是硬证据
			vr := verifyChain(verifyChainGrowth)
			auditVerify(actGuard, "chain_autorecover", c.SSHHost, vr)
			switch {
			case vr.Ok && vr.Verified:
				ok(scChain, "自动恢复", "链端已恢复并通过复验：%s", vr.String())
			case vr.Ok:
				warn(scChain, "自动恢复", "链端已拉起但复验未完成：%s", vr.String())
			default:
				fail(scChain, "自动恢复", "链端恢复失败：%s", vr.String())
			}
			if c.ChainNotify {
				alertDispatch("链端自动恢复已执行", "复验结论："+vr.String())
			}
			doChainCheckWithRecover(false)
			return
		}
		if !ok2 {
			warn(scChain, "自动恢复", "WeBASE-Front 不可达，按配置自动重启：cd %s && %s", c.WebaseDir, c.WebaseStart)
			recordRecover()
			runChainStart("webase")
			vr := verifyChain(verifyChainGrowth)
			auditVerify(actGuard, "webase_autorecover", c.SSHHost, vr)
			if vr.Ok && vr.Verified {
				ok(scChain, "自动恢复", "WeBASE 已重启并通过复验：%s", vr.String())
			} else {
				warn(scChain, "自动恢复", "WeBASE 已重启但复验未完成：%s", vr.String())
			}
			doChainCheckWithRecover(false)
			return
		}
	}

	setChain(func(ci *ChainInfo) {
		ci.Checked = true
		ci.At = now
		ci.Port20200 = nodeAlive
		ci.Lat20200 = extMS
		ci.ChainExtReach = extOK
		ci.ChainBindLocal = !extOK && nodeAlive
		ci.Port5002 = ok2
		ci.Lat5002 = ms2
		ci.WebaseOK = webaseOK
		ci.WebaseURL = webaseBaseURL()
		ci.BlockNumber = blockNo
		ci.TxCount = txCnt
		ci.ClientVer = ver
		ci.NodeProcs = nodeProcs
		ci.LastRecover, ci.RecCount = recoverStats()
		ci.Reach = reach.String()
		ci.ReachMsg = reachHint(c, reach)
	})
}

// 远程（Linux）目录浏览：经 SSH 执行 ls
func handleChainLS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	var body struct {
		Path string `json:"path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	roots := []string{"/", "/root", "/home", "/opt", "/usr/local", "~"}
	if chainCfg().SSHHost == "" {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "未配置 SSH 主机，无法浏览远程目录", "drives": roots})
		return
	}
	p := strings.TrimSpace(body.Path)
	if p == "" {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"path": "", "parent": "", "dirs": []string{}, "files": []string{}, "drives": roots})
		return
	}
	esc := "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
	if p == "~" {
		esc = `"$HOME"`
	}
	cmd := `cd ` + esc + ` 2>/dev/null || { echo __ERR__; exit 3; }; echo "PWD=$(pwd)"; echo "PARENT=$(dirname \"$(pwd)\")"; echo __LIST__; ls -1ap`
	out, err := sshRunCapture(cmd)
	if err != nil && strings.TrimSpace(out) == "" {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "SSH 执行失败：" + err.Error(), "drives": roots})
		return
	}
	if strings.Contains(out, "__ERR__") {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "目录不存在或无法进入：" + p, "drives": roots})
		return
	}
	parts := strings.SplitN(out, "__LIST__", 2)
	var pwd, parent string
	for _, ln := range strings.Split(parts[0], "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "PWD=") {
			pwd = strings.TrimPrefix(ln, "PWD=")
		}
		if strings.HasPrefix(ln, "PARENT=") {
			parent = strings.TrimPrefix(ln, "PARENT=")
		}
	}
	var dirs, files []string
	if len(parts) == 2 {
		for _, ln := range strings.Split(parts[1], "\n") {
			ln = strings.TrimRight(ln, "\r")
			if ln == "" || ln == "./" || ln == "../" || ln == "." || ln == ".." {
				continue
			}
			if strings.HasSuffix(ln, "/") {
				dirs = append(dirs, strings.TrimSuffix(ln, "/"))
			} else {
				files = append(files, ln)
			}
		}
	}
	sort.Strings(dirs)
	sort.Strings(files)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"path": pwd, "parent": parent, "dirs": dirs, "files": files, "drives": roots,
	})
}

// 启动脚本：目标 = chain | webase
func handleChainStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Target string `json:"target"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	c := chainCfg()
	var dir, script string
	switch body.Target {
	case "chain":
		dir, script = c.ChainDir, c.ChainStart
	case "webase":
		dir, script = c.WebaseDir, c.WebaseStart
	default:
		http.Error(w, "target 必须为 chain 或 webase", 400)
		return
	}
	if dir == "" || script == "" {
		http.Error(w, "未配置目录或启动脚本", 400)
		return
	}
	h, granted := beginTaskH("启动 "+body.Target, true)
	if !granted {
		http.Error(w, "有任务正在执行，请稍候", 409)
		return
	}
	go func() {
		defer h.Done()
		fin := auditStart(actUser, "chain_start", body.Target, dir+" :: "+script)
		runChainStart(body.Target)
		fin(resOK, body.Target)
		doChainCheck()
		setChain(func(ci *ChainInfo) {
			if body.Target == "chain" {
				ci.ChainStartOK = ci.Port20200
			} else {
				ci.WebaseStartOK = ci.Port5002
			}
		})
	}()
	_, _ = w.Write([]byte(`{"started":true}`))
}

// runChainStart 执行链端/WeBASE 的启动脚本（不持有 beginTask，调用方负责忙碌锁）。
// 远程经 SSH 用登录 shell + setsid 脱离会话；本机直接执行。启动后按目标等待服务就绪。
func runChainStart(target string) {
	c := chainCfg()
	var dir, script string
	switch target {
	case "chain":
		dir, script = c.ChainDir, c.ChainStart
	case "webase":
		dir, script = c.WebaseDir, c.WebaseStart
	default:
		return
	}
	if dir == "" || script == "" {
		warn(scChain, "启动", "未配置 %s 目录或启动脚本，跳过", target)
		return
	}
	if c.SSHHost != "" {
		info(scChain, "启动", "远程执行（登录 shell + 脱离会话）：cd %s && %s", dir, script)
		if err := sshRunStartScript("启动", dir, script); err != nil {
			fail(scChain, "启动", "远程启动 %s 失败：%v", target, err)
			return
		}
	} else {
		info(scChain, "启动", "本机执行：%s（%s）", script, dir)
		if err := execStreamed(scChain, "chain", "启动", dir, nil, script); err != nil {
			fail(scChain, "启动", "本机启动 %s 失败：%v", target, err)
			return
		}
	}
	ok(scChain, "启动", "启动脚本已提交（进程已脱离 SSH 会话），等待服务就绪...")
	// 轮询就绪替代固定 sleep：起来即提前返回，超时早报（WeBASE/tomcat 较慢给 40s，节点 20s）
	deadline := 20 * time.Second
	if target == "webase" {
		deadline = 40 * time.Second
	}
	waitChainReady(target, deadline)
}

// waitChainReady 每 2s 轮询一次目标服务：chain 看远程本地端口；webase 先试 REST，再退回端口探测。
func waitChainReady(target string, timeout time.Duration) {
	c := chainCfg()
	useSSH := c.SSHHost != ""
	probe := func(port int) bool {
		if useSSH {
			okc, _ := sshLocalPortOpen(port)
			return okc
		}
		okc, _ := tcpCheck(c.ChainHost, port)
		return okc
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if target == "chain" {
			if probe(c.ChainPort) {
				ok(scChain, "启动", "链端已就绪（%s:%d 监听）", map[bool]string{true: "远程本地", false: c.ChainHost}[useSSH], c.ChainPort)
				return
			}
		} else {
			if v, err := webaseText("/" + strconv.Itoa(c.GroupID) + "/web3/blockNumber"); err == nil && strings.TrimSpace(v) != "" {
				ok(scChain, "启动", "WeBASE-Front 已就绪（区块高度 %s）", jsonNum(v))
				return
			}
			if probe(c.WebasePort) {
				// 端口已监听但 REST 未就绪，继续等下轮
			}
		}
		time.Sleep(2 * time.Second)
	}
	warn(scChain, "启动", "等待 %s 就绪超时（%v），可稍后点「一键检测」确认", target, timeout)
}

// guardDownNotifyAt 连续不可达到多少次才升级提示一次。
// 不可达通常是"用户还没开机/还没改 IP"这种会持续很久的状态，
// 每轮都弹通知等于骚扰；但完全不提示又会让用户以为守护没在工作。
const guardDownNotifyAt = 3

// ---------- 后台守护：定时轻量探测 + 宕机自动拉起 ----------
// 与「一键检测」的自动恢复互补：无需人工点按钮，适合挂机场景。
// 只做轻探测（SSH 本地端口 + WeBASE 端口），全量检测仍由用户手动触发，避免日志刷屏。
func startGuardLoop() {
	if chainCfg().SSHHost == "" {
		return
	}
	go func() {
		first := true
		guardDownCount := 0
		for {
			c := chainCfg()
			interval := c.ChainGuardSecs
			if interval < 30 {
				interval = 30
			}
			time.Sleep(time.Duration(interval) * time.Second)
			c = chainCfg()
			if !c.ChainGuard || c.SSHHost == "" {
				first = true
				continue
			}
			if currentTask() != "" {
				continue // 有手动任务在跑，本轮跳过
			}
			// v2.0 P3：必须先分清「连不上」和「服务没跑」。
			// 旧代码 `nodeUp, _ := sshLocalPortOpen(...)` 把 err 丢了，而连接失败时
			// nodeUp 是零值 false —— 于是"虚拟机 IP 变了"被当成"节点宕机"，
			// 每 30~300 秒重启一次 + 弹一次桌面通知，而 IP 不会自己变回来，无限循环。
			// 恢复命令本身就靠 SSH 送达：连不上时重启毫无意义，必须直接跳过。
			st := chainReach(c)
			if st != reachOK {
				guardDownCount++
				first = true // 地址失效期间不做"首次恢复"
				warn(scChain, "守护", "链端不可达（%s），本轮不执行自动重启：%s", st, reachHint(c, st))
				// 持续不可达时才升级提示一次，避免每次守护都刷屏
				if guardDownCount == guardDownNotifyAt {
					auditNow(actGuard, "chain_unreachable", c.SSHHost,
						fmt.Sprintf("port %d unreachable", c.ChainPort), resFail, reachHint(c, st))
					if c.ChainNotify {
						notify("链端不可达", "后台守护连续 "+strconv.Itoa(guardDownCount)+" 次连不上链端，请检查虚拟机是否启动、IP 是否变化")
					}
				}
				continue
			}
			guardDownCount = 0

			nodeUp, nodeErr := sshLocalPortOpen(c.ChainPort)
			if nodeErr != nil {
				// 主机可达但远程命令执行失败：不是"端口没监听"，不能判宕机
				warn(scChain, "守护", "无法在链端执行探测命令（%v），本轮跳过自动重启", nodeErr)
				continue
			}
			if !nodeUp {
				warn(scChain, "守护", "定时检测发现节点未运行，自动重启链端与 WeBASE")
				recordRecover()
				// 守护是无人值守的自动动作，必须留痕：否则"链为什么被重启过"无从追溯
				auditNow(actGuard, "chain_autorecover", c.SSHHost, fmt.Sprintf("port %d down", c.ChainPort), resStart, "守护检测触发")
				if c.ChainNotify {
					notify("链节点宕机", "后台守护检测到节点未运行，正在自动重启...")
				}
				runChainStart("chain")
				runChainStart("webase")
				// v2.0 P2 闭环：拉起脚本执行完 ≠ 链真的恢复了，必须复验（端口 + 节点进程 + 共识推进）。
				// 守护是无人值守的，复验结论就是"这次自动恢复到底成没成"的唯一凭据。
				vr := verifyChain(verifyChainGrowth)
				auditVerify(actGuard, "chain_autorecover", c.SSHHost, vr)
				if vr.Ok && vr.Verified {
					ok(scChain, "守护", "链端已自动恢复并通过复验：%s", vr.String())
					if c.ChainNotify {
						notify("链端已恢复", "节点自动重启成功并通过复验："+vr.Msg)
					}
				} else if vr.Ok {
					warn(scChain, "守护", "链端已拉起但复验未完成：%s", vr.String())
					auditNow(actGuard, "chain_autorecover", c.SSHHost, "chain+webase", resFail, "自动恢复未通过复验："+vr.String())
					if c.ChainNotify {
						notify("链端恢复待确认", "节点已拉起但复验未通过："+vr.Msg)
					}
				} else {
					fail(scChain, "守护", "链端自动恢复失败：%s", vr.String())
					auditNow(actGuard, "chain_autorecover", c.SSHHost, "chain+webase", resFail, "自动恢复失败："+vr.String())
					if c.ChainNotify {
						notify("链端恢复失败", "节点自动重启未成功，请打开 EnvKit 检查")
					}
				}
				first = false
				continue
			}
			// 节点活着，只看 WeBASE
			if wok, _ := tcpCheck(c.ChainHost, c.WebasePort); !wok {
				warn(scChain, "守护", "WeBASE-Front 未响应，自动重启 WeBASE")
				recordRecover()
				auditNow(actGuard, "webase_autorecover", c.SSHHost, fmt.Sprintf("port %d down", c.WebasePort), resStart, "守护检测触发")
				if c.ChainNotify {
					alertDispatch("WeBASE-Front 未响应", "后台守护正在自动重启 WeBASE...")
				}
				runChainStart("webase")
			} else if first {
				info(scChain, "守护", "后台守护已开启（每 %ds 检测一次），当前链端正常", interval)
				first = false
			}
		}
	}()
}
