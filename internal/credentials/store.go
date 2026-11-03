// Package credentials 管理模型凭证（基线 §5）。
//
// 纪律：
//   - 凭证只写 OS 提供的保护存储（Windows 用 DPAPI），配置里只存 credentialRef。
//   - 保护存储不可用时返回 CREDENTIAL_STORE_UNAVAILABLE，绝不回退明文文件。
//   - 允许"仅当前会话"的内存凭证（MemStore），它明确不落盘。
package credentials

import (
	"context"
	"sync"

	"my-agent-go/internal/contracts"
)

// Store 是凭证读写接口。
//
// Get 在 ref 不存在时返回 NOT_FOUND；Put 覆盖同名 ref；Delete 幂等。
type Store interface {
	Put(ctx context.Context, ref string, secret []byte) error
	Get(ctx context.Context, ref string) ([]byte, error)
	Delete(ctx context.Context, ref string) error
}

// MemStore 是仅当前会话有效的内存凭证存储。
//
// 它是 OS 保护存储不可用时的**显式**降级路径：调用方必须自己向用户说明
// "本次会话有效，退出即失效"，不得把它当成持久化。
type MemStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// NewMemStore 创建内存凭证存储。
func NewMemStore() *MemStore {
	return &MemStore{data: map[string][]byte{}}
}

// Put 保存凭证副本（不持有调用方切片）。
func (m *MemStore) Put(ctx context.Context, ref string, secret []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ref == "" {
		return contracts.NewAppError(contracts.CodeUnsupported, "凭证 ref 为空", false)
	}
	if len(secret) == 0 {
		return contracts.NewAppError(contracts.CodeUnsupported, "凭证内容为空", false)
	}
	cp := make([]byte, len(secret))
	copy(cp, secret)
	m.mu.Lock()
	m.data[ref] = cp
	m.mu.Unlock()
	return nil
}

// Get 读取凭证。
func (m *MemStore) Get(ctx context.Context, ref string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	v, ok := m.data[ref]
	m.mu.RUnlock()
	if !ok {
		return nil, contracts.NewAppError(contracts.CodeNotFound, "凭证不存在: "+ref, false)
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

// Delete 删除凭证（幂等）。
func (m *MemStore) Delete(ctx context.Context, ref string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.data, ref)
	m.mu.Unlock()
	return nil
}

// unavailable 构造统一的"保护存储不可用"错误。
func unavailable(detail string) error {
	return contracts.NewAppError(
		contracts.CodeCredentialStoreUnavailable,
		"OS 凭证保护存储不可用，拒绝持久化凭证（不回退明文）: "+detail,
		false,
	)
}
