package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorNoContract(t *testing.T) {
	dir := t.TempDir()
	rep := runContractDoctor(Config{}, filepath.Join(dir, contractFileName))
	if rep.Status != doctorStatusNoContract {
		t.Fatalf("status=%q, want no_contract; conclusion=%q", rep.Status, rep.Conclusion)
	}
	if rep.OK {
		t.Fatal("没有契约时不能判定为 ok")
	}
	if len(rep.Checks) != 1 || rep.Checks[0].Status != doctorUndeclared {
		t.Fatalf("checks=%+v", rep.Checks)
	}
	if rep.Summary.Undeclared != 1 {
		t.Fatalf("summary=%+v, want 1 undeclared", rep.Summary)
	}
}

func TestDoctorContractReadOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "web"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENVKIT_DOCTOR_TEST_MISSING", "")
	yaml := `version: 1
project:
  name: demo
  root: "."
tools:
  go: ">=1.0.0"
services:
  - id: web
    label: 前端
    adapter: local
    risk_tier: low
    dir: web
    start: npm run dev
    probe:
      kind: port
      port: 62111
secrets:
  demo_token: env:ENVKIT_DOCTOR_TEST_MISSING
`
	path := filepath.Join(root, contractFileName)
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	rep := runContractDoctor(Config{}, path)
	if rep.ProjectRoot != root {
		t.Fatalf("project_root=%q, want %q; status=%q error=%q conclusion=%q", rep.ProjectRoot, root, rep.Status, rep.Error, rep.Conclusion)
	}
	if rep.Status != doctorStatusDrift {
		t.Fatalf("status=%q, checks=%+v", rep.Status, rep.Checks)
	}
	assertCheck(t, rep, "contract.parse", doctorOK)
	assertCheck(t, rep, "project.root", doctorOK)
	assertCheck(t, rep, "services.web.dir", doctorOK)
	assertCheck(t, rep, "services.web.probe", doctorDrift)
	assertCheck(t, rep, "secrets.demo_token", doctorDrift)
}

func TestDoctorMalformedContractIsBlocked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, contractFileName)
	if err := os.WriteFile(path, []byte("version: 1\nunknown_field: true\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rep := runContractDoctor(Config{}, path)
	if rep.Status != doctorStatusBlocked {
		t.Fatalf("status=%q, want blocked", rep.Status)
	}
	if rep.OK || rep.Error == "" {
		t.Fatalf("ok=%v error=%q", rep.OK, rep.Error)
	}
}

func TestDoctorFinalize(t *testing.T) {
	rep := DoctorReport{Checks: []DoctorCheck{
		{Status: doctorOK},
		{Status: doctorOK},
		{Status: doctorUndeclared},
	}}
	doctorFinalize(&rep)
	if rep.Status != doctorStatusPartial {
		t.Fatalf("status=%q, want partial", rep.Status)
	}
	if rep.Summary.OK != 2 || rep.Summary.Undeclared != 1 {
		t.Fatalf("summary=%+v", rep.Summary)
	}
	if rep.OK {
		t.Fatal("有未声明项时不能判定为 ok")
	}

	rep = DoctorReport{Checks: []DoctorCheck{{Status: doctorOK}}}
	doctorFinalize(&rep)
	if rep.Status != doctorStatusOK || !rep.OK {
		t.Fatalf("status=%q ok=%v", rep.Status, rep.OK)
	}

	rep = DoctorReport{Checks: []DoctorCheck{{Status: doctorOK}, {Status: doctorUnknown}}}
	doctorFinalize(&rep)
	if rep.Status != doctorStatusPartial {
		t.Fatalf("status=%q, want partial", rep.Status)
	}
}

func TestDoctorTableNamesFromSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema.sql")
	sql := "CREATE TABLE `user` (id INT);\n" +
		"create table if not exists \"orders\" (id INT);\n" +
		"CREATE TABLE [audit_log] (id INT);\n" +
		"CREATE TABLE `user` (id INT);\n"
	if err := os.WriteFile(path, []byte(sql), 0644); err != nil {
		t.Fatal(err)
	}
	names := tableNamesFromSchema(path)
	want := []string{"audit_log", "orders", "user"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("names=%v, want %v", names, want)
	}
}

func TestDoctorPortFromURL(t *testing.T) {
	cases := map[string]int{
		"http://127.0.0.1:8080/api": 8080,
		"http://localhost:5173/":    5173,
		"https://example.com":       443,
		"http://example.com":        80,
		"not-a-url":                 0,
	}
	for in, want := range cases {
		if got := doctorPortFromURL(in); got != want {
			t.Fatalf("doctorPortFromURL(%q)=%d, want %d", in, got, want)
		}
	}
}

func assertCheck(t *testing.T, rep DoctorReport, id, status string) {
	t.Helper()
	for _, c := range rep.Checks {
		if c.ID == id {
			if c.Status != status {
				t.Fatalf("check %s status=%q, want %q (detail=%q)", id, c.Status, status, c.Detail)
			}
			return
		}
	}
	t.Fatalf("check %s not found; checks=%+v", id, rep.Checks)
}
