package pi

import (
	"encoding/json"
	"strconv"
	"sync/atomic"
)

// 协议层常量。type 字符串取自 Pi 1.5.0 的真实捕获记录
// （internal/runtime/pi/testdata/v1.5.0/*.jsonl），不是猜测值。
const (
	// TypeResponse 是命令响应记录。带 id，按 id 归属到发起的 Call。
	TypeResponse = "response"
	// TypeAgentSettled 是 Pi 1.5.0 的最终 idle 信号（基线 §6 唯一认可的完成条件）。
	TypeAgentSettled = "agent_settled"
	// TypeProtocolDiagnostic 是本 worker 自己发出的诊断记录，不是 Pi 的原生事件。
	// 它与 Pi 事件同流输出，便于 adapter 统一落盘，但 type 前缀保证不会与原生 type 混淆。
	TypeProtocolDiagnostic = "protocol_diagnostic"
)

// RawEvent 是一条来自 Pi stdout 的原始记录，仅 adapter 消费，前端不直接使用。
//
// Record 保留 Pi 输出的原始字节（仅做了一次拷贝），不做字段裁剪：
// 未识别的事件必须能原样落盘，避免"看不懂就丢掉"导致的状态丢失。
type RawEvent struct {
	Type   string          `json:"type"`
	Record json.RawMessage `json:"record"`
}

// recordHeader 是分派与归属所需的字段，其余内容一律不看。
type recordHeader struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Command string `json:"command"`
	Success *bool  `json:"success"`
	Aborted *bool  `json:"aborted"`
}

// 诊断 kind（写入 protocol_diagnostic 的 payload）。
const (
	diagMalformedRecord   = "malformed_record"
	diagUntypedRecord     = "untyped_record"
	diagUnknownResponseID = "unknown_response_id"
	diagQueueOverflow     = "event_queue_overflow"
	diagStreamError       = "stream_error"
)

// commandIDs 分配命令 id。单调递增、永不复用：
// 复用 id 会让迟到的上个响应被误归属到新命令，这是最危险的一类串话。
type commandIDs struct {
	n atomic.Uint64
}

func (c *commandIDs) next() string {
	return "myagent-" + strconv.FormatUint(c.n.Add(1), 10)
}

// SettledOutcome 描述一条记录对"run 是否完成"的贡献。
type SettledOutcome struct {
	// Known 表示这是当前认证版本可识别的最终完成事件。
	Known bool
	// Settled 表示 Pi 已进入空闲。
	Settled bool
	// Aborted 表示本轮是被中断结束的，不是正常完成。
	Aborted bool
}

// ReadSettledOutcome 只把 agent_settled 认作完成信号。
//
// 未知 type（例如未来版本新增的 agent_settled_v2 或 agent_idle）一律
// Known=false：上层不得据此判定 completed（基线 §6「未知最终完成语义不得瞎猜 completed」）。
func ReadSettledOutcome(ev RawEvent) SettledOutcome {
	if ev.Type != TypeAgentSettled {
		return SettledOutcome{}
	}
	var h recordHeader
	_ = json.Unmarshal(ev.Record, &h)
	out := SettledOutcome{Known: true, Settled: true}
	if h.Aborted != nil {
		out.Aborted = *h.Aborted
	}
	return out
}

// IsCompleted 是"可以标 completed"的唯一判据：必须已知、已 settle 且未被中断。
// 把它单独抽出来，是为了让任何调用方都没有"看起来结束就算完成"的自由裁量空间。
func (o SettledOutcome) IsCompleted() bool {
	return o.Known && o.Settled && !o.Aborted
}

// diagnosticRecord 构造一条诊断事件的 JSON payload。
func diagnosticRecord(kind, detail string) json.RawMessage {
	b, err := json.Marshal(map[string]string{"kind": kind, "detail": detail})
	if err != nil {
		return json.RawMessage(`{"kind":"diagnostic_encode_failed"}`)
	}
	return b
}
