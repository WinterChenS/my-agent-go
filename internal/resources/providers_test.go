package resources

// MP1：供应商（provider）与导入模型。
//
// 供应商本轮是**派生**的一等对象：配置里有一条 providers 记录
// （id + baseUrl + api），模型可以在 baseUrl/api 为空时继承它；
// models.json 仍然只由 compilePiModels 一处决定某个 provider 用哪个端点。
//
// 守四件事：
//  1. 导入按 provider+model 幂等：重复导入不产生重复条目；
//  2. 重复导入会更新端点/协议，但**不覆盖**用户自己填的 contextWindow/maxOutput；
//  3. 删除仍有模型引用的供应商必须被明确拒绝，并说出是谁在引用；
//  4. 模型留空 baseUrl/api 时从供应商继承，且 models.json 按 providerId 分组。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
)

func importDefaults() ImportDefaults {
	return ImportDefaults{ContextWindow: 128000, MaxOutput: 8192, InputKinds: []string{"text"}}
}

func TestImportModelsIsIdempotentAndPreservesUserFields(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	first, err := svc.ImportModels(ctx, ImportSpec{
		ProviderID: "ollama",
		BaseURL:    "http://127.0.0.1:11434/v1",
		API:        "openai-completions",
		ModelIDs:   []string{"llama3.2:latest", "qwen2.5-coder:7b"},
		Defaults:   importDefaults(),
	})
	if err != nil {
		t.Fatalf("首次导入失败: %v", err)
	}
	if len(first.Imported) != 2 || len(first.Updated) != 0 {
		t.Fatalf("首次导入应新建 2 条，实际 imported=%v updated=%v", first.Imported, first.Updated)
	}

	// 用户把其中一条的容量与凭证改成自己的值。
	set, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("列举失败: %v", err)
	}
	var userID string
	for _, m := range set.Items {
		if m.ModelID == "llama3.2:latest" {
			userID = m.ID
			m.ContextWindow = 65536
			m.MaxOutput = 4096
			m.CredentialRef = ""
		}
	}
	if userID == "" {
		t.Fatal("导入后应能按模型名找到条目")
	}
	custom := ModelDefinition{
		ID:            userID,
		ProviderID:    "ollama",
		ModelID:       "llama3.2:latest",
		API:           "openai-completions",
		BaseURL:       "http://127.0.0.1:11434/v1",
		InputKinds:    []string{"text"},
		ContextWindow: 65536,
		MaxOutput:     4096,
	}
	if _, err := svc.Save(ctx, set.Revision, custom, ""); err != nil {
		t.Fatalf("保存用户自定义值失败: %v", err)
	}

	// 再导入一次，端点改了、模型清单不变。
	second, err := svc.ImportModels(ctx, ImportSpec{
		ProviderID: "ollama",
		BaseURL:    "http://127.0.0.1:11435/v1",
		API:        "openai-responses",
		ModelIDs:   []string{"llama3.2:latest", "qwen2.5-coder:7b"},
		Defaults:   importDefaults(),
	})
	if err != nil {
		t.Fatalf("重复导入失败: %v", err)
	}
	if len(second.Imported) != 0 || len(second.Updated) != 2 {
		t.Fatalf("重复导入应只更新，实际 imported=%v updated=%v", second.Imported, second.Updated)
	}

	after, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("列举失败: %v", err)
	}
	if len(after.Items) != 2 {
		t.Fatalf("重复导入不能产生重复条目，实际 %d 条", len(after.Items))
	}
	for _, m := range after.Items {
		if m.BaseURL != "http://127.0.0.1:11435/v1" || m.API != "openai-responses" {
			t.Fatalf("重复导入应更新 baseUrl/api，实际 %s/%s", m.BaseURL, m.API)
		}
		if m.ID != userID {
			continue
		}
		if m.ContextWindow != 65536 || m.MaxOutput != 4096 {
			t.Fatalf("重复导入覆盖了用户自定义容量: context=%d max=%d", m.ContextWindow, m.MaxOutput)
		}
	}
}

func TestImportModelsUsesLocalPlaceholderWithoutTakingSecret(t *testing.T) {
	svc, _, _ := newTestService(t)
	res, err := svc.ImportModels(context.Background(), ImportSpec{
		ProviderID: "vllm",
		BaseURL:    "http://127.0.0.1:8000/v1",
		API:        "openai-completions",
		ModelIDs:   []string{"meta-llama/Llama-3-8B-Instruct"},
		Defaults:   importDefaults(),
	})
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if len(res.Models) != 1 {
		t.Fatalf("期望 1 条模型，实际 %d", len(res.Models))
	}
	m := res.Models[0]
	// 本地端点没有密钥：credentialRef 必须留空，由 compilePiModels 填
	// "local-no-key" 占位符；填一个不存在的 ref 会让模型编译时直接失败。
	if m.CredentialRef != "" {
		t.Fatalf("本地导入不能写入凭证引用，实际 %q", m.CredentialRef)
	}
	pm, err := svc.CompilePiModels(context.Background())
	if err != nil {
		t.Fatalf("编译 models.json 失败: %v", err)
	}
	var doc struct {
		Providers map[string]struct {
			APIKey string `json:"apiKey"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(pm.JSON, &doc); err != nil {
		t.Fatalf("models.json 解析失败: %v", err)
	}
	if got := doc.Providers["vllm"].APIKey; got != "local-no-key" {
		t.Fatalf("本地端点应使用 local-no-key 占位符，实际 %q", got)
	}
	if strings.Contains(string(pm.JSON), "Authorization") {
		t.Fatalf("models.json 不能带鉴权头: %s", pm.JSON)
	}
}

func TestSaveProviderValidatesAndPropagatesToModels(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	if _, err := svc.ImportModels(ctx, ImportSpec{
		ProviderID: "lmstudio",
		BaseURL:    "http://127.0.0.1:1234/v1",
		API:        "openai-completions",
		ModelIDs:   []string{"local-model"},
		Defaults:   importDefaults(),
	}); err != nil {
		t.Fatalf("导入失败: %v", err)
	}

	// 非法 provider 名与非法端点必须被拒，而不是存下去再在编译时炸。
	if _, _, err := svc.SaveProvider(ctx, "Bad/Name", "http://127.0.0.1:1234/v1", "openai-completions", false); err == nil {
		t.Fatal("非法 provider 名必须被拒绝")
	}
	if _, _, err := svc.SaveProvider(ctx, "lmstudio", "http://user:pass@127.0.0.1:1234/v1", "openai-completions", false); err == nil {
		t.Fatal("内嵌账号密码的端点必须被拒绝")
	}

	got, revision, err := svc.SaveProvider(ctx, "lmstudio", "http://127.0.0.1:1235/v1", "openai-responses", false)
	if err != nil {
		t.Fatalf("保存供应商失败: %v", err)
	}
	if got.ID != "lmstudio" || got.BaseURL != "http://127.0.0.1:1235/v1" {
		t.Fatalf("返回的供应商不对: %+v", got)
	}
	if revision == 0 {
		t.Fatal("保存后 revision 必须前进")
	}

	set, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("列举失败: %v", err)
	}
	if len(set.Items) != 1 || set.Items[0].BaseURL != "http://127.0.0.1:1235/v1" || set.Items[0].API != "openai-responses" {
		t.Fatalf("改供应商端点后引用它的模型必须一起改（否则编译会 CONFIG_CONFLICT）: %+v", set.Items)
	}
}

// TestModelInheritsProviderEndpoint：模型只填 providerId 时，端点/协议来自供应商，
// 且 models.json 按 providerId 分组只出现一个 provider。
func TestModelInheritsProviderEndpoint(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	if _, _, err := svc.SaveProvider(ctx, "ollama", "http://127.0.0.1:11434/v1", "openai-completions", false); err != nil {
		t.Fatalf("保存供应商失败: %v", err)
	}

	doc, err := svc.store.Load(ctx, config.KeyUser)
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	inherit := ModelDefinition{
		ID:            "m-llama",
		ProviderID:    "ollama",
		ModelID:       "llama3.2:latest",
		InputKinds:    []string{"text"},
		ContextWindow: 128000,
		MaxOutput:     8192,
	}
	if _, err := svc.Save(ctx, doc.Revision, inherit, ""); err != nil {
		t.Fatalf("留空 baseUrl/api 应能从供应商继承: %v", err)
	}

	set, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("列举失败: %v", err)
	}
	if len(set.Items) != 1 {
		t.Fatalf("期望 1 条模型，实际 %d", len(set.Items))
	}
	if set.Items[0].BaseURL != "http://127.0.0.1:11434/v1" || set.Items[0].API != "openai-completions" {
		t.Fatalf("列表应展示解析后的端点，实际 %s/%s", set.Items[0].BaseURL, set.Items[0].API)
	}

	pm, err := svc.CompilePiModels(ctx)
	if err != nil {
		t.Fatalf("编译失败: %v", err)
	}
	var parsed struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
			API     string `json:"api"`
			Models  []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(pm.JSON, &parsed); err != nil {
		t.Fatalf("models.json 解析失败: %v", err)
	}
	p, ok := parsed.Providers["ollama"]
	if !ok {
		t.Fatalf("models.json 必须按 providerId 分组，实际 %v", pm.Providers)
	}
	if p.BaseURL != "http://127.0.0.1:11434/v1" || p.API != "openai-completions" {
		t.Fatalf("继承的端点没写进 models.json: %+v", p)
	}
	if len(p.Models) != 1 || p.Models[0].ID != "llama3.2:latest" {
		t.Fatalf("模型没有归到 provider 下: %+v", p.Models)
	}
}

// TestDeleteProviderRefusesWhileReferenced：选"拒绝"语义并断言。
func TestDeleteProviderRefusesWhileReferenced(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	res, err := svc.ImportModels(ctx, ImportSpec{
		ProviderID: "vllm",
		BaseURL:    "http://127.0.0.1:8000/v1",
		API:        "openai-completions",
		ModelIDs:   []string{"m-one", "m-two"},
		Defaults:   importDefaults(),
	})
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}

	_, err = svc.DeleteProvider(ctx, "vllm")
	requireCode(t, err, contracts.CodeConfigConflict)
	var appErr *contracts.AppError
	if !asAppError(err, &appErr) {
		t.Fatalf("期望 AppError，实际 %T", err)
	}
	for _, id := range res.Imported {
		if !strings.Contains(appErr.Message, id) {
			t.Fatalf("拒绝原因必须列出引用者 %s，实际 %q", id, appErr.Message)
		}
	}

	// 删除模型后供应商才能删除；再删一次是 NOT_FOUND。
	for _, m := range res.Models {
		doc, err := svc.store.Load(ctx, config.KeyUser)
		if err != nil {
			t.Fatalf("读取配置失败: %v", err)
		}
		if _, err := svc.Delete(ctx, doc.Revision, m.ID); err != nil {
			t.Fatalf("删除模型失败: %v", err)
		}
	}
	if _, err := svc.DeleteProvider(ctx, "vllm"); err != nil {
		t.Fatalf("没有引用后应能删除供应商: %v", err)
	}
	list, err := svc.ListProviders(ctx)
	if err != nil {
		t.Fatalf("列举供应商失败: %v", err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("删除后不该还有供应商: %+v", list.Items)
	}
	requireCode(t, func() error { _, err := svc.DeleteProvider(ctx, "vllm"); return err }(), contracts.CodeNotFound)
}

// TestListProvidersIsDerivedFromModelsAndRecords：既列显式供应商，
// 也列只靠模型出现的 provider（旧配置没有 providers 记录）。
func TestListProvidersIsDerivedFromModelsAndRecords(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	if _, err := svc.ImportModels(ctx, ImportSpec{
		ProviderID: "llamacpp",
		BaseURL:    "http://127.0.0.1:8080/v1",
		API:        "openai-completions",
		ModelIDs:   []string{"local-gguf"},
		Defaults:   importDefaults(),
	}); err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	// 旧路径：直接 Save 一条模型，没有 providers 记录。
	doc, err := svc.store.Load(ctx, config.KeyUser)
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}
	if _, err := svc.Save(ctx, doc.Revision, validModel(), ""); err != nil {
		t.Fatalf("保存旧式模型失败: %v", err)
	}

	list, err := svc.ListProviders(ctx)
	if err != nil {
		t.Fatalf("列举供应商失败: %v", err)
	}
	byID := map[string]Provider{}
	for _, p := range list.Items {
		byID[p.ID] = p
	}
	if _, ok := byID["llamacpp"]; !ok {
		t.Fatalf("导入产生的供应商缺失: %+v", list.Items)
	}
	if _, ok := byID["demo"]; !ok {
		t.Fatalf("只靠模型出现的供应商也必须列出: %+v", list.Items)
	}
	if len(byID["llamacpp"].ModelIDs) != 1 {
		t.Fatalf("供应商要带引用它的模型: %+v", byID["llamacpp"])
	}
	if byID["demo"].BaseURL != "http://127.0.0.1:8080/v1" {
		t.Fatalf("派生供应商的端点应来自模型: %+v", byID["demo"])
	}
}

// TestProviderEndpointSecurityRules：局域网明文端点必须可用（用户的真实网关
// 就在 192.168.x），但要标 insecure；公网明文默认拒绝，只有显式确认才放行。
func TestProviderEndpointSecurityRules(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	lan, _, err := svc.SaveProvider(ctx, "lan", "http://192.168.3.10:3425/v1", "openai-completions", false)
	if err != nil {
		t.Fatalf("私网明文 HTTP 必须可保存，否则局域网网关用不了: %v", err)
	}
	if !lan.Insecure {
		t.Fatal("私网明文 HTTP 的供应商必须标 insecure")
	}

	if _, _, err := svc.SaveProvider(ctx, "public", "http://203.0.113.9:8000/v1", "openai-completions", false); err == nil {
		t.Fatal("公网明文 HTTP 未确认时必须被拒绝")
	} else {
		requireCode(t, err, contracts.CodeUnsupported)
	}
	pub, _, err := svc.SaveProvider(ctx, "public", "http://203.0.113.9:8000/v1", "openai-completions", true)
	if err != nil {
		t.Fatalf("显式确认后公网明文应可保存: %v", err)
	}
	if !pub.Insecure {
		t.Fatal("公网明文必须标 insecure")
	}

	list, err := svc.ListProviders(ctx)
	if err != nil {
		t.Fatalf("列举供应商失败: %v", err)
	}
	byID := map[string]Provider{}
	for _, p := range list.Items {
		byID[p.ID] = p
	}
	if !byID["lan"].Insecure || !byID["public"].Insecure {
		t.Fatalf("列举结果也要保留 insecure 标记: %+v", list.Items)
	}
}

// TestImportModelsAppliesEndpointSecurity：导入同样不能在用户不知情时把
// 公网明文端点写进配置。
func TestImportModelsAppliesEndpointSecurity(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.ImportModels(ctx, ImportSpec{
		ProviderID: "public",
		BaseURL:    "http://203.0.113.9:8000/v1",
		API:        "openai-completions",
		ModelIDs:   []string{"m-one"},
		Defaults:   importDefaults(),
	})
	requireCode(t, err, contracts.CodeUnsupported)

	if _, err := svc.ImportModels(ctx, ImportSpec{
		ProviderID:    "lan",
		BaseURL:       "http://192.168.3.10:3425/v1",
		API:           "openai-completions",
		ModelIDs:      []string{"lan-model"},
		Defaults:      importDefaults(),
		AllowInsecure: false,
	}); err != nil {
		t.Fatalf("私网明文端点应可导入: %v", err)
	}
}
