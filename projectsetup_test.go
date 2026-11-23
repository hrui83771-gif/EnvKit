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

