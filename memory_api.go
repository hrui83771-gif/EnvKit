package main

// memory_api.go —— 记忆面板的 HTTP 接口
//
// 三个动作，与审计页的做法一致：GET 一次拿全量（前端自己筛），写操作走 POST + 确认。
// 自动经验是系统推断出来的，**不提供写入接口**——它必须是审计的纯视图，
// 一旦允许手动编辑"经验"，就有了"伪造证据骗 AI"的口子。
// 用户能做的只有"否决"（enabled=false）和"查看依据"。

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// handleLaunch GET：返回当前生效的启动方式推断（v2.1）。
// 配置页与 AI 快照共用同一份计算结果，避免两处口径不一致——
// 界面显示"serve"而 AI 实际跑了 dev，是最难查的一类问题。
func handleLaunch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "plan": currentLaunchPlan()})
}

// handleLessons GET：返回自动经验（可禁用状态）+ 用户记忆全量。
func handleLessons(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	ls := lessonsFor()
	sort.Slice(ls, func(i, j int) bool {
		if ls[i].Enabled != ls[j].Enabled {
			return ls[i].Enabled // 未禁用的排前面
		}
		return ls[i].Hits > ls[j].Hits
	})
	writeJSON(w, map[string]any{
		"ok":       true,
		"lessons":  ls, // 恒为数组，前端不必判空
		"memories": loadMemories(),
	})
}

// handleMemory POST：单条增删改。
func handleMemory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Op     string `json:"op"`
		Text   string `json:"text"`
		Tags   string `json:"tags"`
		Always *bool  `json:"always"`
		ID     string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "参数错误"})
		return
	}
	always := false
	if body.Always != nil {
		always = *body.Always
	}
	var (
		msg string
		err error
	)
	switch body.Op {
	case "add":
		var m Memory
		var isNew bool
		m, isNew, err = memUpsert(body.Text, body.Tags, always, "manual")
		if err == nil {
			auditNow(actUser, "memory_add", m.ID, body.Tags, resOK,
				firstLines(m.Text, 80)+boolStr(isNew, "（新增）", "（更新）"))
		}
		msg = "已保存记忆"
	case "delete":
		err = memDelete(body.ID)
		if err == nil {
			auditNow(actUser, "memory_delete", body.ID, "", resOK, "")
		}
		msg = "已删除记忆"
	case "toggle":
		err = memToggle(body.ID)
		if err == nil {
			auditNow(actUser, "memory_toggle", body.ID, "", resOK, "")
		}
		msg = "已切换注入方式"
	default:
		writeJSON(w, map[string]any{"ok": false, "error": "op 必须是 add / delete / toggle"})
		return
	}
	lessonInvalidate()
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "msg": msg, "memories": loadMemories()})
}

// handleMemoryImport POST：批量导入。每行一条，或 JSON 数组。
func handleMemoryImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "参数错误"})
		return
	}
	added, updated, failed := memImportBatch(body.Text)
	lessonInvalidate()
	auditNow(actUser, "memory_import", "", "", resOK,
		"新增 "+strconv.Itoa(added)+" 条 / 更新 "+strconv.Itoa(updated)+" 条 / 失败 "+strconv.Itoa(failed)+" 条")
	writeJSON(w, map[string]any{
		"ok": true, "added": added, "updated": updated, "failed": failed,
		"msg":      "导入完成：新增 " + strconv.Itoa(added) + " 条，更新 " + strconv.Itoa(updated) + " 条",
		"memories": loadMemories(),
	})
}

// handleLessonToggle POST：禁用/启用一条自动经验。
// 注意是"否决"不是"编辑"——经验文本由审计算出，不允许手工改写。
func handleLessonToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "参数错误"})
		return
	}
	id := strings.TrimSpace(body.ID)
	enabled, ok := lessonToggle(id)
	if !ok {
		writeJSON(w, map[string]any{"ok": false, "error": "经验不存在（可能审计记录已过期清理）"})
		return
	}
	auditNow(actUser, "lesson_toggle", id, "", resOK, boolStr(enabled, "启用", "否决"))
	writeJSON(w, map[string]any{"ok": true, "enabled": enabled, "lessons": lessonsFor()})
}

func boolStr(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}
