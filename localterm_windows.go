//go:build windows

package main

// localterm_windows.go —— 手机上的"电脑终端"：ConPTY ↔ WebSocket ↔ xterm.js
//
// 为什么值得单独做一个（桌面明明能直接开 cmd）：手机连着的时候，
// "看得到状态"和"能操作"之间隔着一整类工作 —— 改 config.json、重启某个进程、
// 看一眼 go build 的输出。把人从"回电脑前坐下"里解放出来，是这块功能全部的价值。
//
// 协议与链端终端（webterm.go）完全一致，前端可以直接复用同一套代码：
//   - 二进制帧 = 终端输入 / 输出
//   - 文本帧   = {"type":"resize","cols":N,"rows":M}
//
// ## 为什么它默认是关的
//
// 终端是"什么都能干"的入口：一条命令就能绕过本项目其它所有闸
// （写工具要确认、破坏性操作要 428、AI 只读……）。
// 所以它必须是用户**显式打开**的，并且每次握手都进审计。
// 这不是"多一个开关"，这是这个功能能不能存在的前提。

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/UserExistsError/conpty"
	"github.com/gorilla/websocket"
)

// lanWSUpgrader 已移到 lanws.go（跨平台）：链端 SSH 终端也要用它，
// 而这个文件带 windows 构建约束。

// lanTermShell 选 shell。?sh=cmd 切回 cmd.exe（PowerShell 在旧机器上启动慢，
// 而且某些精简系统上执行策略会拦脚本 —— 给个退路）。
func lanTermShell(q string) string {
	if strings.EqualFold(strings.TrimSpace(q), "cmd") {
		return "cmd.exe"
	}
	return "powershell.exe -NoLogo"
}

// conptyAvailable 这台机器能不能开 ConPTY。界面据此把开关置灰，
// 而不是让用户打开之后点进去看到一句 501。
func conptyAvailable() bool { return conpty.IsConPtyAvailable() }

func lanTermWS(w http.ResponseWriter, r *http.Request) {
	dev, ok := lanFeature(w, r, lanAllowTermOn(lanSnap()), "终端")
	if !ok {
		return
	}
	if !conpty.IsConPtyAvailable() {
		http.Error(w, "这台 Windows 不支持 ConPTY（需要 Windows 10 1809 及以上）", http.StatusServiceUnavailable)
		return
	}
	// Upgrade 必须放在所有检查之后：一旦握手成功，再想返回 403 就只能关连接，
	// 前端看到的是"连上了又被断开"，比直接 403 难查得多。
	ws, err := lanWSUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()

	shell := lanTermShell(r.URL.Query().Get("sh"))
	// 初始行列给个手机竖屏能用的值，前端连上后会立刻发一次 resize 覆盖它。
	p, err := conpty.Start(shell, conpty.ConPtyDimensions(80, 24))
	if err != nil {
		wsWriteError(ws, "启动终端失败："+err.Error())
		return
	}
	/* **ConPTY 只能关一次。**
	   第一版写的是"循环结束处显式 p.Close()" + "defer p.Close()"，
	   结果是双关：ClosePseudoConsole 与 CloseHandle 在已经释放的句柄上再走一遍。
	   在 Windows 上这不是"返回个错误"就完了 —— 句柄号会被系统回收复用，
	   第二次 CloseHandle 关掉的是别人的对象，进程直接
	   STATUS_HEAP_CORRUPTION(0xC0000374) 退出（实测：手机端断开终端的瞬间整进程消失，
	   日志停在"本地终端已关闭"之后，没有任何报错）。
	   所以这里用 sync.Once 兜住，谁先到谁关，两边都能等。 */
	var closeOnce sync.Once
	shutdown := func() { closeOnce.Do(func() { _ = p.Close() }) }
	defer shutdown()

	info(scSys, "手机访问", "%s 打开本地终端（%s）", dev.Name, shell)
	auditNow(actUser, "lan_term", "local", dev.ID, resOK, "手机打开本地终端："+dev.Name)

	var wsMu sync.Mutex // gorilla 不允许并发写
	writeWS := func(mt int, data []byte) bool {
		wsMu.Lock()
		defer wsMu.Unlock()
		return ws.WriteMessage(mt, data) == nil
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 8192)
		for {
			n, rerr := p.Read(buf)
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

	// WS -> ConPTY（二进制=键入；文本=控制 JSON）
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
				_ = p.Resize(ctl.Cols, ctl.Rows)
			}
			continue
		}
		if _, err := p.Write(data); err != nil {
			break
		}
	}
	shutdown() // 唯一的关闭点：在等输出协程之前，否则它会一直卡在 Read 上
	<-done     // 等输出协程收尾，避免丢最后一段输出（与 webterm.go 同一处理）
	info(scSys, "手机访问", "%s 的本地终端已关闭", dev.Name)
}
