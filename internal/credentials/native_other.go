//go:build !windows

package credentials

import "runtime"

// NewNativeStore 在非 Windows 平台返回明确的不可用错误。
//
// 为什么不是"顺手写个文件"：macOS Keychain 与 Linux Secret Service 需要各自的原生
// 绑定，V1 未实现。此时必须报 CREDENTIAL_STORE_UNAVAILABLE 并让上层改用 MemStore，
// 绝不落明文（基线 §5）。
func NewNativeStore(root string) (Store, error) {
	_ = root
	return nil, unavailable("当前平台 " + runtime.GOOS + " 尚未接入 Keychain / Secret Service")
}
