package main

// ai_wait_test.go —— 确认卡"就地等待"的边界（v3.2 T1.x）
//
// 这条链路是本次改动里唯一有并发的地方，所以用测试把它钉住：
//   · 没有等待者时投递必须失败（前端据此退回旧路径，而不是把决定悄悄吞掉）；
//   · 有等待者时投递必须把决定原样送达，且只送达一次；
//   · ctx 取消（用户关页面/停止生成）必须让等待方立即返回 false，不能挂住 goroutine；
//   · 等待期间写入的心跳必须是 SSE 注释行（前端解析器只认 "data: "，注释行会被忽略）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWaitDeliverWithoutWaiter(t *testing.T) {
	// 清场：别的用例可能留下等待者
	aiWaiterMu.Lock()
	aiWaiter = nil
	aiWaiterMu.Unlock()
	if aiWaitDeliver("nope", aiConfirmDecision{}) {
		t.Fatal("没有等待者时投递必须失败（否则决定会被静默吞掉，前端无法退回旧路径）")
	}
}

func TestWaitDeliverOnce(t *testing.T) {
	wt := aiWaitInstall("call-1")
	defer aiWaitRemove(wt)
	d := aiConfirmDecision{Proceed: true, Result: "ok", Rule: "action:db_backup"}
	if !aiWaitDeliver("call-1", d) {
		t.Fatal("有等待者时投递应当成功")
	}
	if aiWaitDeliver("call-1", d) {
		t.Fatal("同一个 callID 不得投递两次（一次确认只能执行一次）")
	}
	select {
	case got := <-wt.ch:
		if !got.Proceed || got.Rule != d.Rule {
			t.Fatalf("送达的裁决被改变：%+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("决定没有到达等待方")
	}
}

func TestAwaitConfirmDelivers(t *testing.T) {
	rec := httptest.NewRecorder()
	go func() {
		// 等安装完成再投递：aiAwaitConfirm 内部才 install
		for i := 0; i < 100; i++ {
			aiWaiterMu.Lock()
			live := aiWaiter != nil && aiWaiter.callID == "call-2"
			aiWaiterMu.Unlock()
			if live {
				aiWaitDeliver("call-2", aiConfirmDecision{Proceed: true, Rule: "action:x"})
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	d, ok := aiAwaitConfirm(context.Background(), rec, rec, "call-2")
	if !ok || !d.Proceed {
		t.Fatalf("应当等到并返回放行：ok=%v d=%+v", ok, d)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"waiting_confirm"`) {
		t.Fatalf("等待开始时应当先发一条 waiting_confirm，body=%q", body)
	}
	if !strings.Contains(body, ": keepalive") {
		// 心跳不一定来得及发（等待很短），这里只要求"若发了必须是注释行"
		if strings.Contains(body, "keepalive") {
			t.Fatalf("心跳必须是 SSE 注释行（以 ': ' 开头），body=%q", body)
		}
	}
}

func TestAwaitConfirmCancel(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	start := time.Now()
	_, ok := aiAwaitConfirm(ctx, rec, rec, "call-3")
	if ok {
		t.Fatal("ctx 取消后不得报告'等到了'")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("ctx 取消后必须立即返回，不能挂着等满超时")
	}
	// 取消后等待者必须被摘掉，否则下一次确认会撞上旧登记
	aiWaiterMu.Lock()
	left := aiWaiter
	aiWaiterMu.Unlock()
	if left != nil {
		t.Fatal("返回后必须移除等待者")
	}
}

func TestConfirmEndpointNoWaiter(t *testing.T) {
	aiWaiterMu.Lock()
	aiWaiter = nil
	aiWaiterMu.Unlock()
	body := strings.NewReader(`{"tool_call_id":"x","tool":"db_backup","approved":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/ai/confirm", body)
	rec := httptest.NewRecorder()
	handleAIConfirm(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("没有等待者时必须回 404（前端据此退回旧路径），实际 %d：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no_waiter") {
		t.Fatalf("响应要说明原因，实际 %s", rec.Body.String())
	}
}

func TestConfirmEndpointRejectsGet(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/ai/confirm", nil)
	rec := httptest.NewRecorder()
	handleAIConfirm(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("只接受 POST，实际 %d", rec.Code)
	}
}
