// 托盘恢复决策的确定性单元测试。
//
// 为什么放在这个包里：third_party/systray 是被 go.mod replace 指向的**独立模块**，
// 宿主模块的 `go test ./...` 不会跑到它，所以恢复逻辑必须自带测试，
// 并在门禁里单独执行 `cd third_party/systray && go test ./...`。
//
// 断言落在真正出问题的那一层：不测 Shell_NotifyIcon 本身（Win32 状态随机器变化，
// 见 .gotmp/traydiag 探针两次结果不同），而是测「某个图标 ID 被拒之后，
// addIcon 到底有没有换 ID / 有没有用 MODIFY 接管 / 失败时有没有给出客观事实」。
package systray

import (
	"errors"
	"strings"
	"testing"
)

// stubScope 把包级钩子换成桩，并在用例结束时字节级复位（别污染其他用例）。
func stubScope(t *testing.T, probe func() (uintptr, uintptr), add, modify func(*notifyIconData) error) {
	t.Helper()
	oldProbe, oldAdd, oldModify := iconReadyProbe, iconAddOp, iconModifyOp
	oldDelay, oldAttempts := iconRetryDelay, iconMaxAttempts
	oldNote, oldCb := iconLastNote, onInitRecovered
	t.Cleanup(func() {
		iconReadyProbe, iconAddOp, iconModifyOp = oldProbe, oldAdd, oldModify
		iconRetryDelay, iconMaxAttempts = oldDelay, oldAttempts
		iconLastNote, onInitRecovered = oldNote, oldCb
	})
	iconReadyProbe, iconAddOp, iconModifyOp = probe, add, modify
	iconRetryDelay, iconMaxAttempts = 0, 4
	iconLastNote = ""
	onInitRecovered = nil
}

func newTray() *winTray {
	return &winTray{nid: &notifyIconData{ID: 100}}
}

var errRejected = errors.New("res=0 LastError=2147500037 Unspecified error")

func readyAlways() (uintptr, uintptr) { return 1, 1 }

// ① 核心回归：ID=100 被占时，必须换到 101 成功，而不是对 100 重试 6 次。
func TestAddIconSwitchesIDWhenOccupied(t *testing.T) {
	var tried []uint32
	stubScope(t, readyAlways,
		func(n *notifyIconData) error {
			tried = append(tried, n.ID)
			if n.ID == 100 {
				return errRejected // 残留条目占着 100
			}
			return nil
		},
		func(*notifyIconData) error { return errRejected },
	)
	tr := newTray()
	if err := tr.addIcon(); err != nil {
		t.Fatalf("换 ID 后应能加上图标，实际失败：%v", err)
	}
	if tr.nid.ID != 101 {
		t.Errorf("应停在成功用的 ID=101，实际 ID=%d", tr.nid.ID)
	}
	if len(tried) < 2 || tried[0] != 100 || tried[1] != 101 {
		t.Errorf("尝试序列必须真的换过键，实际 %v", tried)
	}
	if !strings.Contains(iconLastNote, "换到 ID=101") {
		t.Errorf("恢复说明要写清换键，实际 %q", iconLastNote)
	}
}

// ② 条目已存在、MODIFY 能接管时不该再换 ID（避免把别人的条目/icon 位置丢掉）。
func TestAddIconTakesOverWithModify(t *testing.T) {
	stubScope(t, readyAlways,
		func(*notifyIconData) error { return errRejected },
		func(*notifyIconData) error { return nil },
	)
	tr := newTray()
	if err := tr.addIcon(); err != nil {
		t.Fatalf("MODIFY 接管应算成功，实际失败：%v", err)
	}
	if tr.nid.ID != 100 {
		t.Errorf("接管不该换 ID，实际 %d", tr.nid.ID)
	}
	if !strings.Contains(iconLastNote, "NIM_MODIFY") {
		t.Errorf("恢复说明要写明接管方式，实际 %q", iconLastNote)
	}
}

// ③ 全被拒：必须报失败，且诊断里是客观事实，不是「策略限制/权限不同」这类推测；
//    同时断言每轮都换过 ID（防退回旧实现对同一个 ID 死重试）。
func TestAddIconReportsFactsWhenAllRejected(t *testing.T) {
	var tried []uint32
	stubScope(t, readyAlways,
		func(n *notifyIconData) error {
			tried = append(tried, n.ID)
			return errRejected
		},
		func(*notifyIconData) error { return errRejected },
	)
	tr := newTray()
	err := tr.addIcon()
	if err == nil {
		t.Fatal("全被拒却报成功")
	}
	msg := err.Error()
	for _, want := range []string{"任务栏窗口存在", "通知区域窗口存在", "逐轮事实", "已尝试图标 ID 100.."} {
		if !strings.Contains(msg, want) {
			t.Errorf("诊断缺客观事实 %q：%s", want, msg)
		}
	}
	if strings.Contains(msg, "策略限制") || strings.Contains(msg, "权限级别不同") {
		t.Errorf("别把未证实的推测写进诊断：%s", msg)
	}
	for i := 1; i < len(tried); i++ {
		if tried[i] <= tried[i-1] {
			t.Errorf("重试必须换键而不是原地重来：%v", tried)
			break
		}
	}
}

// ④ 任务栏没出现：一次 Shell_NotifyIcon 都不该调，也不该假装是「内容缺失」。
func TestAddIconSkipsNotifyWhenTrayAbsent(t *testing.T) {
	calls := 0
	stubScope(t, func() (uintptr, uintptr) { return 0, 0 },
		func(*notifyIconData) error { calls++; return nil },
		func(*notifyIconData) error { calls++; return nil },
	)
	err := newTray().addIcon()
	if err == nil {
		t.Fatal("任务栏不在却报成功")
	}
	if calls != 0 {
		t.Errorf("任务栏没出现时不应调用 Shell_NotifyIcon，实际 %d 次", calls)
	}
	if !strings.Contains(err.Error(), "任务栏窗口未出现") {
		t.Errorf("诊断要点明任务栏状态：%v", err)
	}
}

// ⑤ 首次直接成功不该刷「已恢复」日志（宿主已有 Ready 日志）。
func TestAddIconFirstTryDoesNotReportRecovery(t *testing.T) {
	notified := 0
	stubScope(t, readyAlways,
		func(*notifyIconData) error { return nil },
		func(*notifyIconData) error { return errRejected },
	)
	onInitRecovered = func(string) { notified++ }
	if err := newTray().addIcon(); err != nil {
		t.Fatalf("首次成功应直接返回：%v", err)
	}
	if notified != 0 {
		t.Errorf("首次成功不该触发恢复回调，实际 %d 次", notified)
	}
}
