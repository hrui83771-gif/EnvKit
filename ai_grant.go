package main

// ai_grant.go —— 批量授权（v3.2）：一次点头，放行本次任务里剩下的同类步骤。
//
// ## 要解决的问题
//
// 用户说「把环境拉起来」，模型按顺序做「启动后端 → 启动前端 → 复验」。
// 每一步都要弹一张确认卡：三次点头、三次上下文重建、三次把整段历史重发一遍。
// 而这三步**本来就是用户那一句话里的一个动作**，逐次确认换来的不是安全，是摩擦。
//
// ## 边界（为什么这样定）
//
//   - **只放行服务启停**（start/stop/restart）。数据类与系统类动作各自都有
//     "看一眼再点"的理由，不进这个白名单。
//   - **只在本请求内有效**：授权不落盘、不跨回合、请求结束即清。
//     会话里下一次任务重新问——"上次你同意过"不能变成永久通行证。
//   - **次数上限 2 次**（合计最多 3 个动作），外加 5 分钟兜底过期。
//   - **每一步仍然照常执行、照常复验、照常审计**：批量省的是"确认"，
//     不是"验证"。每次使用都落一条 authorize 审计（rule=batch_grant）。
//   - 授权来自用户在确认卡上勾选，**不是模型请求的**：模型没有能力给自己开授权。

import (
	"sync"
	"time"
)

const (
	aiGrantMaxUses = 2               // 批准一次后，最多再免确认执行 2 次
	aiGrantTTL     = 5 * time.Minute // 兜底过期：正常路径靠请求结束清理
)

type aiGrantState struct {
	family string
	left   int
	until  time.Time
}

var (
	aiGrantMu sync.Mutex
	aiGrant   *aiGrantState
)

// aiGrantFamily 可批量的工具族。返回空串 = 不支持批量（永远逐次确认）。
//
// 只收服务启停的道理：这三个动作在同一次任务里天然连着（先停、再起、再复验），
// 且每一个之后都有 `verify_environment` 客观兜底——就算模型乱来，
// 复验会把它抓住。而数据/系统类动作没有这种"事后可发现"的性质。
func aiGrantFamily(tool string) string {
	switch tool {
	case "start_service", "stop_service", "restart_service":
		return "service"
	}
	return ""
}

// aiGrantReset 清空授权。每次 aiRunLoop 开头调用——授权绝不跨请求。
func aiGrantReset() {
	aiGrantMu.Lock()
	aiGrant = nil
	aiGrantMu.Unlock()
}

// aiGrantOpen 在用户批准并勾选"后续同类不再问"之后开启授权。
// 返回剩余可用次数；工具不属于可批量族时返回 ok=false（不开启）。
func aiGrantOpen(tool string) (int, bool) {
	fam := aiGrantFamily(tool)
	if fam == "" {
		return 0, false
	}
	aiGrantMu.Lock()
	defer aiGrantMu.Unlock()
	aiGrant = &aiGrantState{family: fam, left: aiGrantMaxUses, until: time.Now().Add(aiGrantTTL)}
	return aiGrant.left, true
}

// aiGrantAllows 判断当前工具能否免确认执行（不消耗次数）。
func aiGrantAllows(tool string) bool {
	fam := aiGrantFamily(tool)
	if fam == "" {
		return false
	}
	aiGrantMu.Lock()
	defer aiGrantMu.Unlock()
	g := aiGrant
	if g == nil || g.left <= 0 || time.Now().After(g.until) {
		return false
	}
	return g.family == fam
}

// aiGrantTake 消耗一次授权并返回剩余次数。
// 调用前必须先用 aiGrantAllows 确认，否则会把"不可批量"的调用也算掉。
func aiGrantTake(tool string) int {
	aiGrantMu.Lock()
	defer aiGrantMu.Unlock()
	g := aiGrant
	if g == nil || g.left <= 0 {
		return 0
	}
	if aiGrantFamily(tool) != g.family {
		return g.left
	}
	g.left--
	if g.left <= 0 {
		aiGrant = nil
	}
	return g.left
}

// aiGrantLabel 给界面/日志用的人类可读名称。
func aiGrantLabel(tool string) string {
	switch aiGrantFamily(tool) {
	case "service":
		return "服务启停"
	}
	return ""
}
