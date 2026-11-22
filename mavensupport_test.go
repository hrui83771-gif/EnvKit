package main

// mavensupport_test.go —— Java/Maven 项目支持（v3.8）的验证集。
//
// 覆盖四条链路：
//  1. backendProjectKind 项目种类判定（launch/envreq/verify 共用的唯一口径）
//  2. 环境符合性：Maven 项目不误报 Go/Node 缺失；pom 的 java.version 真校验
//  3. 启动分派：Maven 后端在缺 JDK 时给出明确的 missing_dep，而不是跑 go build
//  4. 复验名单：Maven 后端的端口进程是 java.exe，名单必须认它
//
// 变异验证见 docs 之外的手记：删 checkGoReq 的 maven 闸 / 删 opStartBackend 的
// maven 分派 / 删 svcExpectProcs 的 java 追加，三条都要精确红。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMavenFixture 造一个最小 Maven 项目目录（pom.xml 可带自定义内容）。
func writeMavenFixture(t *testing.T, pomContent string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pom.xml"), []byte(pomContent), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const pomTCM = `<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <parent>
    <groupId>org.springframework.boot</groupId>
    <artifactId>spring-boot-starter-parent</artifactId>
    <version>3.2.0</version>
  </parent>
  <groupId>com.example</groupId>
  <artifactId>tcm-inventory</artifactId>
  <version>1.0.0</version>
  <properties>
    <java.version>17</java.version>
    <mybatis-plus.version>3.5.5</mybatis-plus.version>
  </properties>
  <dependencies>
    <dependency>
      <groupId>org.springframework.boot</groupId>
      <artifactId>spring-boot-starter-web</artifactId>
    </dependency>
  </dependencies>
</project>`

func TestBackendProjectKind(t *testing.T) {
	mk := func(files map[string]string) string {
		dir := t.TempDir()
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	cases := []struct {
		name string
		dir  string
		want string
	}{
		{"空目录配置", "", "go"},
		{"不存在的目录", filepath.Join(t.TempDir(), "nope"), "go"},
		{"go.mod 即 go", mk(map[string]string{"go.mod": "module x\n"}), "go"},
		{"裸 main.go 即 go", mk(map[string]string{"main.go": "package main"}), "go"},
		{"pom.xml 即 maven", writeMavenFixture(t, pomTCM), "maven"},
		{"mvnw.cmd 即 maven", mk(map[string]string{"mvnw.cmd": "@echo off"}), "maven"},
		{"go.mod 与 pom 并存偏向 go", mk(map[string]string{"go.mod": "module x\n", "pom.xml": "<project/>"}), "go"},
	}
	for _, c := range cases {
		if got := backendProjectKind(c.dir); got != c.want {
			t.Errorf("%s: backendProjectKind(%q)=%s，要 %s", c.name, c.dir, got, c.want)
		}
	}
}

func TestExtractPomMeta(t *testing.T) {
	a, jv := extractPomMeta(pomTCM)
	// artifactId 必须取 project 级的第一个，不能抓到 spring-boot-starter-parent
	if a != "tcm-inventory" {
		t.Errorf("artifactId=%q，要 tcm-inventory（第一个 <artifactId> 在依赖前面）", a)
	}
	if jv != "17" {
		t.Errorf("javaVer=%q，要 17", jv)
	}
	if _, jv2 := extractPomMeta("<project><maven.compiler.source>1.8</maven.compiler.source></project>"); jv2 != "1.8" {
		t.Errorf("maven.compiler.source 口径没接上：%q", jv2)
	}
	if a3, _ := extractPomMeta("<project/>"); a3 != "" {
		t.Errorf("空 pom 应全空，得 %q", a3)
	}
}

func TestNormalizeJavaVer(t *testing.T) {
	cases := map[string]string{
		"17":        "17",
		"17.0.9":    "17.0.9",
		"1.8.0_392": "1.8.0", // JDK8 构建号截掉
		" 21.0.1 ":  "21.0.1",
	}
	for in, want := range cases {
		if got := normalizeJavaVer(in); got != want {
			t.Errorf("normalizeJavaVer(%q)=%q，要 %q", in, got, want)
		}
	}
}

func TestExtractJavaVersion(t *testing.T) {
	cases := map[string]string{
		"openjdk version \"17.0.9\" 2023-10-17 LTS": "17.0.9",
		"java version \"1.8.0_392\"":                "1.8.0",
		`openjdk version "21" 2023-09-19`:           "21",
		"没有版本信息的输出":                                 "",
	}
	for in, want := range cases {
		if got := extractJavaVersion(in); got != want {
			t.Errorf("extractJavaVersion(%q)=%q，要 %q", in, got, want)
		}
	}
}

func TestJavaReq(t *testing.T) {
	pomDir := writeMavenFixture(t, pomTCM)
	emptyDir := t.TempDir()

	// 声明 17、实际 17.0.9 → 够用
	if r := checkJavaReq(pomDir, "17.0.9"); r.Status != reqOK {
		t.Errorf("17.0.9 应满足 17：status=%s why=%s", r.Status, r.Why)
	}
	// 声明了但机器没装 JDK → not_install（这是真问题，要报）
	if r := checkJavaReq(pomDir, ""); r.Status != reqNotInstall {
		t.Errorf("没装 JDK 应报 not_install：status=%s why=%s", r.Status, r.Why)
	}
	// 声明 17 实际 1.8（JDK8 归一后 1.8.0 < 17）→ too_low
	if r := checkJavaReq(writeMavenFixture(t, strings.Replace(pomTCM, "<java.version>17<", "<java.version>21<", 1)), "1.8.0_392"); r.Status != reqTooLow {
		t.Errorf("JDK8 跑要 21 的项目应 too_low：status=%s why=%s", r.Status, r.Why)
	}
	// pom 未声明 → undeclared（不是问题）
	pomNoProps := `<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <parent>
    <groupId>org.springframework.boot</groupId>
    <artifactId>spring-boot-starter-parent</artifactId>
    <version>3.2.0</version>
  </parent>
  <artifactId>tcm-inventory</artifactId>
  <dependencies>
    <dependency>
      <groupId>org.springframework.boot</groupId>
      <artifactId>spring-boot-starter-web</artifactId>
    </dependency>
  </dependencies>
</project>`
	if r := checkJavaReq(writeMavenFixture(t, pomNoProps), "17.0.9"); r.Status != reqUndeclared {
		t.Errorf("未声明 java.version 应 undeclared：status=%s", r.Status)
	}
	// 非 Maven 目录 → n_a（**不是问题**，这就是"非 Java 项目不报错"的判据）
	if r := checkJavaReq(emptyDir, ""); r.Status != reqNA {
		t.Errorf("非 Maven 项目应 n_a：status=%s why=%s", r.Status, r.Why)
	}
}

func TestGoReqMavenGate(t *testing.T) {
	// 用户问的核心场景：后端是 Java、机器没装 Go —— 绝不能报"未检测到 Go"。
	pomDir := writeMavenFixture(t, pomTCM)
	r := checkGoReq(pomDir, "")
	if r.Status != reqNA {
		t.Errorf("Maven 项目没装 Go 报了 %s（要 n_a）：why=%s —— 这是误报，不是问题", r.Status, r.Why)
	}
	// 对照组：Go 项目没装 Go 仍然必须报 not_install（修误报不能连真问题一起哑掉）
	if r := checkGoReq(t.TempDir(), ""); r.Status != reqNotInstall {
		t.Errorf("Go 项目没装 Go 应仍报 not_install：status=%s", r.Status)
	}
}

func TestNodeReqNoFrontend(t *testing.T) {
	// 纯 Java 后端项目没配前端：Node 装没装都无关，不该报"未检测到 Node.js"
	if r := checkNodeReq("", ""); r.Status != reqUndeclared {
		t.Errorf("未配置前端目录应 undeclared：status=%s why=%s", r.Status, r.Why)
	}
	empty := t.TempDir()
	if r := checkNodeReq(empty, ""); r.Status != reqUndeclared {
		t.Errorf("前端目录没有 package.json 应 undeclared：status=%s why=%s", r.Status, r.Why)
	}
	// 有 package.json 才谈"没装 Node"
	fe := t.TempDir()
	if err := os.WriteFile(filepath.Join(fe, "package.json"), []byte(`{"name":"x"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if r := checkNodeReq(fe, ""); r.Status != reqNotInstall {
		t.Errorf("Node 项目没装 Node 应仍报 not_install：status=%s", r.Status)
	}
}

// TestLaunchPlanMaven 启动推断对 Maven 项目给出 spring-boot:run 而不是找 main.go。
func TestLaunchPlanMaven(t *testing.T) {
	pomDir := writeMavenFixture(t, pomTCM)
	orig := cfg.Projects
	t.Cleanup(func() { cfg.Projects = orig })
	cfg.Projects.BackendDir = pomDir

	p := currentLaunchPlan()
	if p.BackendKind != "maven" {
		t.Fatalf("BackendKind=%q，要 maven", p.BackendKind)
	}
	if p.BackendFile != "pom.xml" {
		t.Errorf("BackendFile=%q，要 pom.xml", p.BackendFile)
	}
	brief := launchPlanBrief(p)
	if !strings.Contains(brief, "mvn spring-boot:run") {
		t.Errorf("launchPlanBrief 没提 mvn spring-boot:run：%s", brief)
	}
	if strings.Contains(brief, "go run") {
		t.Errorf("Maven 项目不应出现 go run：%s", brief)
	}
}

// TestSvcExpectProcsMaven 复验名单：Maven 后端的端口进程是 java。
func TestSvcExpectProcsMaven(t *testing.T) {
	orig := cfg.Projects
	t.Cleanup(func() { cfg.Projects = orig })

	cfg.Projects.BackendDir = writeMavenFixture(t, pomTCM)
	names := svcExpectProcs("backend")
	ok := false
	for _, n := range names {
		if strings.EqualFold(n, "java.exe") {
			ok = true
		}
	}
	if !ok {
		t.Errorf("Maven 后端的复验名单不含 java.exe：%v —— 端口会被当别人的剔除，复验必假红", names)
	}

	// 对照：Go 项目名单不应混进 java.exe（防"java 进程占 8080 被误认成后端"回潮）
	cfg.Projects.BackendDir = t.TempDir()
	for _, n := range svcExpectProcs("backend") {
		if strings.EqualFold(n, "java.exe") {
			t.Error("Go 项目的后端复验名单不应包含 java.exe")
			break
		}
	}
}

// TestOpStartBackendMavenMissingDeps 启动分派 + 依赖前置：清空 PATH 与组件表后，
// Maven 目录的后端启动必须给出明确的缺依赖失败（JDK），绝不能走到 go build。
func TestOpStartBackendMavenMissingDeps(t *testing.T) {
	origComp := cfg.Components
	origProj := cfg.Projects
	origPath := os.Getenv("PATH")
	origJH := os.Getenv("JAVA_HOME")
	t.Cleanup(func() {
		cfg.Components = origComp
		cfg.Projects = origProj
		os.Setenv("PATH", origPath)
		os.Setenv("JAVA_HOME", origJH)
	})
	cfg.Components = nil // findExe 的第一来源
	cfg.Projects.BackendDir = writeMavenFixture(t, pomTCM)
	os.Setenv("JAVA_HOME", "")
	// PATH 指向空目录：LookPath 全瞎（测的是"机器什么都没有"的确定性形态）
	stub := t.TempDir()
	os.Setenv("PATH", stub)
	// opStartBackend 会 setProg("backend-start",...)，这是全局进度表——
	// 不清掉会把 TestEndStartFlagNoopWhenNotStarting 的前置条件打红。
	t.Cleanup(func() {
		progMu.Lock()
		delete(progState, "backend-start")
		progMu.Unlock()
	})

	res := opStartBackend(cfg.Projects.BackendDir, actUser)
	if res.Ok {
		t.Fatal("什么依赖都没有时启动成功了，不可能——复验前置失效")
	}
	if res.ErrKind != errKindMissingDep {
		t.Fatalf("errKind=%s（要 missing_dep）：%s", res.ErrKind, res.Msg)
	}
	// 缺的必须是 JDK。变异哨兵：删掉 opStartBackend 的 maven 分派后，
	// 这条会走到 go 路径报"未找到 go"，Msg 里没有 JDK —— 精确红。
	if !strings.Contains(res.Msg, "JDK") {
		t.Errorf("缺依赖信息没指向 JDK：%s", res.Msg)
	}
}
