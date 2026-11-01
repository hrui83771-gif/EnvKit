package main

// lanfw.go —— 手机访问被 Windows 防火墙拦住时的放行
//
// ## 为什么需要它（真实故障，不是设想）
//
// 用户反馈："我连的一个 wifi，上次这个网址可以，这次就不可以。"
//
// 排查结论（本机实测，不是推测）：
//   · 服务端完全正常 —— 本机访问 http://192.168.1.12:18766/ 返回 200；
//   · 监听正常 —— `:::18766` 在 LISTEN；
//   · 但 **Windows 防火墙入站允许规则总数 = 0**，而当前 WiFi
//     （1704-5G）被 Windows 判为 **Public（公用网络）**，
//     Public 配置文件的默认入站策略是**阻止**。
//
// 也就是说：EnvKit 自己完全没问题，是 Windows 在中间把包丢了。
// 「上次可以」是因为上次连的是另一个 WiFi —— 那个网络被 Windows 判成
// Private（专用网络），或者当时有别的放行规则。
//
// ## 放行范围必须窄
//
// 只放行 **LAN 端口（18766）**，绝不放行主控制台端口（18765）：
//   · 18765 是完整控制台（改配置 / 跑命令 / 导数据库 / 开终端），
//     本机令牌 + 同源 + 环回三重防护**依赖"只有本机能连"这个前提**；
//   · 18766 是独立 mux，只有 6 条只读/受控路由 + 配对码那道门。
// 把 18765 也放行出去 = 把整个控制台开到局域网，是不可接受的安全退化。
//
// ## 只对当前网络类别生效
//
// 规则用 `-Profile Any`：因为网络类别是 Windows 按 SSID 判定的，
// 同一个「手机访问」功能换个 WiFi 就可能变成另一个类别。
// 只放行端口（不放行整个程序、不放行全部端口），影响面仍然很小。

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const lanFwRuleName = "EnvKit 手机访问 (LAN)"

// lanFwMarkerFile 防火墙规则成功标记（提权脚本验证通过后写入，持久化避免重复提示）。
func lanFwMarkerFile() string { return filepath.Join(exeDir(), "lan_fw.marker") }

// lanFwAllowed 返回 (该端口是否已放行, 防火墙模块是否可用)。
//
// **端口必须由调用方传进来**。这里曾经写死 lanDefaultPort：用户把端口改成 18888
// 之后，规则建在 18888 上，验证却一直去查 18766 —— 于是"放行"永远等不到确认，
// 90 秒后报失败，而实际规则早就在了。诊断同理，会指着错误的端口说"没放行"。
//
// 读防火墙规则需要管理员权限；非管理员上下文读不到是正常的，
// 所以这里只作"快速判断"，权威结果以提权脚本写回的标记文件为准
// （与 Defender 白名单同一套做法，见 av.go）。
func lanFwAllowed(port int) (bool, bool) {
	script := fmt.Sprintf(
		`$ErrorActionPreference='SilentlyContinue'; $r=Get-NetFirewallRule -DisplayName '%s' -ErrorAction SilentlyContinue; if($r -eq $null){exit 1}; if($r.Enabled -ne 'True'){exit 1}; $p=$r|Get-NetFirewallPortFilter; if($p.LocalPort -contains '%d'){exit 0}else{exit 1}`,
		lanFwRuleName, port)
	ctxPS, cancelPS := cmdContext(20 * time.Second)
	defer cancelPS()
	err := exec.CommandContext(ctxPS, "powershell", "-NoProfile", "-EncodedCommand", psEncode(script)).Run()
	if err == nil {
		return true, true
	}
	if ee, ok := err.(*exec.ExitError); ok {
		switch ee.ExitCode() {
		case 1:
			return false, true // 规则不存在或端口不匹配，但防火墙模块可用
		case 2:
			return false, false
		}
	}
	// 读不到不代表没有 —— 非管理员上下文的正常情况。
	// 保守起见按"已放行"处理，避免反复弹 UAC 打扰用户。
	return true, true
}

// lanFwMarkerAllows 提权脚本确认过"这个端口"已放行的标记文件是否存在。
//
// 标记文件里存的就是端口号，所以必须比对内容 —— 只看"文件在"会在改端口之后
// 把上一轮的结果当成这一轮的（那个端口早就不是当前监听的端口了）。
func lanFwMarkerAllows(port int) bool {
	b, err := os.ReadFile(lanFwMarkerFile())
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(b)) == strconv.Itoa(port)
}

// lanFwDiagnose 组装"手机连不上"的自查结论。
//
// 目的是**把责任分清**：到底是 EnvKit 的问题，还是 Windows/路由器的问题。
// 手机端看到"连不上电脑"时，最需要知道的就是"该改哪一边"。
type lanFwDiag struct {
	Allowed   bool `json:"allowed"`
	Available bool `json:"available"`
	Port      int  `json:"port"`
	Listening bool `json:"listening"`
	// NetworkCategory 当前网络被 Windows 判定的类别（Private/Public/…）
	NetworkCategory string `json:"network_category,omitempty"`
	// NetworkName 当前网络名（SSID）
	NetworkName string `json:"network_name,omitempty"`
	// Advice 该用户做什么
	Advice string `json:"advice"`
	// Hint 一句话原因（人话）
	Hint string `json:"hint"`
}

// lanFwDiagnoseGet 诊断"手机连不上"。
//
// 查的是**实际在监听的那个端口**：改端口还没重启时，手机打到的是老端口，
// 拿配置里的新端口去查只会得出"没放行"，方向就错了。
func lanFwDiagnoseGet() lanFwDiag {
	port := lanDisplayPort(lanSnap())
	allowed, avail := lanFwAllowed(port)
	if !avail {
		// 读不到防火墙状态（多半是非管理员上下文读不到）——
		// 这种情况不能报"被防火墙拦"，那是把"我不知道"说成"我知道"。
		allowed = lanFwMarkerAllows(port)
	}
	d := lanFwDiag{
		Allowed:   allowed,
		Available: avail,
		Port:      port,
		Listening: lanServeRunning(),
	}
	d.NetworkCategory, d.NetworkName = lanCurrentNetworkCategory()
	switch {
	case !d.Listening:
		d.Hint = "端口没在监听：手机访问的开关可能已关闭，或端口被别的程序占了"
		d.Advice = "在顶栏「个人中心」下打开「手机访问」，并看日志里有没有「端口无法监听」"
	case !allowed:
		d.Hint = "Windows 防火墙没放行这个端口，手机的请求到不了 EnvKit"
		d.Advice = "点「放行防火墙」按钮，在弹出的 UAC 窗口点「是」"
	default:
		d.Hint = "防火墙已放行、服务端也在监听 —— 如果手机仍连不上，多半是手机不在同一个 WiFi，或路由器开了「AP 隔离」"
		d.Advice = "确认手机连的是同一个 WiFi（不是移动数据、不是访客网络）；路由器的「AP 隔离/客户端隔离」要关掉"
	}
	return d
}

// lanCurrentNetworkCategory 取当前默认网络被 Windows 判定的类别与名字。
//
// 取"有默认路由的那张网卡"对应的连接 —— 那才是手机会连过来的那个网络。
//
// 注意：读网络类别在**非管理员上下文可能失败**（Get-NetConnectionProfile 读不到）。
// 失败就返回空 —— **不能拿不到就说"没有网络类别"当结论**，
// 那会把"我不知道"说成"我知道"（用户据此判断方向就错了）。
func lanCurrentNetworkCategory() (string, string) {
	script := `$ErrorActionPreference='SilentlyContinue'
$gw = Get-NetRoute -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue | Sort-Object RouteMetric | Select-Object -First 1
if($gw -eq $null){ exit 2 }
$alias = (Get-NetIPInterface -InterfaceIndex $gw.InterfaceIndex -ErrorAction SilentlyContinue | Select-Object -First 1).InterfaceAlias
$prof = Get-NetConnectionProfile -InterfaceAlias $alias -ErrorAction SilentlyContinue | Select-Object -First 1
if($prof -eq $null){ exit 3 }
[Console]::Out.Write($prof.NetworkCategory + '|' + $prof.Name)`
	ctxPS, cancelPS := cmdContext(20 * time.Second)
	defer cancelPS()
	out, err := exec.CommandContext(ctxPS, "powershell", "-NoProfile", "-EncodedCommand", psEncode(script)).Output()
	if err != nil {
		return "", ""
	}
	s := strings.TrimSpace(string(out))
	parts := strings.SplitN(s, "|", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

// lanFwTask 放行防火墙端口（供按钮与 AI 工具共用）；同步等待确认（最长 90s）。
//
// 与 whitelistTask（Defender 白名单）同一套结构：
// 非管理员 → 经 UAC 提权跑一个脚本 → 脚本回写结果文件 → 本函数轮询确认。
func lanFwTask() error {
	h, granted := beginTaskH("放行防火墙端口", true)
	if !granted {
		return fmt.Errorf("有任务正在执行，请稍候")
	}
	done := make(chan struct{})
	go func() {
		defer h.Done()
		defer close(done)
		// 放行的是**当前实际监听**的端口（手机真正打过来的那个）。
		port := lanDisplayPort(lanSnap())
		fin := auditStart(actUser, "lan_fw_allow", lanFwRuleName, "")
		defer func() {
			if allowed, _ := lanFwAllowed(port); allowed {
				fin(resOK, "已放行 TCP/"+strconv.Itoa(port))
			} else {
				fin(resFail, "未确认放行")
			}
		}()
		// 已经放行过就不用再折腾用户点 UAC
		if allowed, _ := lanFwAllowed(port); allowed && lanFwMarkerAllows(port) {
			return
		}
		dir := exeDir()
		okFile := filepath.Join(dir, "_fw_ok.txt")
		errFile := filepath.Join(dir, "_fw_err.txt")
		_ = os.Remove(okFile)
		_ = os.Remove(errFile)

		// 提权脚本：先删同名规则再重建（避免重复添加产生多条同名规则），
		// 只放行 LAN 端口，不放行主控制台端口。
		inner := fmt.Sprintf(
			`$ErrorActionPreference='Stop'
try {
  Get-NetFirewallRule -DisplayName '%s' -ErrorAction SilentlyContinue | Remove-NetFirewallRule -ErrorAction SilentlyContinue
  New-NetFirewallRule -DisplayName '%s' -Direction Inbound -Action Allow -Protocol TCP -LocalPort %d -Profile Any -Enabled True | Out-Null
  'OK' | Out-File '%s' -Encoding utf8
} catch {
  $_.Exception.Message | Out-File '%s' -Encoding utf8
  exit 1
}`,
			lanFwRuleName, lanFwRuleName, port, okFile, errFile)

		if isAdminFlag {
			warn(scSys, "防火墙", "以管理员身份写入防火墙放行规则...")
			ctxA, cancelA := cmdContext(2 * time.Minute)
			err := exec.CommandContext(ctxA, "powershell", "-NoProfile", "-EncodedCommand", psEncode(inner)).Run()
			cancelA()
			if err != nil {
				fail(scSys, "防火墙", "写入失败：%v", err)
				return
			}
		} else {
			outer := fmt.Sprintf(
				`Start-Process powershell -Verb RunAs -WindowStyle Hidden -ArgumentList @('-NoProfile','-EncodedCommand','%s')`, psEncode(inner))
			warn(scSys, "防火墙", "正在请求管理员权限，请在弹出的 UAC 窗口点「是」...")
			if err := exec.Command("powershell", "-NoProfile", "-EncodedCommand", psEncode(outer)).Start(); err != nil {
				fail(scSys, "防火墙", "启动提权进程失败：%v", err)
				return
			}
		}
		// 轮询确认（等用户点 UAC，最长 90s）
		for i := 0; i < 45; i++ {
			time.Sleep(2 * time.Second)
			// 注意：变量别叫 ok —— 那会遮蔽同名的日志函数 ok()。
			if allowed, _ := lanFwAllowed(port); allowed {
				_ = os.WriteFile(lanFwMarkerFile(), []byte(strconv.Itoa(port)), 0o644)
				ok(scSys, "防火墙", "已放行 TCP/%d（手机可连）", port)
				return
			}
		}
		if b, err := os.ReadFile(errFile); err == nil && len(b) > 0 {
			fail(scSys, "防火墙", "写入被拒绝：%s。可手动执行：netsh advfirewall firewall add rule name=\"%s\" dir=in action=allow protocol=TCP localport=%d",
				firstLines(strings.TrimSpace(string(b)), 200), lanFwRuleName, port)
			return
		}
		fail(scSys, "防火墙", "超时未确认（可能 UAC 弹窗被挡住或点了「否」）。可手动执行：netsh advfirewall firewall add rule name=\"%s\" dir=in action=allow protocol=TCP localport=%d",
			lanFwRuleName, port)
	}()
	<-done
	return nil
}

// handleLanFW 防火墙放行端点。
//
// GET  → 诊断（放行了没 / 谁挡的 / 该怎么办）
// POST → 放行（提权）
func handleLanFW(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method == http.MethodGet {
		writeJSON(w, map[string]any{"ok": true, "diag": lanFwDiagnoseGet()})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if err := lanFwTask(); err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "diag": lanFwDiagnoseGet()})
}
