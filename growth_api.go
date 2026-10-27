package main

// growth_api.go —— Phase 3 成长指标的 HTTP 面。
//
// 只读端点，因此不走 428 needConfirm：这一层不写任何状态，
// 拦它只会让"看一眼数字"变成一次确认点。
//
// 口径与算法全在 growth.go，这里只做参数解析与输出 ——
// 报表要能被第三方复算，就不该在传输层再叠一层加工。

import (
	"net/http"
	"strconv"
)

// handleGrowth GET /api/growth?days=56
//
// days 缺省或非法 → 用默认窗口（8 周）。非法值不报错：
// 这是只读的观察端点，为一个手滑的参数返回 400 不如给一份默认报表。
func handleGrowth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	days := 0
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			days = n
		}
	}
	writeJSON(w, growthReport(days))
}
