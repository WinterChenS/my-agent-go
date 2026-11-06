package run

// 用量报告（RUN06）：把 Pi 记录里的 usage 汇总成快照上的 RunUsage。
//
// 两条纪律：
//  1. 没看到用量就是 unavailable。绝对不能把"provider 没报"写成 0 费用/0 token——
//     那是在用确定的语气说一件不知道的事。
//  2. 同一条助手消息不能重复计数。Pi 会先发 message_end，再在 agent_end 里把
//     整轮消息重发一遍；两边都累加会让费用翻倍。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"my-agent-go/internal/contracts"
)

// piUsage 是 Pi 消息上的 usage 结构（取自 1.5.0 的真实捕获记录：
// internal/runtime/pi/testdata/v1.5.0/text.jsonl）。
type piUsage struct {
	Input       int64   `json:"input"`
	Output      int64   `json:"output"`
	CacheRead   int64   `json:"cacheRead"`
	CacheWrite  int64   `json:"cacheWrite"`
	TotalTokens int64   `json:"totalTokens"`
	Cost        *piCost `json:"cost"`
}

// piCost 是 Pi 的费用结构。
//
// TotalFromProvider 区分两种完全不同的数字：
//   - true：提供方账单金额，可以直接展示；
//   - false：Pi 按本地价目表算出的估算值，必须标成估算并写明价格来源；
//   - 缺失：老记录没有这个字段，按"有非零金额即视为提供方金额"兼容处理。
type piCost struct {
	Total             float64 `json:"total"`
	TotalFromProvider *bool   `json:"totalFromProvider"`
}

// tokens 是不含 totalTokens 的分数之和（缓存读写也是真实消耗的 token）。
func (u *piUsage) tokens() int64 {
	return u.Input + u.Output + u.CacheRead + u.CacheWrite
}

// reported 判断这条 usage 是否真的带了用量。
//
// 为什么不能只看"有没有 usage 对象"：Pi 在 provider 不返回用量时仍会发一条
// 全 0 的占位记录。把它当成"报告了 0 token"正是规格禁止的伪 0。
// 全 0 与"没报"在实际记录里无法区分，因此按"没报"处理。
func (u *piUsage) reported() bool {
	if u == nil {
		return false
	}
	return u.tokens() > 0 || u.TotalTokens > 0
}

// piMessage 是 Pi 消息记录里我们需要的字段。
type piMessage struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Usage     *piUsage        `json:"usage"`
	Timestamp int64           `json:"timestamp"`
	// StopReason / ErrorMessage 是 message_end 的结束原因。真实记录
	// （testdata/v1.5.0/provider-error.jsonl）里 assistant 消息带
	// stopReason:"error" 且 errorMessage 就是 provider 的原始报错。
	StopReason   string `json:"stopReason"`
	ErrorMessage string `json:"errorMessage"`
}

// usageKey 是"同一条助手消息"的判据。
//
// 优先用 Pi 的 timestamp（真实记录里 message_end 与 agent_end 的同一条消息
// 时间戳完全一致）；缺失时退化成 role+content+usage 的哈希。
func (m piMessage) usageKey() string {
	if m.Timestamp != 0 {
		return "ts:" + strconv.FormatInt(m.Timestamp, 10)
	}
	h := sha256.New()
	h.Write([]byte(m.Role))
	h.Write([]byte{0})
	h.Write(m.Content)
	h.Write([]byte{0})
	if b, err := json.Marshal(m.Usage); err == nil {
		h.Write(b)
	}
	return "hash:" + hex.EncodeToString(h.Sum(nil))
}

// usageAccumulator 汇总一次 run 的用量。
//
// 只在 runState.mu 下使用，自身不加锁。
type usageAccumulator struct {
	seen    map[string]struct{}
	input   int64
	output  int64
	total   int64
	cost    float64
	hasCost bool
	// hasCatalogCost 表示至少有一条费用来自 Pi 的本地价目表估算（不是提供方账单）。
	hasCatalogCost bool
	messages       int
}

// add 累加一条助手消息的用量；同一条消息重复到达时忽略。
func (a *usageAccumulator) add(m piMessage) {
	if m.Role != "assistant" || !m.Usage.reported() {
		return
	}
	key := m.usageKey()
	if a.seen == nil {
		a.seen = map[string]struct{}{}
	}
	if _, dup := a.seen[key]; dup {
		return
	}
	a.seen[key] = struct{}{}

	u := m.Usage
	a.input += u.Input
	a.output += u.Output
	// provider 报了 totalTokens 就用它（各家的口径不完全一样），
	// 没报才用分数相加补出来。
	if u.TotalTokens > 0 {
		a.total += u.TotalTokens
	} else {
		a.total += u.tokens()
	}
	if u.Cost != nil {
		if c, ok := u.Cost.reported(); ok {
			a.cost += c
			a.hasCost = true
			if !u.Cost.fromProvider() {
				a.hasCatalogCost = true
			}
		}
	}
	a.messages++
}

// reported 返回这条费用是否可用，以及可用的金额。
//
// 真实记录里 Pi 在不知道价格时会发 total=0 + totalFromProvider=false。
// 那不是"这一轮免费"，是"价格未知"：记成 0 元正是规格禁止的伪 0 费用。
func (c *piCost) reported() (float64, bool) {
	if c.TotalFromProvider != nil && !*c.TotalFromProvider && c.Total == 0 {
		return 0, false
	}
	return c.Total, true
}

// fromProvider 判断这条费用是提供方账单还是本地价目表估算。
func (c *piCost) fromProvider() bool {
	return c.TotalFromProvider == nil || *c.TotalFromProvider
}

// runUsage 把累计值转成快照结构。
//
// messages==0 表示一次用量都没看到：只写 availability，其余字段留空
// （omitempty 保证 JSON 里不会出现 costUsd / totalTokens 这些伪 0）。
func (a *usageAccumulator) runUsage() *contracts.RunUsage {
	if a.messages == 0 {
		return &contracts.RunUsage{Availability: contracts.UsageUnavailable}
	}
	out := &contracts.RunUsage{
		Availability: contracts.UsageReported,
		InputTokens:  a.input,
		OutputTokens: a.output,
		TotalTokens:  a.total,
		PriceSource:  "provider",
		// 数字是 provider 报的，不是我们按价目表估的。V1 不做本地估算：
		// 拿不到 provider 价格就说 unavailable，而不是自己编一个数。
		Estimated: false,
	}
	if a.hasCost {
		cost := a.cost
		out.CostUSD = &cost
		if a.hasCatalogCost {
			// 只要有一条金额是本地价目表算的，就不能标成提供方账单。
			out.PriceSource = "catalog"
			out.Estimated = true
		}
	}
	return out
}

// recordUsage 记录一条助手消息的用量并即时更新快照。
func (s *Service) recordUsage(rs *runState, msg piMessage) {
	if msg.Role != "assistant" || !msg.Usage.reported() {
		return
	}
	rs.mu.Lock()
	rs.usage.add(msg)
	rs.snap.Usage = rs.usage.runUsage()
	rs.mu.Unlock()
}

// recordAgentEndUsage 处理 agent_end 里重发的整轮消息。
//
// 这里刻意不区分"是历史消息还是本轮消息"：去重键保证同一条只算一次，
// 而 agent_end 里若出现本轮之前的助手消息，它们的用量也确实属于本会话的
// 上下文成本——但 V1 只报"这一轮新增"，所以只累加首次见到的消息。
func (s *Service) recordAgentEndUsage(rs *runState, raw json.RawMessage) {
	var e struct {
		Messages []piMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return
	}
	for _, m := range e.Messages {
		s.recordUsage(rs, m)
	}
}

// finalizeUsageLocked 在 run 进入终态时补上 usage 结论。
//
// 调用方必须持有 rs.mu。
func (rs *runState) finalizeUsageLocked() {
	if rs.snap.Usage != nil {
		return
	}
	rs.snap.Usage = &contracts.RunUsage{Availability: contracts.UsageUnavailable}
}
