package credentials

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"my-agent-go/internal/contracts"
)

// sentinelSecret 是一个"绝不允许出现在任何落盘字节里"的哨兵值。
var sentinelSecret = []byte("sk-sentinel-do-not-leak-8f3a1c")

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	appErr, ok := err.(*contracts.AppError)
	if !ok {
		t.Fatalf("期望 AppError(%s)，实际 %v", code, err)
	}
	if appErr.Code != code {
		t.Fatalf("期望 %s，实际 %s（%s）", code, appErr.Code, appErr.Message)
	}
}

// TestNativeStoreRoundTripAndNoPlaintext 覆盖 SEC07：OS 保护存储真实读写成功，
// 且落盘字节里检索不到明文哨兵。
func TestNativeStoreRoundTripAndNoPlaintext(t *testing.T) {
	if runtime.GOOS != "windows" {
		store, err := NewNativeStore(t.TempDir())
		if store != nil {
			t.Fatalf("非 Windows 平台不应返回可用的原生凭证库")
		}
		requireCode(t, err, contracts.CodeCredentialStoreUnavailable)
		// 必须明确不落明文：不可用时连目录都不该被当成凭证库使用。
		return
	}

	ctx := context.Background()
	root := t.TempDir()
	store, err := NewNativeStore(root)
	if err != nil {
		t.Fatalf("NewNativeStore: %v", err)
	}

	if err := store.Put(ctx, "user:openai", sentinelSecret); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// oracle 1：落盘立即扫描。必须在 Get 之前——先证明磁盘上没有明文，
	// 再讨论能不能读回；否则"解密失败"会把"明文落盘"这个问题盖住。
	if leaked := scanForSecret(t, root, sentinelSecret); len(leaked) > 0 {
		t.Fatalf("落盘文件出现明文凭证: %v", leaked)
	}
	got, err := store.Get(ctx, "user:openai")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, sentinelSecret) {
		t.Fatalf("读写不一致")
	}

	// oracle 2：ref 不同不得互相读取。
	if _, err := store.Get(ctx, "user:other"); err == nil {
		t.Fatalf("不存在的 ref 应返回 NOT_FOUND")
	} else {
		requireCode(t, err, contracts.CodeNotFound)
	}

	// 覆盖写：同 ref 再次 Put 覆盖旧值。
	rotated := []byte("sk-rotated-2nd-value")
	if err := store.Put(ctx, "user:openai", rotated); err != nil {
		t.Fatalf("覆盖 Put: %v", err)
	}
	got, err = store.Get(ctx, "user:openai")
	if err != nil {
		t.Fatalf("覆盖后 Get: %v", err)
	}
	if !bytes.Equal(got, rotated) {
		t.Fatalf("覆盖后应为新值")
	}
	if leaked := scanForSecret(t, root, sentinelSecret); len(leaked) > 0 {
		t.Fatalf("旧凭证轮换后仍留明文残片: %v", leaked)
	}

	// 删除幂等。
	if err := store.Delete(ctx, "user:openai"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(ctx, "user:openai"); err != nil {
		t.Fatalf("Delete 应幂等: %v", err)
	}
	if _, err := store.Get(ctx, "user:openai"); err == nil {
		t.Fatalf("删除后应返回 NOT_FOUND")
	} else {
		requireCode(t, err, contracts.CodeNotFound)
	}
}

// TestMemStoreIsSessionOnly 覆盖"仅当前会话内存凭证"路径：可用、不落盘。
func TestMemStoreIsSessionOnly(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	m := NewMemStore()
	if err := m.Put(ctx, "session:key", sentinelSecret); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := m.Get(ctx, "session:key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, sentinelSecret) {
		t.Fatalf("内存凭证读写不一致")
	}
	// 调用方切片后续被改写不得影响存储内容（防别名）。
	got[0] = 'X'
	again, err := m.Get(ctx, "session:key")
	if err != nil {
		t.Fatalf("再次 Get: %v", err)
	}
	if again[0] == 'X' {
		t.Fatalf("Get 应返回副本，不得暴露内部切片")
	}
	// oracle：内存存储不得在磁盘留下任何文件。
	if leaked := scanForSecret(t, root, sentinelSecret); len(leaked) > 0 {
		t.Fatalf("内存凭证不应落盘: %v", leaked)
	}
	if err := m.Delete(ctx, "session:key"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := m.Get(ctx, "session:key"); err == nil {
		t.Fatalf("删除后应 NOT_FOUND")
	}
}

// TestConcurrentMemStore 验证并发读写不产生竞态（配合 -race 有意义）。
func TestConcurrentMemStore(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = m.Put(ctx, "k", []byte{byte(i)})
			_, _ = m.Get(ctx, "k")
		}(i)
	}
	wg.Wait()
}

// scanForSecret 递归扫描目录，返回包含哨兵的文件路径。
//
// 同时检查原始字节与 base64 形式：凭证在 JSON payload 里必然是 base64，
// 只查原始字节会让"忘记加密"这种最危险的缺陷扫不出来（实测过：
// 把 protect 短路后，原始字节扫描 0 命中，因为 secret 被 base64 了）。
func scanForSecret(t *testing.T, root string, secret []byte) []string {
	t.Helper()
	needles := [][]byte{
		secret,
		[]byte(base64.StdEncoding.EncodeToString(secret)),
		[]byte(base64.RawStdEncoding.EncodeToString(secret)),
		[]byte(base64.URLEncoding.EncodeToString(secret)),
	}
	var hits []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, needle := range needles {
			if bytes.Contains(data, needle) {
				hits = append(hits, path)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描目录: %v", err)
	}
	return hits
}
