// winevents.go —— Windows 事件日志读取（wevtutil）
//
// 为什么需要它：EnvKit 现在报"进程已退出（exit status 1）"，但**为什么退出**只有两种证据：
// 子进程自己打印的日志，或者什么都没有。Windows 上"程序闪退"的真实原因通常在
// 应用程序日志里（Event ID 1000 / 1026 / .NET Runtime），那条记录带着出错模块名和
// 异常码——而现在用户得自己去"事件查看器"里翻。
//
// 做法上刻意保守：
//   - 只用固定查询模板，任何用户输入都不拼进 XPath（wevtutil 不走 shell，但慢查询同样能拖死界面）；
//   - 用 /f:xml 而不是 /f:text：text 走系统代码页，中文环境下乱码且字段边界靠猜；
//   - 查询失败就如实说读不到（权限/服务没开），不返回空列表冒充"没有错误"。
package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	winEventsMax      = 40
	winEventsCacheTTL = 30 * time.Second
	winEventsTextMax  = 600 // 单条描述截断长度（栈可能几千字）
)

// winEvent 一条事件（对外 JSON）。
type winEvent struct {
	Time    string `json:"time"`  // 本地时间 YYYY-MM-DD HH:MM:SS
	Log     string `json:"log"`   // Application / System
	Level   int    `json:"level"` // 1 严重 / 2 错误
	ID      int    `json:"id"`
	Source  string `json:"source"`
	Text    string `json:"text"`
	Related bool   `json:"related,omitempty"` // 描述里出现本项目栈的关键词
	Hint    string `json:"hint,omitempty"`    // 命中的关键词（给人看的证据）
}

type winEventsResult struct {
	OK      bool       `json:"ok"`
	Err     string     `json:"err,omitempty"`
	Fetched int64      `json:"fetched"` // 拉取时间（unix）
	Events  []winEvent `json:"events"`
}

// ---------- XML 解析 ----------

type wevtEvents struct {
	XMLName xml.Name   `xml:"Events"`
	Events  []wevtItem `xml:"Event"`
}

type wevtItem struct {
	System struct {
		Provider struct {
			Name string `xml:"Name,attr"`
		} `xml:"Provider"`
		EventID     int    `xml:"EventID"`
		Level       int    `xml:"Level"`
		Computer    string `xml:"Computer"`
		Channel     string `xml:"Channel"`
		TimeCreated struct {
			SystemTime string `xml:"SystemTime,attr"`
		} `xml:"TimeCreated"`
	} `xml:"System"`
	EventData struct {
		Data []string `xml:"Data"`
	} `xml:"EventData"`
}

// winEventKeywords 与 EnvKit 管的这套栈相关的关键词。
// 命中只说明"这条错误提到了它"，不等于"这条错误是它造成的"——Hint 里原样给出命中词，
// 让人自己判断。
var winEventKeywords = []string{
	"node.exe", "nodejs", "npm", "go.exe", "go build", "golang",
	"mysqld", "mysql", "java", "fisco", "webase", "envkit",
	"3306", "8888", "20200", "5002",
}

// parseWinEvents 把 wevtutil /f:xml 的输出解析成条目。纯函数，便于用固定样例验证。
func parseWinEvents(raw []byte) ([]winEvent, error) {
	b := bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return nil, fmt.Errorf("事件日志为空（查询可能被拒绝）")
	}
	var doc wevtEvents
	if err := xml.Unmarshal(b, &doc); err != nil {
		// wevtutil 的一个真实怪癖：**只有一条事件**时根元素是 <Event> 而不是 <Events>。
		// 端到端跑出来的（/c:40 但机器上只有一条匹配），单测样例只覆盖了多条的情况。
		var one wevtItem
		if err2 := xml.Unmarshal(b, &one); err2 != nil {
			return nil, fmt.Errorf("事件日志 XML 解析失败：%v", err)
		}
		if one.System.EventID == 0 && one.System.Provider.Name == "" {
			return nil, fmt.Errorf("事件日志 XML 解析失败：%v", err)
		}
		doc.Events = []wevtItem{one}
	}
	out := make([]winEvent, 0, len(doc.Events))
	for _, it := range doc.Events {
		text := strings.TrimSpace(strings.Join(it.EventData.Data, " ｜ "))
		hint := matchWinEventHint(text)
		logName := it.System.Channel
		if logName == "" {
			logName = "Application"
		}
		out = append(out, winEvent{
			Time:    winEventTime(it.System.TimeCreated.SystemTime),
			Log:     logName,
			Level:   it.System.Level,
			ID:      it.System.EventID,
			Source:  it.System.Provider.Name,
			Text:    clipRunes(text, winEventsTextMax),
			Related: hint != "",
			Hint:    hint,
		})
	}
	return out, nil
}

// matchWinEventHint 返回第一个命中的关键词（小写比较，保留原词用于展示）。
func matchWinEventHint(text string) string {
	low := strings.ToLower(text)
	for _, k := range winEventKeywords {
		if strings.Contains(low, k) {
			return k
		}
	}
	return ""
}

// winEventTime 把 2026-10-31T11:02:33.1234567Z 转成本地时间字符串。
// 解析失败就原样返回——不编一个时间出来。
func winEventTime(s string) string {
	if s == "" {
		return ""
	}
	for _, f := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999Z07:00", time.RFC3339} {
		if t, err := time.Parse(f, s); err == nil {
			return t.Local().Format("2006-01-02 15:04:05")
		}
	}
	return s
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ---------- 拉取 ----------

var (
	winEventsMu   sync.Mutex
	winEventsLast winEventsResult
	winEventsAt   time.Time
)

// wevtQuery 固定查询模板：只要严重(1)/错误(2)两级。
// Level 过滤放在 XPath 里做，系统和 IO 都少干活。
const wevtQuery = `*[System[(Level=1 or Level=2)]]`

// fetchWinEvents 拉取并解析；失败时 err 里带 wevtutil 的原始输出片段。
func fetchWinEvents(log string, max int) ([]winEvent, error) {
	if max <= 0 || max > winEventsMax {
		max = winEventsMax
	}
	ctx, cancel := cmdContext(8 * time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "wevtutil", "qe", log,
		"/q:"+wevtQuery, "/f:xml", fmt.Sprintf("/c:%d", max), "/rd:true").CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s 日志读取失败：%s", log, clipRunes(msg, 200))
	}
	return parseWinEvents(out)
}

// loadWinEvents 带 30 秒缓存地拉 Application + System。
//
// 为什么要缓存：wevtutil 冷启动要 0.5~2 秒，而这个面板会被反复打开。
// 缓存只在**成功**时写——失败不缓存，用户点"重试"就该真的重试。
func loadWinEvents(force bool) winEventsResult {
	winEventsMu.Lock()
	defer winEventsMu.Unlock()
	if !force && !winEventsAt.IsZero() && time.Since(winEventsAt) < winEventsCacheTTL {
		return winEventsLast
	}
	var evs []winEvent
	var errs []string
	for _, log := range []string{"Application", "System"} {
		got, err := fetchWinEvents(log, winEventsMax/2)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		evs = append(evs, got...)
	}
	res := winEventsResult{Fetched: time.Now().Unix(), Events: evs}
	if len(errs) > 0 {
		res.Err = strings.Join(errs, "；")
		if len(evs) == 0 {
			winEventsAt = time.Time{} // 一条都没读到：不缓存，允许重试
			winEventsLast = res
			return res
		}
	}
	res.OK = true
	// 按时间倒序（两段拼接后顺序会乱）。解析不出时间的排最后。
	sortWinEventsByTimeDesc(res.Events)
	winEventsLast = res
	winEventsAt = time.Now()
	return res
}

func sortWinEventsByTimeDesc(evs []winEvent) {
	// 时间已格式化成 "2006-01-02 15:04:05"，字典序即时间序
	for i := 1; i < len(evs); i++ {
		for j := i; j > 0 && evs[j].Time > evs[j-1].Time; j-- {
			evs[j], evs[j-1] = evs[j-1], evs[j]
		}
	}
}

func handleWinEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	force := r.URL.Query().Get("force") == "1"
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(mustJSON(loadWinEvents(force))))
}
