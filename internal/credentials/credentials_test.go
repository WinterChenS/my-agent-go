package credentials

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

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

// TestNativeStoreRoundTripAndNoPlaintext 覆盖 SEC07：
// OS 保护存储真实读写成功，且明文哨兵不出现在任何落盘字节里。
//
// 三个平台三套后端：Windows DPAPI / macOS Keychain / Linux Secret Service。
// 后端不可用（例如容器里没有 keyring daemon）是**合法的环境状态**，但：
//   - 必须报 CREDENTIAL_STORE_UNAVAILABLE，且不留任何明文；
//   - 若环境通过 MYAGENT_REQUIRE_NATIVE_CREDSTORE=1 声明"必须支持"，则不可用即判失败。
//
// 后者是 CI 用的开关：装了后端还不可用就是实现或环境配置错了，不许悄悄 skip。
func TestNativeStoreRoundTripAndNoPlaintext(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	store, err := NewNativeStore(root)
	if err != nil {
		requireCode(t, err, contracts.CodeCredentialStoreUnavailable)
		// 不可用时绝不能有任何东西落盘——这是"降级为内存"与"偷偷写明文"的分界。
		if leaked := scanForSecret(t, root, sentinelSecret); len(leaked) > 0 {
			t.Fatalf("后端不可用时仍写入了明文: %v", leaked)
		}
		if os.Getenv(requireNativeEnv) == "1" {
			t.Fatalf("环境声明必须支持原生凭证库（%s=1），但 NewNativeStore 不可用: %v", requireNativeEnv, err)
		}
		t.Skipf("本机无可用 OS 保护存储: %v", err)
	}

	// ref 里带随机串：Keychain / Secret Service 是**全局命名空间**，
	// 固定 ref 会让并发跑的两个 CI job 互相覆盖（并让结果变得不可复现）。
	ref := "test:roundtrip:" + t.Name() + ":" + strconv.FormatInt(time.Now().UnixNano(), 36)
	t.Cleanup(func() { _ = store.Delete(context.Background(), ref) })

	if err := store.Put(ctx, ref, sentinelSecret); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// oracle 1：落盘立即扫描。必须在 Get 之前——先证明磁盘上没有明文，
	// 再讨论能不能读回；否则“解密失败”会把“明文落盘”这个问题盖住。
	if leaked := scanForSecret(t, root, sentinelSecret); len(leaked) > 0 {
		t.Fatalf("落盘文件出现明文凭证: %v", leaked)
	}
	// SEC07.store 的外部观察：凭证根下此刻到底有哪些文件、重新扫描它们得到的
	// sentinel 命中数，全部由磁盘字节算出（哨兵只记 sha256）。
	observeCredentialStore(t, "after-put", root, [][]byte{sentinelSecret}, map[string]any{
		"backend":              runtime.GOOS + "-native",
		"roundTripVerified":    true,
		"nativeStoreAvailable": true,
	})
	if runtime.GOOS != "windows" {
		// Keychain / Secret Service 不落文件。root 下出现任何文件，
		// 说明有人在"后端不可用"时偷偷写了明文缓存。
		if entries, err := os.ReadDir(root); err == nil && len(entries) > 0 {
			t.Fatalf("非 Windows 后端不应在凭证根目录留下文件，实际有 %d 个", len(entries))
		}
	}

	got, err := store.Get(ctx, ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, sentinelSecret) {
		t.Fatalf("读写不一致：得到 %q", got)
	}

	// oracle 2：ref 不同不得互相读取。
	missing := ref + ":absent"
	if _, err := store.Get(ctx, missing); err == nil {
		t.Fatalf("不存在的 ref 应返回 NOT_FOUND")
	} else {
		requireCode(t, err, contracts.CodeNotFound)
	}

	// 覆盖写：同 ref 再次 Put 覆盖旧值。
	rotated := []byte("sk-rotated-2nd-value")
	if err := store.Put(ctx, ref, rotated); err != nil {
		t.Fatalf("覆盖 Put: %v", err)
	}
	got, err = store.Get(ctx, ref)
	if err != nil {
		t.Fatalf("覆盖后 Get: %v", err)
	}
	if !bytes.Equal(got, rotated) {
		t.Fatalf("覆盖后应为新值，实际 %q", got)
	}
	if leaked := scanForSecret(t, root, sentinelSecret); len(leaked) > 0 {
		t.Fatalf("旧凭证轮换后仍留明文残片: %v", leaked)
	}
	observeCredentialStore(t, "after-rotate", root, [][]byte{sentinelSecret, rotated}, nil)

	// 删除幂等。
	if err := store.Delete(ctx, ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(ctx, ref); err != nil {
		t.Fatalf("Delete 应幂等: %v", err)
	}
	if _, err := store.Get(ctx, ref); err == nil {
		t.Fatalf("删除后应返回 NOT_FOUND")
	} else {
		requireCode(t, err, contracts.CodeNotFound)
	}
}

// TestNativeStoreRejectsBadRef 校验入参约束在两个 CLI 后端上都生效。
// ref 里的换行会破坏"以行为单位"的 CLI 语义，必须在进入外部命令前就拒绝。
func TestNativeStoreRejectsBadRef(t *testing.T) {
	store, err := NewNativeStore(t.TempDir())
	if err != nil {
		t.Skipf("本机无可用 OS 保护存储: %v", err)
	}
	for _, bad := range []string{"", "   ", "a\nb", "a\x00b"} {
		if err := store.Put(context.Background(), bad, []byte("x")); err == nil {
			t.Fatalf("ref %q 应被拒绝", bad)
		}
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
