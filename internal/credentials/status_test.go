package credentials

// B2：OS 凭证库的可用性结论（设置 → 安全分区）。
//
// 这里守两件事：
//  1. "不可用"是可判定的结论，不是模糊的失败：后端名必须说清是哪一套保护存储，
//     原因必须能给人看，错误码必须稳定（CREDENTIAL_STORE_UNAVAILABLE）；
//  2. 结论里绝不出现任何秘密：Status 只有可用性/后端名/原因，没有值字段。
//
// 不做 t.Skip：不可用本身就是要被正确报告的情形，本机没有 secret-tool
// 也照样能验证"结论诚实"。

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func TestStatusFromOpenReportsUnavailable(t *testing.T) {
	cause := unavailable("DPAPI 不可用（测试注入）")
	st := StatusFromOpen(nil, cause)
	if st.Available {
		t.Fatal("打开失败时必须是不可用")
	}
	if st.Backend != NativeBackend() {
		t.Fatalf("后端名 = %q，期望平台真实后端 %q（不能因为失败就写成 unknown）", st.Backend, NativeBackend())
	}
	if !strings.Contains(st.Detail, "DPAPI") {
		t.Fatalf("原因必须保留可读细节，实际 %q", st.Detail)
	}
	if strings.Contains(st.Detail, "sk-") {
		t.Fatalf("原因里出现了疑似秘密: %q", st.Detail)
	}
	// nil store 不是错误：这是"没有可用的库"，同样要报不可用而不是 panic。
	if st := StatusFromOpen(nil, nil); st.Available {
		t.Fatal("没有库句柄时不能声称可用")
	}
}

func TestStatusFromOpenReportsAvailable(t *testing.T) {
	store := NewMemStore()
	st := StatusFromOpen(store, nil)
	if !st.Available {
		t.Fatalf("打开成功时必须可用: %+v", st)
	}
	if st.Backend == "" || st.Backend == BackendUnavailable {
		t.Fatalf("可用时必须给出真实后端名，实际 %q", st.Backend)
	}
	if st.Detail != "" {
		t.Fatalf("可用时不应带失败原因，实际 %q", st.Detail)
	}
}

func TestStatusCarriesNoSecretFields(t *testing.T) {
	raw, err := json.Marshal(Status{Available: false, Backend: NativeBackend(), Detail: "nope"})
	if err != nil {
		t.Fatalf("序列化 Status: %v", err)
	}
	lower := strings.ToLower(string(raw))
	for _, banned := range []string{"secret", "value", "token", "password", "\"apiKey\""} {
		if strings.Contains(lower, strings.ToLower(banned)) {
			t.Fatalf("Status 里不应出现 %q: %s", banned, raw)
		}
	}
}

func TestProbeNativeIsHonestAboutThisHost(t *testing.T) {
	root := t.TempDir()
	store, err := Probe(context.Background(), root)
	st := StatusFromOpen(store, err)
	if st.Available {
		if store == nil {
			t.Fatal("可用但没有返回库句柄")
		}
		if st.Backend != NativeBackend() {
			t.Fatalf("可用时后端名 = %q，期望 %q", st.Backend, NativeBackend())
		}
		if st.Detail != "" {
			t.Fatalf("可用时不应带失败原因: %q", st.Detail)
		}
		return
	}
	if store != nil {
		t.Fatal("不可用时不应返回库句柄（回退内存库必须由调用方显式决定）")
	}
	if st.Detail == "" {
		t.Fatal("不可用时必须说明原因")
	}
	// 三个目标平台都有原生后端；其它平台上后端名必须是 unavailable。
	switch runtime.GOOS {
	case "windows", "darwin", "linux":
		if st.Backend == BackendUnavailable {
			t.Fatalf("%s 上不该报成 %q", runtime.GOOS, st.Backend)
		}
	default:
		if st.Backend != BackendUnavailable {
			t.Fatalf("%s 上后端名应为 %q，实际 %q", runtime.GOOS, BackendUnavailable, st.Backend)
		}
	}
}
