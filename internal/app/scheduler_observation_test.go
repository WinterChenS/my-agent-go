package app

// SCH02/SCH07 的绑定层外部观察：调度库与会话库里的真实行。
//
// 与 internal/scheduler 的域内观察互补：这里读的是**产品 appData 里**那两个库文件，
// 证明"删任务不删 run/会话/occurrence"和"调度会话 kind=scheduled 不进聊天列表"
// 是落库事实，而不是绑定层返回值。

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"my-agent-go/internal/storage"
)

type appScheduleRow struct {
	ID       string `json:"id"`
	Cron     string `json:"cron"`
	Enabled  int64  `json:"enabled"`
	Revision int64  `json:"revision"`
}

type appOccurrenceRow struct {
	ScheduleID  string `json:"scheduleId"`
	ScheduledAt string `json:"scheduledAt"`
	State       string `json:"state"`
	SessionID   string `json:"sessionId"`
	RunID       string `json:"runId"`
	Attempts    int64  `json:"attempts"`
}

// observeScheduleDB 读调度库里的 schedules/occurrence 真实行并落盘。
func observeScheduleDB(t *testing.T, path string, extra map[string]any) {
	t.Helper()
	if appObservationDir() == "" {
		return
	}
	schedules := []appScheduleRow{}
	occurrences := []appOccurrenceRow{}
	if _, err := os.Stat(path); err == nil {
		ctx := context.Background()
		db, oerr := storage.Open(ctx, path)
		if oerr != nil {
			t.Fatalf("用第二个句柄打开调度库 %s 失败: %v", path, oerr)
		}
		defer func() { _ = db.Close() }()

		rows, qerr := db.SQL().QueryContext(ctx,
			`SELECT id, cron, enabled, revision FROM schedules ORDER BY id`)
		if qerr == nil {
			for rows.Next() {
				var r appScheduleRow
				if err := rows.Scan(&r.ID, &r.Cron, &r.Enabled, &r.Revision); err != nil {
					t.Fatalf("扫描 schedules 行失败: %v", err)
				}
				schedules = append(schedules, r)
			}
			_ = rows.Close()
		}
		orows, oerr2 := db.SQL().QueryContext(ctx,
			`SELECT schedule_id, scheduled_at, state, session_id, run_id, attempts
			 FROM schedule_occurrences ORDER BY schedule_id, scheduled_at`)
		if oerr2 == nil {
			for orows.Next() {
				var r appOccurrenceRow
				if err := orows.Scan(&r.ScheduleID, &r.ScheduledAt, &r.State, &r.SessionID, &r.RunID, &r.Attempts); err != nil {
					t.Fatalf("扫描 occurrence 行失败: %v", err)
				}
				occurrences = append(occurrences, r)
			}
			_ = orows.Close()
		}
	}
	dump, err := json.MarshalIndent(map[string]any{"schedules": schedules, "occurrences": occurrences}, "", "  ")
	if err != nil {
		t.Fatalf("序列化调度库快照失败: %v", err)
	}
	record := map[string]any{
		"test":        t.Name(),
		"path":        path,
		"schedules":   schedules,
		"occurrences": occurrences,
	}
	if dump == nil {
		t.Fatalf("调度库快照序列化失败")
	}
	facts := map[string]any{
		"appSchedules":   schedules,
		"appOccurrences": occurrences,
	}
	for k, v := range extra {
		facts[k] = v
	}
	facts["byTest:"+t.Name()] = record
	writeAppObservation(t, facts, map[string][]byte{"scheduler-api-dump.json": appendArtifactRecord(t, "scheduler-api-dump.json", record)})
}

// observeAppSessions 读会话库里的 sessions 行（kind 是关键事实）。
func observeAppSessions(t *testing.T, path string, extra map[string]any) {
	t.Helper()
	if appObservationDir() == "" {
		return
	}
	_, dump := dumpRunIndexFile(t, path)
	record := map[string]any{"test": t.Name(), "path": path, "sessions": dump.Sessions}
	facts := map[string]any{"appSessions": dump.Sessions}
	for k, v := range extra {
		facts[k] = v
	}
	facts["byTest:"+t.Name()] = record
	writeAppObservation(t, facts, map[string][]byte{"sessions-api-dump.json": appendArtifactRecord(t, "sessions-api-dump.json", record)})
}
