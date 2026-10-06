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

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if maybeCrash() || maybeHang() {
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

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if maybeCrash() || maybeHang() {
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
