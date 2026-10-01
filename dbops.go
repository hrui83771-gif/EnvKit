package main

// MySQL 备份/还原 + 环境诊断报告。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------- MySQL 备份 / 还原 ----------

// prunedBackupsKeep 备份保留份数（超出后从最老的开始删除）。
const prunedBackupsKeep = 10

// backupDir 备份目录。
// 历史坑：曾经优先用 SQL 文件所在目录（= 项目源码目录），dump 会混进业务代码被 git 提交，
// 且失败残留的 0 字节文件一直堆着。现在默认放在 exe 同级的 backups/ 独立目录，
// 用户可通过 projects.backup_dir 显式指定（例如挂到别的盘/网络位置）。
func backupDir() string {
	if p := strings.TrimSpace(cfg.Projects.BackupDir); p != "" {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "backups")
	}
	return "backups"
}

func mysqlDumpExe() string { return findExe("mysqldump.exe", "mysqldump") }

// pruneBackups 保留最近 keep 份备份（按文件名中的时间戳字典序），其余连同校验和一起删除。
func pruneBackups(dir, dbName string, keep int) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	prefix := dbName + "-"
	suffix := ".sql"
	var olds []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, prefix) || !strings.HasSuffix(n, suffix) {
			continue
		}
		olds = append(olds, n)
	}
	if len(olds) <= keep {
		return 0
	}
	sort.Strings(olds) // 文件名含 20060102-150405，字典序即时间序
	drop := olds[:len(olds)-keep]
	removed := 0
	for _, n := range drop {
		if os.Remove(filepath.Join(dir, n)) == nil {
			removed++
		}
		_ = os.Remove(filepath.Join(dir, n+".sha256"))
	}
	return removed
}

// writeChecksum 为备份文件生成伴随校验和，让"备份成功"可被事后验证。
func writeChecksum(dest string) {
	sum, err := fileSHA256(dest)
	if err != nil {
		warn(scConfig, "备份", "生成校验和失败：%v", err)
		return
	}
	_ = os.WriteFile(dest+".sha256", []byte(fmt.Sprintf("%s  %s\n", sum, filepath.Base(dest))), 0644)
}

func handleDBBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if err := dbBackupTask(actUser); err != nil {
		auditNow(actUser, "db_backup", cfg.Projects.DBName, "", resDenied, err.Error())
		http.Error(w, err.Error(), 409)
		return
	}
	_, _ = w.Write([]byte(`{"started":true}`))
}

// needConfirm 返回需要二次确认的响应（428）：破坏性操作必须由前端先弹窗。
// highrisk=true 时前端用红色警示样式（可选参数，缺省 false，避免确认疲劳：只有真高危才升级视觉烈度）。
func needConfirm(w http.ResponseWriter, action, target, reason string, highrisk ...bool) {
	hr := false
	if len(highrisk) > 0 {
		hr = highrisk[0]
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(428)
	_, _ = w.Write([]byte(mustJSON(map[string]any{
		"needConfirm": true,
		"action":      action,
		"target":      target,
		"reason":      reason,
		"highrisk":    hr,
	})))
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// dbBackupTask 备份数据库（供按钮与 AI 工具共用）；同步等待完成。
// actor 区分操作主体（user / ai），写入审计流。
func dbBackupTask(actor string) error {
	if actor == "" {
		actor = actUser
	}
	h, granted := beginTaskH("MySQL 备份", true)
	if !granted {
		return fmt.Errorf("有任务正在执行，请稍候")
	}
	done := make(chan struct{})
	go func() {
		defer h.Done()
		defer close(done)
		doBackup(actor, h)
	}()
	<-done
	return nil
}

// doBackup 备份执行体（不含任务锁，供 apply-sql 这类已持锁的调用方复用）。
// 返回备份文件路径；失败时返回空串并已清理残留文件。
func doBackup(actor string, h *TaskHandle) string {
	fin := auditStart(actor, "db_backup", cfg.Projects.DBName, "")
	p := cfg.Projects
	exe := mysqlDumpExe()
	if exe == "" {
		fail(scConfig, "备份", "未找到 mysqldump（需 MySQL 组件或系统 PATH）")
		fin(resFail, "未找到 mysqldump")
		return ""
	}
	dir := backupDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		fail(scConfig, "备份", "创建备份目录失败：%v", err)
		fin(resFail, "创建备份目录失败")
		return ""
	}
	// 超时保护：注册当前命令，看门狗触发时连同进程树一起终止
	var c2 *exec.Cmd
	h.SetKill(func() {
		if c2 != nil {
			killTree(c2)
		}
	})
	dest := filepath.Join(dir, fmt.Sprintf("%s-%s.sql", p.DBName, time.Now().Format("20060102-150405")))
	info(scConfig, "备份", "mysqldump %s -> %s（客户端 %s）", p.DBName, dest, exe)
	out, err := os.Create(dest)
	if err != nil {
		fail(scConfig, "备份", "创建备份文件失败：%v", err)
		fin(resFail, "创建备份文件失败")
		return ""
	}
	args := []string{
		"-h", fmt.Sprint(p.MySQLHost), "-P", fmt.Sprint(p.MySQLPort), "-u", p.MySQLUser,
		"--default-character-set=utf8mb4", "--single-transaction", "--routines", "--triggers", p.DBName,
	}
	var lastErrText string
	attemptIdx := 0
	// 空输出 + exit 非零：大概率被杀软行为拦截（无签名 exe 派生子进程读库写文件）。
	// 备份产物已经写进文件，重试前必须把文件截断（否则第二次的内容会追加在损坏数据后面）。
	// 这里 AnyErr=true：备份是"要么完整要么失败"的操作，任何错误都值得重试一次。
	_, err = runRetry(retryPolicy{
		Attempts: 2,
		Backoff:  2 * time.Second,
		Scope:    scConfig,
		Tag:      "备份",
		Label:    "mysqldump",
		AnyErr:   true,
	}, func(wrap bool) (bool, error) {
		if attemptIdx > 0 {
			if _, seekErr := out.Seek(0, 0); seekErr == nil {
				_ = out.Truncate(0)
			}
		}
		attemptIdx++
		ctxB, cancelB := cmdContext(10 * time.Minute)
		defer cancelB()
		c2 = buildCmd(ctxB, wrap, exe, args...)
		c2.Env = append(toolEnv(mysqlEnv()), "MYSQL_PWD="+p.MySQLPass)
		var stderr, stdout strings.Builder
		c2.Stderr = &stderr
		c2.Stdout = io.MultiWriter(out, &stdout)
		e := c2.Run()
		lastErrText = strings.TrimSpace(stderr.String() + " " + stdout.String())
		return lastErrText != "", e
	})
	out.Close()
	if err != nil {
		hint := ""
		if strings.TrimSpace(lastErrText) == "" {
			hint = "；子进程无任何输出即退出，疑似被杀毒软件拦截——可点击顶部「杀软白名单」一键加入 Defender 排除项"
		}
		fail(scConfig, "备份", "mysqldump 失败（已自动重试 %d 次）：%v：%s%s", attemptIdx-1, err, firstLines(lastErrText, 400), hint)
		// 失败产物必须清掉：留着 0 字节/半截文件会让人误以为"有备份"
		_ = os.Remove(dest)
		_ = os.Remove(dest + ".sha256")
		fin(resFail, firstLines(lastErrText, 200))
		return ""
	}
	if fi, err := os.Stat(dest); err != nil || fi.Size() == 0 {
		fail(scConfig, "备份", "备份文件为空：%s", firstLines(lastErrText, 300))
		_ = os.Remove(dest)
		_ = os.Remove(dest + ".sha256")
		fin(resFail, "备份文件为空")
		return ""
	}
	// 成功：写校验和 + 按保留策略清理旧备份
	writeChecksum(dest)
	if n := pruneBackups(dir, p.DBName, prunedBackupsKeep); n > 0 {
		info(scConfig, "备份", "已清理 %d 份超出保留上限（%d 份）的旧备份", n, prunedBackupsKeep)
	}
	ok(scConfig, "备份", "备份完成：%s（%.1f KB）", dest, float64(mustFileSize(dest))/1024)
	fin(resOK, dest)
	return dest
}

// mustFileSize 取文件大小，取不到返回 0（仅用于日志展示）。
func mustFileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// ---------- 恢复演练 ----------
// 备份成功 ≠ 备份可恢复。演练流程：备份 → 恢复到临时库（__drill_ 后缀，不碰业务库）
// → 对比表数 → 删临时库。演练用的 dump 本身就是一份真实备份，保留不删。

// countTables 查询某库的表数量（information_schema，不依赖该库可执行查询权限以外的权限）。
func countTables(db string) (int, error) {
	base := []string{"-h", fmt.Sprint(cfg.Projects.MySQLHost), "-P", fmt.Sprint(cfg.Projects.MySQLPort), "-u", cfg.Projects.MySQLUser, "-N", "-B"}
	q := fmt.Sprintf("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='%s';", db)
	out, err := runMysqlOut(append(base, "-e", q)...)
	if err != nil {
		return 0, err
	}
	n, convErr := strconv.Atoi(strings.TrimSpace(out))
	if convErr != nil {
		return 0, fmt.Errorf("表数查询结果异常：%s", firstLines(out, 100))
	}
	return n, nil
}

// dropDatabaseQuiet 安静地删库（演练临时库专用）；失败只记 warn。
func dropDatabaseQuiet(db string) {
	if db == "" {
		return
	}
	base := []string{"-h", fmt.Sprint(cfg.Projects.MySQLHost), "-P", fmt.Sprint(cfg.Projects.MySQLPort), "-u", cfg.Projects.MySQLUser}
	sql := "DROP DATABASE IF EXISTS `" + db + "`;"
	if _, err := runMysqlOut(append(base, "-e", sql)...); err != nil {
		warn(scConfig, "演练", "删除临时库 %s 失败（不影响业务库，可手动清理）：%v", db, err)
	}
}

// dbDrillTask 恢复演练执行体（供按钮与 AI 工具共用）；同步等待完成。
func dbDrillTask(actor string) error {
	if actor == "" {
		actor = actUser
	}
	h, granted := beginTaskH("恢复演练", true)
	if !granted {
		return fmt.Errorf("有任务正在执行，请稍候")
	}
	done := make(chan struct{})
	go func() {
		defer h.Done()
		defer close(done)
		fin := auditStart(actor, "db_drill", cfg.Projects.DBName, "")
		p := cfg.Projects
		if p.DBName == "" {
			fail(scConfig, "演练", "数据库名未配置，请先在「程序配置」里填好")
			fin(resFail, "数据库名未配置")
			return
		}
		// 第 1 步：备份（复用 doBackup —— 产物就是一份正常备份，演练通过后继续服役）
		info(scConfig, "演练", "第 1/4 步：生成新备份")
		bp := doBackup(actor, h)
		if bp == "" {
			fail(scConfig, "演练", "备份失败，演练中止（未触碰业务库）")
			fin(resFail, "备份失败")
			return
		}
		// 第 2 步：建临时库
		info(scConfig, "演练", "第 2/4 步：创建临时库")
		base := p.DBName
		if len(base) > 40 {
			base = base[:40] // MySQL 库名上限 64 字符，给后缀留余地
		}
		drillDB := base + "__drill_" + time.Now().Format("0102150405")
		if err := createDatabaseNamed(drillDB); err != nil {
			fail(scConfig, "演练", "创建临时库 %s 失败：%v", drillDB, err)
			fin(resFail, "创建临时库失败")
			return
		}
		// 看门狗触发/中途失败时都要清掉临时库
		h.SetKill(func() { dropDatabaseQuiet(drillDB) })
		cleanup := func() { dropDatabaseQuiet(drillDB); h.SetKill(func() {}) }
		// 第 3 步：把备份灌进临时库
		info(scConfig, "演练", "第 3/4 步：备份恢复到临时库 %s", drillDB)
		mcl := mysqlClient()
		if mcl == "" {
			fail(scConfig, "演练", "未找到 mysql 客户端")
			cleanup()
			fin(resFail, "未找到 mysql 客户端")
			return
		}
		sqlf, err := os.Open(bp)
		if err != nil {
			fail(scConfig, "演练", "打开备份文件失败：%v", err)
			cleanup()
			fin(resFail, "打开备份文件失败")
			return
		}
		var rc *exec.Cmd
		h.SetKill(func() {
			if rc != nil {
				killTree(rc)
			}
			dropDatabaseQuiet(drillDB)
		})
		args := []string{"-h", fmt.Sprint(p.MySQLHost), "-P", fmt.Sprint(p.MySQLPort), "-u", p.MySQLUser, drillDB}
		ctxD, cancelD := cmdContext(10 * time.Minute)
		rc = exec.CommandContext(ctxD, mcl, args...)
		rc.Env = append(toolEnv(mysqlEnv()), "MYSQL_PWD="+p.MySQLPass)
		rc.Stdin = sqlf
		var eb strings.Builder
		rc.Stderr = &eb
		rErr := rc.Run()
		cancelD()
		sqlf.Close()
		if rErr != nil {
			fail(scConfig, "演练", "恢复到临时库失败：%v：%s", rErr, firstLines(eb.String(), 400))
			cleanup()
			fin(resFail, "恢复失败")
			return
		}
		// 第 4 步：对比表数 → 清理 → 结论
		srcN, errS := countTables(p.DBName)
		drillN, errD := countTables(drillDB)
		cleanup()
		switch {
		case errS != nil || errD != nil:
			fail(scConfig, "演练", "表数对比失败（源库：%v；临时库：%v）——恢复流程本身已走通，但无法自动判定完整性", errS, errD)
			fin(resFail, "表数对比失败")
		case drillN < srcN:
			fail(scConfig, "演练", "演练未通过：源库 %d 张表，恢复后只有 %d 张。请检查备份是否完整", srcN, drillN)
			fin(resFail, fmt.Sprintf("表数不一致 src=%d drill=%d", srcN, drillN))
		case srcN == 0:
			ok(scConfig, "演练", "演练通过（源库为空库，验证了备份→恢复→清理全流程）：临时库已删除，备份保留在 %s", bp)
			fin(resOK, bp)
		default:
			ok(scConfig, "演练", "演练通过：备份 %s 可恢复（表数 %d/%d 一致），临时库已删除", bp, drillN, srcN)
			fin(resOK, bp)
		}
	}()
	<-done
	return nil
}

func handleDBDrill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if err := dbDrillTask(actUser); err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	_, _ = w.Write([]byte(`{"started":true}`))
}

func handleDBRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		File    string `json:"file"`
		Confirm bool   `json:"confirm"`
		Lang    string `json:"lang"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	file := strings.TrimSpace(body.File)
	if file == "" {
		http.Error(w, "请先选择要还原的 .sql 文件", 400)
		return
	}
	fi, err := os.Stat(file)
	if err != nil {
		http.Error(w, "文件不存在："+file, 400)
		return
	}
	// 二次确认：还原会覆盖目标库现有数据，属于不可逆操作
	if !body.Confirm {
		auditNow(actUser, "db_restore", cfg.Projects.DBName, file, resDenied, "等待二次确认")
		needConfirm(w, "db_restore", cfg.Projects.DBName, confirmReason(body.Lang,
			fmt.Sprintf("即将用 %s（%.1f KB）覆盖库 %s 的现有数据，此操作不可撤销。建议先点「备份数据库」。",
				filepath.Base(file), float64(fi.Size())/1024, cfg.Projects.DBName),
			fmt.Sprintf("About to overwrite database %s with %s (%.1f KB). This cannot be undone. "+
				"It is recommended to back up the database first.",
				cfg.Projects.DBName, filepath.Base(file), float64(fi.Size())/1024)), true)
		return
	}
	h, ok2 := beginTaskH("MySQL 还原", true)
	if !ok2 {
		http.Error(w, "有任务正在执行，请稍候", 409)
		return
	}
	go func() {
		defer h.Done()
		fin := auditStart(actUser, "db_restore", cfg.Projects.DBName, file)
		p := cfg.Projects
		exe := mysqlClient()
		if exe == "" {
			fail(scConfig, "还原", "未找到 mysql 客户端")
			fin(resFail, "未找到 mysql 客户端")
			return
		}
		info(scConfig, "还原", "%s -> 库 %s（%.1f KB，客户端 %s）", file, p.DBName, float64(fi.Size())/1024, exe)
		sqlf, err := os.Open(file)
		if err != nil {
			fail(scConfig, "还原", "打开文件失败：%v", err)
			fin(resFail, "打开文件失败")
			return
		}
		defer sqlf.Close()
		args := []string{"-h", fmt.Sprint(p.MySQLHost), "-P", fmt.Sprint(p.MySQLPort), "-u", p.MySQLUser, p.DBName}
		ctxR, cancelR := cmdContext(10 * time.Minute)
		cmd := exec.CommandContext(ctxR, exe, args...)
		h.SetKill(func() { killTree(cmd) })
		cmd.Env = append(toolEnv(mysqlEnv()), "MYSQL_PWD="+p.MySQLPass)
		cmd.Stdin = sqlf
		var stderr, stdout strings.Builder
		cmd.Stderr = &stderr
		cmd.Stdout = &stdout
		runErr := cmd.Run()
		cancelR()
		if err := runErr; err != nil {
			hint := ""
			if strings.TrimSpace(stderr.String()) == "" && strings.TrimSpace(stdout.String()) == "" {
				hint = "；子进程无任何输出即退出，疑似被杀毒软件拦截——可点击顶部「杀软白名单」一键加入"
			}
			fail(scConfig, "还原", "还原失败（注意：部分数据可能已写入）：%v：stderr=%s stdout=%s%s", err, firstLines(stderr.String(), 300), firstLines(stdout.String(), 300), hint)
			fin(resFail, firstLines(stderr.String(), 200))
			return
		}
		ok(scConfig, "还原", "还原完成：%s -> %s", file, p.DBName)
		fin(resOK, file)
	}()
	_, _ = w.Write([]byte(`{"started":true}`))
}

// ---------- 环境诊断报告 ----------

func buildDiagReport() string {
	var b strings.Builder
	w1 := func(f string, a ...interface{}) { fmt.Fprintf(&b, f+"\n", a...) }

	w1("# EnvKit 诊断报告")
	w1("")
	w1("- 生成时间：%s", time.Now().Format("2006-01-02 15:04:05"))
	w1("- 版本：%s（%s）", appVersion, buildDate)
	w1("- 系统：%s/%s · 管理员：%v", runtime.GOOS, runtime.GOARCH, isAdminFlag)
	w1("")

	w1("## 组件环境")
	w1("")
	w1("| 组件 | 状态 | 位置 |")
	w1("|---|---|---|")
	resultsMu.Lock()
	rs := make([]CheckResult, 0, len(results))
	for _, v := range results {
		rs = append(rs, v)
	}
	resultsMu.Unlock()
	if len(rs) == 0 {
		w1("| - | 未检测 | - |")
	}
	for _, r2 := range rs {
		loc := r2.Location
		if loc == "" {
			loc = r2.Detail
		}
		w1("| %s | %s %s | %s |", r2.Name, map[bool]string{true: "已安装", false: "未安装"}[r2.Installed], r2.Version, loc)
	}
	w1("")

	w1("## 数据库")
	w1("")
	resultsMu.Lock()
	h := dbHealth
	resultsMu.Unlock()
	w1("- 状态：%s", h.Msg)
	w1("- 目标库：%s（host=%s:%d user=%s）", cfg.Projects.DBName, cfg.Projects.MySQLHost, cfg.Projects.MySQLPort, cfg.Projects.MySQLUser)
	w1("- 前端目录：%s", cfg.Projects.FrontendDir)
	w1("- 后端目录：%s", cfg.Projects.BackendDir)
	w1("- SQL 文件：%s", cfg.Projects.SQLFile)
	w1("")

	w1("## 链端")
	w1("")
	chainMu.Lock()
	ci := chainInfo
	chainMu.Unlock()
	if ci.Checked {
		w1("- 检测时间：%s", ci.At)
		w1("- 节点存活：%v（公网可达 %v，进程数 %d）", ci.Port20200, ci.ChainExtReach, ci.NodeProcs)
		w1("- WeBASE-Front：%v（%s）", ci.Port5002, ci.WebaseURL)
		w1("- 区块高度 %s · 交易数 %s · 客户端 %s", ci.BlockNumber, ci.TxCount, ci.ClientVer)
		lr, rc := recoverStats()
		if rc > 0 {
			w1("- 自动恢复：共 %d 次，最近 %s", rc, lr)
		}
	} else {
		w1("- 未检测")
	}
	w1("")

	w1("## 最近日志（200 行）")
	w1("")
	w1("```")
	hub.mu.Lock()
	hist := append([]LogMsg{}, hub.history...)
	hub.mu.Unlock()
	if len(hist) > 200 {
		hist = hist[len(hist)-200:]
	}
	for _, m := range hist {
		if m.Src != "" {
			fmt.Fprintf(&b, "[%s/%s] %s\n", m.Scope, m.Src, m.Line)
		} else {
			fmt.Fprintf(&b, "[%s] %s\n", m.Scope, m.Line)
		}
	}
	w1("```")
	w1("")
	w1("> 本报告不含任何密码；SSH/数据库凭据已在内部脱敏。")

	return b.String()
}

func handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	text := buildDiagReport()
	name := "envkit-report-" + time.Now().Format("20060102-150405") + ".md"
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+name)
	_, _ = w.Write([]byte(text))
}
