package main

// lesson_v1_test.go —— 经验选择的 v1 回归：没命中就不再硬塞。

import (
	"testing"
)

func TestLessonsForTaskNoArbitraryFallback(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		ev("2026-10-03 10:00:00.000", actAI, "start_service", "web", resFail),
		ev("2026-10-03 10:01:00.000", actAI, "start_service", "web", resFail),
		ev("2026-10-03 10:02:00.000", actAI, "start_service", "web", resFail),
	})()

	if got := lessonsForTask("随便什么任务", 5); len(got) != 0 {
		t.Fatalf("任务不相关时不该硬塞经验，实际 %d 条：%+v", len(got), got)
	}
	// 空任务（recall_lessons 不带 task）仍允许按命中次数取前 n 条
	if got := lessonsForTask("", 5); len(got) == 0 {
		t.Fatal("空任务时应返回可用的经验")
	}
}

func TestLessonsForTaskMatchesActionName(t *testing.T) {
	defer withLessonEnv(t, []AuditEntry{
		ev("2026-10-03 10:00:00.000", actAI, "start_service", "web", resFail),
		ev("2026-10-03 10:01:00.000", actAI, "start_service", "web", resFail),
		ev("2026-10-03 10:02:00.000", actAI, "start_service", "web", resFail),
	})()

	got := lessonsForTask("用 start_service 起前端", 5)
	if len(got) != 1 {
		t.Fatalf("命中动作名时应返回该条，实际 %d 条", len(got))
	}
}
