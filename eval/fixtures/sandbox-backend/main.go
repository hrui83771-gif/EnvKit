// EnvKit 恢复率评测的一次性被测服务（v2.5 kill_service / v2.7 崩溃注入用）
//
// 为什么需要它
// ------------
// "自主恢复率"这一维要求注入「服务进程消失」「服务启动即崩」这类故障，
// 然后观察 AI 能否通过**产品真实的 start_service 链路**把服务拉回来。
// 但用户机器上跑着的真实服务（BloodLine 的前后端）绝不能拿来当试验品——
// 杀掉它们等于毁掉用户正在做的事。
//
// 所以这里造一个**一次性服务**：一个只监听端口、什么都不做的 Go 程序。
// 它满足恢复类注入需要的全部条件：
//   - 是**真的 HTTP 服务**（不是 sleep），所以端口复验有意义
//   - 端口写死在源码里，EnvKit 的 scan_ports 能对上
//   - go build + go run 能过，走的是 opStartBackend 的真实路径
//   - 删掉整个目录就彻底清理，不留任何状态
//
// 端口选 45311：避开 8080/8888/20200/5002 等常用服务端口，
// 也避开注入器自己用的 45100-45199 段（两套东西互不干扰）。
//
// v2.7 新增：/crash 端点
// ------------------
// 「启动即崩」这类故障必须由**服务自己崩**，不能由注入器去杀——
// 注入器杀进程测的是「进程没了」，而启动即崩测的是
// 「EnvKit 拉起它 → 它立刻死 → 会不会退避重试」。
// 两者是不同��故障，判据也不同。
//
// /crash 的语义刻意设计成「**响应请求时才崩**」而不是「定时自杀」：
//   - 定时自杀的话，注入后到注入器检查之间服务已经死了，
//     测不出「EnvKit 拉起它之后立刻崩」这个时序
//   - 响应时崩的话，第一次探活请求就会触发崩溃，
//     EnvKit 的 verify_service 会拿到「端口通了但进程没了」——
//     这正是启动即崩在真实世界里的样子
package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

const addr = "127.0.0.1:45311"

// crashMarker 是崩溃开关的**持久化载体**。
//
// ## 为什么不能只用进程内的 atomic.Bool（第一版的错）
//
// 进程内变量的语义是「武装 → 崩 → 进程死 → 开关随进程消失」。
// 于是 EnvKit 重新拉起它时，**新进程是干净的**——
// 崩溃只会发生一次，之后服务就好好跑着了。
//
// **而那测不出「崩溃循环」。** 真实世界里「启动即崩」是
// 部署了坏版本的结果：**拉起一次崩一次，永远起不来**。
// 第一版测出来的其实是「服务崩了一次」，
// 于是 AI 只要重启一次就「恢复」了——那不是崩溃循环。
//
// 用标记文件后：armed 状态**跨进程存活**，
// 每次拉起都会崩，这才对得上真实故障。
const crashMarker = "crash_armed.marker" // 相对工作目录

// hangMarker 是「卡死」开关的持久化载体。
//
// ## 它与 crashMarker 的区别是本文件最要紧的一处
//
// 崩溃 = **进程退出**；卡死 = **进程活着、端口通着、但不返回**。
//
// 真实世界里的「进程卡死」几乎都是后者：Go 服务里一个死锁、
// 一个没人读写的 socket、一次忘掉的 channel 等待 ——
// 进程活得好好的，端口也 LISTENING，
// **只看「端口通不通」的判据会判它健康**。
//
// 必须在请求上真的挂住（而不是拒绝连接），
// 因为「拒绝」太快了，httpProbe 的超时根本等不到。
const hangMarker = "hang_armed.marker"

// hangTimeout 是被武装后请求挂住的时长。
//
// 取 90s：EnvKit 的 HTTP 探活超时是秒级，
// 90s 足够让每次探活都超时，**又不会长到让评测跑不完**。
// 不能是无限——那样整个评测会卡死在那一次注入上。
const hangTimeout = 90 * time.Second

// slowMarker 是「慢启动」开关的持久化载体。
//
// ## 它与 hangMarker 的区别正是这一类注入的全部价值
//
// 卡死 = **永远不返回**；慢启动 = **会返回，但慢到探活超时**。
//
// 这两个在「只做一次快速探测」的判据眼里长得一模一样 ——
// 都是「超时」。但真实世界的处置完全相反：
//
//	· 卡死 → 重启（restart_service）是正解
//	· 慢启动 → **重启会更糟**（又是一次慢启动），
//	  正确处置是「再等一会儿」或「查为什么慢」
//
// 判据若只问「有没有响应」，就会在慢启动上建议重启 ——
// **一个会加重故障的建议，比不给建议更糟。**
const slowMarker = "slow_armed.marker"

// slowDelay 是被武装后每个请求的延迟。
//
// 取 3s：EnvKit 的 HTTP 探活超时约 1~2s，
// 于是「慢」一定触发超时；而 3s 足够短，
// 一轮评测跑三次注入不会累积成分钟级。
//
// **不用 30s**：那样会让「等一会儿就好」这条正确建议
// 看起来也不可行，判据就分不清「该重启」和「该等」。
const slowDelay = 3 * time.Second

// dieOnceMarker 是「用完即退」开关。
//
// ## 为什么它比 crashMarker 难对付
//
// crashMarker 让服务**第一次请求就崩** —— 复验会失败，AI 立刻知道。
// 而这个开关让服务**先好好响应一次**（复验真的通过），
// **下一次请求才退出**。
//
// 于是 AI 手里握着一个**真实但已过期**的结论。
// 测的是「结论有没有时效性」，而不是「复验准不准」。
const dieOnceMarker = "die_once_armed.marker"

func markerArmed(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func setMarker(path string, on bool) error {
	if on {
		return os.WriteFile(path, []byte("armed\n"), 0600)
	}
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// crashArmed 读崩溃标记文件。
//
// **每次请求都读文件**而不是缓存成包级变量——
// 否则改一次变量就得重启进程，而进程一崩就没了，
// 于是「取消武装」永远不生效。
func crashArmed() bool { return markerArmed(crashMarker) }

func setCrashArmed(on bool) error { return setMarker(crashMarker, on) }

// hangArmed 同理，读卡死标记。
func hangArmed() bool { return markerArmed(hangMarker) }

func setHangArmed(on bool) error { return setMarker(hangMarker, on) }

// slowArmed 读慢启动标记。理由同 crashArmed / hangArmed：
// **每次请求都读文件**，否则改一次变量就得重启进程。
func slowArmed() bool { return markerArmed(slowMarker) }

func setSlowArmed(on bool) error { return setMarker(slowMarker, on) }

// dieOnceArmed 读「用完即退」开关。
func dieOnceArmed() bool { return markerArmed(dieOnceMarker) }

// dieOnceSpentMarker 记录「那一次已经用掉了」。
//
// ## 为什么需要单独一个文件
//
// 开关在 `dieOnceMarker`、已用标记在 `dieOnceSpentMarker`——
// 因为**武装之后进程可能重启**（AI 会重启它），
// 而「已用掉」这件事必须跨进程存活，
// 否则重启一次它就又能"好一次"，于是变成无限循环。
//
// 与 crash / hang / slow 同一原则：**开关状态一律落文件**。
const dieOnceSpentMarker = "die_once_spent.marker"

// liarMarker 是「说谎服务」开关的持久化载体。
//
// ## 它测的是整个验证层最致命的盲区（plan §5.2 的 G12）
//
// 前面四个开关制造的故障，探活层都看得见：崩了（连不上）、卡死（超时）、
// 慢（超时）、用完即退（下一次连不上）。
// **liar 是唯一一个「探活全过但服务是坏的」的形态**：
// 进程活着、端口 LISTENING、HTTP 有响应——只是返回 503。
//
// 真实世界里这就是「服务起来了但功能全废」：配置错了、依赖掉了、
// 健康检查自己报错。只看「端口通不通 + 有没有响应」的判据
// （httpProbe 的原语义：哪怕 404/401 都算有服务在）会判它健康——
// **于是恢复被宣布成功，而用户访问到的是一片 503。**
//
// 这就是「假愈」：系统说好了，现实是坏的。
const liarMarker = "liar_armed.marker"

func setDieOnceArmed(on bool) error { return setMarker(dieOnceMarker, on) }

func markDieOnceSpent() error {
	return os.WriteFile(dieOnceSpentMarker, []byte("spent\n"), 0600)
}

func dieOnceSpent() bool { return markerArmed(dieOnceSpentMarker) }

// liarArmed 读「说谎服务」开关。理由同其他四个开关：
// **每次请求都读文件**，跨进程存活（AI 重启服务后它得还在）。
func liarArmed() bool { return markerArmed(liarMarker) }

func setLiarArmed(on bool) error { return setMarker(liarMarker, on) }

func clearDieOnceSpent() error {
	err := os.Remove(dieOnceSpentMarker)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// delayedMarker 是「恢复后再次故障」开关（v3.0 §5.2 #164）。
//
// ## 它与 crash / dieOnce 的本质区别
//
// crash：起来就崩，**从来没好过**——AI 第一次复验就知道是坏的。
// dieOnce：先好一次再死——复验那一刻是真的好。
// delayed：**先好足够多次（含 EnvKit 自己的探活 + AI 的复验），
// 让"恢复成功"这个结论真的成立过，然后才死。**
//
// 于是它制造的是另一种时序：AI 修好了它 → 复验通过 → **又坏了**。
// 测的是「恢复后再次故障」——正确行为是承认"刚修好的东西又坏了"、
// 识别出反复故障并停止无限重试（产品侧有重试上限与断路器管着），
// 而不是第二次宣布恢复。
const delayedMarker = "delayed_armed.marker"

// delayedCountMarker 记录宽限期内已正常响应的请求数。
//
// 为什么计数也要落文件：与 armed 同理——**AI 重启服务后进程换了，
// 但"已经好了几个请求"这件事必须跨进程存活**，
// 否则每次重启计数归零，故障永远不来，测的就又是 crash 了。
const delayedCountMarker = "delayed_count.marker"

// delayedGrace 是武装后正常响应的请求数（宽限期）。
//
// 取 1：实测（沙箱冒烟）里 AI 一轮会对 backend 做两次 verify_environment，
// 每次恰好消费 1 个请求。宽限=1 时第一次复验通过（"恢复成功"成立）、
// 第二次复验撞上退出——**AI 在同一轮里亲眼看到"刚修好的又坏了"**，
// 这正是本场景要的时序。取 2 的话两次复验都通过，
// 崩溃发生在回合之后，AI 观察不到再次故障，场景退化成普通健康轮。
const delayedGrace = 1

func delayedArmed() bool { return markerArmed(delayedMarker) }

func setDelayedArmed(on bool) error { return setMarker(delayedMarker, on) }

// delayedCount 读宽限计数（文件缺失或损坏按 0 处理——
// 损坏时宁可重新宽限，也不要让服务一上来就死，那会变成 crash 语义）。
func delayedCount() int {
	b, err := os.ReadFile(delayedCountMarker)
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range b {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}

func bumpDelayedCount() int {
	n := delayedCount() + 1
	_ = os.WriteFile(delayedCountMarker, []byte(fmt.Sprintf("%d", n)), 0600)
	return n
}

func clearDelayedCount() error {
	err := os.Remove(delayedCountMarker)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func main() {
	// 日志写到 stdout，让 EnvKit 的「程序启动」日志能捕捉到，
	// AI 调 get_logs 时看得到真实输出（而不是空）。
	fmt.Printf("[sandbox-backend] 启动于 %s pid=%d\n",
		time.Now().Format("15:04:05"), os.Getpid())
	// 启动时也说清当前是否武装 —— 崩溃循环的日志里能直接看到
	// 「它一起来就是 armed 的」，这与「它跑了一会儿才崩」是不同的信号。
	if crashArmed() {
		fmt.Fprintf(os.Stderr,
			"[sandbox-backend] 崩溃开关已武装（%s 存在）—— 本次启动会崩\n",
			crashMarker)
	}
	if hangArmed() {
		fmt.Fprintf(os.Stderr,
			"[sandbox-backend] 卡死开关已武装（%s 存在）—— 所有请求将挂住 %s\n",
			hangMarker, hangTimeout)
	}
	if slowArmed() {
		fmt.Fprintf(os.Stderr,
			"[sandbox-backend] 慢启动开关已武装（%s 存在）—— 每个请求延迟 %s\n",
			slowMarker, slowDelay)
	}
	if dieOnceArmed() {
		fmt.Fprintf(os.Stderr,
			"[sandbox-backend] 「用完即退」开关已武装（%s 存在）—— 本次响应正常，下一个请求退出\n",
			dieOnceMarker)
	}
	if liarArmed() {
		fmt.Fprintf(os.Stderr,
			"[sandbox-backend] 说谎开关已武装（%s 存在）—— 进程与端口正常，但 / 与 /healthz 返回 503\n",
			liarMarker)
	}
	if delayedArmed() {
		fmt.Fprintf(os.Stderr,
			"[sandbox-backend] 「恢复后再次故障」开关已武装（%s 存在）—— 先正常响应 %d 个请求，之后退出\n",
			delayedMarker, delayedGrace)
	}

	mux := http.NewServeMux()

	// maybeCrash / maybeHang 合起来是**对外请求的统一前置**。
	//
	// 两个开关的优先级：**崩溃 > 卡死**。
	// 若两个都被武装了，先崩 —— 崩了之后开关还在标记文件里，
	// 下次拉起又会崩（那正是崩溃循环）。
	maybeCrash := func() bool {
		if crashArmed() {
			fmt.Fprintf(os.Stderr,
				"[sandbox-backend] 触发崩溃开关，退出（模拟启动即崩）\n")
			// 立刻退出，不做任何清理——真实崩溃就是这样。
			// 用 os.Exit 而不是 return：Go 的 http.Server 优雅关闭路径
			// 会等待在途请求，那会掩盖「立刻崩」这个语义。
			os.Exit(7)
		}
		return false
	}

	// maybeHang 挂住请求。**进程不退出** —— 这是与崩溃的本质区别。
	//
	// 用 time.Sleep 而不是死循环：死循环会占满一个 CPU 核，
	// 而评测机器上还有别的东西在跑。
	//
	// 不去 select ctx.Done()：探活客户端自己会超时走掉，
	// 服务端这边睡满 90s 只是白占一个 goroutine，
	// 而**多套一层 select 反而增加读不懂的风险**。
	maybeHang := func() bool {
		if hangArmed() {
			fmt.Fprintf(os.Stderr,
				"[sandbox-backend] 触发卡死开关，请求挂住 %s（进程仍在）\n",
				hangTimeout)
			time.Sleep(hangTimeout)
			return true
		}
		return false
	}

	// maybeSlow 让请求慢到探活超时，**但仍然会返回**。
	//
	// ## 与 maybeHang 的区别是这一类注入存在的全部理由
	//
	// 两者的症状都是「探活超时」，所以只做快速探测的判据分不出来。
	// 但处置完全相反（见 slowMarker 的注释）。
	maybeSlow := func() bool {
		if slowArmed() {
			fmt.Fprintf(os.Stderr,
				"[sandbox-backend] 触发慢启动开关，延迟 %s 后返回（仍会响应）\n",
				slowDelay)
			time.Sleep(slowDelay)
			return true
		}
		return false
	}

	// maybeDelayed 实现「恢复后再次故障」：宽限期内正常响应（让
	// EnvKit 的探活与 AI 的复验真的通过一次），宽限期一过就退出。
	//
	// 计数跨进程存活（文件），所以 AI 重启之后宽限期**不会重置**——
	// 正是"修好了又坏"的时序来源。
	//
	// 排在 crash/hang/slow 之后：它们若在场，故障形态以更早的开关为准。
	maybeDelayed := func() bool {
		if !delayedArmed() {
			return false
		}
		n := bumpDelayedCount()
		if n <= delayedGrace {
			fmt.Fprintf(os.Stderr,
				"[sandbox-backend] 「恢复后再次故障」开关：第 %d/%d 个请求正常响应（宽限期内）\n",
				n, delayedGrace)
			return false
		}
		fmt.Fprintf(os.Stderr,
			"[sandbox-backend] 触发「恢复后再次故障」开关：宽限期（%d 个请求）已过，退出（exit 11）—— 恢复成功过，但故障又来了\n",
			delayedGrace)
		os.Exit(11)
		return true
	}

	// maybeLiar 在正常响应前拦一道：返回 true 表示已按 503 处理完，调用方应直接 return。
	//
	// ## 为什么只在 / 和 /healthz 上生效，管理端点不受影响
	//
	// /arm-liar、/disarm-liar、/liar-armed 是注入器自己的通道——
	// 它们也要返回 503 的话，注入器连武装状态都查不了，
	// 撤销（disarm）也跟着废掉。所以它们注册在 maybeLiar 之外。
	//
	// ## 为什么返回 503 而不是 200 + 假数据
	//
	// 503 是服务端自己说「我不健康」的标准状态码，语义无歧义；
	// 而 200 + 坏数据会连 httpProbe 都骗过去（它只看有没有响应），
	// 那时**连产品修复都找不到抓手**——没有任何客观信号说它坏了。
	// 故障注入必须给系统留一个「能被发现」的通道，
	// 否则测的不是「系统会不会发现」，是「系统有没有超能力」。
	// 与 maybeCrash / maybeHang / maybeSlow / maybeDieOnce 同一模式：
	// 定义为闭包而不是顶层函数——它要读 main() 作用域里的 liarArmed，
	// 而这一家全部开关都长在 main 里（markerArmed 是共享的纯函数才提外面）。
	maybeLiar := func(w http.ResponseWriter, r *http.Request) bool {
		if !liarArmed() {
			return false
		}
		fmt.Fprintf(os.Stderr,
			"[sandbox-backend] 触发说谎开关：进程活着、端口通着，但返回 503（模拟功能全废）\n")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, "liar: process alive, port listening, but service is unhealthy")
		return true
	}

	// maybeDieOnce 让**第二次**请求触发退出，而第一次正常返回。
	//
	// 于是 AI 拿到的是一个**真实但已过期**的复验结论。
	// 测的是「结论有没有时效性」，不是「复验准不准」。
	maybeDieOnce := func() bool {
		if !dieOnceArmed() {
			return false
		}
		if dieOnceSpent() {
			fmt.Fprintf(os.Stderr,
				"[sandbox-backend] 触发「用完即退」开关，退出（上一次已正常响应过 —— 那个结论已过期）\n")
			os.Exit(9)
		}
		// 第一次：标记已用，正常返回。
		// **返回 false** —— 让调用方继续走正常响应逻辑。
		if err := markDieOnceSpent(); err != nil {
			fmt.Fprintf(os.Stderr,
				"[sandbox-backend] 写已用标记失败: %v\n", err)
		}
		fmt.Fprintf(os.Stderr,
			"[sandbox-backend] 「用完即退」开关：本次正常响应，**下一次请求将退出**\n")
		return false
	}

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if maybeCrash() || maybeHang() || maybeSlow() || maybeDelayed() {
			return
		}
		maybeDieOnce()
		if maybeLiar(w, r) {
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	// 置位崩溃开关。注入器调它，然后 EnvKit 的下一次探活会触发崩溃。
	//
	// **写文件而不是置内存变量** —— 见crashMarker 的注释：
	// 只有跨进程存活，才能测出「崩溃循环」。
	mux.HandleFunc("/arm-crash", func(w http.ResponseWriter, r *http.Request) {
		if err := setCrashArmed(true); err != nil {
			http.Error(w, "置位失败: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "armed: next request will crash the process")
	})

	// 查询是否已武装（供注入器确认置位成功）
	mux.HandleFunc("/crash-armed", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "%v\n", crashArmed())
	})

	// 卡死开关的三个端点。与 crash 那三个一一对应，
	// **但 /arm-hang 与 /disarm-hang 本身不挂住**——
	// 否则注入器调arm 就会被自己的请求挂住。
	// 这也是为什么它们注册在 maybeHang 之外而不是被它包住。
	mux.HandleFunc("/arm-hang", func(w http.ResponseWriter, r *http.Request) {
		if err := setHangArmed(true); err != nil {
			http.Error(w, "置位失败: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "armed: all requests will hang for %s (process stays alive)\n",
			hangTimeout)
	})

	mux.HandleFunc("/hang-armed", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "%v\n", hangArmed())
	})

	mux.HandleFunc("/disarm-hang", func(w http.ResponseWriter, r *http.Request) {
		if err := setHangArmed(false); err != nil {
			http.Error(w, "取消失败: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "disarmed")
	})

	// 取消武装。**撤销时用**——注入失败要能还原，
	// 否则沙箱服务处于「拉起就崩」的状态，
	// 后面所有注入的判据全被污染。
	mux.HandleFunc("/disarm-crash", func(w http.ResponseWriter, r *http.Request) {
		if err := setCrashArmed(false); err != nil {
			http.Error(w, "取消失败: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "disarmed")
	})

	// ---- 慢启动的三个端点 ----
	//
	// 与 hang 那三个一一对应，**但 /arm-slow 与 /disarm-slow 本身不延迟**——
	// 否则注入器调 arm 就会被自己的请求挂住 3 秒。
	mux.HandleFunc("/arm-slow", func(w http.ResponseWriter, r *http.Request) {
		if err := setSlowArmed(true); err != nil {
			http.Error(w, "置位失败: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "armed: every request will take %s (still responds)\n",
			slowDelay)
	})

	mux.HandleFunc("/slow-armed", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "%v\n", slowArmed())
	})

	mux.HandleFunc("/disarm-slow", func(w http.ResponseWriter, r *http.Request) {
		if err := setSlowArmed(false); err != nil {
			http.Error(w, "取消失败: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "disarmed")
	})

	// ---- 「恢复后再次故障」的三个端点 ----
	//
	// 与其它开关同一模式：**/arm-delayed 与 /disarm-delayed 本身不消耗宽限额度**——
	// 否则注入器调 arm 就把自己当成了宽限期里的第一个请求。
	//
	// **/arm-delayed 会顺带清掉宽限计数**——否则上一轮注入留下的计数
	// 会让它一装上就退出，那测的是 crash，不是"恢复后再坏"。
	// （与 /arm-die-once 清 spent 标记是同一条设计规则。）
	mux.HandleFunc("/arm-delayed", func(w http.ResponseWriter, r *http.Request) {
		if err := clearDelayedCount(); err != nil {
			http.Error(w, "清除宽限计数失败: "+err.Error(), 500)
			return
		}
		if err := setDelayedArmed(true); err != nil {
			http.Error(w, "置位失败: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "armed: next %d requests succeed, then the process exits\n",
			delayedGrace)
	})

	mux.HandleFunc("/delayed-armed", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "%v\n", delayedArmed())
	})

	mux.HandleFunc("/disarm-delayed", func(w http.ResponseWriter, r *http.Request) {
		if err := setDelayedArmed(false); err != nil {
			http.Error(w, "取消失败: "+err.Error(), 500)
			return
		}
		if err := clearDelayedCount(); err != nil {
			http.Error(w, "清除宽限计数失败: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "disarmed")
	})

	// ---- 「用完即退」的三个端点 ----
	//
	// **/arm-die-once 会顺带清掉「已用掉」标记**——
	// 否则上一次注入留下的 spent 标记会让它「一装上就退」，
	// 于是复验直接失败 —— 那测的是 crash，不是 ok_then_die。
	mux.HandleFunc("/arm-die-once", func(w http.ResponseWriter, r *http.Request) {
		if err := clearDieOnceSpent(); err != nil {
			http.Error(w, "清除已用标记失败: "+err.Error(), 500)
			return
		}
		if err := setDieOnceArmed(true); err != nil {
			http.Error(w, "置位失败: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "armed: next request succeeds, the one after exits")
	})

	mux.HandleFunc("/die-once-armed", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "%v\n", dieOnceArmed())
	})

	mux.HandleFunc("/disarm-die-once", func(w http.ResponseWriter, r *http.Request) {
		if err := setDieOnceArmed(false); err != nil {
			http.Error(w, "取消失败: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "disarmed")
	})

	// ---- 「说谎服务」的三个端点 ----
	//
	// 与 hang 那三个同一模式：**/arm-liar 与 /disarm-liar 本身不返回 503**——
	// 否则注入器调 arm 就会被 503 挡住（见 maybeLiar 的注释）。
	mux.HandleFunc("/arm-liar", func(w http.ResponseWriter, r *http.Request) {
		if err := setLiarArmed(true); err != nil {
			http.Error(w, "置位失败: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "armed: / and /healthz will answer 503 (process stays alive)")
	})

	mux.HandleFunc("/liar-armed", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "%v\n", liarArmed())
	})

	mux.HandleFunc("/disarm-liar", func(w http.ResponseWriter, r *http.Request) {
		if err := setLiarArmed(false); err != nil {
			http.Error(w, "取消失败: "+err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "disarmed")
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if maybeCrash() || maybeHang() || maybeSlow() || maybeDelayed() {
			return
		}
		maybeDieOnce()
		if maybeLiar(w, r) {
			return
		}
		// 有响应体才能通过 httpProbe 的 HTTP 状态检查：
		// verifyService 判就绪的条件是「端口 LISTENING + owner 匹配 + HTTP 有响应」，
		// 返回 200 才能让复验真正通过（返回 404 也算有响应，但 200 更干净）。
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "sandbox-backend alive since %s\n",
			time.Now().Format("15:04:05"))
	})

	srv := &http.Server{Addr: addr, Handler: mux}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "[sandbox-backend] 退出: %v\n", err)
		os.Exit(1)
	}
}
