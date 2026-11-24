package main

// contract_api.go —— v4.0 Phase 1 契约只读接口
//
// 三个入口都不改变运行行为：
//   GET  /api/contract          读当前契约（不存在时只报 exists=false）
//   POST /api/contract/generate 生成候选；save=true 时才写项目根
//   POST /api/contract/validate 校验一段 YAML 或指定文件

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func handleContractGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	c := cfgSnapshot()
	root, path, err := contractPathOf(c)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	resp := map[string]any{
		"ok":     true,
		"root":   root,
		"path":   path,
		"exists": false,
		"mode":   contractModeOf(c),
	}
	if _, err := os.Stat(path); err != nil {
		writeJSON(w, resp)
		return
	}
	contract, problems, err := loadContractFile(path)
	if err != nil {
		resp["ok"] = false
		resp["error"] = err.Error()
		writeJSON(w, resp)
		return
	}
	resp["exists"] = true
	resp["contract"] = contract
	resp["problems"] = problems
	resp["valid"] = len(problems) == 0
	writeJSON(w, resp)
}

func handleContractGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		FrontendDir string `json:"frontend_dir"`
		BackendDir  string `json:"backend_dir"`
		Save        bool   `json:"save"`
		Overwrite   bool   `json:"overwrite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "请求解析失败: "+err.Error(), 400)
		return
	}
	c := cfgSnapshot()
	build, err := buildContractFromConfig(c, body.FrontendDir, body.BackendDir)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	problems := validateContract(build.Contract)
	resp := map[string]any{
		"ok":       len(problems) == 0,
		"root":     build.Root,
		"path":     build.Path,
		"contract": build.Contract,
		"warnings": build.Warnings,
		"problems": problems,
		"written":  false,
	}
	if len(problems) > 0 && body.Save {
		writeJSONStatus(w, http.StatusBadRequest, resp)
		return
	}
	if !body.Save {
		if _, err := os.Stat(build.Path); err == nil {
			resp["existing"] = true
		}
		writeJSON(w, resp)
		return
	}

	if _, err := os.Stat(build.Path); err == nil && !body.Overwrite {
		resp["existing"] = true
		resp["written"] = false
		writeJSON(w, resp)
		return
	}
	if err := writeContractFile(build.Path, build.Contract); err != nil {
		http.Error(w, "写入契约失败: "+err.Error(), 500)
		return
	}
	saved := cfgUpdate(func(g *Config) {
		g.Contract = &ContractConfig{
			Root: build.Root,
			Path: contractFileName,
			Mode: contractModeReadOnly,
		}
	})
	saveExternalConfig(saved)
	setCfgStale(true)
	auditNow(actUser, "contract_generate", filepath.Base(build.Path), "", resOK,
		"项目契约已生成（只读模式）")
	resp["written"] = true
	resp["mode"] = contractModeReadOnly
	writeJSON(w, resp)
}

func handleContractValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		YAML string `json:"yaml"`
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "请求解析失败: "+err.Error(), 400)
		return
	}
	var (
		contract *EnvironmentContract
		problems []string
		err      error
	)
	if strings.TrimSpace(body.YAML) != "" {
		contract, problems, err = parseContractYAML([]byte(body.YAML))
	} else {
		path := strings.TrimSpace(body.Path)
		if path == "" {
			_, path, err = contractPathOf(cfgSnapshot())
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
		}
		contract, problems, err = loadContractFile(path)
	}
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{
		"ok":       len(problems) == 0,
		"contract": contract,
		"problems": problems,
	})
}

// handleContractMode 切换契约模式。readonly 只能生成计划和快照；
// enforce 才允许 Apply 执行。切 enforce 需要一次显式确认（428），
// 但不等于以后每个动作都放行——导入 schema 仍然逐次确认。
func handleContractMode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		Mode    string `json:"mode"`
		Confirm bool   `json:"confirm"`
		Lang    string `json:"lang"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "请求解析失败: "+err.Error(), 400)
		return
	}
	mode := strings.ToLower(strings.TrimSpace(body.Mode))
	if !contractModeValid(mode) {
		writeJSONStatus(w, 400, map[string]any{"ok": false, "error": "契约模式必须是 off / readonly / enforce"})
		return
	}
	c := cfgSnapshot()
	if mode != contractModeOff {
		if c.Contract == nil {
			writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "还没有契约配置，先生成 envkit.yaml"})
			return
		}
		_, path, err := contractPathOf(c)
		if err != nil {
			writeJSONStatus(w, 409, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		_, problems, err := loadContractFile(path)
		if err != nil {
			writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "契约不可读：" + err.Error()})
			return
		}
		if len(problems) > 0 {
			writeJSONStatus(w, 409, map[string]any{"ok": false, "error": "契约自身有硬错误：" + strings.Join(problems, "；")})
			return
		}
	}
	if mode == contractModeEnforce && !body.Confirm {
		reason := "切换到 enforce 后，环境事务可以执行建库、启动服务和导入 schema。高风险动作仍需逐次确认，执行前会自动快照。"
		if strings.EqualFold(body.Lang, "en") {
			reason = "After switching to enforce, environment transactions may create databases, start services and import schemas. High-risk actions still require separate confirmation and a checkpoint is taken first."
		}
		needConfirm(w, "contract_mode", mode, reason, true)
		return
	}
	saved := cfgUpdate(func(g *Config) {
		if g.Contract == nil {
			g.Contract = &ContractConfig{}
		}
		g.Contract.Mode = mode
	})
	normalizeConfig(&saved)
	if hard := hardProblems(checkConfig(saved)); len(hard) > 0 {
		msgs := []string{}
		for _, p := range hard {
			msgs = append(msgs, p.String())
		}
		writeJSONStatus(w, 409, map[string]any{"ok": false, "error": strings.Join(msgs, "；")})
		return
	}
	saveExternalConfig(saved)
	setCfgStale(true)
	auditNow(actUser, "contract_mode", mode, "", resOK, "")
	writeJSON(w, map[string]any{"ok": true, "mode": mode})
}

// handleContractDoctor 只读诊断：GET/POST 都不启动、不停止、不修复。
func handleContractDoctor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	c := cfgSnapshot()
	rep := runContractDoctor(c, "")
	writeJSON(w, rep)
}
