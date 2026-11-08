// Package credentials 管理模型凭证（基线 §5）。
//
// 纪律：
//   - 凭证只写 OS 提供的保护存储（Windows 用 DPAPI），配置里只存 credentialRef。
//   - 保护存储不可用时返回 CREDENTIAL_STORE_UNAVAILABLE，绝不回退明文文件。
//   - 允许"仅当前会话"的内存凭证（MemStore），它明确不落盘。
package credentials

import (
	"context"
	"errors"
	"strings"
	"sync"

	"my-agent-go/internal/contracts"
)

// requireNativeEnv 是"本环境必须支持原生保护存储"的开关，仅测试使用。
//
// CI 在六个目标上都装好对应后端并设置它：此时后端不可用就是 FAIL，
// 不允许悄悄 skip（跳过不是通过）。本地没装 secret-tool 时靠 skip 避免误报。
const requireNativeEnv = "MYAGENT_REQUIRE_NATIVE_CREDSTORE"

// Store 是凭证读写接口。
//
// Get 在 ref 不存在时返回 NOT_FOUND；Put 覆盖同名 ref；Delete 幂等。
type Store interface {
	Put(ctx context.Context, ref string, secret []byte) error
	Get(ctx context.Context, ref string) ([]byte, error)
	Delete(ctx context.Context, ref string) error
}

// validateRef 是所有后端共同的入参校验。
//
// 必须在进入任何后端之前统一执行：三个平台各自实现校验的话，
// 迟早会出现"Windows 允许、Keychain 拒绝"这种平台间语义漂移
// （实测就发生过：Windows 只查空串，CLI 后端还查空白与换行）。
func validateRef(ref string) error {
	if strings.TrimSpace(ref) == "" {
		return contracts.NewAppError(contracts.CodeUnsupported, "凭证 ref 为空", false)
	}
	// 换行 / NUL 会破坏以"行"或"argv"为单位的文本接口，
	// 且没有任何合法 ref 需要它们。
	if strings.ContainsAny(ref, "\r\n\x00") {
		return contracts.NewAppError(contracts.CodeUnsupported, "凭证 ref 不得包含换行或 NUL", false)
	}
	return nil
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

// 凭证库后端名（设置页需要说清"用的是哪一套保护存储"）。
//
// 名字是稳定标识，不是给人看的句字：前端按它选文案，所以不能因为失败就写成
// unknown——"不可用"与"不是 OS 凭证库"是两件事。
const (
	BackendWindowsCredentialManager = "windows-credential-manager"
	BackendMacOSKeychain            = "macos-keychain"
	BackendLinuxSecretService       = "linux-secret-service"
	// BackendUnavailable 表示本平台没有接入 OS 保护存储（不在 V1 目标矩阵内）。
	BackendUnavailable = "unavailable"
)

// Status 是 OS 凭证库可用性的结论。
//
// 只能有这三个字段：可用性、后端名、不可用原因。没有任何值字段、没有 ref 列表，
// 也不把错误里的凭证内容带出去——凭证库的状态页不能成为泄漏面。
type Status struct {
	Available bool   `json:"available"`
	Backend   string `json:"backend"`
	// Detail 只在不可用时出现（不含秘密：来源是后端能力错误，不是凭证内容）。
	Detail string `json:"detail,omitempty"`
}

// NativeBackend 返回本平台 OS 保护存储的后端名（不可用时为 BackendUnavailable）。
func NativeBackend() string { return nativeBackend }

// Probe 打开一次 OS 保护存储（含平台自检）。
//
// 返回的 error 就是"为什么不可用"：绑定层用 StatusFromOpen 把它转成结论，
// 所以生产与测试走的是同一条判断路径，不会出现两份互相矛盾的结论。
// 回退到内存库（MemStore）必须由调用方显式决定，Probe 只报告事实。
func Probe(ctx context.Context, root string) (Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store, err := NewNativeStore(root)
	if err != nil {
		return nil, err
	}
	if err := nativeSelfTest(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

// StatusFromOpen 把一次"打开 OS 凭证库"的结果转成可用性结论。
//
// 抽出来是为了让测试与生产走同一条判断：如果测试自己写一份"不可用"结论，
// 那就只验证了测试自己。
func StatusFromOpen(store Store, err error) Status {
	if err != nil {
		detail := err.Error()
		var appErr *contracts.AppError
		if errors.As(err, &appErr) && appErr != nil {
			// 只取消息体：Cause/堆栈不进状态页（那里容易混进外部输入）。
			detail = appErr.Message
		}
		return Status{Available: false, Backend: NativeBackend(), Detail: detail}
	}
	if store == nil {
		return Status{Available: false, Backend: NativeBackend(), Detail: "凭证库打开没有返回可用句柄"}
	}
	return Status{Available: true, Backend: NativeBackend()}
}
