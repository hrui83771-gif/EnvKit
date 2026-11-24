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
