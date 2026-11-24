package main

import (
	"strings"
	"testing"
)

func TestClassifyEvidenceProjectFingerprintWins(t *testing.T) {
	meta := procDetails{
		Name:    "node.exe",
		Cmdline: `node C:\work\demo\node_modules\vite\bin\vite.js --port 5173`,
		Exe:     `C:\Program Files\nodejs\node.exe`,
	}
	svc := projectServiceProfile{
		Service: "web", Dir: `C:\work\demo`, Kind: "vite",
		HealthPaths: []string{"/"},
	}
	hint := svcPortHint{Port: 5173, Source: "vite.config.ts", Confidence: 85}
	ob, score := classifyEvidence("web", hint, 42, "node.exe", meta, true, true, true, svc, "", 0)
	if ob.State != "external" || score < 60 {
		t.Fatalf("state=%s score=%d, want external >=60 (%+v)", ob.State, score, ob)
	}
	if ob.Port != 5173 || ob.PID != 42 || ob.PortSource != "vite.config.ts" {
		t.Fatalf("bad observed service: %+v", ob)
	}
}

func TestClassifyEvidenceNameOnlyIsUnknown(t *testing.T) {
	meta := procDetails{Name: "java.exe", Cmdline: `java -jar C:\other\app.jar`}
	svc := projectServiceProfile{Service: "backend", Dir: `C:\work\demo`, Kind: "maven"}
	hint := svcPortHint{Port: 8080, Source: "application.yml", Confidence: 85}
	ob, score := classifyEvidence("backend", hint, 77, "java.exe", meta, true, false, false, svc, "", 0)
	if ob.State != "unknown" || score >= 60 {
		t.Fatalf("state=%s score=%d, want unknown <60 (%+v)", ob.State, score, ob)
	}
}

func TestClassifyEvidenceForeign(t *testing.T) {
	meta := procDetails{Name: "python.exe", Cmdline: `python -m http.server 8080`}
	svc := projectServiceProfile{Service: "backend", Dir: `C:\work\demo`, Kind: "maven"}
	hint := svcPortHint{Port: 8080, Source: "application.yml", Confidence: 85}
	ob, score := classifyEvidence("backend", hint, 88, "python.exe", meta, false, false, false, svc, "", 0)
	if ob.State != "foreign" {
		t.Fatalf("state=%s score=%d, want foreign (%+v)", ob.State, score, ob)
	}
}

func TestHealthFromProbe(t *testing.T) {
	cases := []struct {
		code int
		want string
	}{
		{0, "unknown"},
		{200, "ok"},
		{302, "ok"},
		{401, "auth"},
		{403, "auth"},
		{500, "fail"},
	}
	for _, c := range cases {
		if got := healthFromProbe("", c.code, "web"); got != c.want {
			t.Fatalf("code %d -> %s, want %s", c.code, got, c.want)
		}
	}
}

func TestFirstPortForPIDFollowsDrift(t *testing.T) {
	occ := map[int][]int{
		5173: {100},
		5174: {100},
		8080: {200},
	}
	if got := firstPortForPID(occ, 100); got != 5173 {
		t.Fatalf("first port=%d, want 5173", got)
	}
	if got := firstPortForPID(occ, 200); got != 8080 {
		t.Fatalf("first port=%d, want 8080", got)
	}
}

func TestRuntimeServiceMergesExternalObservation(t *testing.T) {
	withObservedForTest(t, "web", observedInternal{observedService: observedService{
		Service: "web", State: "external", Port: 5173, PID: 4242,
		OwnerName: "node.exe", Health: "ok", PortSource: "vite.config.ts",
		PortConfidence: 85, ObservedAt: "2026-11-23 19:00:00",
		Evidence: []string{"fingerprint=进程命令行含项目目录"},
	}})
	withManagedStateForTest(t, "web", &SvcInfo{})

	st := runtimeService("web")
	if !st.Running || st.Source != srcExternal {
		t.Fatalf("running=%v source=%s, want external running", st.Running, st.Source)
	}
	if st.Verified {
		t.Fatalf("external must not be verified=true：verified 只表示 EnvKit 自己的复验")
	}
	if st.Health != "ok" || st.Port != 5173 || st.PID != 4242 || st.Phase != phRunning {
		t.Fatalf("bad state: %+v", st)
	}
	if st.URL != "http://127.0.0.1:5173/" {
		t.Fatalf("url=%q, want external open link", st.URL)
	}
	if !strings.Contains(st.LastVerify, "外部实例") {
		t.Fatalf("last_verify=%q, want 外部实例说明", st.LastVerify)
	}
}

func TestRuntimeServiceMergesForeignConflict(t *testing.T) {
	withObservedForTest(t, "backend", observedInternal{observedService: observedService{
		Service: "backend", State: "foreign", Port: 8080, PID: 999,
		OwnerName: "python.exe", Health: "ok", PortSource: "application.yml",
		PortConfidence: 85, ObservedAt: "2026-11-23 19:00:00",
	}})
	withManagedStateForTest(t, "backend", &SvcInfo{})

	st := runtimeService("backend")
	if st.Running {
		t.Fatalf("foreign port must not be running: %+v", st)
	}
	if st.Phase != phConflict || st.Source != srcForeign {
		t.Fatalf("phase=%s source=%s, want conflict/foreign", st.Phase, st.Source)
	}
}

func TestAnnotatePortOwnersUsesObservation(t *testing.T) {
	withObservedForTest(t, "web", observedInternal{observedService: observedService{
		Service: "web", State: "external", Port: 5173, PID: 4242,
		OwnerName: "node.exe", Health: "ok",
	}})
	withManagedStateForTest(t, "web", &SvcInfo{})

	rows := []portOcc{{Port: 5173, State: "LISTENING", PID: 4242, Name: "node.exe"}}
	annotatePortOwners(rows)
	if rows[0].Source != "external" || rows[0].Service != "web" {
		t.Fatalf("row=%+v, want external/web", rows[0])
	}
}

func TestSvcStartSkipExternalDoesNotSpawn(t *testing.T) {
	withObservedForTest(t, "web", observedInternal{observedService: observedService{
		Service: "web", State: "external", Port: 5173, PID: 1680,
		OwnerName: "node.exe", Health: "ok",
	}})
	withManagedStateForTest(t, "web", &SvcInfo{})

	res, hit := svcStartSkip("web")
	if !hit || !res.Ok || !res.SkipVerify {
		t.Fatalf("hit=%v res=%+v, want external skip with SkipVerify", hit, res)
	}
	if !strings.Contains(res.Msg, "外部实例") {
		t.Fatalf("msg=%q, want 外部实例说明", res.Msg)
	}
}

func TestSvcStartSkipForeignPortConflict(t *testing.T) {
	withObservedForTest(t, "backend", observedInternal{observedService: observedService{
		Service: "backend", State: "foreign", Port: 8080, PID: 4488,
		OwnerName: "python.exe", PortConfidence: 85,
	}})
	withManagedStateForTest(t, "backend", &SvcInfo{})

	res, hit := svcStartSkip("backend")
	if !hit || res.Ok || res.ErrKind != errKindPortConflict {
		t.Fatalf("hit=%v res=%+v, want port_conflict", hit, res)
	}
}

// scan_ports 是诊断清单（60 分），不是项目服务端口。后端正在 8080 跑、
// 前端真实端口 5173 还没起来时，不能拿 8080 的 java 阻断“启动前端”。
func TestSvcStartSkipIgnoresScanPortsForWeb(t *testing.T) {
	withObservedForTest(t, "web", observedInternal{observedService: observedService{
		Service: "web", State: "foreign", Port: 8080, PID: 4488,
		OwnerName: "java.exe", PortConfidence: 60,
	}})
	withManagedStateForTest(t, "web", &SvcInfo{})

	if res, hit := svcStartSkip("web"); hit {
		t.Fatalf("scan_ports 端口不应阻断前端启动，res=%+v", res)
	}
}

func withObservedForTest(t *testing.T, service string, v observedInternal) {
	t.Helper()
	observedMu.Lock()
	old, had := observedState[service]
	observedState[service] = v
	observedMu.Unlock()
	t.Cleanup(func() {
		observedMu.Lock()
		if had {
			observedState[service] = old
		} else {
			delete(observedState, service)
		}
		observedMu.Unlock()
	})
}

func withManagedStateForTest(t *testing.T, service string, v *SvcInfo) {
	t.Helper()
	svcMu.Lock()
	old, had := svcState[service]
	svcState[service] = v
	svcMu.Unlock()
	t.Cleanup(func() {
		svcMu.Lock()
		if had {
			svcState[service] = old
		} else {
			delete(svcState, service)
		}
		svcMu.Unlock()
	})
}
