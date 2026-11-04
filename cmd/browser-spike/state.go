package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"my-agent-go/internal/browser"
	"my-agent-go/internal/contracts"
)

// 探针步骤结论。
const (
	// resultAsExpected 表示"该失败的确实按预期失败了"。
	// 对 B00 来说这是通过：我们要的记录就是"没有原生视图"。
	resultAsExpected = "AS_EXPECTED"
	// resultUnexpectedSuccess 表示"本该失败的调用成功了"，即静默成功，属于真失败。
	resultUnexpectedSuccess = "UNEXPECTED_SUCCESS"
	// resultUnexpectedError 表示失败方式不对（错误码不是 NOT_IMPLEMENTED 等）。
	resultUnexpectedError = "UNEXPECTED_ERROR"
	// resultWarn 是诊断信息，不影响结论。
	resultWarn = "WARN"
)

// verdictAsExpected 是 B00 模式的"预期结论"：所有占位调用都明确失败。
const verdictAsExpected = "AS_EXPECTED"

// verdictNativePresent 是 B01a 模式的"预期结论"：同一主窗口右侧真的有 WebView2 子视图。
//
// 它只在三条硬条件同时成立时才成立（父子句柄关系、导航完成、CDP 截图非纯色），
// 任何一条不成立都打 FAIL 并让进程退出码为 1。
const verdictNativePresent = "NATIVE_VIEW_PRESENT"

// probeStep 是探针的一行记录，既进 stdout 也进壳页面的 /probe.json。
type probeStep struct {
	Name   string `json:"name"`
	Want   string `json:"want"`
	Got    string `json:"got"`
	Code   string `json:"code,omitempty"`
	Result string `json:"result"`
}

// slotReport 是壳页面量出来的右侧槽位几何（含诊断用的额外字段）。
//
// 它刻意不是 browser.SlotGeometry：页面还会带 devicePixelRatio、CSS viewport
// 这些只用于诊断的字段，把它们塞进契约会把调试信息变成协议。
type slotReport struct {
	SlotID           string  `json:"slotId"`
	Revision         uint64  `json:"revision"`
	Left             float64 `json:"left"`
	Top              float64 `json:"top"`
	Width            float64 `json:"width"`
	Height           float64 `json:"height"`
	Visible          bool    `json:"visible"`
	Zoom             float64 `json:"zoom"`
	DevicePixelRatio float64 `json:"devicePixelRatio"`
	CSSWidth         float64 `json:"cssWidth"`
	CSSHeight        float64 `json:"cssHeight"`
	ReportedAt       string  `json:"reportedAt"`
}

// geometry 把页面上报的几何转成契约类型。
func (r slotReport) geometry() browser.SlotGeometry {
	return browser.SlotGeometry{
		SlotID:   r.SlotID,
		Revision: r.Revision,
		Left:     r.Left,
		Top:      r.Top,
		Width:    r.Width,
		Height:   r.Height,
		Visible:  r.Visible,
		Zoom:     r.Zoom,
	}
}

// spikeState 汇总探针结果与页面几何，供 HTTP 端点和退出码共用。
type spikeState struct {
	mu      sync.Mutex
	steps   []probeStep
	slot    slotReport
	hasSlot bool
	verdict string

	// B01a 模式的状态。
	nativeMode     bool
	nativePass     bool
	nativeFailures []string
	dialogOpen     bool
	pageDialogOpen bool
}

// note 记录一条探针步骤并打到 stdout。
//
// Result 为空时按 Code 推断：空码与 NOT_IMPLEMENTED 视为"按预期"，
// NO_SLOT_REPORT 只是警告，其余错误码视为非预期错误。
func (s *spikeState) note(name string, step probeStep) {
	if step.Result == "" {
		switch step.Code {
		case "", browser.CodeNotImplemented:
			step.Result = resultAsExpected
		case "NO_SLOT_REPORT":
			step.Result = resultWarn
		default:
			step.Result = resultUnexpectedError
		}
	}
	step.Name = name
	s.mu.Lock()
	s.steps = append(s.steps, step)
	s.mu.Unlock()
	fmt.Printf("B00|probe|%s|%s|want=%s|got=%s|code=%s\n",
		step.Result, name, step.Want, step.Got, step.Code)
}

// expectNotImplemented 核对一个"必须明确失败"的调用。
//
// nil 错误是静默成功，直接判失败：那正是 B00 要防的"看起来能用"。
func (s *spikeState) expectNotImplemented(name string, err error) {
	step := probeStep{Want: "NOT_IMPLEMENTED（明确失败，不能静默成功）"}
	if err == nil {
		step.Got = "nil（调用成功返回）"
		step.Code = "SILENT_SUCCESS"
		step.Result = resultUnexpectedSuccess
		s.note(name, step)
		return
	}
	step.Got = err.Error()
	var ae *contracts.AppError
	if errors.As(err, &ae) {
		step.Code = ae.Code
		if ae.Code == browser.CodeNotImplemented {
			step.Result = resultAsExpected
		} else {
			step.Result = resultUnexpectedError
		}
	} else {
		step.Code = "NON_APP_ERROR"
		step.Result = resultUnexpectedError
	}
	s.note(name, step)
}

// markUnexpected 记录一条"不该发生的事"。
func (s *spikeState) markUnexpected(name, detail string) {
	s.note(name, probeStep{
		Want:   "占位实现不得报告可用",
		Got:    detail,
		Code:   "UNEXPECTED",
		Result: resultUnexpectedError,
	})
}

// setSlot 保存页面上报的几何。revision 更旧的上报直接丢弃（B07 的语义在这里先立住）。
func (s *spikeState) setSlot(r slotReport) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasSlot && r.Revision <= s.slot.Revision {
		return false
	}
	s.slot = r
	s.hasSlot = true
	return true
}

// waitSlot 等到页面第一次上报几何，或超时。
func (s *spikeState) waitSlot(ctx context.Context, timeout time.Duration) (slotReport, bool) {
	deadline := time.Now().Add(timeout)
	for {
		s.mu.Lock()
		r, ok := s.slot, s.hasSlot
		s.mu.Unlock()
		if ok {
			return r, true
		}
		if time.Now().After(deadline) {
			return slotReport{}, false
		}
		select {
		case <-ctx.Done():
			return slotReport{}, false
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// computeVerdict 把步骤汇总成结论：任何非预期成功/错误都判 FAIL。
func (s *spikeState) computeVerdict() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nativeMode {
		if s.nativePass && len(s.nativeFailures) == 0 {
			return verdictNativePresent
		}
		return "FAIL"
	}
	if len(s.steps) == 0 {
		return "PENDING"
	}
	for _, st := range s.steps {
		if st.Result == resultUnexpectedSuccess || st.Result == resultUnexpectedError {
			return "FAIL"
		}
	}
	return verdictAsExpected
}

func (s *spikeState) setVerdict(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verdict = v
}

// Verdict 返回最近一次计算的结论。
func (s *spikeState) Verdict() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.verdict == "" {
		return "PENDING"
	}
	return s.verdict
}

// probePayload 是 /probe.json 的响应体。
type probePayload struct {
	Verdict string      `json:"verdict"`
	Steps   []probeStep `json:"steps"`
	Slot    *slotReport `json:"slot,omitempty"`
	// OpenDialog 让页面在收到指令后打开应用内 dialog（B01a 遮挡证据）。
	OpenDialog bool `json:"openDialog"`
	// DialogOpen 由页面自己回报，用来证明 dialog 真的打开了。
	DialogOpen bool `json:"dialogOpen"`
}

// requestDialog 让页面下一次轮询时打开应用内 dialog。
func (s *spikeState) requestDialog() {
	s.mu.Lock()
	s.dialogOpen = true
	s.mu.Unlock()
}

// setPageDialogOpen 记录页面自己回报的 dialog 状态。
func (s *spikeState) setPageDialogOpen(open bool) {
	s.mu.Lock()
	s.pageDialogOpen = open
	s.mu.Unlock()
}

// PageDialogOpen 返回页面自己回报的 dialog 状态。
func (s *spikeState) PageDialogOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pageDialogOpen
}

// snapshot 取一份一致的快照给 HTTP 用。
func (s *spikeState) snapshot() probePayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := probePayload{
		Verdict:    s.verdict,
		Steps:      append([]probeStep(nil), s.steps...),
		OpenDialog: s.dialogOpen,
		DialogOpen: s.pageDialogOpen,
	}
	if s.hasSlot {
		r := s.slot
		out.Slot = &r
	}
	if out.Verdict == "" {
		out.Verdict = "PENDING"
	}
	return out
}

// nativeFail 记录 B01a 的一条硬条件失败，并打到 stdout。
func (s *spikeState) nativeFail(reason string) {
	s.mu.Lock()
	s.nativeFailures = append(s.nativeFailures, reason)
	s.mu.Unlock()
	fmt.Printf("B01|fail|%s\n", reason)
}

// setNativePass 记录 B01a 的三条硬条件全部成立。
func (s *spikeState) setNativePass() {
	s.mu.Lock()
	s.nativePass = true
	s.mu.Unlock()
}

// NativeFailures 返回已记录的硬条件失败。
func (s *spikeState) NativeFailures() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.nativeFailures...)
}

// nativeStep 记录一条 B01a 观测（同时进 stdout 与壳页面的 /probe.json）。
//
// 与 B00 的 note 分开打印前缀：证据里 "B00|probe|" 表示占位实现的预期失败，
// "B01|probe|" 表示原生宿主的真实观测，混在一起会让复核者误读。
func (s *spikeState) nativeStep(name, got string, ok bool) {
	result := resultAsExpected
	if !ok {
		result = resultUnexpectedError
	}
	step := probeStep{Name: name, Want: "B01a 观测", Got: got, Result: result}
	s.mu.Lock()
	s.steps = append(s.steps, step)
	s.mu.Unlock()
	fmt.Printf("B01|probe|%s|%s|%s\n", result, name, got)
}
