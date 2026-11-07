package contracts

import (
	"encoding/json"
	"strings"
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

// jsonKeysOf 返回序列化后的顶层字段名集合，用来断言 omitempty 的真实行为
// （看的是线上字节，不是结构体字段表）。
func jsonKeysOf(t *testing.T, v any) map[string]json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, b)
	}
	return m
}

// U00：错误契约增加 Params / Operation / Cause，且必须**只增不改**。
//
// 旧字段（code/message/retryable）的 JSON 名一旦变化，前端按 code 分支的
// 所有既有调用都会静默失配；旧调用方不改一行也必须能编译。
func TestAppErrorCarriesParamsOperationAndCause(t *testing.T) {
	err := NewAppError(CodeConfigConflict, "配置冲突", false).
		WithParams(map[string]string{"key": "user", "revision": "3"}).
		WithOperation("config.save").
		WithCause("checksum mismatch")

	keys := jsonKeysOf(t, err)
	for _, k := range []string{"code", "message", "retryable", "params", "operation", "cause"} {
		if _, ok := keys[k]; !ok {
			t.Fatalf("缺少字段 %q；实际 %v", k, keys)
		}
	}
	var params map[string]string
	if e := json.Unmarshal(keys["params"], &params); e != nil {
		t.Fatalf("params 不是字符串映射: %v", e)
	}
	if params["key"] != "user" || params["revision"] != "3" {
		t.Fatalf("params 内容不符: %v", params)
	}
	if err.Code != CodeConfigConflict || err.Retryable {
		t.Fatalf("链式调用不得改旧字段: %+v", err)
	}
	// Error() 是既有契约（"CODE: message"），加字段不许改它。
	if err.Error() != "CONFIG_CONFLICT: 配置冲突" {
		t.Fatalf("Error() 变化: %q", err.Error())
	}

	// 未设置的字段必须不出现在 JSON 里：前端据此区分"没有参数"和"参数为空"。
	plain := NewAppError(CodeNotFound, "缺失", false)
	pk := jsonKeysOf(t, plain)
	for _, k := range []string{"params", "operation", "cause"} {
		if _, ok := pk[k]; ok {
			t.Fatalf("未设置的 %q 不应出现在 JSON 中: %v", k, pk)
		}
	}

	// 传入 nil 映射等价于没有参数，不能序列化成 "params": null。
	if nk := jsonKeysOf(t, NewAppError(CodeNotFound, "缺失", false).WithParams(nil)); len(nk["params"]) != 0 {
		t.Fatalf("WithParams(nil) 不应产生 params 字段: %v", nk)
	}

	// Params 必须是防御性拷贝：调用方随后改自己的 map 不该改到错误里，
	// 否则同一个错误在日志与响应里会显示两份不同参数。
	src := map[string]string{"key": "workspace"}
	shared := NewAppError(CodeUnsupported, "不支持", false).WithParams(src)
	src["key"] = "被改掉了"
	if shared.Params["key"] != "workspace" {
		t.Fatalf("Params 与入参共享了底层映射: %v", shared.Params)
	}

	// Cause 只放脱敏摘要：这里守住"不是堆栈"的最低要求。
	if strings.Contains(err.Cause, "\n") {
		t.Fatalf("Cause 不应包含多行堆栈: %q", err.Cause)
	}
}

// TestAppErrorJSONStaysBackwardCompatible 守住"旧调用方原样编译"的另一半：
// 用最朴素的三字段字面量构造，序列化结果必须仍是三个字段。
func TestAppErrorJSONStaysBackwardCompatible(t *testing.T) {
	legacy := AppError{Code: CodeInternal, Message: "内部错误", Retryable: true}
	b, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"code":"INTERNAL_ERROR","message":"内部错误","retryable":true}`
	if string(b) != want {
		t.Fatalf("旧构造方式的 JSON 变了:\n got %s\nwant %s", b, want)
	}
}
