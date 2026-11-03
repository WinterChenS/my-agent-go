//go:build windows

package credentials

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"

	"my-agent-go/internal/contracts"
)

// nativeStore 把凭证用 DPAPI（用户作用域）加密后落盘。
//
// 为什么是 DPAPI 而不是文件权限：DPAPI 的密钥由 OS 按当前用户派生，
// 复制文件到另一账户/机器无法解密；明文从不落盘，也不进配置与日志。
type nativeStore struct {
	root string
}

// payload 是解密后的内容，同时保存 ref 以便校验文件名与内容是否对得上。
type payload struct {
	Ref    string `json:"ref"`
	Secret []byte `json:"secret"`
}

// NewNativeStore 打开（必要时创建）基于 OS 保护存储的凭证库。
func NewNativeStore(root string) (Store, error) {
	if root == "" {
		return nil, unavailable("凭证库根目录为空")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, unavailable("创建凭证库目录失败: " + err.Error())
	}
	return &nativeStore{root: root}, nil
}

// credPath 用 ref 的哈希做文件名，避免 ref 里的路径字符影响落盘位置。
func (s *nativeStore) credPath(ref string) string {
	sum := sha256.Sum256([]byte(ref))
	return filepath.Join(s.root, hex.EncodeToString(sum[:16])+".cred")
}

// Put 加密后原子写入。
func (s *nativeStore) Put(ctx context.Context, ref string, secret []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRef(ref); err != nil {
		return err
	}
	if len(secret) == 0 {
		return contracts.NewAppError(contracts.CodeUnsupported, "凭证内容为空", false)
	}
	plain, err := json.Marshal(payload{Ref: ref, Secret: secret})
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "序列化凭证失败: "+err.Error(), false)
	}
	sealed, err := protect(plain)
	if err != nil {
		return unavailable("DPAPI 加密失败: " + err.Error())
	}
	return writeFileAtomic(s.credPath(ref), sealed)
}

// Get 解密读取；ref 不匹配视为数据损坏而不是"就当没找到"。
func (s *nativeStore) Get(ctx context.Context, ref string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.credPath(ref))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, contracts.NewAppError(contracts.CodeNotFound, "凭证不存在: "+ref, false)
		}
		return nil, contracts.NewAppError(contracts.CodeInternal, "读取凭证失败: "+err.Error(), false)
	}
	plain, err := unprotect(data)
	if err != nil {
		return nil, unavailable("DPAPI 解密失败（可能来自其它用户或机器）: " + err.Error())
	}
	var p payload
	if err := json.Unmarshal(plain, &p); err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "凭证内容损坏: "+err.Error(), false)
	}
	if p.Ref != ref {
		return nil, contracts.NewAppError(contracts.CodeInternal, "凭证 ref 与请求不一致，拒绝返回", false)
	}
	return p.Secret, nil
}

// Delete 删除凭证（幂等）。
func (s *nativeStore) Delete(ctx context.Context, ref string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := os.Remove(s.credPath(ref))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return contracts.NewAppError(contracts.CodeInternal, "删除凭证失败: "+err.Error(), false)
	}
	return nil
}

// protect 调用 DPAPI 加密，作用域为当前用户（不传 CRYPTPROTECT_LOCAL_MACHINE）。
func protect(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, contracts.NewAppError(contracts.CodeUnsupported, "空内容不可加密", false)
	}
	in := windows.DataBlob{Size: uint32(len(plain)), Data: &plain[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	sealed := make([]byte, out.Size)
	copy(sealed, unsafe.Slice(out.Data, out.Size))
	return sealed, nil
}

// unprotect 调用 DPAPI 解密。
func unprotect(sealed []byte) ([]byte, error) {
	if len(sealed) == 0 {
		return nil, contracts.NewAppError(contracts.CodeUnsupported, "空密文不可解密", false)
	}
	in := windows.DataBlob{Size: uint32(len(sealed)), Data: &sealed[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	plain := make([]byte, out.Size)
	copy(plain, unsafe.Slice(out.Data, out.Size))
	return plain, nil
}

// writeFileAtomic 写临时文件 → fsync → rename，保证不产生半个密文文件。
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "创建凭证目录失败: "+err.Error(), false)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "写入凭证失败: "+err.Error(), false)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return contracts.NewAppError(contracts.CodeInternal, "写入凭证失败: "+err.Error(), false)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return contracts.NewAppError(contracts.CodeInternal, "凭证落盘失败: "+err.Error(), false)
	}
	if err := f.Close(); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "关闭凭证文件失败: "+err.Error(), false)
	}
	if err := os.Rename(tmp, path); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "激活凭证文件失败: "+err.Error(), false)
	}
	return nil
}
