package scheduler

// SCH01–SCH05 / SCH07 的外部观察值输出。
//
// 契约与 internal/memory/observation_test.go 一致：目录来自 ANVIL_OBSERVATION_DIR，
// observation.json 合并写、artifact 覆盖写；未设置时不写任何文件。
//
// 观察值来自**调度库文件本身**（source: database）：第二个 storage.DB 句柄读出
// schedules / schedule_occurrences / schedule_occurrence_events / runs 的真实行。
// "唯一 occurrence 键没有重复""漏跑只补规定次数""无人值守仍 waiting_approval"
// 这些结论都在库里的行上，不在被测函数的返回值里。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"my-agent-go/internal/storage"
)

func observationDir() string {
	return strings.TrimSpace(os.Getenv("ANVIL_OBSERVATION_DIR"))
}

type scheduleRow struct {
	ID           string `json:"id"`
	Cron         string `json:"cron"`
	TimeZone     string `json:"timeZone"`
	Enabled      int64  `json:"enabled"`
	Revision     int64  `json:"revision"`
	Misfire      string `json:"misfirePolicy"`
	CatchUpLimit int64  `json:"catchUpLimit"`
	OutputTarget string `json:"outputTarget"`
}

type occurrenceRow struct {
	ScheduleID  string `json:"scheduleId"`
	ScheduledAt string `json:"scheduledAt"`
	State       string `json:"state"`
	Reason      string `json:"reason"`
	SessionID   string `json:"sessionId"`
	RunID       string `json:"runId"`
	Attempts    int64  `json:"attempts"`
}

type occurrenceEventRow struct {
	Seq         int64  `json:"seq"`
	ScheduleID  string `json:"scheduleId"`
	ScheduledAt string `json:"scheduledAt"`
	State       string `json:"state"`
	Previous    string `json:"previous"`
	Reason      string `json:"reason"`
	RunID       string `json:"runId"`
}

type runRow struct {
	ID             string `json:"id"`
	SessionID      string `json:"sessionId"`
	State          string `json:"state"`
	ConfigRevision int64  `json:"configRevision"`
}

type schedulerDump struct {
	Path        string               `json:"path"`
	Schedules   []scheduleRow        `json:"schedules"`
	Occurrences []occurrenceRow      `json:"occurrences"`
	Events      []occurrenceEventRow `json:"occurrenceEvents"`
	Runs        []runRow             `json:"runs"`
	Errors      []string             `json:"errors,omitempty"`
}

// dumpSchedulerFile 用第二个句柄读调度库真实行。
func dumpSchedulerFile(t *testing.T, path string) *schedulerDump {
	t.Helper()
	dump := &schedulerDump{Path: path, Schedules: []scheduleRow{}, Occurrences: []occurrenceRow{}, Events: []occurrenceEventRow{}, Runs: []runRow{}}
	if _, err := os.Stat(path); err != nil {
		return dump
	}
	ctx := context.Background()
	db, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatalf("用第二个句柄打开调度库 %s 失败: %v", path, err)
	}
	defer func() { _ = db.Close() }()

	rows, qerr := db.SQL().QueryContext(ctx, `
		SELECT id, cron, time_zone, enabled, revision, misfire_policy, catch_up_limit, output_target
		FROM schedules ORDER BY id`)
	if qerr != nil {
		dump.Errors = append(dump.Errors, "schedules: "+qerr.Error())
	} else {
		for rows.Next() {
			var r scheduleRow
			if err := rows.Scan(&r.ID, &r.Cron, &r.TimeZone, &r.Enabled, &r.Revision, &r.Misfire, &r.CatchUpLimit, &r.OutputTarget); err != nil {
				t.Fatalf("扫描 schedules 行失败: %v", err)
			}
			dump.Schedules = append(dump.Schedules, r)
		}
		_ = rows.Close()
	}

	orows, oerr := db.SQL().QueryContext(ctx, `
		SELECT schedule_id, scheduled_at, state, reason, session_id, run_id, attempts
		FROM schedule_occurrences ORDER BY schedule_id, scheduled_at`)
	if oerr != nil {
		dump.Errors = append(dump.Errors, "schedule_occurrences: "+oerr.Error())
	} else {
		for orows.Next() {
			var r occurrenceRow
			if err := orows.Scan(&r.ScheduleID, &r.ScheduledAt, &r.State, &r.Reason, &r.SessionID, &r.RunID, &r.Attempts); err != nil {
				t.Fatalf("扫描 occurrence 行失败: %v", err)
			}
			dump.Occurrences = append(dump.Occurrences, r)
		}
		_ = orows.Close()
	}

	erows, eerr := db.SQL().QueryContext(ctx, `
		SELECT seq, schedule_id, scheduled_at, state, previous, reason, run_id
		FROM schedule_occurrence_events ORDER BY seq`)
	if eerr != nil {
		dump.Errors = append(dump.Errors, "schedule_occurrence_events: "+eerr.Error())
	} else {
		for erows.Next() {
			var r occurrenceEventRow
			if err := erows.Scan(&r.Seq, &r.ScheduleID, &r.ScheduledAt, &r.State, &r.Previous, &r.Reason, &r.RunID); err != nil {
				t.Fatalf("扫描 occurrence 事件失败: %v", err)
			}
			dump.Events = append(dump.Events, r)
		}
		_ = erows.Close()
	}

	rrows, rerr := db.SQL().QueryContext(ctx, `SELECT id, session_id, state, config_revision FROM runs ORDER BY id`)
	if rerr != nil {
		dump.Errors = append(dump.Errors, "runs: "+rerr.Error())
	} else {
		for rrows.Next() {
			var r runRow
			if err := rrows.Scan(&r.ID, &r.SessionID, &r.State, &r.ConfigRevision); err != nil {
				t.Fatalf("扫描 runs 行失败: %v", err)
			}
			dump.Runs = append(dump.Runs, r)
		}
		_ = rrows.Close()
	}
	return dump
}

// observeScheduler 把调度库的真实行写进观察目录（按测试追加，后写者不覆盖）。
func observeScheduler(t *testing.T, dbPath string, extra map[string]any) {
	t.Helper()
	if observationDir() == "" {
		return
	}
	dump := dumpSchedulerFile(t, dbPath)
	record := map[string]any{
		"test":             t.Name(),
		"path":             dump.Path,
		"schedules":        dump.Schedules,
		"occurrences":      dump.Occurrences,
		"occurrenceEvents": dump.Events,
		"runs":             dump.Runs,
		"errors":           dump.Errors,
	}
	facts := map[string]any{
		"scheduleRows":     dump.Schedules,
		"occurrenceRows":   dump.Occurrences,
		"occurrenceEvents": dump.Events,
		"runRows":          dump.Runs,
	}
	for k, v := range extra {
		facts[k] = v
	}
	facts["byTest:"+t.Name()] = record
	writeSchedulerObservation(t, facts, map[string][]byte{"scheduler-dump.json": appendSchedulerRecord(t, "scheduler-dump.json", record)})
}

// appendSchedulerRecord 把本条测试的记录追加进已有 artifact 数组。
func appendSchedulerRecord(t *testing.T, artifact string, record map[string]any) []byte {
	t.Helper()
	dir := observationDir()
	records := []json.RawMessage{}
	if dir != "" {
		if raw, err := os.ReadFile(filepath.Join(dir, artifact)); err == nil {
			if uerr := json.Unmarshal(raw, &records); uerr != nil {
				t.Fatalf("已有 %s 不是合法 JSON 数组: %v", artifact, uerr)
			}
		}
	}
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("序列化 %s 记录失败: %v", artifact, err)
	}
	records = append(records, json.RawMessage(body))
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatalf("序列化 %s 失败: %v", artifact, err)
	}
	return data
}

// writeSchedulerObservation 合并写 observation.json，并覆盖写 artifact。
func writeSchedulerObservation(t *testing.T, facts map[string]any, artifacts map[string][]byte) {
	t.Helper()
	dir := observationDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建观察目录 %s 失败: %v", dir, err)
	}
	for name, content := range artifacts {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatalf("写 artifact %s 失败: %v", name, err)
		}
	}
	merged := map[string]any{}
	if raw, err := os.ReadFile(filepath.Join(dir, "observation.json")); err == nil {
		if err := json.Unmarshal(raw, &merged); err != nil {
			t.Fatalf("已有 observation.json 不是合法 JSON: %v", err)
		}
	}
	for k, v := range facts {
		merged[k] = v
	}
	var tests []any
	if cur, ok := merged["tests"].([]any); ok {
		tests = cur
	}
	self := t.Name()
	seen := false
	for _, v := range tests {
		if s, ok := v.(string); ok && s == self {
			seen = true
			break
		}
	}
	if !seen {
		tests = append(tests, self)
	}
	merged["tests"] = tests
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		t.Fatalf("序列化观察值失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "observation.json"), data, 0o644); err != nil {
		t.Fatalf("写 observation.json 失败: %v", err)
	}
}
