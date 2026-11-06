package run

// C06f：真实 Pi 1.5.0 的澄清端到端验证。
//
// 与上面那些替身测试不同，这里跑的是**真的 Pi 进程**（官方独立包）与**真的产品扩展**
// （pi-extensions/anvil-clarification.ts），受控 provider（openaisse fixture）真的
// 让模型发起一次 `anvil_ask_user` 调用。因此它回答的是一组替身回答不了的问题：
//   - Pi 会不会真的把扩展工具的 ctx.ui.* 变成 stdout 上的 extension_ui_request；
//   - 我们回过去的 extension_ui_response 会不会真的解开那个对话框；
//   - 工具结果会不会真的回到模型（transcript 里能看到回答）；
//   - 整条链路上会不会意外产生一次审批（澄清不是审批）。
//
// 运行条件：本机存在 .tools/pi/standalone/v1.5.0-win-x64/pi.exe（只读引用，从不写入）。
// 没有这个参考二进制时用 t.Skip 说明原因——这是本仓库所有真实 Pi 测试的统一口径
// （见 internal/runtime/pi/contract_realpi_test.go）；本机有二进制时它**默认就运行**，
// 不靠环境变量开后门。

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/fixtures/openaisse"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/runtime/pi"
)

// repoRootForRun 从 internal/run 回溯到仓库根。
func repoRootForRun(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录: %v", err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("定位仓库根失败（%s 下没有 go.mod）: %v", root, err)
	}
	return root
}

// standalonePiExe 返回官方独立包的 pi.exe；不存在则跳过（本机已有则为默认运行）。
func standalonePiExe(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("C06f 的真实 Pi 端到端按 Windows 独立包编写")
	}
	exe := filepath.Join(repoRootForRun(t), ".tools", "pi", "standalone", "v1.5.0-win-x64", "pi.exe")
	if _, err := os.Stat(exe); err != nil {
		t.Skipf("本机没有参考 Pi 独立包（%s）；这不是被测行为失败，是环境缺失: %v", exe, err)
	}
	return exe
}

// writeFixtureModelsForRun 在隔离 agentDir 里写 models.json，把 Pi 指向本地 fixture。
func writeFixtureModelsForRun(t *testing.T, agentDir, baseURL string) {
	t.Helper()
	doc := map[string]any{
		"$schema": "https://pi.dev/schemas/models.schema.json",
		"providers": map[string]any{
			"fixture": map[string]any{
				"baseUrl": baseURL + "/v1",
				"api":     "openai-completions",
				"apiKey":  "fixture-key",
				"models": []map[string]any{{
					"id":            "fixture-model",
					"name":          "Fixture",
					"input":         []string{"text"},
					"contextWindow": 8000,
					"maxTokens":     2048,
					"reasoning":     false,
					"cost":          map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
				}},
			},
		},
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("序列化 models.json: %v", err)
	}
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("建 agentDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), raw, 0o600); err != nil {
		t.Fatalf("写 models.json: %v", err)
	}
}

// TestRealPiClarificationEndToEnd 是 C06f 的真实 Pi 证据。
func TestRealPiClarificationEndToEnd(t *testing.T) {
	exe := standalonePiExe(t)
	root := repoRootForRun(t)
	work := t.TempDir()
	agentDir := filepath.Join(work, "agent")
	workspace := filepath.Join(work, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("建工作目录: %v", err)
	}

	// 受控 provider：脚本化回放，模型会请求一次 anvil_ask_user(select)。
	srv := httptest.NewServer(openaisse.NewHandler(filepath.Join(work, "requests")))
	defer srv.Close()
	writeFixtureModelsForRun(t, agentDir, srv.URL)

	clarificationExt := filepath.Join(root, "pi-extensions", "anvil-clarification.ts")
	policyExt := filepath.Join(root, "pi-extensions", "harness-policy.ts")
	for _, p := range []string{clarificationExt, policyExt} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("产品扩展缺失: %s: %v", p, err)
		}
	}

	guard, err := policy.NewPathGuard(workspace)
	if err != nil {
		t.Fatalf("构造路径守卫: %v", err)
	}
	opts := Options{
		WorkspaceID:  "ws-realpi",
		SessionID:    "sess-realpi-clarify",
		WorkspaceDir: workspace,
		AgentDir:     agentDir,
		Installation: pi.PiInstallation{
			ID: "test:pi-standalone", Executable: exe, Version: "1.5.0",
		},
		Isolation: pi.IsolationSpec{
			PolicyExtension: policyExt,
			HarnessMCP:      false,
			ExtraExtensions: []string{clarificationExt},
		},
		Provider:       "fixture",
		Model:          "fixture-model",
		ConfigRevision: 1,
		Policy: policy.New(policy.Options{
			Permissions: config.Permissions{Filesystem: "workspace"},
			PathGuard:   guard,
			ApprovalTTL: time.Minute,
		}),
		PolicyCeiling:  PolicyCeiling{Permissions: config.Permissions{Filesystem: "workspace"}},
		PolicySnapshot: PolicySnapshot{Permissions: config.Permissions{Filesystem: "workspace"}},
		Clarification:  ClarificationOptions{Enabled: true, ExtensionPath: clarificationExt, Timeout: 30 * time.Second},
		Now:            time.Now,
		// 真实 Pi 启动比替身慢：给足关闭宽限，避免拖慢但也不误判。
		StopGrace:    2 * time.Second,
		StopDeadline: 15 * time.Second,
	}
	svc := New(opts)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	snap, err := svc.Start(context.Background(), StartRunRequest{
		Message: "请提问：[[scenario:clarify-select]]",
	})
	if err != nil {
		t.Fatalf("启动真实 Pi run 失败: %v", err)
	}

	// 1) 真实 Pi 必须把扩展工具的 ctx.ui.select 变成 extension_ui_request → 澄清事件。
	payload := waitClarificationRequested(t, svc, snap.RunID)
	if got := strings.TrimSpace(fmtString(payload["kind"])); got != "select" {
		t.Fatalf("kind 应为 select，实际 %q（载荷 %v）", got, payload)
	}
	if !strings.Contains(fmtString(payload["question"]), "需要你确认这次澄清的问题") {
		t.Fatalf("question 应原样到达（且已去掉身份前缀），实际 %q", payload["question"])
	}
	options, _ := payload["options"].([]any)
	if len(options) != 2 || fmtString(options[0]) != "staging" || fmtString(options[1]) != "prod" {
		t.Fatalf("options 应该是 Pi 真实带出的选项，实际 %v", payload["options"])
	}
	requestID := fmtString(payload["requestId"])
	if requestID == "" {
		t.Fatalf("requestId 为空：无法回答，载荷 %v", payload)
	}

	// 2) Go 侧回答 → 真实 Pi 必须把 extension_ui_response 交回扩展。
	if err := svc.AnswerClarification(context.Background(), snap.SessionID, requestID,
		ClarificationResponse{Value: &ClarificationValue{Text: "prod"}}); err != nil {
		t.Fatalf("回答澄清失败: %v", err)
	}

	// 3) 工具结果必须真的回到模型：transcript 里要有工具结束事件与答案文本，
	//    随后 provider 的 follow-up 文本出现，本轮正常完成。
	if state := waitTerminal(t, svc, snap.RunID); state != contracts.RunStateCompleted {
		t.Fatalf("run 应 completed，实际 %s（事件：%s）", state, eventTypes(t, svc, snap.RunID))
	}
	var sawToolEnd bool
	var toolText string
	for _, ev := range eventsOfType(t, svc, snap.RunID, EventToolEnd) {
		var p struct {
			ToolName string `json:"toolName"`
			Text     string `json:"text"`
			IsError  bool   `json:"isError"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			continue
		}
		if p.ToolName != clarificationToolName {
			continue
		}
		sawToolEnd = true
		toolText = p.Text
		if p.IsError {
			t.Fatalf("澄清工具以错误结束：%s", p.Text)
		}
	}
	if !sawToolEnd {
		t.Fatalf("没有看到 anvil_ask_user 的工具结束事件（事件：%s）", eventTypes(t, svc, snap.RunID))
	}
	if !strings.Contains(toolText, "prod") {
		t.Fatalf("工具结果里应含用户回答 %q，实际 %q", "prod", toolText)
	}

	// 4) 澄清不是审批：整轮零审批请求。
	if evs := eventsOfType(t, svc, snap.RunID, EventApprovalRequest); len(evs) != 0 {
		t.Fatalf("真实 Pi 的澄清链路产生了 %d 条审批请求（澄清不该走审批）", len(evs))
	}

	// 5) 收尾事件里应有 resolved/value，便于 UI 关掉卡片。
	resolved := eventsOfType(t, svc, snap.RunID, EventClarificationResolved)
	if len(resolved) == 0 {
		t.Fatalf("没有 clarification.resolved（事件：%s）", eventTypes(t, svc, snap.RunID))
	}
}

// fmtString 把任意 JSON 值转成字符串（断言用，不做格式化）。
func fmtString(v any) string {
	switch got := v.(type) {
	case nil:
		return ""
	case string:
		return got
	default:
		raw, err := json.Marshal(got)
		if err != nil {
			return ""
		}
		return string(raw)
	}
}
