package browser

// 引用注册表：把 ref 绑定到"某次快照里的某个元素本体"。
//
// 为什么需要独立一层：ref 只在产生它的快照语义下有效。任何"找不到就按位置
// 重新编号"的实现，在 SPA 换节点之后都会把动作点到**另一个元素**上——那正是
// BR08 明令禁止的失败模式（旧 ref 必须结构化拒绝，且服务端 counter 保持 0）。
//
// 所以这里只有精确查找，没有顺延、没有重排、没有按索引猜：
//   - (snapshotID, navigationID) 对不上 → STALE_SNAPSHOT；
//   - ref 不在该快照里（或快照已被淘汰）→ STALE_SNAPSHOT；
//   - backend 报回来的元素身份与快照记录不一致 → STALE_SNAPSHOT。
//
// 身份（Identity）由 backend 提供：native 实现用元素上的不透明 uid 属性，
// 契约层不解释它，也不让它进 JSON。这样"同一个 DOM 节点在两次观察里是同一个
// 元素"可以被验证，而"另一个节点冒用旧 ref"会被上面第三条挡掉。

import (
	"fmt"
	"sync"

	"my-agent-go/internal/contracts"
)

// MaxRetainedSnapshots 是一个 tab 里保留的最近快照数量。
//
// 保留多个是因为"观察 → 观察 → 用第一份快照动作"在真实流程里很常见；
// 上限存在是因为快照表会随长会话无限增长。被淘汰的快照不是"过期但仍可用"，
// 而是明确返回 STALE_SNAPSHOT：宁可让调用方重新观察，也不猜。
const MaxRetainedSnapshots = 32

// RefTable 是一份快照的引用表。
//
// 只由 RefRegistry.Begin 创建；Bind 之后不再增删（快照是不可变的一帧）。
type RefTable struct {
	// SnapshotID / NavigationID / ControlGeneration 是这份引用的有效期判据。
	SnapshotID        string
	NavigationID      string
	ControlGeneration uint64

	// BackendRef 放 backend 自己的执行上下文句柄（native 放 executionContextId）。
	// 契约层不解释、不序列化它；放在这里是为了让 backend 不必再维护一张平行表。
	BackendRef any

	entries map[string]refEntry
}

type refEntry struct {
	element  Element
	identity string
}

// Bind 把快照里的一个元素记进引用表。
//
// ref 重复时后者覆盖前者：调用方（backend）必须先自己保证 ref 唯一
// （Snapshot.ValidateRefs 会挡重复），这里覆盖只是不让表结构坏掉。
func (t *RefTable) Bind(el Element, identity string) {
	if t.entries == nil {
		t.entries = map[string]refEntry{}
	}
	t.entries[el.Ref] = refEntry{element: el, identity: identity}
}

// Len 返回引用表里的元素数。
func (t *RefTable) Len() int { return len(t.entries) }

// IsSecret 报告某个 ref 是否指向"值不得回显"的敏感字段（password）。
//
// 后置条件核验要用它：field_value 核验密码字段时，比较必须留在页面侧完成，
// 观测值与期望值都不能出现在结果/日志里。
func (t *RefTable) IsSecret(ref string) bool {
	entry, ok := t.entries[ref]
	if !ok {
		return false
	}
	return entry.element.IsSecretInput()
}

// Resolve 精确查找 ref，并返回它在本快照里绑定的元素与身份。
//
// 找不到就是 STALE_SNAPSHOT：绝不返回"最接近的一个"。
func (t *RefTable) Resolve(ref string) (Element, string, error) {
	if t == nil {
		return Element{}, "", appErr(contracts.CodeStaleSnapshot,
			"快照不存在或已被淘汰；必须重新 observe")
	}
	if ref == "" {
		return Element{}, "", appErr(contracts.CodeStaleSnapshot,
			"动作缺少 ref；必须重新 observe")
	}
	entry, ok := t.entries[ref]
	if !ok {
		return Element{}, "", appErr(contracts.CodeStaleSnapshot, fmt.Sprintf(
			"ref %q 不在快照 %s 里；ref 只对产生它的那次观察有效，必须重新 observe", ref, t.SnapshotID))
	}
	return entry.element, entry.identity, nil
}

// CheckIdentity 核对 backend 现在找到的元素是不是快照里那一个。
//
// 不一致说明节点被替换（SPA 断开旧节点后又塞了新节点），按坐标继续动作会点到
// 别的元素，所以必须拒绝。
func (t *RefTable) CheckIdentity(ref, identity string) error {
	entry, ok := t.entries[ref]
	if !ok {
		return appErr(contracts.CodeStaleSnapshot, fmt.Sprintf(
			"ref %q 不在快照 %s 里；必须重新 observe", ref, t.SnapshotID))
	}
	if identity != "" && entry.identity != "" && identity != entry.identity {
		return appErr(contracts.CodeStaleSnapshot, fmt.Sprintf(
			"ref %q 现在对应元素身份 %s，快照里是 %s：DOM 已被替换，必须重新 observe",
			ref, identity, entry.identity))
	}
	return nil
}

// RefRegistry 是"每个快照一份引用表"的容器。
//
// 并发安全：native 在 UI 线程上访问，但注册表本身带锁，避免以后被搬到别的线程
// 时静默产生数据竞争。
type RefRegistry struct {
	mu     sync.Mutex
	order  []string
	tables map[string]*RefTable
	max    int
}

// NewRefRegistry 创建引用注册表。
func NewRefRegistry() *RefRegistry {
	return &RefRegistry{tables: map[string]*RefTable{}, max: MaxRetainedSnapshots}
}

// Begin 开始一份新快照的引用表。
//
// 超出保留上限时淘汰最旧的那份：被淘汰的快照在 Lookup 里会返回 STALE_SNAPSHOT，
// 而不是"悄悄换成新的"。
func (r *RefRegistry) Begin(id, navigationID string, generation uint64, backend any) *RefTable {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tables == nil {
		r.tables = map[string]*RefTable{}
	}
	if r.max <= 0 {
		r.max = MaxRetainedSnapshots
	}
	if _, exists := r.tables[id]; exists {
		// 重复的 id 意味着 backend 的快照序号坏了：让旧的先失效，别让两份混在一起。
		r.removeLocked(id)
	}
	t := &RefTable{
		SnapshotID:        id,
		NavigationID:      navigationID,
		ControlGeneration: generation,
		BackendRef:        backend,
		entries:           map[string]refEntry{},
	}
	r.tables[id] = t
	r.order = append(r.order, id)
	for len(r.order) > r.max {
		r.removeLocked(r.order[0])
	}
	return t
}

// Lookup 取出快照的引用表，同时校验导航身份。
func (r *RefRegistry) Lookup(snapshotID, currentNavigationID string) (*RefTable, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tables[snapshotID]
	if !ok {
		return nil, appErr(contracts.CodeStaleSnapshot, fmt.Sprintf(
			"快照 %q 不存在或已被淘汰（tab 关闭/超出保留上限）；必须重新 observe", snapshotID))
	}
	if t.NavigationID != currentNavigationID {
		return nil, appErr(contracts.CodeStaleSnapshot, fmt.Sprintf(
			"快照 %q 属于文档 %s，当前文档是 %s：必须重新 observe",
			snapshotID, t.NavigationID, currentNavigationID))
	}
	return t, nil
}

// Drop 显式丢弃一份快照（关 tab / 重新导航时调用）。
func (r *RefRegistry) Drop(snapshotID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removeLocked(snapshotID)
}

// Reset 丢弃全部快照（关 tab 时调用）。
func (r *RefRegistry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tables = map[string]*RefTable{}
	r.order = nil
}

// Len 返回当前保留的快照数量。
func (r *RefRegistry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.tables)
}

func (r *RefRegistry) removeLocked(id string) {
	delete(r.tables, id)
	for i, v := range r.order {
		if v == id {
			r.order = append(r.order[:i], r.order[i+1:]...)
			return
		}
	}
}
