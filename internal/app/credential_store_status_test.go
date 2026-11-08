package app

// B2：设置 → 安全的"凭证库是否可用"。
//
// 守三件事：
//  1. 不可用时给**稳定错误码** CREDENTIAL_STORE_UNAVAILABLE，并带后端名与可读原因；
//  2. 可用时有真实后端名（不是"unknown"）；
//  3. 结论里绝不出现任何秘密：即使注入的库里真的存着 sentinel，序列化结果也不能带出来。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"my-agent-go/internal/contracts"
	"my-agent-go/internal/credentials"
)

func TestCredentialStoreStatusUnavailableUsesStableCode(t *testing.T) {
	d, _ := newTestDesktop(t)

	// 注入一个"打不开"的凭证库：真实的失败打开路径就是这个形状
	// （NewNativeStore 在缺少 secret-tool / Keychain 锁定时返回同样的错误）。
	d.credStoreProbe = func(context.Context, string) (credentials.Store, error) {
		return nil, contracts.NewAppError(contracts.CodeCredentialStoreUnavailable,
			"OS 凭证保护存储不可用，拒绝持久化凭证（不回退明文）: 注入的失败后端", false)
	}

	res := d.CredentialStoreStatus()
	if res.Available {
		t.Fatal("打开失败时不能声称可用")
	}
	if res.Backend != credentials.NativeBackend() {
		t.Fatalf("后端名 = %q，期望平台真实后端 %q", res.Backend, credentials.NativeBackend())
	}
	if res.Error == nil || res.Error.Code != contracts.CodeCredentialStoreUnavailable {
		t.Fatalf("必须返回稳定错误码 %q，实际 %+v", contracts.CodeCredentialStoreUnavailable, res.Error)
	}
	if res.Detail == "" {
		t.Fatal("不可用时必须说明原因")
	}
}

func TestCredentialStoreStatusNeverReturnsSecrets(t *testing.T) {
	d, _ := newTestDesktop(t)
	const sentinel = "sk-b2-credential-status-sentinel"

	// 注入一个真的存着秘密的库：状态结论不得把它（或任何值字段）带出去。
	store := credentials.NewMemStore()
	if err := store.Put(context.Background(), "model-sentinel", []byte(sentinel)); err != nil {
		t.Fatalf("写入注入库: %v", err)
	}
	d.credStoreProbe = func(context.Context, string) (credentials.Store, error) { return store, nil }

	res := d.CredentialStoreStatus()
	if res.Error != nil {
		t.Fatalf("注入可用的库时不应报错: %+v", res.Error)
	}
	if !res.Available {
		t.Fatal("打开成功时必须报可用")
	}
	if res.Backend == "" || res.Backend == credentials.BackendUnavailable {
		t.Fatalf("可用时必须给真实后端名，实际 %q", res.Backend)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("序列化状态: %v", err)
	}
	if strings.Contains(string(raw), sentinel) {
		t.Fatalf("凭证库状态里出现了秘密: %s", raw)
	}
	for _, banned := range []string{`"secret"`, `"value"`, `"apiKey"`, `"token"`} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("凭证库状态不应包含 %s 字段: %s", banned, raw)
		}
	}
}

func TestCredentialStoreStatusRealBackendIsSelfConsistent(t *testing.T) {
	d, _ := newTestDesktop(t)

	// 不注入：走生产路径（本机 Windows = DPAPI）。两个方向都必须自洽，
	// 不在"本机有没有 secret-tool"上做假设，也不跳过。
	res := d.CredentialStoreStatus()
	if res.Available {
		if res.Error != nil {
			t.Fatalf("可用时不应带错误: %+v", res.Error)
		}
		if res.Backend != credentials.NativeBackend() {
			t.Fatalf("可用时后端名 = %q，期望 %q", res.Backend, credentials.NativeBackend())
		}
		return
	}
	if res.Error == nil || res.Error.Code != contracts.CodeCredentialStoreUnavailable {
		t.Fatalf("不可用时错误码必须是 %q，实际 %+v", contracts.CodeCredentialStoreUnavailable, res.Error)
	}
	if res.Detail == "" {
		t.Fatal("不可用时必须说明原因")
	}
}
