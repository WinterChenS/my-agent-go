package contracts

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEventRoundTrip(t *testing.T) {
	ev := Event{
		SchemaVersion: EventSchemaVersion,
		EventID:       "evt-1",
		WorkspaceID:   "ws-1",
		SessionID:     "sess-1",
		RunID:         "run-1",
		Seq:           1,
		Type:          "token",
		OccurredAt:    time.Now().UTC().Truncate(time.Second),
		Payload:       json.RawMessage(`{"text":"hi"}`),
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Event
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.EventID != ev.EventID || got.Seq != ev.Seq || got.Type != ev.Type {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if string(got.Payload) != `{"text":"hi"}` {
		t.Fatalf("payload mismatch: %s", got.Payload)
	}
}

func TestAppErrorImplementsError(t *testing.T) {
	e := NewAppError(CodeNotFound, "缺失目标", false)
	if e.Error() != "NOT_FOUND: 缺失目标" {
		t.Fatalf("unexpected error string: %q", e.Error())
	}
	// 前端必须按 Code 分支，这里验证 Code 可稳定取得。
	if e.Code != CodeNotFound {
		t.Fatalf("code mismatch: %q", e.Code)
	}
	if e.Retryable {
		t.Fatalf("retryable should be false")
	}
}

func TestGateEvidenceRequiresExplicitStatus(t *testing.T) {
	// 缺 status 字段时，反序列化后为空串，IsValidStatus 必须判为不合法。
	raw := `{"id":"GOV01","expected":"x","actual":"y","startedAt":"s","endedAt":"e"}`
	var cr CaseResult
	if err := json.Unmarshal([]byte(raw), &cr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if IsValidStatus(cr.Status) {
		t.Fatalf("空 status 不应被判为合法")
	}
	for _, s := range []string{StatusPass, StatusFail, StatusBlocked, StatusSkip, StatusNotRun} {
		if !IsValidStatus(s) {
			t.Fatalf("%s 应为合法状态", s)
		}
	}
}

func TestGateEvidenceRoundTrip(t *testing.T) {
	ev := GateEvidence{
		SchemaVersion:    1,
		GateID:           "G0",
		TaskID:           "F01",
		SourceRevision:   "abc123",
		VersionsLockHash: "sha256:deadbeef",
		TargetID:         "win-x64",
		StartedAt:        "2026-11-03T00:00:00Z",
		EndedAt:          "2026-11-03T00:01:00Z",
		Command:          "go test",
		ExitCode:         0,
		CaseResults: []CaseResult{
			{ID: "GOV01", Status: StatusPass, Expected: "0", Actual: "0", StartedAt: "s", EndedAt: "e"},
		},
		Reviewer:   Reviewer{Name: "ci", Machine: "host-x", ReviewedAt: "2026-11-03T00:02:00Z"},
		ReportHash: "sha256:hash",
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got GateEvidence
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ExitCode != 0 || got.TargetID != "win-x64" || len(got.CaseResults) != 1 {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if got.CaseResults[0].Status != StatusPass {
		t.Fatalf("status mismatch: %q", got.CaseResults[0].Status)
	}
}
