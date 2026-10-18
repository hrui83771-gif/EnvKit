// logrotate_test.go —— v3.0 T1.8 受控日志轮转（plan §3.2.1）
//
// 覆盖三条验收：超阈值轮转、归档份数上限、轮转后磁盘释放可复验（实际字节）。
// 全部经产品真实路径（writeLogFile / rotateSink / logSinkDisk / cleanupOldLogs），
// 不 mock 文件系统——"测的东西就是产品看的东西"。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withLogDir 把日志目录切到临时目录并返回它。
// 同步说明：本包测试默认串行执行（无 t.Parallel）；切目录时必须同时清空
// logSinks——别的用例（exec/start 测试）经 infoS(..., "web") 已在全局 map 里
// 留下指向测试二进制目录的 "day/web" sink，只换 logDirOverride 不清 map 的话，
// 本用例的写入会跑到别人的目录去（现象：临时目录里文件根本不出现，轮转
// 永不触发——全量套件里挂过一次，隔离跑反而过，正是这个原因）。
func withLogDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logFileMu.Lock()
	logDirOverride = dir
	for k, s := range logSinks {
		_ = s.f.Close()
		delete(logSinks, k)
	}
	logLastDay = ""
	logFileMu.Unlock()
	t.Cleanup(func() {
		logFileMu.Lock()
		logDirOverride = ""
		for k, s := range logSinks {
			_ = s.f.Close()
			delete(logSinks, k)
		}
		logLastDay = ""
		logFileMu.Unlock()
	})
	return dir
}

// writeLogLine 直接走产品落盘路径写一行（src 决定分流到哪个文件）。
func writeLogLine(t *testing.T, src, line string) {
	t.Helper()
	writeLogFile(LogMsg{Scope: "test", Src: src, Line: line})
}

// svcLogPathFor 测试侧拼路径（与产品 svcLogPath 同一规则，单独一份防"测试自证"）。
func svcLogPathFor(dir, day, src string) string {
	if src == "" {
		return filepath.Join(dir, "envkit-"+day+".log")
	}
	return filepath.Join(dir, "envkit-"+day+"-"+src+".log")
}

// writeUntilRotate 反复写行直到发生一次轮转（当前文件被重置为 0 字节），
// 返回是否轮转成功。i 上限防死循环把测试挂死。
func writeUntilRotate(t *testing.T, src, line string, path string, maxIter int) bool {
	t.Helper()
	for i := 0; i < maxIter; i++ {
		writeLogLine(t, src, line)
		if fi, err := os.Stat(path); err == nil && fi.Size() == 0 {
			return true // 刚写过又归零 = 这一次写入触发了轮转
		}
	}
	return false
}

// ---------- 验收 1：超阈值轮转 ----------

func TestSvcLogRotatesOverThreshold(t *testing.T) {
	dir := withLogDir(t)
	day := time.Now().Format("20060102")
	path := svcLogPathFor(dir, day, "web")

	line := strings.Repeat("x", 64<<10) // 64KB 行，约 129 行过一个阈值
	if !writeUntilRotate(t, "web", line, path, 10000) {
		t.Fatalf("写入远超 %d 字节后仍未触发轮转", svcLogMaxSize)
	}
	// 归档确实是"超阈值的那一份"
	arc, err := os.Stat(path + ".1")
	if err != nil {
		t.Fatalf("轮转后没有归档 .1：%v", err)
	}
	if arc.Size() <= svcLogMaxSize {
		t.Errorf("归档大小 %d 未超阈值 %d——轮转时机不对", arc.Size(), svcLogMaxSize)
	}
	// 当前文件从零重新开始（轮转后新句柄）
	cur, err := os.Stat(path)
	if err != nil {
		t.Fatalf("轮转后当前文件不在：%v", err)
	}
	if cur.Size() != 0 {
		t.Errorf("轮转后当前文件未归零：%d 字节", cur.Size())
	}
	// 实际字节口径：份数=2，总字节=两者之和（logSinkDisk 与磁盘一致）
	files, total := logSinkDisk(dir, day, "web")
	if files != 2 {
		t.Errorf("份数 %d，期望 2（当前+1 归档）", files)
	}
	if want := arc.Size() + cur.Size(); total != want {
		t.Errorf("总字节 %d 与磁盘实际 %d 不一致", total, want)
	}
	// 分流隔离：写业务日志不应产生 EnvKit 自身文件
	if _, err := os.Stat(svcLogPathFor(dir, day, "")); err == nil {
		t.Error("业务日志混进了 EnvKit 自身文件")
	}
}

// ---------- 验收 2：归档份数上限 ----------

func TestSvcLogArchiveCap(t *testing.T) {
	dir := withLogDir(t)
	day := time.Now().Format("20060102")
	path := svcLogPathFor(dir, day, "web")

	line := strings.Repeat("y", 32<<10)
	rotations := 0
	for i := 0; i < 500000 && rotations < svcLogKeep+3; i++ {
		writeLogLine(t, "web", line)
		if fi, err := os.Stat(path); err == nil && fi.Size() == 0 {
			rotations++
		}
	}
	if rotations < svcLogKeep+3 {
		t.Fatalf("只发生 %d 次轮转，不足以验证份数上限（需 %d）", rotations, svcLogKeep+3)
	}
	// 份数封顶：当前 + keep 份归档，一份不多
	files, _ := logSinkDisk(dir, day, "web")
	if files != svcLogKeep+1 {
		t.Errorf("份数 %d，期望 %d（当前+%d 归档）——最老一份没被删或删多了", files, svcLogKeep+1, svcLogKeep)
	}
	// 每份归档都在"阈值+一行"量级（不存在漏转的巨无霸）
	for i := 1; i <= svcLogKeep; i++ {
		p := fmt.Sprintf("%s.%d", path, i)
		fi, err := os.Stat(p)
		if err != nil {
			t.Errorf("归档缺份：%s：%v", p, err)
			continue
		}
		if fi.Size() > svcLogMaxSize+int64(len(line))+1024 {
			t.Errorf("归档 %s 大小 %d 超预期（阈值 %d + 一行 %d）", p, fi.Size(), svcLogMaxSize, len(line))
		}
	}
}

// ---------- 验收 3：轮转后磁盘释放可复验（实际字节） ----------
//
// 做法：写到份数封顶后继续灌 3 个满轮次的数据，然后核对：
//  1. 份数始终不破顶（最老一份被真实删除，不是改名留下来）；
//  2. 磁盘实际总字节 ≤ (keep+1) 份容量上界——即 node1 960MB 事故的不变式；
//  3. 灌进去的总字节 > 该上界（证明上界真的在约束，不是刚好够用）。
func TestSvcLogDiskReleaseByActualBytes(t *testing.T) {
	dir := withLogDir(t)
	day := time.Now().Format("20060102")
	path := svcLogPathFor(dir, day, "web")

	line := strings.Repeat("z", 64<<10)
	lineBytes := int64(len(line) + 22) // 落盘行 = 日期(10)+空格+正文+ [src=web]+\n
	written := int64(0)
	maxFiles := 0
	for cycle := 0; cycle < svcLogKeep+5; cycle++ {
		if !writeUntilRotate(t, "web", line, path, 10000) {
			t.Fatalf("第 %d 个轮次未能触发轮转", cycle)
		}
		written += int64(svcLogMaxSize/lineBytes+2) * lineBytes // 这一轮大致灌入量
		if files, _ := logSinkDisk(dir, day, "web"); files > maxFiles {
			maxFiles = files
		}
	}
	if maxFiles > svcLogKeep+1 {
		t.Errorf("份数曾破顶：%d > %d", maxFiles, svcLogKeep+1)
	}
	files, total := logSinkDisk(dir, day, "web")
	bound := int64(svcLogKeep+1) * (svcLogMaxSize + logLineCap)
	if files != svcLogKeep+1 {
		t.Errorf("最终份数 %d，期望 %d", files, svcLogKeep+1)
	}
	if total > bound {
		t.Errorf("磁盘实际总字节 %d 超出容量上界 %d——单服务配额没兜住", total, bound)
	}
	if written <= bound {
		t.Errorf("灌入 %d 字节未超出容量上界 %d，该用例证明不了约束有效", written, bound)
	}
}

// ---------- src 口径隔离：复验不能张冠李戴 ----------

func TestLogSinkDiskScopeIsolation(t *testing.T) {
	dir := withLogDir(t)
	day := time.Now().Format("20060102")

	writeLogLine(t, "web", "hello-web")
	writeLogLine(t, "backend", "hello-backend")
	writeLogLine(t, "", "hello-self")

	_, wb0 := logSinkDisk(dir, day, "web")
	_, bb0 := logSinkDisk(dir, day, "backend")
	writeLogLine(t, "web", "more-web")
	_, wb1 := logSinkDisk(dir, day, "web")
	_, bb1 := logSinkDisk(dir, day, "backend")

	if wb1 <= wb0 {
		t.Error("web 口径没看到自己的写入")
	}
	if bb1 != bb0 {
		t.Error("web 的写入串进了 backend 口径——复验会张冠李戴")
	}
	for _, src := range []string{"web", "backend", ""} {
		if files, _ := logSinkDisk(dir, day, src); files != 1 {
			t.Errorf("src=%q 份数 %d，期望 1", src, files)
		}
	}
}

// ---------- EnvKit 自身日志：旧名与旧配额不变 ----------

func TestSelfLogKeepsLegacyNameAndQuota(t *testing.T) {
	dir := withLogDir(t)
	day := time.Now().Format("20060102")
	self := svcLogPathFor(dir, day, "")

	line := strings.Repeat("s", 64<<10)
	if !writeUntilRotate(t, "", line, self, 10000) {
		t.Fatalf("自身日志写入远超 %d 字节后仍未轮转", logMaxSize)
	}
	if _, err := os.Stat(self + ".1"); err != nil {
		t.Fatalf("自身日志归档不在：%v", err)
	}
	// 只轮转过一次：份数恰为 2（当前 + 1 归档）
	if files, _ := logSinkDisk(dir, day, ""); files != 2 {
		t.Errorf("自身日志份数 %d，期望 2", files)
	}
	// 业务文件没有被自身日志的写入带出来
	if _, err := os.Stat(svcLogPathFor(dir, day, "web")); err == nil {
		t.Error("自身日志的写入串进了业务分流文件")
	}
}

// ---------- parseLogDay：全部历史形态 + 分流中缀 ----------

func TestParseLogDay(t *testing.T) {
	cases := []struct {
		name string
		want string
		ok   bool
	}{
		{"envkit-20261018.log", "20261018", true},
		{"envkit-20261018.log.3", "20261018", true},
		{"envkit-20261018.log.10", "20261018", true},
		{"envkit-20261018.old", "20261018", true},
		{"envkit-20261018-web.log", "20261018", true},
		{"envkit-20261018-web.log.2", "20261018", true},
		{"envkit-20261018-backend.log.5", "20261018", true},
		{"envkit-2026101.log", "", false},   // 日期位数不对
		{"envkit-20261018x.log", "", false}, // 日期后直接跟字符（无分隔）
		{"envkit-foo.log", "", false},
		{"envkit-.log", "", false},
		{"other-20261018.log", "", false},
	}
	for _, c := range cases {
		got, ok := parseLogDay(c.name)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseLogDay(%q) = (%q,%v)，期望 (%q,%v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

// ---------- 30 天清理必须覆盖分流文件（v3.0 前只认旧名，分会话永远逃过清理） ----------

func TestCleanupOldLogsCoversPerSrc(t *testing.T) {
	dir := withLogDir(t)
	old := time.Now().AddDate(0, 0, -40).Format("20060102")
	today := time.Now().Format("20060102")
	mk := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644); err != nil {
			t.Fatalf("造文件 %s 失败：%v", name, err)
		}
	}
	stale := []string{
		"envkit-" + old + ".log",
		"envkit-" + old + ".log.3",
		"envkit-" + old + ".old",
		"envkit-" + old + "-web.log",
		"envkit-" + old + "-web.log.2",
		"envkit-" + old + "-backend.log.1",
	}
	for _, n := range stale {
		mk(n)
	}
	fresh := []string{
		"envkit-" + today + ".log",
		"envkit-" + today + "-web.log",
		"other.txt",
	}
	for _, n := range fresh {
		mk(n)
	}

	cleanupOldLogs()

	for _, n := range stale {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			t.Errorf("40 天前的旧日志未清理：%s", n)
		}
	}
	for _, n := range fresh {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("不该动的文件被清了：%s", n)
		}
	}
}
