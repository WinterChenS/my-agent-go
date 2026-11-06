package run

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/contracts"
)

// TestDeltaCoalescingDoesNotDropToolResults 覆盖 RUN02 的合并窗口边界：
// 文本增量与工具事件落在同一个 50ms 合并窗口里时，
//   - 拼接后的 message.delta 文本必须等于输入文本（不丢字、不重复）；
//   - 工具开始/结束事件必须照样存在，工具结果文本不能被合并窗口吞掉；
//   - 文本必须在工具事件之前落地（叙述顺序不能颠倒：先"我来执行"再 tool.start）；
//   - run.state 的 seq 必须严格递增（合并不能打乱事件序号）。
func TestDeltaCoalescingDoesNotDropToolResults(t *testing.T) {
	w := newFakeWorker()
	svc, snap := runWithWorker(t, testOptions(t), w)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	id := snap.RunID

	const text = "我先解释一下，再执行工具。"
	// 三段 text_delta 紧跟着同一次合并窗口内的 tool_execution_start/end。
	sendRaw(w, "message_update", `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"我先解释"}}`)
	sendRaw(w, "message_update", `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"一下，"}}`)
	sendRaw(w, "message_update", `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"再执行工具。"}}`)
	sendRaw(w, "tool_execution_start",
		`{"type":"tool_execution_start","toolCallId":"call_merge_1","toolName":"fixture_write","args":{"path":"a.txt"}}`)
	sendRaw(w, "tool_execution_end",
		`{"type":"tool_execution_end","toolCallId":"call_merge_1","toolName":"fixture_write",`+
			`"result":{"content":[{"type":"text","text":"工具结果没有被吞掉"}],"structuredContent":{"exit_code":0}},`+
			`"isError":false,"durationMs":5}`)

	if !eventually(t, 3*time.Second, func() bool {
		return len(eventsOfType(t, svc, id, EventToolEnd)) == 1
	}) {
		t.Fatal("工具结束事件丢失：合并窗口把工具结果吞掉了")
	}
	if !eventually(t, 3*time.Second, func() bool {
		var got strings.Builder
		for _, ev := range eventsOfType(t, svc, id, EventMessageDelta) {
			var p struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatalf("解析 message.delta 载荷失败: %v", err)
			}
			got.WriteString(p.Text)
		}
		return got.String() == text
	}) {
		t.Fatalf("合并后的文本增量不等于输入文本 %q", text)
	}

	all, err := svc.Replay(id, 0)
	if err != nil {
		t.Fatalf("重放事件失败: %v", err)
	}

	var (
		textOut  strings.Builder
		deltaIdx = -1
		toolIdx  = -1
		toolText string
		prevSeq  uint64
	)
	for i, ev := range all {
		if prevSeq != 0 && ev.Seq <= prevSeq {
			t.Fatalf("事件 seq 必须严格递增：%d 之后出现 %d（type=%s）", prevSeq, ev.Seq, ev.Type)
		}
		prevSeq = ev.Seq
		switch ev.Type {
		case EventMessageDelta:
			if deltaIdx < 0 {
				deltaIdx = i
			}
			var p struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatalf("解析 message.delta 载荷失败: %v", err)
			}
			textOut.WriteString(p.Text)
		case EventToolStart:
			if toolIdx < 0 {
				toolIdx = i
			}
		case EventToolEnd:
			var p struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatalf("解析 tool.end 载荷失败: %v", err)
			}
			toolText = p.Text
		}
	}
	if textOut.String() != text {
		t.Fatalf("message.delta 拼接结果 %q，期望 %q", textOut.String(), text)
	}
	if deltaIdx < 0 {
		t.Fatal("缺失 message.delta：文本增量被丢弃")
	}
	if toolIdx < 0 {
		t.Fatal("缺失 tool.start：工具事件被合并窗口吞掉")
	}
	if deltaIdx > toolIdx {
		t.Fatalf("文本必须先于工具事件落地（delta 序号 %d，tool.start 序号 %d）："+
			"否则界面会先显示工具开始，再补上这句说明", deltaIdx, toolIdx)
	}
	if toolText != "工具结果没有被吞掉" {
		t.Fatalf("工具结果文本不符: %q", toolText)
	}
	if got := mustRunState(t, svc, id); got != contracts.RunStateRunning {
		t.Fatalf("这一段不该结束 run，实际状态 %s", got)
	}

	writeCaseJournal(t, svc)
}
