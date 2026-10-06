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
	"sync/atomic"
	"time"
)

const addr = "127.0.0.1:45311"

// crashOnNext atomic.Bool：置位后，下一个 /healthz 或 / 请求触发 os.Exit。
//
// 用原子量而不是裸 bool：HTTP handler 是并发执行的，
// 裸 bool 在 -race 下会报 DATA RACE。
var crashOnNext atomic.Bool

func main() {
	// 日志写到 stdout，让 EnvKit 的「程序启动」日志能捕捉到，
	// AI 调 get_logs 时看得到真实输出（而不是空）。
	fmt.Printf("[sandbox-backend] 启动于 %s pid=%d\n",
		time.Now().Format("15:04:05"), os.Getpid())

	mux := http.NewServeMux()

	// guard 放在最外层：任何路径（包括 /crash 自身）先看是否要崩。
	//
	// 顺序很重要——如果 /crash 排在 guard 后面，它自己会先把进程干掉，
	// 永远走不到「置位」那一步，注入器就拿不到确认。
	maybeCrash := func() bool {
		if crashOnNext.Load() {
			fmt.Fprintf(os.Stderr,
				"[sandbox-backend] 触发崩溃开关，退出（模拟启动即崩）\n")
			// 立刻退出，不做任何清理——真实崩溃就是这样。
			// 用 os.Exit 而不是 return：Go 的 http.Server 优雅关闭路径
			// 会等待在途请求，那会掩盖「立刻崩」这个语义。
			os.Exit(7)
		}
		return false
	}

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if maybeCrash() {
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	// 置位崩溃开关。注入器调它，然后 EnvKit 的下一次探活会触发崩溃。
	mux.HandleFunc("/arm-crash", func(w http.ResponseWriter, r *http.Request) {
		crashOnNext.Store(true)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "armed: next request will crash the process")
	})

	// 查询是否已武装（供注入器确认置位成功）
	mux.HandleFunc("/crash-armed", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "%v\n", crashOnNext.Load())
	})

	// 取消武装。**撤销时用**——注入失败要能还原，
	// 否则沙箱服务处于「下一次探活就崩」的状态，
	// 后面所有注入的判据全被污染。
	mux.HandleFunc("/disarm-crash", func(w http.ResponseWriter, r *http.Request) {
		crashOnNext.Store(false)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "disarmed")
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if maybeCrash() {
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