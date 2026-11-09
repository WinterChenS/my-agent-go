package app

// MDIAG：模型配置诊断。
//
// 用户实测的困惑：模型能保存，启动却只说「读不到有效配置：缺少必要的配置」。
// 这句话对"没设为当前"、"编译时被跳过"、"凭证引用悬空"完全一样，而三种的
// 修复动作不同。这些用例守三件事：
//  1. 诊断把每条模型的状态与**原因**说出来（不重写编译规则，复用同一份）；
//  2. 正常模型仍然判 ready，且 models.json 还没落盘不算不可用；
//  3. Start 的失败消息里带着同一句原因，用户不用再来回试。

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
)

// trustDefaultWorkspace 过掉 Start 的第一道闸门（工作区已信任）。
func trustDefaultWorkspace(t *testing.T, d *Desktop) {
	t.Helper()
	cur := d.CurrentWorkspace()
	if cur.Error != nil {
		t.Fatalf("登记工作区: %s", cur.Error.Message)
	}
	if tr := d.SetWorkspaceTrust(string(cur.Item.ID), true); tr.Error != nil {
		t.Fatalf("信任工作区: %s", tr.Error.Message)
	}
}

// TestDiagnoseModelConfigReportsDanglingCredential：凭证引用悬空必须判不可用，
// 且 Skipped 带出这条模型与原因（提到凭证引用名，便于用户去重存密钥）。
func TestDiagnoseModelConfigReportsDanglingCredential(t *testing.T) {
	d, _ := newTestDesktop(t)

	def := demoModel()
	def.CredentialRef = "cred-gone"
	if saved := d.SaveModel(0, def, "{{API_KEY_diag2}}"); saved.Error != nil {
		t.Fatalf("保存模型: %s: %s", saved.Error.Code, saved.Error.Message)
	}
	if act := d.SetActiveModel("m-demo"); act.Error != nil {
		t.Fatalf("设为当前: %s", act.Error.Message)
	}

	// 凭证在保存之后消失（换机器、清过凭证库、改过 ref 都会导致这个）。
	if _, err := d.resourceServices(); err != nil {
		t.Fatalf("取资源服务: %v", err)
	}
	if d.creds == nil {
		t.Fatal("测试需要一个可用凭证库")
	}
	if err := d.creds.Delete(context.Background(), "cred-gone"); err != nil {
		t.Fatalf("删除凭证: %v", err)
	}

	diag := d.DiagnoseModelConfig()
	if diag.Error != nil {
		t.Fatalf("诊断不该整份失败: %s: %s", diag.Error.Code, diag.Error.Message)
	}
	if diag.Ready {
		t.Fatalf("凭证悬空时必须判不可用，实际 ready=true（reason=%q）", diag.Reason)
	}
	if diag.Reason == "" {
		t.Fatal("不可用时必须给出一句原因")
	}
	if diag.SelectedRef != "m-demo" || diag.SelectedSource != "resources" {
		t.Fatalf("选中的模型不对: ref=%q source=%q", diag.SelectedRef, diag.SelectedSource)
	}
	// 只给引用，不给密钥：诊断结果里不允许出现密钥值。
	if diag.Selected == nil || diag.Selected.CredentialRef != "cred-gone" {
		t.Fatalf("选中模型应只带凭证引用，实际 %+v", diag.Selected)
	}

	if len(diag.Skipped) != 1 || diag.Skipped[0].ModelID != "m-demo" {
		t.Fatalf("Skipped 应含 m-demo 及原因，实际 %+v", diag.Skipped)
	}
	if !strings.Contains(diag.Skipped[0].Reason, "cred-gone") {
		t.Fatalf("跳过原因要提到悬空的凭证引用，实际 %q", diag.Skipped[0].Reason)
	}
	if len(diag.Models) != 1 || diag.Models[0].Status != "skipped" || diag.Models[0].Reason == "" {
		t.Fatalf("逐模型状态应为 skipped+原因，实际 %+v", diag.Models)
	}
	if diag.Compiled != 0 {
		t.Fatalf("悬空凭证的模型不该算进 compiled，实际 %d", diag.Compiled)
	}
	if diag.ModelsJSONPath == "" || !strings.HasSuffix(diag.ModelsJSONPath, "models.json") {
		t.Fatalf("要给出 models.json 的真实路径，实际 %q", diag.ModelsJSONPath)
	}

	raw, err := json.Marshal(diag)
	if err != nil {
		t.Fatalf("序列化诊断: %v", err)
	}
	if strings.Contains(string(raw), "{{API_KEY_diag2}}") {
		t.Fatal("诊断结果里出现明文密钥")
	}
}

// TestDiagnoseModelConfigReadyWithOneCompiled：正常模型 → ready、compiled=1。
// models.json 尚未落盘**不算**不可用（下一次启动才会写）。
func TestDiagnoseModelConfigReadyWithOneCompiled(t *testing.T) {
	d, _ := newTestDesktop(t)
	if saved := d.SaveModel(0, demoModel(), ""); saved.Error != nil {
		t.Fatalf("保存模型: %s: %s", saved.Error.Code, saved.Error.Message)
	}
	if act := d.SetActiveModel("m-demo"); act.Error != nil {
		t.Fatalf("设为当前: %s", act.Error.Message)
	}

	// 设为当前模型会顺手重写一次 models.json（覆盖可能残留的明文密钥）。
	// 这里先把它删掉，验证"文件还没生成"不会被误判成不可用。
	path := filepath.Join(d.agentDir(), "models.json")
	if err := os.Remove(path); err != nil {
		t.Fatalf("删除 models.json: %v", err)
	}

	diag := d.DiagnoseModelConfig()
	if diag.Error != nil {
		t.Fatalf("诊断: %s: %s", diag.Error.Code, diag.Error.Message)
	}
	if !diag.Ready {
		t.Fatalf("正常模型应判 ready，实际 reason=%q", diag.Reason)
	}
	if diag.Reason != "" {
		t.Fatalf("ready 时不该有原因，实际 %q", diag.Reason)
	}
	if diag.Compiled != 1 {
		t.Fatalf("compiled 应为 1，实际 %d", diag.Compiled)
	}
	if len(diag.Skipped) != 0 {
		t.Fatalf("正常模型不该进 Skipped，实际 %+v", diag.Skipped)
	}
	if diag.ModelsJSONExists {
		t.Fatal("还没启动过，models.json 不该已经存在（这个用例要证明它不影响 ready）")
	}
	if diag.Selected == nil || diag.Selected.ProviderID != "demo" || diag.Selected.ModelID != "demo-model" {
		t.Fatalf("选中模型的 provider/model 不对: %+v", diag.Selected)
	}
	if diag.Selected.BaseURL != "http://127.0.0.1:8080/v1" || diag.Selected.API != "openai-completions" {
		t.Fatalf("选中模型的端点/协议不对: %+v", diag.Selected)
	}
}

// TestStartReportsSkippedReasonWhenNothingSelected：最容易被当成"保存失败"的那种——
// 模型存进去了，却没设为当前模型，而且它还是旧格式被跳过。
// Start 的错误必须把"未设为当前"和"为什么跳过"一起说出来。
func TestStartReportsSkippedReasonWhenNothingSelected(t *testing.T) {
	d, _ := newTestDesktop(t)
	trustDefaultWorkspace(t, d)

	// 真实现场：旧的四个设置字段是空的（用户只有资源库里的模型），
	// 于是 Start 走 legacyModelSetup 并回 CONFIG_MISSING。
	d.mu.Lock()
	d.settings.BaseURL = ""
	d.settings.Model = ""
	d.mu.Unlock()

	// 直接写配置：Save 会拒绝旧格式（没 providerId/modelId）的条目，
	// 但磁盘上真的存在这种历史数据，而它们正是被跳过的那批。
	store, err := config.NewStore(filepath.Join(d.root, "config"))
	if err != nil {
		t.Fatalf("打开配置: %v", err)
	}
	doc, err := store.Load(context.Background(), config.KeyUser)
	if err != nil {
		t.Fatalf("读配置: %v", err)
	}
	legacy, err := json.Marshal(map[string]any{
		"schemaVersion": 1,
		"models": []json.RawMessage{
			json.RawMessage(`{"id":"m-old","provider":"deepseek","baseUrl":"http://127.0.0.1:1/v1","model":"x"}`),
		},
	})
	if err != nil {
		t.Fatalf("序列化: %v", err)
	}
	if _, err := store.CompareAndSwap(context.Background(), config.KeyUser, doc.Revision, legacy); err != nil {
		t.Fatalf("写入旧格式配置: %v", err)
	}
	// 资源服务自带缓存，重建一个以便读到刚写的配置。
	d.svcMu.Lock()
	d.modelSvc = nil
	d.svcMu.Unlock()
	if _, err := d.resourceServices(); err != nil {
		t.Fatalf("重建资源服务: %v", err)
	}

	_, startErr := d.Start("你好")
	if startErr == nil {
		t.Fatal("没有可用模型时必须拒绝启动")
	}
	var appErr *contracts.AppError
	if !errors.As(startErr, &appErr) {
		t.Fatalf("期望契约错误，实际 %T: %v", startErr, startErr)
	}
	if appErr.Code != contracts.CodeConfigMissing {
		t.Fatalf("期望 CONFIG_MISSING，实际 %s: %s", appErr.Code, appErr.Message)
	}
	if !strings.Contains(appErr.Message, "m-old") {
		t.Fatalf("错误消息要点名被跳过的模型，实际 %q", appErr.Message)
	}
	if !strings.Contains(appErr.Message, "provider") {
		t.Fatalf("错误消息要带跳过原因（缺 provider 名），实际 %q", appErr.Message)
	}
	if !strings.Contains(appErr.Message, "设为当前模型") {
		t.Fatalf("错误消息要说清缺少的一步是“设为当前模型”，实际 %q", appErr.Message)
	}
}

// TestStartReportsDanglingCredential：选中的模型凭证悬空时，启动失败消息必须
// 带着那条凭证引用，而不是一句通用的配置缺失。
func TestStartReportsDanglingCredential(t *testing.T) {
	d, _ := newTestDesktop(t)
	trustDefaultWorkspace(t, d)

	def := demoModel()
	def.CredentialRef = "cred-gone2"
	if saved := d.SaveModel(0, def, "{{API_KEY_diag3}}"); saved.Error != nil {
		t.Fatalf("保存模型: %s: %s", saved.Error.Code, saved.Error.Message)
	}
	if act := d.SetActiveModel("m-demo"); act.Error != nil {
		t.Fatalf("设为当前: %s", act.Error.Message)
	}
	if d.creds == nil {
		t.Fatal("测试需要一个可用凭证库")
	}
	if err := d.creds.Delete(context.Background(), "cred-gone2"); err != nil {
		t.Fatalf("删除凭证: %v", err)
	}

	_, startErr := d.Start("你好")
	if startErr == nil {
		t.Fatal("凭证悬空时必须拒绝启动")
	}
	if !strings.Contains(startErr.Error(), "cred-gone2") {
		t.Fatalf("启动失败消息要指明悬空的凭证引用，实际 %q", startErr.Error())
	}
	if strings.Contains(startErr.Error(), "{{API_KEY_diag3}}") {
		t.Fatal("启动失败消息里出现明文密钥")
	}
}
