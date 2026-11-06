package policybridge

// C06f：本地无副作用工具 catalog 的失败测试。
//
// 这里守的是一条容易做错的边界：**身份，不是名字**。
//   - 产品自带的那条工具（身份=产品扩展路径）不该变成一次审批；
//   - 第三方注册的同名工具必须照常走 policy.Service（ask/deny），
//     不能因为名字相同就被当成"本地无副作用"。
//
// 因此同一份请求会跑两遍：只有 toolIdentity 不同。结论必须不同。

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/policy"
)

// localCatalog 构造一份"只有产品澄清工具命中"的 catalog。
func localCatalog(productExt string) func(call policy.ToolCall, identity string) (policy.Result, bool) {
	return func(call policy.ToolCall, identity string) (policy.Result, bool) {
		if call.Name != "anvil_ask_user" || call.Source != policy.SourceExtension {
			return policy.Result{}, false
		}
		if identity == "" || identity != productExt {
			return policy.Result{}, false
		}
		return policy.Result{Decision: policy.DecisionAllow, Reason: "local clarification"}, true
	}
}

// newLocalToolsServer 起一个带 catalog 与真策略服务的端点。
func newLocalToolsServer(t *testing.T, productExt string) (*Server, *policy.Service) {
	t.Helper()
	svc := policy.New(policy.Options{
		Permissions: config.Permissions{Filesystem: "workspace"},
		PathGuard:   nil,
		ApprovalTTL: time.Minute,
	})
	srv, err := Start(context.Background(), Options{
		Service: svc,
		Binding: Binding{
			SessionID: "sess-local", RunID: "run-local",
			ConfigRevision: 1, ControlGeneration: 1,
		},
		AskTimeout: 500 * time.Millisecond,
		LocalTools: localCatalog(productExt),
	})
	if err != nil {
		t.Fatalf("启动端点: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close(context.Background()) })
	return srv, svc
}

func tokenOf(srv *Server) string {
	for _, kv := range srv.Env() {
		if len(kv) > len("MYAGENT_POLICY_TOKEN=") && kv[:len("MYAGENT_POLICY_TOKEN=")] == "MYAGENT_POLICY_TOKEN=" {
			return kv[len("MYAGENT_POLICY_TOKEN="):]
		}
	}
	return ""
}

// TestLocalToolsCatalogMatchesIdentityNotName 是这一组的主断言。
func TestLocalToolsCatalogMatchesIdentityNotName(t *testing.T) {
	productExt := `/repo/pi-extensions/anvil-clarification.ts`
	srv, _ := newLocalToolsServer(t, productExt)
	client := NewClient(srv.Endpoint(), tokenOf(srv), contracts.SessionID("sess-local"), 5*time.Second)

	call := policy.ToolCall{
		ID: "c-1", Name: "anvil_ask_user", Source: policy.SourceExtension,
		Arguments: json.RawMessage(`{"question":"q","kind":"input"}`),
	}

	// 产品身份：直接 allow，且不产生审批。
	dec, _, err := client.AuthorizeWithIdentity(context.Background(), call, productExt)
	if err != nil {
		t.Fatalf("产品身份请求失败: %v", err)
	}
	if dec != policy.DecisionAllow {
		t.Fatalf("产品自带工具的同一身份应 allow，实际 %s", dec)
	}

	// 第三方同名工具：身份不同 → 照常走策略（这里会 ask 并等到 500ms 超时 → deny）。
	dec, reason, err := client.AuthorizeWithIdentity(context.Background(), call, `/tmp/vendor/anvil-clarification.ts`)
	if err != nil {
		t.Fatalf("第三方请求失败: %v", err)
	}
	if dec != policy.DecisionDeny {
		t.Fatalf("第三方同名工具必须照常走 policy（无人审批 → deny），实际 %s（%s）", dec, reason)
	}

	// 没报身份：同样走策略，不能默认命中 catalog。
	dec, _, err = client.Authorize(context.Background(), call)
	if err != nil {
		t.Fatalf("空身份请求失败: %v", err)
	}
	if dec != policy.DecisionDeny {
		t.Fatalf("没报身份的调用仍须走 policy，实际 %s", dec)
	}
}

// TestLocalToolsDoesNotBypassGate 守"硬闸门先于本地 catalog"：
// 预算/停机这类判定不能因为"这是本地工具"被跳过。
func TestLocalToolsDoesNotBypassGate(t *testing.T) {
	productExt := `/repo/pi-extensions/anvil-clarification.ts`
	svc := policy.New(policy.Options{
		Permissions: config.Permissions{Filesystem: "workspace"},
		ApprovalTTL: time.Minute,
	})
	srv, err := Start(context.Background(), Options{
		Service: svc,
		Binding: Binding{SessionID: "sess-gate", RunID: "run-gate", ConfigRevision: 1, ControlGeneration: 1},
		Gate: func(ctx context.Context, call policy.ToolCall) error {
			return contracts.NewAppError(contracts.CodeBudgetExceeded, "预算已用尽", false)
		},
		LocalTools: localCatalog(productExt),
	})
	if err != nil {
		t.Fatalf("启动端点: %v", err)
	}
	defer func() { _ = srv.Close(context.Background()) }()

	client := NewClient(srv.Endpoint(), tokenOf(srv), contracts.SessionID("sess-gate"), 5*time.Second)
	dec, reason, err := client.AuthorizeWithIdentity(context.Background(), policy.ToolCall{
		ID: "c-1", Name: "anvil_ask_user", Source: policy.SourceExtension,
		Arguments: json.RawMessage(`{"question":"q","kind":"input"}`),
	}, productExt)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if dec != policy.DecisionDeny {
		t.Fatalf("闸门先于 catalog：预算用尽时应 deny，实际 %s（%s）", dec, reason)
	}
}
