package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)


func TestProjectInspectFindsDBCandidateWithoutLeakingPassword(t *testing.T) {
	root := t.TempDir()
	front := filepath.Join(root, "web")
	back := filepath.Join(root, "server")
	writeTestFile(t, front, "package.json", `{"name":"tcm-web","scripts":{"dev":"vite --port 5173 --host 127.0.0.1"},"devDependencies":{"vite":"^5"}}`)
	writeTestFile(t, back, "pom.xml", `<project><properties><java.version>17</java.version></properties></project>`)
	writeTestFile(t, filepath.Join(back, "src", "main", "resources"), "application.yml", "server:\n  port: 8080\n")
	writeTestFile(t, filepath.Join(back, "src", "main", "resources"), "application-dev.yml", `
spring:
  datasource:
    url: jdbc:mysql://localhost:3306/tcm_inventory?useUnicode=true&serverTimezone=Asia/Shanghai
    username: ${DB_USERNAME:root}
    password: ${DB_PASSWORD:}
`)
	writeTestFile(t, back, "application-local.yml", "DB_PASSWORD: s3cr3t-value\n")

	res := inspectProjectSetup(front, back)
	if res.Frontend.Kind != "vite" {
		t.Fatalf("frontend kind = %q, want vite", res.Frontend.Kind)
	}
	if res.Backend.Kind != "maven" {
		t.Fatalf("backend kind = %q, want maven", res.Backend.Kind)
	}
	if !res.Database.Found || len(res.Database.Candidates) == 0 {
		t.Fatalf("database candidates not found: %+v", res.Database)
	}
	c := res.Database.Candidates[0]
	if c.Name != "tcm_inventory" || c.User != "root" || c.Port != 3306 {
		t.Fatalf("candidate identity = %+v", c)
	}
	if !c.HasPassword {
		t.Fatalf("password placeholder should resolve from application-local.yml: %+v", c)
	}
	if c.PasswordSource != "application-local.yml" {
		t.Fatalf("password source = %q", c.PasswordSource)
	}
	if c.ID == "" {
		t.Fatal("candidate id empty")
	}
	if len(c.Notes) == 0 || !strings.Contains(c.Notes[0], "DB_PASSWORD") {
		t.Fatalf("notes = %v", c.Notes)
	}

	body, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "s3cr3t-value") {
		t.Fatal("API response leaked the password value")
	}
	if strings.Contains(string(body), `"password"`) {
		t.Fatal("API response contains a password field")
	}
}

func TestProjectInspectRejectsEmptyDirs(t *testing.T) {
	res := inspectProjectSetup("", "")
	if res.Frontend.Provided || res.Backend.Provided {
		t.Fatalf("empty dirs should not be provided: %+v", res)
	}
	if res.Database.Found {
		t.Fatalf("empty dirs should not scan database: %+v", res.Database)
	}
}

func TestParseDBFileYAMLQuotedValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "application.yml")
	text := `spring:
  datasource:
    url: "jdbc:mysql://db.example.com:3307/shop?useSSL=false"
    username: "shop_user"
    password: "p#ss word"
`
	c, ok := parseDBFile(path, "application.yml", text)
	if !ok {
		t.Fatal("nested YAML database config not found")
	}
	if c.Host != "db.example.com" || c.Port != 3307 || c.Name != "shop" || c.User != "shop_user" {
		t.Fatalf("candidate = %+v", c)
	}
	if c.password != "p#ss word" || !c.HasPassword {
		t.Fatalf("quoted password was not preserved: %+v", c)
	}
}

func TestParseDBFileDotEnvQuotedValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	text := "export DB_HOST=127.0.0.1\nDB_PORT=\"3308\"\nDB_NAME='inventory'\nDB_USER=root\nDB_PASSWORD='p#ss word'\n"
	c, ok := parseDBFile(path, ".env", text)
	if !ok {
		t.Fatal(".env database config not found")
	}
	if c.Host != "127.0.0.1" || c.Port != 3308 || c.Name != "inventory" || c.User != "root" {
		t.Fatalf("candidate = %+v", c)
	}
	if c.password != "p#ss word" {
		t.Fatalf("quoted .env password = %q", c.password)
	}
}

func TestCollectSecretValuesUsesStructuredParsers(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "application-local.yml", "DB_PASSWORD: \"p#ss word\"\n")
	writeTestFile(t, dir, ".env", "MYSQL_PASSWORD='from env'\n")
	writeTestFile(t, dir, "application.properties", "DB_PASSWORD=from props\n")

	secrets := collectSecretValues([]string{
		filepath.Join(dir, "application-local.yml"),
		filepath.Join(dir, ".env"),
		filepath.Join(dir, "application.properties"),
	})
	if secrets["DB_PASSWORD"].value != "p#ss word" {
		t.Fatalf("yaml secret = %+v", secrets["DB_PASSWORD"])
	}
	if secrets["MYSQL_PASSWORD"].value != "from env" {
		t.Fatalf(".env secret = %+v", secrets["MYSQL_PASSWORD"])
	}
}


func TestScanProjectSQLFilesPrefersSchemaAndSkipsDeps(t *testing.T) {
	root := t.TempDir()
	back := filepath.Join(root, "server")
	writeTestFile(t, filepath.Join(back, "sql"), "data.sql", "INSERT INTO t VALUES (1);")
	writeTestFile(t, filepath.Join(back, "sql"), "schema.sql", "CREATE TABLE t (id INT);")
	writeTestFile(t, filepath.Join(back, "node_modules", "pkg"), "ignored.sql", "SELECT 1;")
	files := scanProjectSQLFiles("", back)
	if len(files) != 2 {
		t.Fatalf("sql files = %+v, want 2 (node_modules ignored)", files)
	}
	if files[0].Name != "schema.sql" {
		t.Fatalf("schema.sql should sort first: %+v", files)
	}
	if !strings.HasSuffix(files[0].Path, filepath.Join("sql", "schema.sql")) {
		t.Fatalf("sql path = %q", files[0].Path)
	}
}

