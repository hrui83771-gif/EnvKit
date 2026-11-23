package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFrontendProfileVitePorts(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "package.json", `{"name":"demo-web","devDependencies":{"vite":"^5.0.0"},"scripts":{"dev":"vite --port 5174"}}`)
	writeTestFile(t, dir, "vite.config.ts", "export default { server: {\n  port: 5173,\n  strictPort: true\n} }\n")

	p := buildFrontendProfile(dir)
	if p.Kind != "vite" {
		t.Fatalf("kind=%q, want vite", p.Kind)
	}
	assertHint(t, p.PortHints, 5173, "vite.config.ts", 85)
	assertHint(t, p.PortHints, 5174, "package.json", 75)
	assertHint(t, p.PortHints, 4173, "framework-default", 50)
}

func TestBackendProfileSpringPort(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "pom.xml", `<project><artifactId>demo-server</artifactId></project>`)
	writeTestFile(t, dir, "src/main/resources/application.yml", "server:\n  port: 8080\nspring:\n  datasource:\n    url: jdbc:mysql://127.0.0.1:3306/demo\n")

	p := buildBackendProfile(dir)
	if p.Kind != "maven" {
		t.Fatalf("kind=%q, want maven", p.Kind)
	}
	assertHint(t, p.PortHints, 8080, "application.yml", 85)
	// 3306 是数据库端口，不能被当成后端服务端口。
	if hasHint(p.PortHints, 3306) {
		t.Fatalf("3306 不应进入后端服务端口线索：%+v", p.PortHints)
	}
}

func TestFrontendConfigInlineAndEnvPorts(t *testing.T) {
	if got := portsFromFrontendConfig(`export default { server: { port: 5175 } }`); len(got) != 1 || got[0] != 5175 {
		t.Fatalf("inline vite port=%v, want [5175]", got)
	}
	if got := portsFromEnv("PORT=3000\nVITE_PORT=5176\nDATABASE_PORT=3306\n"); len(got) != 2 || got[0] != 3000 || got[1] != 5176 {
		t.Fatalf("env ports=%v, want [3000 5176]", got)
	}
}

func TestYAMLServerPortIgnoresDatasource(t *testing.T) {
	src := "spring:\n  datasource:\n    port: 3306\nserver:\n  port: 8081\n"
	if got := yamlServerPort(src); got != 8081 {
		t.Fatalf("yaml server port=%d, want 8081", got)
	}
}

func assertHint(t *testing.T, hints []svcPortHint, port int, source string, confidence int) {
	t.Helper()
	for _, h := range hints {
		if h.Port == port {
			if h.Source != source || h.Confidence != confidence {
				t.Fatalf("port %d hint=%+v, want source=%s confidence=%d", port, h, source, confidence)
			}
			return
		}
	}
	t.Fatalf("missing port %d in %+v", port, hints)
}

func hasHint(hints []svcPortHint, port int) bool {
	for _, h := range hints {
		if h.Port == port {
			return true
		}
	}
	return false
}
