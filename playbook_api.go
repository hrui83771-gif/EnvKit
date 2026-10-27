package main

// playbook_api.go —— 预案库 API（Phase 2）。
//
// 端点（与 mission_api.go 同风格：无路径参数，单端点 + op 字段）：
//   GET  /api/playbooks                 列表（含统计与"这一步是不是写操作"）
//   POST /api/playbooks                 {op: save_mission|update|set_state|delete, ...}
//
// 只读优先：这里**没有任何执行入口**——套预案只是给行动起草步骤，
// 真正动手永远走 AI 回合 + 确认卡（红线 2）。这条边界由本文件"没有工具调用"
// 这一点保证，而不是靠注释提醒。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// pbkStepView 步骤的展示视图：补一个 write 标记。
//
// 为什么在前端显示"这一步会改状态"：用户点「继续」前要能一眼看出
// 这个预案会不会动他的服务。让系统自己判断而不是让人读工具名猜。
type pbkStepView struct {
	Tool   string            `json:"tool"`
	Target string            `json:"target,omitempty"`
	Why    string            `json:"why,omitempty"`
	Args   map[string]string `json:"args,omitempty"`
	Write  bool              `json:"write"`
}

// pbkView 列表项视图（Playbook 的字段原样展开 + 派生字段）。
//
// 注意 StepViews 与内嵌的 Steps 是两个不同字段：Steps 是存储形态（无 write 标记），
// StepViews 是展示形态。前端只读 step_views，避免"要不要改状态"这种事让前端自己猜。
type pbkView struct {
	Playbook
	Rate      int           `json:"rate"` // 成功率百分比；used=0 时为 0（不是 100）
	StepViews []pbkStepView `json:"step_views"`
}

func pbkViewOf(p Playbook) pbkView {
	vs := make([]pbkStepView, 0, len(p.Steps))
	for _, st := range p.Steps {
		w := false
		if t, ok := aiToolRegistry[st.Tool]; ok {
			w = t.Write
		}
		vs = append(vs, pbkStepView{Tool: st.Tool, Target: st.Target, Why: st.Why, Args: st.Args, Write: w})
	}
	return pbkView{Playbook: p, Rate: pbkRate(p.Stats), StepViews: vs}
}

// handlePlaybooks 预案列表与操作。
func handlePlaybooks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		all := pbkList()
		out := make([]pbkView, 0, len(all))
		for _, p := range all {
			out = append(out, pbkViewOf(p))
		}
		writeJSON(w, map[string]any{"ok": true, "playbooks": out})
	case http.MethodPost:
		var body struct {
			Op        string           `json:"op"`
			ID        string           `json:"id"`
			MissionID string           `json:"mission_id"`
			Title     string           `json:"title"`
			State     string           `json:"state"`
			Matcher   *PlaybookMatcher `json:"matcher"`
			Steps     *[]PlaybookStep  `json:"steps"`
			LessonIDs *[]string        `json:"lesson_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": "参数错误"})
			return
		}
		var (
			p   Playbook
			err error
		)
		switch body.Op {
		case "save_mission":
			// 人工沉淀：把一次已完成行动的步骤序列打包成预案（设计 3.1）。
			// 只有用户点了按钮才走这里 —— Phase 2 不做自动沉淀（那要等成功率统计攒够）。
			p, err = pbkFromMission(body.MissionID, body.Title)
			if err == nil {
				auditNow(actUser, "playbook_save", p.ID, body.MissionID, resOK, p.Title)
				info(scSys, "预案", "已从行动 %s 沉淀预案「%s」（%d 步）", body.MissionID, p.Title, len(p.Steps))
			}
		case "update":
			// 只允许改可编辑项：标题 / 匹配键 / 步骤 / 关联经验。
			// ID 与统计不可改（统计是事实，改了就是伪造账本）。
			if body.Matcher != nil {
				if err = pbkMatcherCheck(*body.Matcher); err != nil {
					break
				}
			}
			if body.Steps != nil {
				if err = pbkCheckSteps(*body.Steps); err != nil {
					break
				}
			}
			p, err = pbkUpdate(body.ID, func(x *Playbook) error {
				if t := strings.TrimSpace(body.Title); t != "" {
					x.Title = truncateRunes(t, 120)
				}
				if body.Matcher != nil {
					x.Matcher = pbkNormMatcher(*body.Matcher)
				}
				if body.Steps != nil {
					x.Steps = append([]PlaybookStep(nil), (*body.Steps)...)
				}
				if body.LessonIDs != nil {
					x.LessonIDs = append([]string(nil), (*body.LessonIDs)...)
				}
				return nil
			})
			if err == nil {
				auditNow(actUser, "playbook_update", p.ID, "", resOK, p.Title)
			}
		case "set_state":
			st := strings.TrimSpace(body.State)
			if st != pbkActive && st != pbkReview && st != pbkRetired {
				writeJSON(w, map[string]any{"ok": false, "error": "state 必须是 active / review / retired"})
				return
			}
			p, err = pbkUpdate(body.ID, func(x *Playbook) error {
				x.State = st
				// 人工改状态即人工表态：把连败计数清零，否则刚从 review 放出来
				// 就会被上一次的 streak 立刻打回去 —— 那是"放了个假行"。
				if st == pbkActive {
					x.Stats.Streak = 0
				}
				return nil
			})
			if err == nil {
				auditNow(actUser, "playbook_state", p.ID, st, resOK, p.Title)
			}
		case "delete":
			if err = pbkDelete(body.ID); err != nil {
				break
			}
			auditNow(actUser, "playbook_delete", body.ID, "", resOK, "")
			writeJSON(w, map[string]any{"ok": true})
			return
		default:
			writeJSON(w, map[string]any{"ok": false,
				"error": "op 必须是 save_mission / update / set_state / delete"})
			return
		}
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "playbook": pbkViewOf(p)})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

// pbkDelete 删除一条预案。
func pbkDelete(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("缺少预案 ID")
	}
	pbkMu.Lock()
	defer pbkMu.Unlock()
	all := loadPlaybooks()
	out := make([]Playbook, 0, len(all))
	found := false
	for _, p := range all {
		if p.ID == id {
			found = true
			continue
		}
		out = append(out, p)
	}
	if !found {
		return fmt.Errorf("预案不存在：%s", id)
	}
	return savePlaybooks(out)
}

// pbkMatcherCheck 校验匹配键。
//
// 为什么非要拦"全空 matcher"：全空的预案能匹配任何告警，用户只会看到
// 一个莫名其妙的建议，且永远查不出是谁配的。
func pbkMatcherCheck(m PlaybookMatcher) error {
	if len(pbkCleanList(m.Services)) == 0 && len(pbkCleanList(m.ErrKinds)) == 0 &&
		len(pbkCleanList(m.Triggers)) == 0 && len(pbkCleanList(m.Keywords)) == 0 {
		return fmt.Errorf("匹配条件不能全空（全空会匹配任何告警）")
	}
	return nil
}

// pbkNormMatcher 归一化：去空白、去空项、去重（保序）。
func pbkNormMatcher(m PlaybookMatcher) PlaybookMatcher {
	return PlaybookMatcher{
		Services: pbkCleanList(m.Services),
		ErrKinds: pbkCleanList(m.ErrKinds),
		Triggers: pbkCleanList(m.Triggers),
		Keywords: pbkCleanList(m.Keywords),
	}
}

func pbkCleanList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[strings.ToLower(s)] {
			continue
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}
	return out
}
