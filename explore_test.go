package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withProjectRoot 把临时目录设为唯一的后端项目目录，测试结束还原。
func withProjectRoot(t *testing.T) string {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		tmp = t.TempDir()
	}
	cfgMu.Lock()
	oldFE, oldBE := cfg.Projects.FrontendDir, cfg.Projects.BackendDir
	cfg.Projects.FrontendDir, cfg.Projects.BackendDir = "", tmp
	cfgMu.Unlock()
	t.Cleanup(func() {
		cfgMu.Lock()
		cfg.Projects.FrontendDir, cfg.Projects.BackendDir = oldFE, oldBE
		cfgMu.Unlock()
	})
	return tmp
}

func writeF(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("写文件失败：%v", err)
	}
}

// ---------- 沙箱边界 ----------

func TestExploreSandboxRejectsOutsidePath(t *testing.T) {
	root := withProjectRoot(t)
	outside := filepath.Join(filepath.Dir(root), "outside-secret.txt")

	if _, ok := aiSafePath(outside); ok {
		t.Fatalf("项目外的路径竟被放行：%s", outside)
	}
	if _, ok := aiSafePath("..\\..\\Windows\\System32"); ok {
		t.Fatal("相对路径逃逸未被拦截")
	}
	if _, ok := aiSafePath(root); !ok {
		t.Fatal("项目根自身应被放行")
	}
	if _, ok := aiSafePath("src"); !ok {
		t.Fatal("项目内的相对路径应被放行")
	}
}

func TestExploreRejectsSensitiveFile(t *testing.T) {
	root := withProjectRoot(t)
	writeF(t, filepath.Join(root, "config.json"), `{"mysql_password":"123456"}`)
	writeF(t, filepath.Join(root, ".env"), "SECRET_TOKEN=abc123")
	writeF(t, filepath.Join(root, "server.key"), "-----BEGIN KEY-----")

	for _, name := range []string{"config.json", ".env", "server.key"} {
		if !exploreSecretPath(filepath.Join(root, name)) {
			t.Fatalf("%s 应被判定为敏感文件", name)
		}
		out := exploreRead(map[string]any{"path": name})
		if !strings.Contains(out, "拒绝读取") {
			t.Fatalf("读取 %s 未被拒绝，实际输出：%s", name, out)
		}
	}
}

func TestExploreRedactsSecretsInSource(t *testing.T) {
	root := withProjectRoot(t)
	writeF(t, filepath.Join(root, "internal", "db.go"), "dsn := \"root:123456@tcp(127.0.0.1:3306)/farm\"\npassword := \"p@ssw0rd\"\n")
	out := exploreRead(map[string]any{"path": "internal/db.go"})
	if strings.Contains(out, "123456") || strings.Contains(out, "p@ssw0rd") {
		t.Fatalf("源码里的密码没有被脱敏：%s", out)
	}
	if !strings.Contains(out, "***") {
		t.Fatalf("脱敏后应出现 *** 占位：%s", out)
	}
}

// ---------- 三个工具 ----------

func TestExploreListSkipsNodeModulesAndRespectsLimits(t *testing.T) {
	root := withProjectRoot(t)
	writeF(t, filepath.Join(root, "src", "main.js"), "console.log(1)")
	writeF(t, filepath.Join(root, "package.json"), `{"name":"demo"}`)
	// node_modules 里放一个必然命中关键词的文件，用于证明依赖目录被跳过
	writeF(t, filepath.Join(root, "node_modules", "evil", "index.js"), "SHOULD_NEVER_APPEAR")

	out := exploreList(map[string]any{})
	if strings.Contains(out, "SHOULD_NEVER_APPEAR") || strings.Contains(out, "node_modules") {
		t.Fatalf("依赖目录应被跳过：%s", out)
	}
	if !strings.Contains(out, "src/") || !strings.Contains(out, "package.json") {
		t.Fatalf("未列出项目内文件：%s", out)
	}

	// 深度限制：depth=1 时不该出现二级文件
	shallow := exploreList(map[string]any{"depth": 1})
	if strings.Contains(shallow, "main.js") {
		t.Fatalf("depth=1 不该展开到二级文件：%s", shallow)
	}
}

func TestExploreSearchAndOutOfProject(t *testing.T) {
	root := withProjectRoot(t)
	writeF(t, filepath.Join(root, "server", "config.yaml"), "server:\n  port: 8888\n")
	writeF(t, filepath.Join(root, "server", "main.go"), "package main\n// listen 8888\n")

	byName := exploreSearch(map[string]any{"pattern": "*.go", "mode": "name"})
	if !strings.Contains(byName, "server/main.go") {
		t.Fatalf("文件名搜索未命中：%s", byName)
	}
	byContent := exploreSearch(map[string]any{"pattern": "8888", "mode": "content"})
	if !strings.Contains(byContent, "8888") {
		t.Fatalf("内容搜索未命中：%s", byContent)
	}

	escaped := exploreSearch(map[string]any{"pattern": "*", "mode": "name"})
	if strings.Contains(escaped, "Windows") {
		t.Fatalf("搜索越界到了项目之外：%s", escaped)
	}
}

func TestExploreToolsRegistered(t *testing.T) {
	for _, name := range []string{"list_project", "search_files", "read_file", "get_project_brief"} {
		if _, ok := aiToolRegistry[name]; !ok {
			t.Fatalf("工具未注册：%s", name)
		}
		if aiToolRegistry[name].Write {
			t.Fatalf("探索工具必须是只读：%s", name)
		}
	}
	// 工具清单顺序必须稳定（否则打碎上游前缀缓存）
	a, b := aiToolDefs(), aiToolDefs()
	if len(a) != len(b) {
		t.Fatal("两次生成的工具清单长度不一致")
	}
	for i := range a {
		if a[i].Function.Name != b[i].Function.Name {
			t.Fatalf("工具顺序不稳定：%s != %s", a[i].Function.Name, b[i].Function.Name)
		}
	}
}

// ---------- 项目画像 ----------

func TestProjectBriefDetectsGoAndNode(t *testing.T) {
	root := withProjectRoot(t)
	writeF(t, filepath.Join(root, "go.mod"), "module example.com/farm\n\ngo 1.23\n")
	writeF(t, filepath.Join(root, "main.go"), "package main\nfunc main(){}\n")
	writeF(t, filepath.Join(root, "web", "package.json"),
		`{"name":"farm-web","scripts":{"serve":"vue-cli-service serve","build":"vue-cli-service build"},"dependencies":{"vue":"^3.0.0"}}`)
	writeF(t, filepath.Join(root, "web", "src", "main.js"), "console.log('hi')")
	writeF(t, filepath.Join(root, "web", "vue.config.js"), "module.exports = { devServer: { port: 8080 } }")

	raw := aiProjectBriefJSON()
	for _, want := range []string{"go", "node", "vue", "example.com/farm", "serve:", "8080"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("画像缺少 %s：%s", want, raw)
		}
	}
	// node_modules 缺失是这个 fixture 的事实，画像应给出提示（否则 npm run 必然失败）
	if !strings.Contains(raw, "node_modules") {
		t.Fatalf("画像应体现依赖安装状态：%s", raw)
	}
}

// ---------- OpResult 契约 ----------

func TestOpResultTextIncludesErrKindAndEvidence(t *testing.T) {
	r := opFail("start_service", "web", errKindMissingDep, "未找到 npm", "need=npm")
	s := r.String()
	if !strings.Contains(s, errKindMissingDep) || !strings.Contains(s, "need=npm") {
		t.Fatalf("失败文本缺少归类或证据：%s", s)
	}
	if r.Err() == nil {
		t.Fatal("失败结果必须能转成 error")
	}
	okRes := opOK("start_service", "web", "已派生", "dir=D:\\x")
	if okRes.Err() != nil {
		t.Fatal("成功结果不应产生 error")
	}
	if !strings.Contains(okRes.String(), "dir=D:\\x") {
		t.Fatalf("成功结果缺少证据：%s", okRes.String())
	}
	err := aiOpErr("前端启动", r)
	if !strings.Contains(err.Error(), "前端启动失败") || !strings.Contains(err.Error(), "need=npm") {
		t.Fatalf("AI 错误文本不完整：%v", err)
	}
	// 无证据时必须明说，避免模型脑补
	if got := aiOpEvidence(opOK("a", "b", "done")); got != "无" {
		t.Fatalf("无证据时应返回「无」，实际：%s", got)
	}
}
