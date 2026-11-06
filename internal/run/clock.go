package run

import "time"

// Clock 是停止与预算计时用的可控时钟。
//
// 为什么要抽象出来：基线 §6 的时限是"2 秒可见、5 秒升级、10 秒清理结论"，
// 用真实时间去等这三条，测试要么慢 10 秒、要么在慢机器上假红。
// 生产实现直接落回 time，测试用可推进的假时钟。
//
// 只覆盖停止与预算计时：50ms 文本合并窗口刻意仍用真实时间，
// 那是 UI 刷新节奏，不是语义时限。
type Clock interface {
	// Now 返回当前时间（预算的"已运行多久"用它算）。
	Now() time.Time
	// After 在 d 之后触发一次，语义同 time.After。
	After(d time.Duration) <-chan time.Time
}

// realClock 是 Clock 的生产实现。
//
// now 可为空：调用方只给了 Now（既有字段）时用它，否则用 time.Now。
// 定时器永远是真实时间——把 Now 也换成假的时候不该连带伪造"过了 5 秒"。
type realClock struct {
	now func() time.Time
}

func (c realClock) Now() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

func (c realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
