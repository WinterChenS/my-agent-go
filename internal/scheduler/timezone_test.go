package scheduler

// C09：cron / IANA 时区校验与 DST 两个方向上的"只触发一次"。
//
// 这一组测试刻意只用**运行时的 IANA 时区数据库**（time.LoadLocation +
// 本包内嵌的 time/tzdata），不写死任何偏移量：写死 -5/-4 的测试在
// "哪天某国改了夏令时规则"之后仍然会绿，而生产会错。

import (
	"context"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
)

// validSchedule 返回一份字段完整、可被接受的任务定义。
func validSchedule() Schedule {
	return Schedule{
		ID:            "sch-daily",
		Name:          "每日待办整理",
		Cron:          "0 9 * * *",
		TimeZone:      "America/New_York",
		WorkspaceID:   "ws-1",
		Prompt:        "整理今日待办",
		PermissionRef: "ceiling:workspace",
		Enabled:       true,
	}
}

// TestRejectsInvalidCronAndTimezone 覆盖 SCH01 的输入面：坏 cron、6 段 cron、
// Local、未知 IANA 名、非 IANA 缩写都必须给出**结构化**错误码，
// 而不是"悄悄当成每分钟跑一次"。
func TestRejectsInvalidCronAndTimezone(t *testing.T) {
	h := newHarness(t, time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC))

	cases := []struct {
		name     string
		mutate   func(*Schedule)
		wantCode string
	}{
		{"空 cron", func(s *Schedule) { s.Cron = "" }, contracts.CodeConfigMissing},
		{"四段 cron", func(s *Schedule) { s.Cron = "0 9 * *" }, contracts.CodeUnsupported},
		{"六段 cron（带秒，不支持）", func(s *Schedule) { s.Cron = "0 0 9 * * *" }, contracts.CodeUnsupported},
		{"分钟越界", func(s *Schedule) { s.Cron = "60 * * * *" }, contracts.CodeUnsupported},
		{"小时越界", func(s *Schedule) { s.Cron = "0 24 * * *" }, contracts.CodeUnsupported},
		{"日期为 0", func(s *Schedule) { s.Cron = "0 9 0 * *" }, contracts.CodeUnsupported},
		{"步长为 0", func(s *Schedule) { s.Cron = "*/0 * * * *" }, contracts.CodeUnsupported},
		{"非数字字段", func(s *Schedule) { s.Cron = "abc * * * *" }, contracts.CodeUnsupported},
		{"未知月份名", func(s *Schedule) { s.Cron = "0 9 * FOO *" }, contracts.CodeUnsupported},
		{"描述符", func(s *Schedule) { s.Cron = "@daily" }, contracts.CodeUnsupported},
		{"空时区", func(s *Schedule) { s.TimeZone = "" }, contracts.CodeConfigMissing},
		{"Local 不是 IANA 名", func(s *Schedule) { s.TimeZone = "Local" }, contracts.CodeUnsupported},
		{"非 IANA 缩写", func(s *Schedule) { s.TimeZone = "EST" }, contracts.CodeUnsupported},
		{"未知 IANA 名", func(s *Schedule) { s.TimeZone = "Mars/Phobos" }, contracts.CodeUnsupported},
		{"空工作区", func(s *Schedule) { s.WorkspaceID = "" }, contracts.CodeConfigMissing},
		{"空提示词", func(s *Schedule) { s.Prompt = "" }, contracts.CodeConfigMissing},
		{"未知漏跑策略", func(s *Schedule) { s.MisfirePolicy = MisfirePolicy("run_all") }, contracts.CodeUnsupported},
		{"负补跑上限", func(s *Schedule) { s.CatchUpLimit = -1 }, contracts.CodeUnsupported},
		{"补跑上限过大", func(s *Schedule) { s.CatchUpLimit = MaxCatchUpLimit + 1 }, contracts.CodeUnsupported},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sch := validSchedule()
			tc.mutate(&sch)

			if err := sch.validate(); err == nil {
				t.Fatalf("校验必须拒绝：%+v", sch)
			} else {
				assertCode(t, err, tc.wantCode)
			}

			// 跨边界（API/服务）给的必须是同一个结构化码，不能退化成字符串。
			if _, err := h.svc.Create(context.Background(), sch); err == nil {
				t.Fatalf("Create 必须拒绝：%+v", sch)
			} else {
				assertCode(t, err, tc.wantCode)
			}
		})
	}

	// 合法名字必须被接受，否则上面那组"拒绝"可能只是拒绝了一切。
	ok := validSchedule()
	ok.Cron = "0 9 * * MON-FRI"
	ok.TimeZone = "Asia/Shanghai"
	if err := ok.validate(); err != nil {
		t.Fatalf("合法的 cron/时区不应被拒绝: %v", err)
	}
}

// TestCronNextAcrossWallClock 钉住 cron 的下一跳语义（分钟粒度、名称、范围/步长）。
func TestCronNextAcrossWallClock(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("加载 IANA 时区失败（tzdata 必须来自运行时）: %v", err)
	}

	cases := []struct {
		expr  string
		after time.Time
		want  time.Time
	}{
		// 普通推进：下一个 09:00 本地时间。
		{"0 9 * * *", time.Date(2025, 6, 2, 12, 0, 0, 0, ny), time.Date(2025, 6, 3, 9, 0, 0, 0, ny)},
		// 名称与范围等价。
		{"0 9 * * MON-FRI", time.Date(2025, 6, 6, 12, 0, 0, 0, ny), time.Date(2025, 6, 9, 9, 0, 0, 0, ny)},
		{"*/15 * * * *", time.Date(2025, 6, 2, 12, 7, 0, 0, ny), time.Date(2025, 6, 2, 12, 15, 0, 0, ny)},
		{"0 0 1 * *", time.Date(2025, 6, 2, 12, 0, 0, 0, ny), time.Date(2025, 7, 1, 0, 0, 0, 0, ny)},
	}
	for _, tc := range cases {
		spec, perr := parseCron(tc.expr)
		if perr != nil {
			t.Fatalf("解析 cron %q 失败: %v", tc.expr, perr)
		}
		got, nerr := spec.nextAfter(tc.after, ny)
		if nerr != nil {
			t.Fatalf("%q 的下一跳失败: %v", tc.expr, nerr)
		}
		if !got.Equal(tc.want) {
			t.Fatalf("%q：下一跳 = %s，期望 %s", tc.expr, got.UTC().Format(time.RFC3339), tc.want.UTC().Format(time.RFC3339))
		}
	}
}

// TestDSTSpringForwardAndFallBackFireOnce 覆盖 SCH01 的 DST 两个方向。
//
// 春跳（2025-03-09）：America/New_York 的 02:00 不存在。规则是"缺口结束后的
// 第一个瞬时"（03:00 EDT），**不是**"补一次 02:00 再补一次 03:00"。
// 秋回（2025-11-02）：01:00 出现两次。规则是"取较早的那个瞬时"（EDT），
// 后面那次 EST 不产生第二条 occurrence。
//
// 两个方向都同时断言：落库的 occurrence 条数（unique 键的最终效果）。
func TestDSTSpringForwardAndFallBackFireOnce(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("加载 IANA 时区失败: %v", err)
	}

	t.Run("春跳 02:00 不存在", func(t *testing.T) {
		h := newHarness(t, time.Date(2025, 3, 8, 12, 0, 0, 0, time.UTC))
		sch := validSchedule()
		sch.ID = "sch-spring"
		sch.Cron = "0 2 * * *"
		created, cerr := h.svc.Create(context.Background(), sch)
		if cerr != nil {
			t.Fatalf("创建任务失败: %v", cerr)
		}
		// 2025-03-09 02:00 本地不存在 → 缺口结束后第一个瞬时 03:00 EDT = 07:00Z。
		want := time.Date(2025, 3, 9, 7, 0, 0, 0, time.UTC)
		if !created.NextScheduledAt.Equal(want) {
			t.Fatalf("春跳日的下一跳 = %s，期望 %s（缺口结束后的第一个瞬时）",
				created.NextScheduledAt.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
		}

		// 缺口前后各推一步：只能在 07:00Z 触发一次，不能"补一次缺口再补一次 03:00"。
		h.clock.Set(want.Add(-1 * time.Minute))
		h.tick()
		h.clock.Set(want)
		h.tick()
		h.tick()
		if got := len(h.occurrences(created.ID)); got != 1 {
			t.Fatalf("春跳日应只落 1 条 occurrence，实际 %d 条", got)
		}

		// 次日 02:00 EDT = 06:00Z 正常触发。
		h.advance(24 * time.Hour)
		h.tick()
		occs := h.occurrences(created.ID)
		if len(occs) != 2 {
			t.Fatalf("春跳次日应新增 1 条，实际总数 %d", len(occs))
		}
		if wantNext := time.Date(2025, 3, 10, 6, 0, 0, 0, time.UTC); !occs[1].ScheduledAt.Equal(wantNext) {
			t.Fatalf("次日 occurrence = %s，期望 %s",
				occs[1].ScheduledAt.UTC().Format(time.RFC3339), wantNext.Format(time.RFC3339))
		}
		h.assertUniqueKeyRejectsDuplicate(t, created.ID, want)
	})

	t.Run("秋回 01:00 出现两次", func(t *testing.T) {
		h := newHarness(t, time.Date(2025, 11, 1, 12, 0, 0, 0, time.UTC))
		sch := validSchedule()
		sch.ID = "sch-fall"
		sch.Cron = "0 1 * * *"
		created, cerr := h.svc.Create(context.Background(), sch)
		if cerr != nil {
			t.Fatalf("创建任务失败: %v", cerr)
		}
		// 2025-11-02 01:00 出现两次（05:00Z EDT / 06:00Z EST）：取较早的那一次。
		want := time.Date(2025, 11, 2, 5, 0, 0, 0, time.UTC)
		if !created.NextScheduledAt.Equal(want) {
			t.Fatalf("秋回日的下一跳 = %s，期望 %s（较早的瞬时）",
				created.NextScheduledAt.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
		}

		h.advance(17 * time.Hour) // 2025-11-02T05:00Z
		h.tick()
		// 再跨过第二次 01:00（06:00Z EST）：不得再触发。
		h.advance(90 * time.Minute)
		h.tick()
		occs := h.occurrences(created.ID)
		if len(occs) != 1 {
			t.Fatalf("秋回日应只落 1 条 occurrence（unique 键 + 只取较早瞬时），实际 %d 条", len(occs))
		}
		if !occs[0].ScheduledAt.Equal(want) {
			t.Fatalf("秋回 occurrence = %s，期望 %s",
				occs[0].ScheduledAt.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
		}

		// 次日 01:00 EST = 06:00Z（11-03）照常触发。
		h.advance(24 * time.Hour)
		h.tick()
		occs = h.occurrences(created.ID)
		if len(occs) != 2 {
			t.Fatalf("秋回次日应新增 1 条，实际总数 %d", len(occs))
		}
		if wantNext := time.Date(2025, 11, 3, 6, 0, 0, 0, time.UTC); !occs[1].ScheduledAt.Equal(wantNext) {
			t.Fatalf("次日 occurrence = %s，期望 %s",
				occs[1].ScheduledAt.UTC().Format(time.RFC3339), wantNext.Format(time.RFC3339))
		}
		_ = ny
	})
}
