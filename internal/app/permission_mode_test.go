package app

// 权限档位在绑定层的口径（规格 docs/specs/2026-11-08-permission-mode-full-access.md）。
//
// 这一层回答三个问题：
//   1. 开关持久化走既有 settings CAS，默认是 ask；
//   2. 档位在 run 受理时读一次并进 ExecutionBinding，运行中改设置只影响下一条 run，
//      而且**不重建运行时**（重建会打断正在跑的那条 run）；
//   3. 定时任务即使在 full_access 下也按 ask 受理（无人值守）。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/runtime/pi"
)

// TestPermissionModeDefaultsToAskAndPersistsThroughCAS 覆盖 Settings 的默认值与落盘。
func TestPermissionModeDefaultsToAskAndPersistsThroughCAS(t *testing.T) {
	d, _ := newTestDesktop(t)

	if got := d.Settings().PermissionMode; got != config.PermissionModeAsk {
		t.Fatalf("默认档位必须是 ask（逐次审批），实际 %q", got)
	}

	res := d.SetPermissionMode(config.PermissionModeFullAccess)
	if res.Error != nil {
		t.Fatalf("开启完全访问失败: %s: %s", res.Error.Code, res.Error.Message)
	}
	if res.Settings.PermissionMode != config.PermissionModeFullAccess {
		t.Fatalf("返回的设置里档位应已生效，实际 %q", res.Settings.PermissionMode)
	}

	// 真实落盘：restart 之后仍然是 full_access（关闭窗口不改变设置）。
	raw, err := os.ReadFile(filepath.Join(d.root, "settings.json"))
	if err != nil {
		t.Fatalf("读取 settings.json: %v", err)
	}
	var onDisk struct {
		PermissionMode string `json:"permissionMode"`
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("settings.json 不是合法 JSON: %v", err)
	}
	if onDisk.PermissionMode != config.PermissionModeFullAccess {
		t.Fatalf("settings.json 里应写出 full_access，实际 %q", onDisk.PermissionMode)
	}

	again, err := New(d.root)
	if err != nil {
		t.Fatalf("重启后构造 Desktop: %v", err)
	}
	t.Cleanup(again.Shutdown)
	if got := again.Settings().PermissionMode; got != config.PermissionModeFullAccess {
		t.Fatalf("重启后档位应保持 full_access，实际 %q", got)
	}

	// 认不出的档位既不能写盘，也不能被当成更宽的规则。
	if bad := d.SetPermissionMode("yolo"); bad.Error == nil {
		t.Fatal("未知档位必须被拒")
	} else if bad.Error.Code != contracts.CodeInvalidInput {
		t.Fatalf("未知档位应报 %s，实际 %s", contracts.CodeInvalidInput, bad.Error.Code)
	}
	if got := d.Settings().PermissionMode; got != config.PermissionModeFullAccess {
		t.Fatalf("被拒的写入不得改变生效档位，实际 %q", got)
	}

	// 关回 ask 同样立即生效并落盘。
	if res := d.SetPermissionMode(config.PermissionModeAsk); res.Error != nil {
		t.Fatalf("关闭完全访问失败: %s: %s", res.Error.Code, res.Error.Message)
	}
	if got := d.Settings().PermissionMode; got != config.PermissionModeAsk {
		t.Fatalf("关闭后档位应为 ask，实际 %q", got)
	}
}

// TestAutoAllowedCallLandsInRunEvents 是“跳过审批 ≠ 不留痕”的端到端证据：
//
//	full_access → 真实的策略端点（worker 手里的 URL+令牌）收到一次 write 授权请求
//	→ allow → policy.auto_allowed 经**同一个事件 sink** 落进 run_events。
//
// 这里不拿运行内部字段冒充证据：请求进的是真实 HTTP 端点，落库的是真实 run_events 行。
func TestAutoAllowedCallLandsInRunEvents(t *testing.T) {
	d := prepareRuntimeDesktop(t)
	work := d.Settings().Workspace
	ws := d.CreateWorkspace(work)
	if ws.Error != nil {
		t.Fatalf("登记工作区失败: %s: %s", ws.Error.Code, ws.Error.Message)
	}
	if res := d.SetWorkspaceTrust(string(ws.Item.ID), true); res.Error != nil {
		t.Fatalf("信任工作区失败: %s: %s", res.Error.Code, res.Error.Message)
	}

	var (
		mu   sync.Mutex
		envs [][]string
	)
	d.mu.Lock()
	d.workerStarter = func(_ context.Context, spec pi.WorkerSpec) (pi.Worker, error) {
		mu.Lock()
		envs = append(envs, append([]string(nil), spec.Env...))
		mu.Unlock()
		return newC06aWorker(), nil
	}
	d.mu.Unlock()

	if res := d.SetPermissionMode(config.PermissionModeFullAccess); res.Error != nil {
		t.Fatalf("开启完全访问失败: %s: %s", res.Error.Code, res.Error.Message)
	}
	snap, err := d.Start("让一次写动作被自动放行")
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}

	mu.Lock()
	if len(envs) == 0 {
		mu.Unlock()
		t.Fatal("run 没有启动 worker，拿不到策略端点")
	}
	workerEnv := envs[0]
	mu.Unlock()
	policyURL, okURL := c06aEnvValue(workerEnv, "MYAGENT_POLICY_URL")
	policyToken, okToken := c06aEnvValue(workerEnv, "MYAGENT_POLICY_TOKEN")
	if !okURL || !okToken {
		t.Fatalf("worker 环境里没有策略端点（URL=%v TOKEN=%v）", okURL, okToken)
	}

	body, _ := json.Marshal(map[string]any{
		"toolCallId":    "call-auto-write",
		"toolName":      "write",
		"source":        "builtin",
		"origin":        "user",
		"arguments":     map[string]any{"path": "notes.md", "content": "hi"},
		"resourceScope": filepath.Join(work, "notes.md"),
	})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, policyURL+"/v1/authorize", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("构造授权请求: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+policyToken)
	// 会话头是端点的第三道闸：令牌证明“属于这条 run”，会话头证明“属于这个会话”。
	req.Header.Set("X-MyAgent-Session", string(snap.SessionID))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("调用策略端点: %v", err)
	}
	defer resp.Body.Close()
	var decision struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decision); err != nil {
		t.Fatalf("解析授权响应: %v", err)
	}
	if resp.StatusCode != http.StatusOK || decision.Decision != "allow" {
		t.Fatalf("full_access 下写动作应被放行，实际 HTTP %d %+v", resp.StatusCode, decision)
	}

	// 留痕：run_events 里必须有一条 policy.auto_allowed，且能定位到 run/会话/工具。
	db, err := d.indexDB()
	if err != nil {
		t.Fatalf("indexDB: %v", err)
	}
	var payload string
	readRow := func() bool {
		return db.SQL().QueryRowContext(context.Background(),
			`SELECT payload FROM run_events WHERE run_id = ? AND type = ?`,
			string(snap.RunID), "policy.auto_allowed").Scan(&payload) == nil
	}
	if !eventuallyTrue(5*time.Second, readRow) {
		t.Fatalf("run_events 里没有 policy.auto_allowed（跳过了审批却没留痕）")
	}
	var fact struct {
		RunID          string `json:"runId"`
		SessionID      string `json:"sessionId"`
		ToolName       string `json:"toolName"`
		PermissionMode string `json:"permissionMode"`
	}
	if err := json.Unmarshal([]byte(payload), &fact); err != nil {
		t.Fatalf("事件载荷不是合法 JSON: %v（%s）", err, payload)
	}
	if fact.ToolName != "write" || fact.RunID != string(snap.RunID) || fact.SessionID != string(snap.SessionID) {
		t.Fatalf("自动放行记录应指明 run/session/工具，实际 %+v", fact)
	}
	if fact.PermissionMode != config.PermissionModeFullAccess {
		t.Fatalf("记录里应说明生效档位，实际 %q", fact.PermissionMode)
	}
}

// TestUnreadablePermissionModeFallsBackToAskOnLoad：磁盘上写了认不出的值时按 ask 加载。
//
// 方向只能是“更严”：读不懂的配置不能让人以为还开着完全访问，更不能被当成 full_access。
func TestUnreadablePermissionModeFallsBackToAskOnLoad(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "workspace")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatalf("建工作目录: %v", err)
	}
	raw := `{"workspace":` + quoteJSON(work) + `,"permissionMode":"yolo"}`
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(raw), 0o600); err != nil {
		t.Fatalf("写 settings.json: %v", err)
	}

	d, err := New(root)
	if err != nil {
		t.Fatalf("构造 Desktop: %v", err)
	}
	t.Cleanup(d.Shutdown)
	if got := d.Settings().PermissionMode; got != config.PermissionModeAsk {
		t.Fatalf("认不出的档位必须退回 ask，实际 %q", got)
	}
	if d.LastError() == "" {
		t.Fatal("退回 ask 必须留下原因（不能静默改写规则）")
	}
}

// quoteJSON 是测试里拼 JSON 的小工具（避免手写转义）。
func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestFullAccessPinsAtAdmissionAndOnlyAffectsNextRun 是这次增量的核心断言：
//
//	设置 full_access → 起一条 run（binding=full_access）→ 运行中改回 ask
//	→ 该 run 的 binding 仍是 full_access，新起的 run 是 ask。
//
// 同时钉住“不重建运行时”：切换开关若走 resetRuntime，正在跑的 run 会直接被打断，
// 也就没有“运行中切换”这回事了。
func TestFullAccessPinsAtAdmissionAndOnlyAffectsNextRun(t *testing.T) {
	d := prepareRuntimeDesktop(t)
	work := d.Settings().Workspace
	ws := d.CreateWorkspace(work)
	if ws.Error != nil {
		t.Fatalf("登记工作区失败: %s: %s", ws.Error.Code, ws.Error.Message)
	}
	if res := d.SetWorkspaceTrust(string(ws.Item.ID), true); res.Error != nil {
		t.Fatalf("信任工作区失败: %s: %s", res.Error.Code, res.Error.Message)
	}
	d.mu.Lock()
	d.workerStarter = func(context.Context, pi.WorkerSpec) (pi.Worker, error) {
		return newC06aWorker(), nil
	}
	d.mu.Unlock()

	if res := d.SetPermissionMode(config.PermissionModeFullAccess); res.Error != nil {
		t.Fatalf("开启完全访问失败: %s: %s", res.Error.Code, res.Error.Message)
	}
	first, err := d.Start("第一条：完全访问")
	if err != nil {
		t.Fatalf("第一条提交失败: %v", err)
	}
	svc := mustRuntime(t, d)
	if first.State != contracts.RunStateRunning {
		t.Fatalf("第一条应已在跑，实际 %s", first.State)
	}

	// 运行中切回 ask：既不能重建运行时，也不能改变已经在跑的那条 run。
	if res := d.SetPermissionMode(config.PermissionModeAsk); res.Error != nil {
		t.Fatalf("关闭完全访问失败: %s: %s", res.Error.Code, res.Error.Message)
	}
	if again := mustRuntime(t, d); again != svc {
		t.Fatal("切换档位重建了运行时：正在跑的 run 会被打断，“运行中切换只影响下一条”无从成立")
	}

	second, err := d.Start("第二条：询问")
	if err != nil {
		t.Fatalf("第二条提交失败: %v", err)
	}
	if second.SessionID != first.SessionID {
		// 两条交互 run 属于同一会话运行时（同一 admission queue）；会话不同不影响本用例结论。
		t.Logf("两条 run 的会话不同: %s / %s", first.SessionID, second.SessionID)
	}

	firstBinding, ok := svc.Binding(first.RunID)
	if !ok {
		t.Fatal("找不到第一条 run 的 binding")
	}
	secondBinding, ok := svc.Binding(second.RunID)
	if !ok {
		t.Fatal("找不到第二条 run 的 binding")
	}
	if firstBinding.PermissionMode != config.PermissionModeFullAccess || !firstBinding.AutoApproves() {
		t.Fatalf("第一条 run 的档位应钉成 full_access，实际 %q", firstBinding.PermissionMode)
	}
	if secondBinding.PermissionMode != config.PermissionModeAsk || secondBinding.AutoApproves() {
		t.Fatalf("第二条 run 的档位应钉成 ask，实际 %q", secondBinding.PermissionMode)
	}
	// 第一条 run 仍在跑（切换没有把它结算掉）。
	snap, err := svc.Get(first.RunID)
	if err != nil {
		t.Fatalf("读取第一条 run: %v", err)
	}
	if snap.State != contracts.RunStateRunning {
		t.Fatalf("切档位不得打断正在跑的 run，实际状态 %s", snap.State)
	}
}

// TestScheduledRunAsksEvenWhenSettingIsFullAccess：定时任务在 full_access 下仍按 ask。
//
// 断言的是**run 的绑定快照**（走真实 RunService 与真实调度派发），不是日志。
func TestScheduledRunAsksEvenWhenSettingIsFullAccess(t *testing.T) {
	d, _, wsID := scheduleFixture(t)
	d.mu.Lock()
	d.workerStarter = func(context.Context, pi.WorkerSpec) (pi.Worker, error) {
		return newC06aWorker(), nil
	}
	d.mu.Unlock()
	if res := d.SetPermissionMode(config.PermissionModeFullAccess); res.Error != nil {
		t.Fatalf("开启完全访问失败: %s: %s", res.Error.Code, res.Error.Message)
	}

	created := saveSchedule(t, d, ScheduleSaveRequest{
		Name: "夜间任务", Cron: "0 3 * * *", TimeZone: "Etc/UTC",
		WorkspaceID: wsID, Prompt: "生成本地周报", Enabled: true,
	})
	fired := d.RunScheduleNow(created.ID)
	if fired.Error != nil {
		t.Fatalf("立即运行失败: %s: %s", fired.Error.Code, fired.Error.Message)
	}
	if fired.Item.RunID == "" {
		t.Fatalf("调度没有派发出 run：%+v", fired.Item)
	}
	svc := mustRuntime(t, d)
	binding, ok := svc.Binding(fired.Item.RunID)
	if !ok {
		t.Fatalf("找不到 run %s 的 binding", fired.Item.RunID)
	}
	if binding.PermissionMode != config.PermissionModeAsk || binding.AutoApproves() {
		t.Fatalf("无人值守 run 即使设置为 full_access 也必须按 ask，实际 %q", binding.PermissionMode)
	}

	// 对照：同一条运行时上，交互提交仍然按设置拿到 full_access。
	attended, err := d.Start("交互提交：按设置")
	if err != nil {
		t.Fatalf("交互提交失败: %v", err)
	}
	if b, _ := svc.Binding(attended.RunID); !b.AutoApproves() {
		t.Fatalf("交互 run 应按设置使用 full_access，实际 %q", b.PermissionMode)
	}
}
