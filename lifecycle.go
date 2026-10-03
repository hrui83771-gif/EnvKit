package main

// lifecycle.go —— v2.2 M10：进程生命周期验证
//
// ## 现有验证的盲区
//
// verifyService 判"就绪"的依据是：进程存活 + 端口 LISTENING + HTTP 握手。
// 这三项都在**同一时刻**成立才算通过——但现实里最常见的故障是：
//
//	服务起来了 → 端口监听了 → 复验通过 → 2 秒后崩了
//	（内存溢出、依赖连不上、健康检查失败、OOM killer、配置错误只在处理请求时暴露）
//
// 现有验证对这种故障**完全失明**：它只看"监听的那一刻"，之后发生了什么一概不管。
// 于是用户看到的是"EnvKit 说启动成功"，几秒后进程没了，日志里只有一句 stack trace。
//
// 三个要补的洞：
//
//  1. **存活观察窗**：端口监听后再观察若干秒，期间崩溃才算启动失败
//  2. **崩溃证据**：失败必须附日志尾部，否则用户只能自己去翻日志猜
//  3. **端口漂移**：实际监听端口与上次不同要记下来——服务换端口了但配置没改，
//     下次 AI 复验会去错误的端口上找
//
// ## 为什么观察窗默认只有几秒
//
// 观察窗是**同步阻塞**的，AI 工具调用会等它。窗口开太大（30s）会让每次启动
// 验证都慢成蜗牛，用户会以为卡死了。默认 5 秒——足以抓住"启动即崩"这类
// 绝大多数故障，又不至于让人难受。可通过 verifyWaitWeb/BACKEND 之外单独配置。
//
// ## 端口漂移为什么只记录不判失败
//
// 服务换了端口**可能是对的**（用户改了配置、框架自动选端口）。判失败会造成误报，
// 而误报在验证层是特别糟糕的——用户会开始不信复验结论。
// 所以只记入证据与状态，让人和 AI 都能看到"端口变了"，但结论仍以端口是否监听说。

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// verifyObserveWindowDefault 端口监听后的存活观察窗。
	verifyObserveWindowDefault = 5 * time.Second
	// verifyObserveEveryDefault 观察窗内的轮询间隔。
	verifyObserveEveryDefault = 500 * time.Millisecond
)

// 实际使用的窗口与间隔。用 var 而非 const 是为了让单测能压短——
// 真实环境里"起来又崩"难复现，靠注入假实现来测，窗口必须能缩到毫秒级。
// 生产代码不要改这两个值。
var (
	verifyObserveWindow = verifyObserveWindowDefault
	verifyObserveEvery  = verifyObserveEveryDefault
)

// 错误归类：启动后崩溃。与 errKindSpawnFail（根本没起来）区分开，
// 因为两者的处置完全不同——前者要看日志找运行时问题，后者要看环境。
const errKindCrash = "crash_after_start"

// svcPortDrift 记录每个服务最近一次验证通过的端口，用于发现漂移。
var svcPortDrift = struct {
	sync.Mutex
	m map[string]int
}{m: map[string]int{}}

// svcRememberPort 记住本次验证通过的端口，返回上一次的值（无则 0）。
func svcRememberPort(target string, port int) int {
	svcPortDrift.Lock()
	defer svcPortDrift.Unlock()
	prev := svcPortDrift.m[target]
	svcPortDrift.m[target] = port
	return prev
}

// svcForgetPort 服务停止/重启时清掉记录，避免拿旧端口去比。
func svcForgetPort(target string) {
	svcPortDrift.Lock()
	delete(svcPortDrift.m, target)
	svcPortDrift.Unlock()
}

// 可替换点：单测注入假实现，构造"起来又崩"这种真实环境里难复现的场景。
// 生产代码走 svcAlive / portOwner 本身，这里只是转发。
var (
	lifecycleAlive  = func(target string) (bool, int) { return svcAlive(target) }
	lifecycleListen = func(port int) bool { return portOwner(port) != "" }
)

// verifyObserve 端口已监听、owner 已匹配之后的存活观察。
//
// 这是整个验证层最关键的一段：它把"曾经监听过"升级为"现在还在跑"。
// 期间任何一次发现进程消失，都判为启动失败并附上日志尾部。
func verifyObserve(action, target string, port, pid int, owner string, baseEv []string) OpResult {
	label := svcLabel(target)
	ev := append([]string{}, baseEv...)
	ev = append(ev, "observe_window="+verifyObserveWindow.String())

	// 端口漂移：与上次验证通过的端口比对
	if prev := svcRememberPort(target, port); prev > 0 && prev != port {
		ev = append(ev, fmt.Sprintf("port_drift=%d→%d（服务换了端口，配置里的 scan_ports 可能已过期）", prev, port))
		warn(scStart, target, label+"监听端口从 %d 变为 %d", prev, port)
	}

	end := time.Now().Add(verifyObserveWindow)
	for time.Now().Before(end) {
		time.Sleep(verifyObserveEvery)
		// ① 进程还在吗
		if alive, p := lifecycleAlive(target); !alive {
			tail := svcLogTail(target, 12)
			ev2 := append([]string{}, ev...)
			ev2 = append(ev2, "crashed_after="+verifyObserveWindow.String(),
				"last_pid="+strconv.Itoa(pid), "dead_pid="+strconv.Itoa(p))
			if tail != "" {
				ev2 = append(ev2, "crash_log_tail="+tail)
			}
			return opFail(action, target, errKindCrash,
				label+"在启动成功后的 "+verifyObserveWindow.String()+" 内崩溃退出（端口曾监听但进程已消失）",
				append(ev2, "hint="+lifecycleHint(errKindCrash))...)
		}
		// ② 端口还在监听吗（进程活着但端口没了 = 监听被关闭，也不算就绪）
		if !lifecycleListen(port) {
			ev2 := append([]string{}, ev...)
			ev2 = append(ev2, "port_closed="+strconv.Itoa(port))
			if tail := svcLogTail(target, 12); tail != "" {
				ev2 = append(ev2, "log_tail="+tail)
			}
			return opFail(action, target, errKindCrash,
				label+"进程仍在但端口 "+strconv.Itoa(port)+" 已不再监听（服务内部异常或健康检查失败）",
				append(ev2, "hint="+lifecycleHint(errKindCrash))...)
		}
	}
	// 观察窗内一切正常
	ev = append(ev, fmt.Sprintf("survived=%s", verifyObserveWindow))
	if owner != "" {
		ev = append(ev, "owner="+owner)
	}
	return opVerified(action, target,
		fmt.Sprintf("%s已就绪：端口 %d 在监听且进程持续存活 %s", label, port, verifyObserveWindow), ev...)
}

// svcLogTail 取某服务最近的日志尾部，作为崩溃证据。
//
// 为什么必须带日志：只说"进程退出"对用户毫无帮助价值——真正的原因
// （端口被占、依赖缺失、配置错误、OOM）几乎总在最后几行里。
func svcLogTail(target string, n int) string {
	if n <= 0 {
		n = 10
	}
	hub.mu.Lock()
	hist := append([]LogMsg{}, hub.history...)
	hub.mu.Unlock()

	var picked []string
	for i := len(hist) - 1; i >= 0 && len(picked) < n; i-- {
		m := hist[i]
		// 严格匹配 Src：只有该服务自己打的日志才算崩溃证据。
		//
		// 踩过的坑：原先的条件是 `m.Src != "" && m.Src != target`，
		// 意图是"没标来源的就放过"。但服务日志一律带 Src（infoS(scStart, "web", …)），
		// 没带 Src 的全是系统级日志（配置校验、告警、检测…）。
		// 那些日志会挤满 12 条上限，把真正的崩溃原因顶出去——
		// 实测一次崩溃取证拿到的是三条"Go 代理不能为空"，崩溃原因不翼而飞。
		if m.Src != target {
			continue
		}
		line := strings.TrimSpace(m.Line)
		if line == "" {
			continue
		}
		// 跳过 EnvKit 自己写的"已启动"这类噪音——它不是崩溃原因
		if strings.Contains(line, "已启动") || strings.Contains(line, "启动中") {
			continue
		}
		picked = append(picked, line)
	}
	if len(picked) == 0 {
		return ""
	}
	// 反转成时间正序
	for i, j := 0, len(picked)-1; i < j; i, j = i+1, j-1 {
		picked[i], picked[j] = picked[j], picked[i]
	}
	return firstLines(strings.Join(picked, " | "), 400)
}

// portListening 该端口当前是否处于 LISTENING 状态。
func portListening(port int) bool {
	return lifecycleListen(port)
}

// lifecycleHint 按错误归类给出下一步建议。
// 崩溃与"起不来"要分开指引：前者去看运行时日志，后者去看环境。
func lifecycleHint(kind string) string {
	switch kind {
	case errKindCrash:
		return "崩溃说明进程起来了但没能持续运行：先看「程序启动」日志的最后几行（通常是依赖缺失、配置错误或内存不足），修完再重启"
	case errKindSpawnFail:
		return "进程没能启动：常见原因是端口被占用、依赖缺失或编译产物崩溃"
	}
	return ""
}
