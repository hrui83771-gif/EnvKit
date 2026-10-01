package main

// 内嵌 Web 终端：把页面里的 xterm.js 通过 WebSocket 桥接到远程 SSH 会话（PTY）。
// 协议：
//   - 二进制帧 = 终端输入（浏览器→SSH） / 终端输出（SSH→浏览器）
//   - 文本帧   = 控制消息 JSON，如 {"type":"resize","cols":120,"rows":30}

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// 不放行任意来源：本机任何网页都能发起 WS 握手，光靠"只监听 127.0.0.1"拦不住
	// 浏览器发起的跨站连接。这里要求 Origin 缺失（非浏览器客户端）或指向本机控制台。
	// authMW 已经校验过令牌与 Origin，此为纵深防御的第二层。
	CheckOrigin: func(r *http.Request) bool {
		o := r.Header.Get("Origin")
		if o == "" {
			return true // curl / 非浏览器客户端没有 Origin
		}
		if !sameOrigin(o) {
			warn(scChain, "安全", "已拒绝跨站 WebSocket 握手：Origin=%s", o)
			return false
		}
		return true
	},
}

func wsWriteError(ws *websocket.Conn, msg string) {
	_ = ws.WriteMessage(websocket.BinaryMessage,
		[]byte("\r\n\x1b[31m"+msg+"\x1b[0m\r\n"))
}

func handleChainTermWS(w http.ResponseWriter, r *http.Request) {
	c := chainCfg()
	if c.SSHHost == "" {
		http.Error(w, "未配置 SSH 主机", 400)
		return
	}
	ws, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()

	client, err := sshDial()
	if err != nil {
		wsWriteError(ws, "SSH 连接失败："+err.Error())
		return
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		wsWriteError(ws, "创建 SSH 会话失败："+err.Error())
		return
	}
	defer session.Close()

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 115200,
		ssh.TTY_OP_OSPEED: 115200,
	}
	if err := session.RequestPty("xterm-256color", 30, 100, modes); err != nil {
		wsWriteError(ws, "申请 PTY 失败："+err.Error())
		return
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		wsWriteError(ws, "获取输入管道失败："+err.Error())
		return
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		wsWriteError(ws, "获取输出管道失败："+err.Error())
		return
	}
	stderr, _ := session.StderrPipe()
	if err := session.Shell(); err != nil {
		wsWriteError(ws, "启动远程 shell 失败："+err.Error())
		return
	}
	defer stdin.Close()

	var wsMu sync.Mutex // gorilla 不允许并发写
	writeWS := func(mt int, data []byte) bool {
		wsMu.Lock()
		defer wsMu.Unlock()
		return ws.WriteMessage(mt, data) == nil
	}

	done := make(chan struct{})
	// SSH stdout -> WS
	go func() {
		defer close(done)
		buf := make([]byte, 8192)
		for {
			n, rerr := stdout.Read(buf)
			if n > 0 {
				if !writeWS(websocket.BinaryMessage, buf[:n]) {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()
	// SSH stderr -> WS
	go func() {
		if stderr == nil {
			return
		}
		buf := make([]byte, 4096)
		for {
			n, rerr := stderr.Read(buf)
			if n > 0 {
				if !writeWS(websocket.BinaryMessage, buf[:n]) {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	// WS -> SSH（二进制=键入；文本=控制 JSON）
	for {
		mt, data, err := ws.ReadMessage()
		if err != nil {
			break
		}
		if mt == websocket.TextMessage {
			var ctl struct {
				Type string `json:"type"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
			}
			if json.Unmarshal(data, &ctl) == nil && ctl.Type == "resize" && ctl.Cols > 0 && ctl.Rows > 0 {
				_ = session.WindowChange(ctl.Rows, ctl.Cols)
			}
			continue
		}
		if _, err := stdin.Write(data); err != nil {
			break
		}
	}
	_ = session.Close()
	_ = client.Close()
	<-done // 等输出协程收尾，避免丢最后一段输出
}
