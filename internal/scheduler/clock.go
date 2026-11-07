package scheduler

// C09 的时钟抽象。
//
// 为什么必须有它：调度器的全部语义都关于时间——DST 两个方向、系统时钟前后跳、
// 无人值守审批超时、漏跑补跑。用 time.Sleep / time.After 去测这些，测试要么慢
// 几天，要么在慢机器上假绿/假红。所以生产落回真实时间，测试用可推进的 ManualClock，
// 正确性完全不依赖"睡够多久"。
//
// tzdata 内嵌：本包显式导入 time/tzdata，让 IANA 时区在**所有**目标平台与
// 任何缺 /usr/share/zoneinfo 的机器上都可用。DST 语义必须来自运行时 tzdata，
// 绝不写死偏移量（写死的测试在换规则那天仍然会绿，而生产会错）。

import (
	"sync"
	"time"

	// 内嵌 IANA 时区数据库：不依赖宿主机 zoneinfo，六目标行为一致。
	_ "time/tzdata"
)

// Clock 是调度器唯一的取时入口。
type Clock interface {
	// Now 返回当前时间。
	Now() time.Time
	// After 在 d 之后触发一次，语义同 time.After。
	After(d time.Duration) <-chan time.Time
}

// loadLocation 加载 IANA 时区名（间接给测试留一个替换点）。
func loadLocation(name string) (*time.Location, error) { return time.LoadLocation(name) }

// RealClock 是生产实现：直接落回 time 包。
type RealClock struct{}

// Now 返回 time.Now()。
func (RealClock) Now() time.Time { return time.Now() }

// After 等价于 time.After。
func (RealClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// ManualClock 是可推进的假时钟。
//
// 它是**生产代码的一部分**而不是测试文件的私有件：假时钟与真时钟必须实现同一个
// 接口，否则"测试里跑通的时间语义"和"生产里跑的时间语义"可以是两套东西。
// 测试用 Set/Advance 推进时间，进程内的后台循环则照常挂在 After 上。
type ManualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*manualTimer
}

type manualTimer struct {
	deadline time.Time
	ch       chan time.Time
}

// NewManualClock 以给定时刻建一个假时钟。
func NewManualClock(now time.Time) *ManualClock {
	return &ManualClock{now: now}
}

// Now 返回当前（被设置过的）时间。
func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After 在时钟推进到 now+d 时触发一次。
func (c *ManualClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.timers = append(c.timers, &manualTimer{deadline: c.now.Add(d), ch: ch})
	return ch
}

// Set 把时钟设到某个绝对时刻（可以**向前也可以向后**：时钟跳变是真实场景）。
//
// 到期的定时器按当前时间触发；向后跳不会"取消"已经注册的定时器
// （它们的 deadline 仍然是绝对时刻），这与真实定时器的行为一致。
func (c *ManualClock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now
	var fired []chan time.Time
	kept := c.timers[:0]
	for _, t := range c.timers {
		if !t.deadline.After(now) {
			fired = append(fired, t.ch)
			continue
		}
		kept = append(kept, t)
	}
	c.timers = kept
	c.mu.Unlock()

	for _, ch := range fired {
		// 缓冲 1，且每个定时器只会被触发一次。
		ch <- now
	}
}

// Advance 把时钟向前推进 d。
func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	next := c.now.Add(d)
	c.mu.Unlock()
	c.Set(next)
}
