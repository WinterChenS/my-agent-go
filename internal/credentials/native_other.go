//go:build !windows && !darwin && !linux

package credentials

import "runtime"

// NewNativeStore 在尚未接入 OS 保护存储的平台返回明确的不可用错误。
//
// 目前只有 Windows（DPAPI）、macOS（Keychain）、Linux（Secret Service）三套实现。
// 其余平台（FreeBSD 等）不在 V1 目标矩阵内，此时必须报
// CREDENTIAL_STORE_UNAVAILABLE 并让上层改用 MemStore，绝不落明文（基线 §5）。
func NewNativeStore(root string) (Store, error) {
	_ = root
	return nil, unavailable("当前平台 " + runtime.GOOS + " 尚未接入 OS 保护存储")
}
