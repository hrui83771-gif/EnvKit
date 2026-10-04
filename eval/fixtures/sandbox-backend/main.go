// EnvKit 恢复率评测的一次性被测服务（v2.5 kill_service 注入用）
//
// 为什么需要它
// ------------
// "自主恢复率"这一维要求注入「服务进程消失」这类故障，然后观察 AI 能否
// 通过**产品真实的 start_service 链路**把服务拉回来。
// 但用户机器上跑着的真实服务（BloodLine 的前后端）绝不能拿来当试验品——
// 杀掉它们等于毁掉用户正在做的事。
//
// 所以这里造一个**一次性服务**：一个只监听端口、什么都不做的 Go 程序。
// 它满足 kill_service 注入需要的全部条件：
//   - 是**真的 HTTP 服务**（不是 sleep），所以端口复验有意义
//   - 端口写死在源码里，EnvKit 的 scan_ports 能对上
//   - go build + go run 能过，走的是 opStartBackend 的真实路径
//   - 删掉整个目录就彻底清理，不留任何状态
//
// 端口选 45311：避开 8080/8888/20200/5002 等常用服务端口，
// 也避开注入器自己用的 45100-45199 段（两套东西互不干扰）。
package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

const addr = "127.0.0.1:45311"

func main() {
	// 日志写到 stdout，让 EnvKit 的「程序启动」日志能捕捉到，
	// AI 调 get_logs 时看得到真实输出（而不是空）。
	fmt.Printf("[sandbox-backend] 启动于 %s pid=%d\n",
		time.Now().Format("15:04:05"), os.Getpid())

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// 有响应体才能通过 httpProbe 的 HTTP 状态检查：
		// verifyService 判就绪的条件是「端口 LISTENING + owner 匹配 + HTTP 有响应」，
		// 返回 200 才能让复验真正通过（返回 404 也算有响应，但 200 更干净）。
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "sandbox-backend alive since %s\n",
			time.Now().Format("15:04:05"))
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	srv := &http.Server{Addr: addr, Handler: mux}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "[sandbox-backend] 退出: %v\n", err)
		os.Exit(1)
	}
}
