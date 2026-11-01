package main

// lanws.go —— 手机端 WebSocket 握手的统一校验。
//
// 单独一个文件、不加 build tag 是有原因的：终端（ConPTY）只有 Windows 有实现，
// 而**链端 SSH 终端是跨平台的**。校验逻辑若跟着 windows 文件走，
// 非 Windows 构建上就会少一个可用符号，加新 WS 路由时才发现。

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gorilla/websocket"
)

// lanWSUpgrader 手机端的 WS 握手校验。
//
// **不能复用桌面那份 wsUpgrader**：它的 CheckOrigin 只认 127.0.0.1 / localhost，
// 而手机的 Origin 是 http://192.168.x.x:18766 —— 会被自家逻辑判成跨站，握手直接失败。
//
// 这里的判据换成"同源 + 本机地址白名单"，和 lanHostAllowed 是同一套口径：
//   - Origin 的 host 必须和本请求的 Host 完全一致（真的同源，防第三方页面挂 WS）；
//   - 这个 Host 必须在本机地址白名单里（防 DNS rebinding，同 lanHostAllowed）。
//
// 没有 Origin 的客户端**一律拒**：浏览器一定带 Origin，
// 空 Origin 只可能来自脚本 —— 而脚本已经有 X-EK-Lan 那条路可以走 HTTP 接口了。
var lanWSUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		o := r.Header.Get("Origin")
		if o == "" {
			return false
		}
		u, err := url.Parse(o)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			warn(scSys, "手机访问", "已拒绝跨站终端握手：Origin=%s Host=%s", o, r.Host)
			return false
		}
		return lanHostAllowed(r)
	},
}
