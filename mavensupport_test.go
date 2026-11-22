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

func TestJavaModernForm(t *testing.T) {
	cases := map[string]string{
		"1.8.0":     "8.0", // JDK8 历史形态 → 现代口径
		"1.8.0_392": "8.0", // 带构建号也先截（normalizeJavaVer 在上游）
		"17.0.11":   "17.0.11",
		"1":         "1", // 1.x 但次段为 0（1.0）不是 JDK8 形态，原样
		"1.0":       "1.0",
		"":          "",
		"abc":       "abc", // 解析不了原样交下游
	}
	for in, want := range cases {
		if got := javaModernForm(in); got != want {
			t.Errorf("javaModernForm(%q)=%q，要 %q", in, got, want)
		}
	}
	if javaVerLess("1.8.0", "8") {
		t.Error("1.8.0 与 8 是同一个 Java，不应判 less")
	}
	if !javaVerLess("1.8.0", "17") {
		t.Error("JDK8 < 17 应成立")
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
	// pom 声明 8、实际 JDK8（报 1.8.0 历史形态）→ 必须 ok。
	// 变异哨兵：删掉 javaModernForm 的 1.x 平移后，1.8.0 < 8 会被误判 too_low。
	if r := checkJavaReq(writeMavenFixture(t, strings.Replace(pomTCM, "<java.version>17<", "<java.version>8<", 1)), "1.8.0_392"); r.Status != reqOK {
		t.Errorf("JDK8 跑声明 8 的项目应 ok（1.8.0≡8）：status=%s why=%s", r.Status, r.Why)
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

// TestMavenWrapperCachePickHighest 缓存回退要挑"完整且版本最高"的那套，
// 且对长而怪的 hash 目录名免疫（真实机器上缓存嵌套就是不定长的）。
func TestMavenWrapperCachePickHighest(t *testing.T) {
	root := filepath.Join(t.TempDir(), "dists")
	mk := func(rel ...string) string {
		p := filepath.Join(append([]string{root}, rel...)...)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("@echo off"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mk("apache-maven-3.6.2-bin", "hashA", "apache-maven-3.6.2", "bin", "mvn.cmd")
	mk("apache-maven-3.9.9-bin", "hashB", "apache-maven-3.9.9", "bin", "mvn.cmd")
	// 解压到一半的空壳（有版本号目录但没有 bin）：绝不能选中
	mk("apache-maven-3.11.0-bin", "hashC", "apache-maven-3.11.0", "README.txt")
	// 长而怪的 hash 名 + 最高版本：必须能走通并选中
	weird := mk("apache-maven-4.0.0-bin", "795eh28tki48bv3l67maojf0ra-and-even-longer", "apache-maven-4.0.0", "bin", "mvn.cmd")

	if got := mavenWrapperCacheCmdIn(root); got != weird {
		t.Errorf("缓存回退选中 %q，要版本最高且完整的 %q", got, weird)
	}
	// 不存在的根：返回空，绝不能 panic 或瞎猜
	if got := mavenWrapperCacheCmdIn(filepath.Join(t.TempDir(), "nope")); got != "" {
		t.Errorf("缓存根不存在应返回空，得 %q", got)
	}
}

// TestResolveMavenCmdOrder 三级回退的优先级：项目 mvnw > PATH > wrapper 缓存。
func TestResolveMavenCmdOrder(t *testing.T) {
	origComp := cfg.Components
	origPath := os.Getenv("PATH")
	origCache := probeMavenCache
	t.Cleanup(func() {
		cfg.Components = origComp
		os.Setenv("PATH", origPath)
		probeMavenCache = origCache
	})
	cfg.Components = nil
	os.Setenv("PATH", t.TempDir()) // LookPath 全瞎
	sentinel := filepath.Join(t.TempDir(), "cache-mvn.cmd")
	probeMavenCache = func() string { return sentinel }

	dir := t.TempDir()
	if got := resolveMavenCmd(dir); got != sentinel {
		t.Errorf("无 mvnw 无 PATH 应回退到缓存，得 %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "mvnw.cmd"), []byte("@echo off"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveMavenCmd(dir); got != filepath.Join(dir, "mvnw.cmd") {
		t.Errorf("项目自带 mvnw 应最优先，得 %q", got)
	}
	if got := resolveMavenCmd(""); got != sentinel {
		t.Errorf("projectDir 为空应跳过 mvnw 走缓存（组件检测场景），得 %q", got)
	}
}

// TestMavenStartEnvSwitch JAVA_HOME 错配修正：JAVA_HOME 指着 JDK8、
// 项目要求 17、PATH 的 java 够用 → 只对本次启动切 JAVA_HOME；
// 任何一边不满足都不动——能不切就不切，切也只切子进程环境。
func TestMavenStartEnvSwitch(t *testing.T) {
	origJH := os.Getenv("JAVA_HOME")
	origProbe := probeJavaVer
	t.Cleanup(func() {
		os.Setenv("JAVA_HOME", origJH)
		probeJavaVer = origProbe
	})

	base := t.TempDir()
	jdk8 := filepath.Join(base, "jdk8")
	if err := os.MkdirAll(filepath.Join(jdk8, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jdk8, "bin", "java.exe"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	pathJava := filepath.Join(base, "jdk17", "bin", "java.exe")
	if err := os.MkdirAll(filepath.Dir(pathJava), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathJava, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	probeJavaVer = func(p string) string {
		if strings.Contains(p, "jdk8") {
			return "1.8.0"
		}
		if strings.Contains(p, "jdk17") {
			return "17.0.11"
		}
		return ""
	}
	os.Setenv("JAVA_HOME", jdk8)

	// 要 17：JAVA_HOME=8 < 17，PATH 的 17.0.11 够 → 切到 PATH java 的 home
	env := mavenStartEnv(writeMavenFixture(t, pomTCM), pathJava)
	wantHome := filepath.Dir(filepath.Dir(pathJava))
	if env == nil || env["JAVA_HOME"] != wantHome {
		t.Errorf("应把 JAVA_HOME 切到 %s，得 %v", wantHome, env)
	}
	// 要 8：JAVA_HOME=1.8.0 已满足 → 不切（能不切就不切）
	pom8 := strings.Replace(pomTCM, "<java.version>17<", "<java.version>8<", 1)
	if env := mavenStartEnv(writeMavenFixture(t, pom8), pathJava); env != nil {
		t.Errorf("JAVA_HOME 已满足要求不应切换：%v", env)
	}
	// PATH java 探测不到版本 → 不切（宁缺毋乱，让 Maven 自己把错报出来）
	probeJavaVer = func(p string) string {
		if strings.Contains(p, "jdk8") {
			return "1.8.0"
		}
		return ""
	}
	if env := mavenStartEnv(writeMavenFixture(t, pomTCM), pathJava); env != nil {
		t.Errorf("PATH java 版本未知不应切换：%v", env)
	}
	// pom 没声明 java.version → 没有依据，不切
	if env := mavenStartEnv(t.TempDir(), pathJava); env != nil {
		t.Errorf("pom 未声明要求不应切换：%v", env)
	}
}

// TestDetectComponentMavenCache 组件检测与启动同口径：PATH 里没有 mvn 时，
// wrapper 缓存回退要把"未安装"翻成"已安装（缓存位置）"。
func TestDetectComponentMavenCache(t *testing.T) {
	origComp := cfg.Components
	origPath := os.Getenv("PATH")
	origCache := probeMavenCache
	t.Cleanup(func() {
		cfg.Components = origComp
		os.Setenv("PATH", origPath)
		probeMavenCache = origCache
	})
	cfg.Components = nil
	os.Setenv("PATH", t.TempDir())
	stub := filepath.Join(t.TempDir(), "mvn.cmd")
	if err := os.WriteFile(stub, []byte("@echo Apache Maven 3.9.9 (mock)\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	probeMavenCache = func() string { return stub }

	r := detectComponent(Component{Name: "Maven", CheckCmd: "mvn", CheckArgs: []string{"-v"}})
	if !r.Installed {
		t.Fatalf("缓存回退没被检测用到：%+v", r)
	}
	if !strings.Contains(r.Version, "3.9.9") {
		t.Errorf("版本没读出来：%q", r.Version)
	}
	if r.Location != stub {
		t.Errorf("Location=%q，要缓存路径 %q", r.Location, stub)
	}
}
