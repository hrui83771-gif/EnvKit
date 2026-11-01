package main

// winevents_test.go —— 事件日志解析的验收点：
//   - 真实 wevtutil /f:xml 的形状（带 namespace、UTF-8 BOM）必须能解析；
//   - 中文描述不许被截断成乱码（按 rune 截，不按字节）；
//   - 与项目栈相关的关键词只做"提到过"的标注，不据此下结论（Hint 原样给出命中词）；
//   - 解析失败必须报错，不许返回空列表冒充"没有错误"。

import (
	"strings"
	"testing"
)

const sampleWinEventsXML = `<?xml version="1.0" encoding="utf-8"?>
<Events>
<Event xmlns="http://schemas.microsoft.com/win/2004/08/events/event">
 <System>
  <Provider Name="Application Error"/>
  <EventID>1000</EventID>
  <Level>2</Level>
  <Channel>Application</Channel>
  <Computer>DESKTOP-TEST</Computer>
  <TimeCreated SystemTime="2026-10-31T03:12:33.1234567Z"/>
 </System>
 <EventData>
  <Data>node.exe</Data>
  <Data>0.0.0.0:8888</Data>
  <Data>应用程序发生异常 未知的软件异常 (0xc0000409)</Data>
 </EventData>
</Event>
<Event xmlns="http://schemas.microsoft.com/win/2004/08/events/event">
 <System>
  <Provider Name="Service Control Manager"/>
  <EventID>7000</EventID>
  <Level>2</Level>
  <Channel>System</Channel>
  <TimeCreated SystemTime="2026-10-30T22:01:00.0000000Z"/>
 </System>
 <EventData>
  <Data>Print Spooler 服务启动失败</Data>
 </EventData>
</Event>
</Events>`

func TestParseWinEvents(t *testing.T) {
	evs, err := parseWinEvents([]byte(sampleWinEventsXML))
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("应解析出 2 条，得到 %d", len(evs))
	}
	e := evs[0]
	if e.ID != 1000 || e.Level != 2 {
		t.Fatalf("第一条应为 1000/Level2，得到 %d/%d", e.ID, e.Level)
	}
	if e.Log != "Application" || e.Source != "Application Error" {
		t.Fatalf("日志/来源不对：%q / %q", e.Log, e.Source)
	}
	if !strings.Contains(e.Text, "0xc0000409") {
		t.Fatalf("描述丢了关键异常码：%q", e.Text)
	}
	if !e.Related || e.Hint == "" {
		t.Fatalf("node.exe 应命中项目栈关键词，得到 related=%v hint=%q", e.Related, e.Hint)
	}
	// 时间要转成本地可读格式，而不是原样吐 ISO 串
	if !strings.HasPrefix(e.Time, "2026-10-3") {
		t.Fatalf("时间没有格式化：%q", e.Time)
	}

	// 第二条：一条与项目无关的系统服务失败，不该被判成"本项目相关"
	if evs[1].Related {
		t.Fatalf("打印服务启动失败不该凭空关联到项目栈，命中词=%q", evs[1].Hint)
	}
	if !strings.Contains(evs[1].Text, "Print Spooler") {
		t.Fatalf("中文描述被破坏了：%q", evs[1].Text)
	}
}

func TestParseWinEventsBOM(t *testing.T) {
	raw := append([]byte{0xEF, 0xBB, 0xBF}, []byte(sampleWinEventsXML)...)
	if _, err := parseWinEvents(raw); err != nil {
		t.Fatalf("带 BOM 的输出必须能解析（wevtutil 会带 BOM）：%v", err)
	}
}

// wevtutil 的真实怪癖：匹配到**只有一条**事件时，根元素是 <Event> 而不是 <Events>。
// 这条是端到端跑出来的（样例只覆盖了多条），修完必须留一条测试钉住。
const singleWinEventXML = `<?xml version="1.0" encoding="utf-8"?>
<Event xmlns="http://schemas.microsoft.com/win/2004/08/events/event">
 <System>
  <Provider Name="Application Error"/>
  <EventID>1000</EventID>
  <Level>2</Level>
  <Channel>Application</Channel>
  <TimeCreated SystemTime="2026-10-31T03:12:33.1234567Z"/>
 </System>
 <EventData>
  <Data>go.exe 异常终止</Data>
 </EventData>
</Event>`

func TestParseWinEventsSingleEventRoot(t *testing.T) {
	evs, err := parseWinEvents([]byte(singleWinEventXML))
	if err != nil {
		t.Fatalf("单条事件（根元素 <Event>）必须能解析：%v", err)
	}
	if len(evs) != 1 || evs[0].ID != 1000 {
		t.Fatalf("应得到 1 条 ID=1000 的事件，实际 %+v", evs)
	}
	if !evs[0].Related {
		t.Fatal("go.exe 应命中项目栈关键词")
	}
}

func TestParseWinEventsEmptyIsError(t *testing.T) {
	// 关键：读不到 ≠ 没有错误。空输入必须报错，让界面显示"读不到"。
	if _, err := parseWinEvents(nil); err == nil {
		t.Fatal("空输入应返回错误，而不是空列表")
	}
	if _, err := parseWinEvents([]byte("   \r\n")); err == nil {
		t.Fatal("只有空白的输入应返回错误")
	}
	if _, err := parseWinEvents([]byte("<Events><Event")); err == nil {
		t.Fatal("半截 XML 应返回错误")
	}
}

func TestWinEventTextClippedByRune(t *testing.T) {
	long := strings.Repeat("错", winEventsTextMax+50)
	got := clipRunes(long, winEventsTextMax)
	if !strings.HasSuffix(got, "…") {
		t.Fatal("超长文本应以省略号结尾")
	}
	if n := len([]rune(got)); n != winEventsTextMax+1 {
		t.Fatalf("应按 rune 截到 %d（含省略号），得到 %d", winEventsTextMax+1, n)
	}
	if strings.Contains(got, "\ufffd") {
		t.Fatal("按 rune 截断不该产生替换字符（说明切断了 UTF-8 序列）")
	}
}

func TestWinEventTimeFallback(t *testing.T) {
	if got := winEventTime(""); got != "" {
		t.Fatalf("空时间应返回空串，得到 %q", got)
	}
	// 认不出来的格式原样返回——不编一个时间出来
	if got := winEventTime("not-a-time"); got != "not-a-time" {
		t.Fatalf("无法解析时应原样返回，得到 %q", got)
	}
}

func TestSortWinEventsByTimeDesc(t *testing.T) {
	evs := []winEvent{
		{Time: "2026-10-30 10:00:00", ID: 1},
		{Time: "2026-10-31 09:00:00", ID: 2},
		{Time: "2026-10-31 11:00:00", ID: 3},
	}
	sortWinEventsByTimeDesc(evs)
	if evs[0].ID != 3 || evs[2].ID != 1 {
		t.Fatalf("未按时间倒序：%v", evs)
	}
}
