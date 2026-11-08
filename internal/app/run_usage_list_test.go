package app

// B2：观测页跨 run 列表的绑定。
//
// 守的是"空 与 unavailable 必须分开"这一条：
//   - 查询成功但库里没有匹配记录 → availability=reported、items=[]、total=0，
//     界面显示"没有记录"，不是"读不出来"；
//   - 索引库不可用 → availability=unavailable + 结构化错误码 + 原因，
//     界面必须显示"读不出来"，而不是"这一段时间什么都没发生"。
//
// 把两者混成一个空数组，是这个页面最典型的假绿形态。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
)

func TestListRunUsageBindingSeparatesEmptyFromUnavailable(t *testing.T) {
	d, _ := newTestDesktop(t)

	// 1) 空库：查询成功，结论是"空"。
	empty := d.ListRunUsage(RunUsageListQuery{})
	if empty.Error != nil {
		t.Fatalf("空库列表必须成功: %+v", empty.Error)
	}
	if empty.Availability != contracts.UsageReported {
		t.Fatalf("空库 availability = %q，期望 %q（空不是不可用）", empty.Availability, contracts.UsageReported)
	}
	if empty.Items == nil {
		t.Fatal("空结果的 items 必须是空数组（序列化成 null 会让界面以为绑定坏了）")
	}
	if empty.Total != 0 || len(empty.Items) != 0 {
		t.Fatalf("空库结果 = %d 项 / total %d", len(empty.Items), empty.Total)
	}
	raw, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("序列化空结果: %v", err)
	}
	if !strings.Contains(string(raw), `"items":[]`) {
		t.Fatalf("空结果必须序列化成 items: []，实际 %s", raw)
	}

	// 2) 有记录：数量与顺序可核对（updated_at DESC）。
	rec, err := d.diagnosticsRecorder()
	if err != nil {
		t.Fatalf("diagnosticsRecorder: %v", err)
	}
	base := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	for i, runID := range []string{"run-old", "run-new"} {
		rec.SetClock(func() time.Time { return base.Add(time.Duration(i) * time.Hour) })
		if err := rec.RecordRun(context.Background(), contracts.RunSnapshot{
			RunID: contracts.RunID(runID), SessionID: "sess-1", WorkspaceID: "ws-1",
			State: contracts.RunStateCompleted,
			Usage: &contracts.RunUsage{
				Availability: contracts.UsageReported,
				InputTokens:  3, OutputTokens: 4, TotalTokens: 7,
			},
		}); err != nil {
			t.Fatalf("RecordRun %s: %v", runID, err)
		}
	}
	listed := d.ListRunUsage(RunUsageListQuery{Limit: 1})
	if listed.Error != nil {
		t.Fatalf("列表: %+v", listed.Error)
	}
	if listed.Total != 2 {
		t.Fatalf("Total = %d，期望 2", listed.Total)
	}
	if len(listed.Items) != 1 || listed.Items[0].RunID != "run-new" {
		t.Fatalf("第一页 = %+v，期望最新的 run-new", listed.Items)
	}
	if listed.Items[0].Usage.TotalTokens == nil || *listed.Items[0].Usage.TotalTokens != 7 {
		t.Fatalf("usage 必须原样带出: %+v", listed.Items[0].Usage)
	}

	// 3) 索引库不可用：必须显式 unavailable，且错误码结构化。
	// 真实地关掉并摘掉库句柄（不是假造一个错误），然后确认绑定报“读不出来”而不是“没有记录”。
	d.svcMu.Lock()
	sessionDB := d.sessionDB
	d.sessionDB = nil
	d.svcMu.Unlock()
	if sessionDB != nil {
		_ = sessionDB.Close()
	}

	gone := d.ListRunUsage(RunUsageListQuery{})
	if gone.Availability != contracts.UsageUnavailable {
		t.Fatalf("索引库不可用时 availability = %q，期望 %q", gone.Availability, contracts.UsageUnavailable)
	}
	if gone.Error == nil || gone.Error.Code != contracts.CodeConfigMissing {
		t.Fatalf("索引库不可用必须带结构化错误码，实际 %+v", gone.Error)
	}
	if gone.Note == "" {
		t.Fatal("不可用时必须给出能给人看的原因（不能让界面自己猜）")
	}
	if gone.Items == nil {
		t.Fatal("不可用结果的 items 仍必须是空数组，界面才好渲染")
	}

	// 4) 时间范围格式错误：CONFIG_MISSING，不静默当成"不限"。
	bad := d.ListRunUsage(RunUsageListQuery{From: "2026-11-05"})
	if bad.Error == nil || bad.Error.Code != contracts.CodeConfigMissing {
		t.Fatalf("非法时间范围应返回 CONFIG_MISSING，实际 %+v", bad.Error)
	}
	if bad.Availability != contracts.UsageUnavailable {
		t.Fatalf("非法时间范围也要标成 unavailable，实际 %q", bad.Availability)
	}
}
