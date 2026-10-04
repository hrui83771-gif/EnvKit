package main

// ai_providers_test.go —— 服务商预设的时效性守卫
//
// ## 为什么需要这个
//
// 厂商会改名、下架模型，而**硬编码的预设列表不会自己更新**。
// 用户从下拉里点一个失效的名字，上游直接返回 400，而软件无从提示。
//
// 真实踩过：预设写着 `deepseek-chat` / `deepseek-reasoner`，
// 而这两个别名 DeepSeek 已于 2026-07-24 停用
// （过渡期内二者都指向 v4-flash，分别是其非思考 / 思考模式）。
//
// ## 这个测试锁住什么
//
// **不是**锁死「当前该有哪些模型」——厂商下次改名它又会失效，
// 那时该由人来更新，而不是让测试偷偷改期望值。
//
// 它锁的是三条**结构性**规则，任何厂商都适用：
//  1. 预设里不得出现**已知停用**的模型名（停用清单要显式写出并定期核���）
//  2. 预设里的每个模型名都要能通过 JSON 往返（格式合法）
//  3. 服务商必须带 Note——思考型厂商的「思考模式是请求参数不是模型」
//     这类概念不说明，用户会以为下拉里少了模型
//
// 另外提供一个**离线核实工具**给人工用：
// 配好 key 后跑它可拉服务商真实列表并与预设对比，
// 确认预设没落后于厂商。见 TestProviderPresetMatchesUpstream（需网络，默认跳过）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// retiredModels 已确认停用的模型名 → 停用日期。
//
// **这份清单必须定期核实**（厂商改一次就更新一次），
// 它是「预设是否过期」的唯一判据。
var retiredModels = map[string]string{
	"deepseek-chat":     "2026-07-24 停用，兼容期内指向 v4-flash 的非思考模式",
	"deepseek-reasoner": "2026-07-24 停用，兼容期内指向 v4-flash 的思考模式",
}

// TestProviderPresetsNoRetiredModel 预设里不得出现已停用的模型名。
//
// **这是本文件的核心断言。** 上一版就是它没被锁住，
// 于是两个 2026-07 停用的名字在预设里躺了很久没人发现。
func TestProviderPresetsNoRetiredModel(t *testing.T) {
	for _, p := range aiProviderPresets {
		for _, m := range p.Models {
			if why, bad := retiredModels[m]; bad {
				t.Errorf("服务商 %s 的预设里含已停用的模型 %q —— "+
					"用户从下拉里选中它会直接吃 400。%s\n"+
					"处置：从预设移除（真实可用名请查厂商文档或用「拉取模型列表」取真值）",
					p.Name, m, why)
			}
		}
	}
}

// TestProviderPresetsHaveNote 思考型厂商必须带说明。
//
// 不说明的后果很具体：DeepSeek 的 flash 一个模型覆盖思考与非思考两种模式，
// 下拉里只有一个名字，用户会以为「不支持思考」而去找根本不存在的原因。
func TestProviderPresetsHaveNote(t *testing.T) {
	for _, p := range aiProviderPresets {
		if len(p.Models) == 0 {
			continue // custom 没有预设模型，不需要说明
		}
		if strings.TrimSpace(p.Note) == "" {
			t.Errorf("服务商 %s 有预设模型但没有 Note", p.Name)
		}
	}
}

// TestProviderPresetsJSONRoundTrip 预设必须能安全序列化给前端。
func TestProviderPresetsJSONRoundTrip(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/ai/providers", nil)
	rec := httptest.NewRecorder()
	handleAIProviders(rec, req)
	if rec.Code != 200 {
		t.Fatalf("HTTP %d", rec.Code)
	}
	var out struct {
		Providers []struct {
			ID      string   `json:"id"`
			Note    string   `json:"note"`
			Models  []string `json:"models"`
			BaseURL string   `json:"base_url"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if len(out.Providers) == 0 {
		t.Fatal("未返回任何服务商")
	}
	// custom 是兜底项，必须存在且没有 base_url（不能预设陌生域名）
	var sawCustom bool
	for _, p := range out.Providers {
		if p.ID == "custom" {
			sawCustom = true
			if p.BaseURL != "" {
				t.Errorf("custom 不该预设 BaseURL（那是用户填的），实际 %q", p.BaseURL)
			}
		}
	}
	if !sawCustom {
		t.Error("缺少 custom 兜底项，任意服务商都必须能用自定义手填")
	}
}

// TestProviderNoteIsTranslatable 服务商说明必须是 i18n 字典里的键。
//
// **服务端直出中文会让英文界面露中文** —— 与其他文案一样，
// 说明文字也得走 t() 翻译。所以它必须是 I18N 表里存在的原文。
func TestProviderNoteIsTranslatable(t *testing.T) {
	html, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Skipf("读不到前端文件: %v", err)
	}
	s := string(html)
	for _, p := range aiProviderPresets {
		if p.Note == "" {
			continue
		}
		if !strings.Contains(s, p.Note) {
			t.Errorf("服务商 %s 的 Note 不在 web/index.html 的 I18N_EN 字典里，"+
				"英文界面会显示中文。\n需补进 tools/i18n_en.json 后跑 i18n_build.py。\nNote=%q",
				p.Name, p.Note)
		}
	}
}

// TestProviderPresetMatchesUpstream 预设是否落后于厂商（需网络，默认跳过）。
//
// **这个测试不参与常规回归**，它需要真实 API Key 且会发网络请求，
// 所以默认 skip。它的用途是「改预设时手动跑一次核实」。
//
// 跑法：
//
//	ENVKIT_UPSTREAM_CHECK=1 go test -run TestProviderPresetMatchesUpstream -v .
//
// 它拉服务商真实返回的模型列表，与预设对比并打印差异 ——
// **只报告不失败**，因为服务商随时可能新增模型，那不是代码缺陷。
func TestProviderPresetMatchesUpstream(t *testing.T) {
	if os.Getenv("ENVKIT_UPSTREAM_CHECK") != "1" {
		t.Skip("需 ENVKIT_UPSTREAM_CHECK=1 且配置了 API Key（会发网络请求）")
	}
	ac := aiSnap()
	if ac.BaseURL == "" || ac.keyPlain() == "" {
		t.Skip("未配置 BaseURL 或 API Key")
	}
	probe := AIConfig{BaseURL: ac.BaseURL}
	req, err := http.NewRequest(http.MethodGet, probe.modelsEndpoint(), nil)
	if err != nil {
		t.Skipf("无法构造请求: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+ac.keyPlain())
	resp, err := httpShort.Do(req)
	if err != nil {
		t.Skipf("拉取失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Skipf("HTTP %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Skipf("解析失败: %v", err)
	}
	live := map[string]bool{}
	for _, m := range out.Data {
		live[m.ID] = true
	}
	for _, p := range aiProviderPresets {
		var inPreset []string
		for _, m := range p.Models {
			if !live[m] {
				inPreset = append(inPreset, m)
			}
		}
		var missing []string
		for id := range live {
			found := false
			for _, m := range p.Models {
				if m == id {
					found = true
					break
				}
			}
			if !found {
				missing = append(missing, id)
			}
		}
		t.Logf("服务商 %s：上游返回 %d 个模型", p.Name, len(live))
		if len(inPreset) > 0 {
			t.Logf("  ⚠ 预设里上游没有的：%v", inPreset)
		}
		if len(missing) > 0 {
			t.Logf("  ·上游有而预设没有的（可能该补）：%v", missing)
		}
		if len(inPreset) == 0 && len(missing) == 0 {
			t.Logf("  ✓ 预设与上游完全一致")
		}
	}
}
