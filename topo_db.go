// topo_db.go —— DB 适配器（v3.0 第二阶段 T2.4）：MySQL
//
// 收编的现有能力（plan §4.1 的 DB 适配器条目）：
//
//	Observe ← dbTest（mysql.go）
//	Verify  ← verifyBackupStatic / latestBackup（restore_drill.go / verify.go）
//	Execute ← dbBackupTask（dbops.go，自带组锁 + 就地复验）
//
// 与 LocalProc 适配器同一条原则：**不重写采集与复验逻辑**。
// 尤其是复验——verifyBackupStatic 是 v3.0.1 判据补口（备份 target）
// 的落点，另写一套就会有两个口径，而判据只认其中一个。
package main

import (
	"fmt"
	"strings"
	"time"
)

// DBAdapter 数据库适配器。无状态。
type DBAdapter struct{}

// Observe 采集数据库健康事实。
//
// 两种不健康必须分开：连不上（服务没起/网络/凭据）与 连上了但库不存在。
// 前者才该触发恢复，后者是配置或初始化问题——对它发起"重启数据库"
// 治不了病，还会把一个配置错误升级成一次服务重启。
func (DBAdapter) Observe(svc Service) Observation {
	obs := Observation{Service: svc.ID, At: time.Now()}
	h, err := dbTest()
	if err != nil {
		obs.ErrKind = "not_listening"
		obs.Evidence = []string{"err=" + firstLine(strings.TrimSpace(err.Error()))}
		return obs
	}
	obs.Evidence = []string{"connected=true", "version=" + h.Version, "db_exists=" + fmt.Sprint(h.DBExists)}
	if !h.DBExists {
		obs.ErrKind = "db_missing"
		obs.Evidence = append(obs.Evidence,
			fmt.Sprintf("库 %s 不存在（version=%s）", cfg.Projects.DBName, h.Version))
		return obs
	}
	obs.Healthy = true
	return obs
}

// Plan 规划数据库类动作。
//
// 支持 backup（刷新备份，low 档可自动）。
// restart（db_restart 策略，high 档）**在本适配器里只规划不承诺执行**——
// 执行前必须过人闸与策略前置检查，那是 T2.6 事务层的事；
// 适配器能做的是把"这一步需要人"如实写进计划，而不是偷偷做掉。
func (DBAdapter) Plan(svc Service, a Action) (Plan, error) {
	kind := strings.TrimSpace(a.Kind)
	p := Plan{Service: svc.ID, Action: kind, RiskTier: svc.RiskTier}
	switch kind {
	case "backup":
		p.Command = fmt.Sprintf("mysqldump %s -> %s", cfg.Projects.DBName, backupDir())
		p.Precheck = []string{PrecheckGroupLock, PrecheckBudget}
		return p, nil
	case "restart":
		if svc.RiskTier != RiskHigh {
			return Plan{}, fmt.Errorf("restart 动作只允许 high 档服务，%s 是 %s 档",
				svc.ID, svc.RiskTier)
		}
		p.Command = fmt.Sprintf("重启 MySQL 服务（库 %s）", cfg.Projects.DBName)
		p.Precheck = []string{PrecheckGroupLock, PrecheckBudget, PrecheckBreaker, PrecheckAttended}
		return p, nil
	case "":
		return Plan{}, fmt.Errorf("未指定动作（支持 backup/restart）")
	}
	return Plan{}, fmt.Errorf("DB 适配器不支持的动作 %q", kind)
}

// Execute 执行数据库类动作。
//
// **high 档的 restart 在这里直接拒绝**：适配器不知道有没有人在看。
// "自动执行高风险动作"这件事只能由事务层在人闸通过后放行——
// 适配器自己放行等于把闸门绕过去了（R2）。
func (DBAdapter) Execute(p Plan) OpResult {
	switch p.Action {
	case "backup":
		if p.RiskTier == RiskHigh {
			return opFail("db_backup", p.Service, errKindBadConfig,
				"备份动作不允许标记为 high 档")
		}
		// dbBackupTask 自带组锁 + 就地复验，返回三态结论
		return dbBackupTask(actGuard)
	case "restart":
		return opFail("db_restart", p.Service, errKindBadConfig,
			"数据库重启是 high 档动作，必须经人闸与策略前置检查后由事务层执行；"+
				"适配器不自行执行（R2：不绕过权限闸门）")
	}
	return opFail(p.Action, p.Service, errKindBadConfig, "DB 适配器不支持的动作 "+p.Action)
}

// Verify 复验。
//
// 备份类：找目录里最新的一份 .sql，走 verifyBackupStatic（sha256 + 内容完整性）。
// **没有备份文件时不算"复验失败"**，而是"没有可复验的产物"——
// 这两件事的后续动作完全不同：前者要重新备份，后者要先做一次备份。
// 混在一起会让恢复策略对空目录反复发起备份。
func (DBAdapter) Verify(svc Service, spec VerifySpec) OpResult {
	return verifyBackupPathOrNoArtifact(cfg.Projects.DBName)
}

// verifyBackupPathOrNoArtifact 复验指定库的最新备份；没有产物时返回
// Ok&&!Verified 的诚实态（no_artifact），**不返回失败态**。
// 抽成纯函数是为了让单测能用"必然不存在的库名"覆盖这条分支，
// 而不必真的去动开发机上的备份目录。
func verifyBackupPathOrNoArtifact(dbName string) OpResult {
	path := latestBackup(dbName)
	if path == "" {
		return OpResult{
			Ok: true, Verified: false,
			Action: "verify_backup", Target: dbName,
			Msg:     "备份目录里没有可复验的 .sql 产物（尚未备份过，或目录被清空）",
			ErrKind: "no_artifact",
		}
	}
	return verifyBackupStatic(path)
}
