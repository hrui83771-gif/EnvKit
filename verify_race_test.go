package main

// verify_race_test.go —— 复验路径读配置必须走锁。
//
// 2026-11-23 审查发现：verifyWaitBackend / svcExpectProcs 直接读全局
// cfg.Projects.BackendDir，而配置保存/热重载持 cfgMu 整体替换 cfg。
// data race 无法靠返回值断言观测（读到的旧值/新值都"合法"），
// 只能由 race detector 判——故本测试须在 go test -race 下运行才有意义。
// 变异验证：把任一处改回裸读 cfg.Projects.BackendDir，-race 必报 DATA RACE。

import (
	"sync"
	"testing"
)

func TestVerifyReadsConfigUnderRace(t *testing.T) {
	orig := cfgSnapshot().Projects.BackendDir
	t.Cleanup(func() {
		cfgUpdate(func(c *Config) { c.Projects.BackendDir = orig })
	})

	dirs := []string{`C:\work\go-svc`, `C:\work\java-svc`, ""}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 400; i++ {
			d := dirs[i%len(dirs)]
			cfgUpdate(func(c *Config) { c.Projects.BackendDir = d })
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 400; i++ {
			_ = verifyWaitBackend()
			_ = svcExpectProcs("backend")
		}
	}()
	wg.Wait()
}

// TestSvcExpectProcsReadsConfigUnderRace 单独钉住 svcExpectProcs：
// 与 verifyWaitBackend 分开跑，避免前者的 cfgSnapshot 取锁建立 happens-before
// 而掩盖这一处的裸读（实测两处必须分别验证，合并跑会假绿）。
func TestSvcExpectProcsReadsConfigUnderRace(t *testing.T) {
	orig := cfgSnapshot().Projects.BackendDir
	t.Cleanup(func() {
		cfgUpdate(func(c *Config) { c.Projects.BackendDir = orig })
	})

	dirs := []string{`C:\work\go-svc`, `C:\work\java-svc`, ""}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 800; i++ {
			d := dirs[i%len(dirs)]
			cfgUpdate(func(c *Config) { c.Projects.BackendDir = d })
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 800; i++ {
			_ = svcExpectProcs("backend")
		}
	}()
	wg.Wait()
}
