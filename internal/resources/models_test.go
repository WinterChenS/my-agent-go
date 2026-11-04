package resources

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/credentials"
)

func newTestService(t *testing.T) (*Service, string, *credentials.MemStore) {
	t.Helper()
	root := t.TempDir()
	store, err := config.NewStore(filepath.Join(root, "config"))
	if err != nil {
		t.Fatalf("创建配置存储: %v", err)
	}
	mem := credentials.NewMemStore()
	return New(store, mem), root, mem
}

func validModel() ModelDefinition {
	return ModelDefinition{
		ID:            "m-demo",
		ProviderID:    "demo",
		ModelID:       "demo-model",
		API:           "openai-completions",
		BaseURL:       "http://127.0.0.1:8080/v1",
		InputKinds:    []string{"text"},
		ContextWindow: 128000,
		MaxOutput:     8192,
	}
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望错误 %s，实际为 nil", code)
	}
	var appErr *contracts.AppError
	if !asAppError(err, &appErr) {
		t.Fatalf("期望 *contracts.AppError，实际 %T: %v", err, err)
	}
	if appErr.Code != code {
		t.Fatalf("期望错误码 %s，实际 %s（%s）", code, appErr.Code, appErr.Message)
	}
}

func asAppError(err error, target **contracts.AppError) bool {
	for err != nil {
		if ae, ok := err.(*contracts.AppError); ok {
			*target = ae
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// TestModelSchemaValidation 覆盖 MOD01 的校验面：
// ID、API 白名单、URL、context/output 数值关系。每条都必须是"拒绝"，
// 而不是"接受后静默降级"。
func TestModelSchemaValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ModelDefinition)
		code   string
	}{
		{"模型ID大写", func(m *ModelDefinition) { m.ID = "M-Demo" }, contracts.CodeUnsupported},
		{"模型ID带斜杠", func(m *ModelDefinition) { m.ID = "a/b" }, contracts.CodeUnsupported},
		{"模型ID为空", func(m *ModelDefinition) { m.ID = "" }, contracts.CodeUnsupported},
		{"provider名为空", func(m *ModelDefinition) { m.ProviderID = "" }, contracts.CodeUnsupported},
		{"模型名为空", func(m *ModelDefinition) { m.ModelID = " " }, contracts.CodeUnsupported},
		{"模型名带换行", func(m *ModelDefinition) { m.ModelID = "a\nb" }, contracts.CodeUnsupported},
		{"API不在白名单", func(m *ModelDefinition) { m.API = "openai-chat" }, contracts.CodeUnsupported},
		{"API分类器接口", func(m *ModelDefinition) { m.API = "openai-decisions" }, contracts.CodeUnsupported},
		{"baseUrl为空", func(m *ModelDefinition) { m.BaseURL = "" }, contracts.CodeUnsupported},
		{"baseUrl非http", func(m *ModelDefinition) { m.BaseURL = "ftp://example.com/v1" }, contracts.CodeUnsupported},
		{"baseUrl缺主机", func(m *ModelDefinition) { m.BaseURL = "http:///v1" }, contracts.CodeUnsupported},
		{"baseUrl内嵌密码", func(m *ModelDefinition) { m.BaseURL = "https://user:pass@example.com/v1" }, contracts.CodeUnauthorized},
		{"baseUrl查询带key", func(m *ModelDefinition) { m.BaseURL = "https://example.com/v1?api_key=abc" }, contracts.CodeUnauthorized},
		{"context为零", func(m *ModelDefinition) { m.ContextWindow = 0 }, contracts.CodeUnsupported},
		{"context为负", func(m *ModelDefinition) { m.ContextWindow = -1 }, contracts.CodeUnsupported},
		{"context大到离谱", func(m *ModelDefinition) { m.ContextWindow = 20_000_000 }, contracts.CodeUnsupported},
		{"maxOutput为零", func(m *ModelDefinition) { m.MaxOutput = 0 }, contracts.CodeUnsupported},
		{"maxOutput大于context", func(m *ModelDefinition) { m.MaxOutput = 128001 }, contracts.CodeUnsupported},
		{"inputKinds非法", func(m *ModelDefinition) { m.InputKinds = []string{"audio"} }, contracts.CodeUnsupported},
		{"inputKinds缺text", func(m *ModelDefinition) { m.InputKinds = []string{"image"} }, contracts.CodeUnsupported},
		{"思考等级非法", func(m *ModelDefinition) { m.ThinkingLevels = []string{"ultra"} }, contracts.CodeUnsupported},
		{"凭证引用非法", func(m *ModelDefinition) { m.CredentialRef = "cred with space" }, contracts.CodeUnsupported},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, _, _ := newTestService(t)
			def := validModel()
			c.mutate(&def)
			_, err := svc.Save(context.Background(), 0, def, "")
			requireCode(t, err, c.code)
		})
	}
}

// TestModelNormalization 验证"接受的值被归一成唯一形式"，
// 而不是原样存回去让下游各自处理大小写与顺序。
func TestModelNormalization(t *testing.T) {
	svc, _, _ := newTestService(t)
	def := validModel()
	def.InputKinds = []string{"IMAGE", "text", "text"}
	def.ThinkingLevels = []string{"HIGH", "low"}

	if _, err := svc.Save(context.Background(), 0, def, ""); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	got, err := svc.Get(context.Background(), def.ID)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if strings.Join(got.InputKinds, ",") != "text,image" {
		t.Fatalf("inputKinds 应归一为 text,image，实际 %v", got.InputKinds)
	}
	if strings.Join(got.ThinkingLevels, ",") != "low,high" {
		t.Fatalf("思考等级应按固定顺序归一，实际 %v", got.ThinkingLevels)
	}
}

// TestModelSaveNoSecretsInJSON 是 SEC07 在模型资源这一侧的核心断言：
// 密钥只在凭证存储里，配置目录里任何文件（含 models.json）都不能出现它。
func TestModelSaveNoSecretsInJSON(t *testing.T) {
	svc, root, _ := newTestService(t)
	def := validModel()
	def.CredentialRef = "cred-demo"
	const secret = "sk-sentinel-3f9a2b7c-never-on-disk"

	if _, err := svc.Save(context.Background(), 0, def, secret); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	// 配置目录不得出现明文。
	if hits := scanForSecret(t, root, secret); len(hits) > 0 {
		t.Fatalf("配置目录里出现明文密钥: %v", hits)
	}

	pm, err := svc.CompilePiModels(context.Background())
	if err != nil {
		t.Fatalf("编译失败: %v", err)
	}
	if strings.Contains(string(pm.JSON), secret) {
		t.Fatal("models.json 里出现明文密钥")
	}
	want := "$" + CredentialEnvName("cred-demo")
	if !strings.Contains(string(pm.JSON), want) {
		t.Fatalf("models.json 应使用环境引用 %s，实际：%s", want, pm.JSON)
	}
	if len(pm.Env) != 1 || !strings.HasPrefix(pm.Env[0], CredentialEnvName("cred-demo")+"=") {
		t.Fatalf("凭证应经环境变量交付，实际 %v", pm.Env)
	}

	agentDir := filepath.Join(root, "agent")
	if err := WritePiModels(agentDir, pm); err != nil {
		t.Fatalf("写 models.json: %v", err)
	}
	if hits := scanForSecret(t, root, secret); len(hits) > 0 {
		t.Fatalf("落盘 models.json 后出现明文密钥: %v", hits)
	}
}

// TestModelSaveRequiresExistingCredential 防止"保存成功但引用了不存在的凭证"：
// 那种配置要到启动时才炸，且报错与根因无关。
func TestModelSaveRequiresExistingCredential(t *testing.T) {
	svc, _, _ := newTestService(t)
	def := validModel()
	def.CredentialRef = "cred-missing"

	_, err := svc.Save(context.Background(), 0, def, "")
	requireCode(t, err, contracts.CodeDependencyMissing)

	if _, err := svc.Get(context.Background(), def.ID); err == nil {
		t.Fatal("校验失败的模型不应被写入")
	}
}

// TestModelSaveWithoutCredentialStore 验证"没有凭证库时拒绝保存密钥"，
// 而不是悄悄退化成明文（SEC07 明确禁止 fallback）。
func TestModelSaveWithoutCredentialStore(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("创建存储: %v", err)
	}
	svc := New(store, nil)
	def := validModel()
	def.CredentialRef = "cred-demo"

	_, err = svc.Save(context.Background(), 0, def, "sk-x")
	requireCode(t, err, contracts.CodeCredentialStoreUnavailable)

	// 没有密钥的本地端点仍然可以保存：它不需要凭证存储。
	plain := validModel()
	if _, err := svc.Save(context.Background(), 0, plain, ""); err != nil {
		t.Fatalf("无凭证模型应可保存: %v", err)
	}
}

// TestModelUpsertPreservesOtherFields：用户层可能同时有权限等配置，
// 写模型不能把它们整份覆盖掉。
func TestModelUpsertPreservesOtherFields(t *testing.T) {
	svc, root, _ := newTestService(t)
	// 先写入一份带 permissions 的用户配置。
	body := json.RawMessage(`{"schemaVersion":1,"permissions":{"filesystem":"workspace","network":true,"shell":true}}`)
	store, err := config.NewStore(filepath.Join(root, "config"))
	if err != nil {
		t.Fatalf("打开存储: %v", err)
	}
	if _, err := store.CompareAndSwap(context.Background(), config.KeyUser, 0, body); err != nil {
		t.Fatalf("预置用户配置: %v", err)
	}

	if _, err := svc.Save(context.Background(), 1, validModel(), ""); err != nil {
		t.Fatalf("保存模型: %v", err)
	}
	doc, err := store.Load(context.Background(), config.KeyUser)
	if err != nil {
		t.Fatalf("读回: %v", err)
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(doc.Body, &parsed); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if _, ok := parsed["permissions"]; !ok {
		t.Fatal("写模型时把用户的 permissions 覆盖掉了")
	}

	// 合并结果里权限仍然生效，模型也在。
	eff, err := store.Compile(context.Background(), config.CompileRequest{WorkspaceID: "ws-default", ProfileID: ""})
	if err != nil {
		t.Fatalf("编译: %v", err)
	}
	var values config.Values
	if err := json.Unmarshal(eff.Values, &values); err != nil {
		t.Fatalf("解析有效配置: %v", err)
	}
	if values.Permissions.Filesystem != "workspace" || !values.Permissions.Network {
		t.Fatalf("权限被破坏: %+v", values.Permissions)
	}
	if len(values.Models) != 1 || values.Models[0].ID != "m-demo" {
		t.Fatalf("模型未进入合并结果: %+v", values.Models)
	}
}

// TestModelSaveIsCAS：同 revision 只有一个 writer 成功。
func TestModelSaveUsesCAS(t *testing.T) {
	svc, _, _ := newTestService(t)
	doc, err := svc.Save(context.Background(), 0, validModel(), "")
	if err != nil {
		t.Fatalf("首次保存: %v", err)
	}

	other := validModel()
	other.ID = "m-other"
	_, err = svc.Save(context.Background(), 0, other, "")
	requireCode(t, err, contracts.CodeConfigConflict)

	if _, err := svc.Save(context.Background(), doc.Revision, other, ""); err != nil {
		t.Fatalf("用最新 revision 保存应成功: %v", err)
	}
}

// TestCompileRejectsProviderDrift：同 provider 名不同端点必须拒绝，
// 否则其中一个模型的请求会打到另一个端点，且界面上看不出来。
func TestCompileRejectsProviderDrift(t *testing.T) {
	svc, _, _ := newTestService(t)
	a := validModel()
	if _, err := svc.Save(context.Background(), 0, a, ""); err != nil {
		t.Fatalf("保存 a: %v", err)
	}
	b := validModel()
	b.ID = "m-two"
	b.ModelID = "demo-model-2"
	b.BaseURL = "http://127.0.0.1:9090/v1"
	if _, err := svc.Save(context.Background(), 1, b, ""); err != nil {
		t.Fatalf("保存 b: %v", err)
	}

	_, err := svc.CompilePiModels(context.Background())
	requireCode(t, err, contracts.CodeConfigConflict)
}

// TestCompileSkipsLegacyShape 守一条真实数据：C01 期间的模型条目是
// {provider, baseUrl, model}，没有 providerId/modelId。直接编译会在 providers
// 里生成一个空键（"": {...}），Pi 要么拒绝整份文件、要么匹配到一个不存在的
// provider。但它也不能把同目录下正常的模型一起拖垮——跳过的同时必须说出来。
func TestCompileSkipsLegacyShape(t *testing.T) {
	svc, _, _ := newTestService(t)
	good := validModel()
	if _, err := svc.Save(context.Background(), 0, good, ""); err != nil {
		t.Fatalf("保存正常模型: %v", err)
	}
	doc, err := svc.store.Load(context.Background(), config.KeyUser)
	if err != nil {
		t.Fatalf("读配置: %v", err)
	}
	var body struct {
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(doc.Body, &body); err != nil {
		t.Fatalf("解析配置: %v", err)
	}
	body.Models = append(body.Models, json.RawMessage(`{"id":"m-old","provider":"deepseek","baseUrl":"http://127.0.0.1:1/v1","model":"x"}`))
	merged, err := json.Marshal(map[string]any{"schemaVersion": 1, "models": body.Models})
	if err != nil {
		t.Fatalf("序列化: %v", err)
	}
	if _, err := svc.store.CompareAndSwap(context.Background(), config.KeyUser, doc.Revision, merged); err != nil {
		t.Fatalf("写入旧形状配置: %v", err)
	}

	pm, err := svc.CompilePiModels(context.Background())
	if err != nil {
		t.Fatalf("一条旧数据不该让正常模型无法编译: %v", err)
	}
	if len(pm.Skipped) != 1 || pm.Skipped[0] != "m-old" {
		t.Fatalf("跳过的条目要能说出来，实际 %v", pm.Skipped)
	}
	if strings.Contains(string(pm.JSON), `"":`) {
		t.Fatalf("providers 里出现了空键：\n%s", pm.JSON)
	}
	if !strings.Contains(string(pm.JSON), "demo-model") {
		t.Fatalf("正常模型应参与编译：\n%s", pm.JSON)
	}
}

// TestCompileSkipsDisabledAndGroupsProviders：禁用不编译；同 provider 多模型
// 共用一个 provider 条目。
func TestCompileSkipsDisabledAndGroupsProviders(t *testing.T) {
	svc, _, _ := newTestService(t)
	a := validModel()
	if _, err := svc.Save(context.Background(), 0, a, ""); err != nil {
		t.Fatalf("保存 a: %v", err)
	}
	b := validModel()
	b.ID = "m-two"
	b.ModelID = "demo-model-2"
	if _, err := svc.Save(context.Background(), 1, b, ""); err != nil {
		t.Fatalf("保存 b: %v", err)
	}
	off := false
	c := validModel()
	c.ID = "m-off"
	c.ModelID = "demo-model-3"
	c.Enabled = &off
	if _, err := svc.Save(context.Background(), 2, c, ""); err != nil {
		t.Fatalf("保存 c: %v", err)
	}

	pm, err := svc.CompilePiModels(context.Background())
	if err != nil {
		t.Fatalf("编译: %v", err)
	}
	var doc struct {
		Providers map[string]struct {
			Models []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(pm.JSON, &doc); err != nil {
		t.Fatalf("解析 models.json: %v", err)
	}
	if len(doc.Providers) != 1 {
		t.Fatalf("应只有一个 provider，实际 %d", len(doc.Providers))
	}
	models := doc.Providers["demo"].Models
	if len(models) != 2 {
		t.Fatalf("应编译 2 个启用模型，实际 %d（%+v）", len(models), models)
	}
	for _, m := range models {
		if m.ID == "demo-model-3" {
			t.Fatal("禁用的模型出现在 models.json 里")
		}
	}
}

// TestThinkingMapOnlyForEffortAPIs：思考等级的映射只对"参数即级别名"的 API 下发。
// 对 anthropic 之类的预算型接口凭空造映射，等于把错的值发给 provider。
func TestThinkingMapOnlyForEffortAPIs(t *testing.T) {
	svc, _, _ := newTestService(t)
	def := validModel()
	def.ThinkingLevels = []string{"low", "high"}
	if _, err := svc.Save(context.Background(), 0, def, ""); err != nil {
		t.Fatalf("保存: %v", err)
	}
	pm, err := svc.CompilePiModels(context.Background())
	if err != nil {
		t.Fatalf("编译: %v", err)
	}
	var doc struct {
		Providers map[string]struct {
			Models []map[string]any `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(pm.JSON, &doc); err != nil {
		t.Fatalf("解析: %v", err)
	}
	m := doc.Providers["demo"].Models[0]
	if m["reasoning"] != true {
		t.Fatalf("声明了思考等级应置 reasoning=true: %+v", m)
	}
	mapping, ok := m["thinkingLevelMap"].(map[string]any)
	if !ok {
		t.Fatalf("openai-completions 应下发 thinkingLevelMap: %+v", m)
	}
	if mapping["low"] != "low" || mapping["high"] != "high" {
		t.Fatalf("映射错误: %+v", mapping)
	}
	if mapping["max"] != nil {
		t.Fatalf("未声明 max 应为 null（不支持），实际 %v", mapping["max"])
	}

	// anthropic：只置 reasoning，不下发映射。
	svc2, _, _ := newTestService(t)
	ant := validModel()
	ant.API = "anthropic-messages"
	ant.ThinkingLevels = []string{"low"}
	if _, err := svc2.Save(context.Background(), 0, ant, ""); err != nil {
		t.Fatalf("保存 anthropic: %v", err)
	}
	pm2, err := svc2.CompilePiModels(context.Background())
	if err != nil {
		t.Fatalf("编译 anthropic: %v", err)
	}
	var doc2 struct {
		Providers map[string]struct {
			Models []map[string]any `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(pm2.JSON, &doc2); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if _, ok := doc2.Providers["demo"].Models[0]["thinkingLevelMap"]; ok {
		t.Fatal("anthropic-messages 不应下发 thinkingLevelMap")
	}
}

// TestNoImplicitAuthImport：不读取、不修改 Pi 旧的 auth marker。
//
// 我们的凭证只来自 OS 凭证库。宿主若存在 ~/.pi/agent/auth.json，
// 它必须保持原样——"顺手导入一下"会让密钥的归属变得无法解释。
func TestNoImplicitAuthImport(t *testing.T) {
	svc, root, _ := newTestService(t)
	piDir := filepath.Join(root, "fake-pi-agent")
	if err := os.MkdirAll(piDir, 0o700); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	authPath := filepath.Join(piDir, "auth.json")
	original := []byte(`{"openai":{"type":"apiKey","key":"sk-legacy-sentinel"}}`)
	if err := os.WriteFile(authPath, original, 0o600); err != nil {
		t.Fatalf("写入旧 auth: %v", err)
	}
	t.Setenv("PI_CODING_AGENT_DIR", piDir)

	def := validModel()
	if _, err := svc.Save(context.Background(), 0, def, ""); err != nil {
		t.Fatalf("保存: %v", err)
	}
	pm, err := svc.CompilePiModels(context.Background())
	if err != nil {
		t.Fatalf("编译: %v", err)
	}
	if strings.Contains(string(pm.JSON), "sk-legacy-sentinel") {
		t.Fatal("旧 auth marker 被读入模型配置")
	}
	after, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("旧 auth 被删除: %v", err)
	}
	if string(after) != string(original) {
		t.Fatalf("旧 auth 被修改: %s", after)
	}
}

// TestDeleteModel 删除后不再出现在列表与编译结果里。
func TestDeleteModel(t *testing.T) {
	svc, _, _ := newTestService(t)
	doc, err := svc.Save(context.Background(), 0, validModel(), "")
	if err != nil {
		t.Fatalf("保存: %v", err)
	}
	del, err := svc.Delete(context.Background(), doc.Revision, "m-demo")
	if err != nil {
		t.Fatalf("删除: %v", err)
	}
	set, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("列举: %v", err)
	}
	if len(set.Items) != 0 {
		t.Fatalf("删除后仍列出 %d 条", len(set.Items))
	}
	if set.Revision != del.Revision {
		t.Fatalf("列举 revision 应为 %d，实际 %d", del.Revision, set.Revision)
	}
	pm, err := svc.CompilePiModels(context.Background())
	if err != nil {
		t.Fatalf("编译: %v", err)
	}
	if len(pm.JSON) != 0 {
		t.Fatalf("没有模型时不应产生 models.json: %s", pm.JSON)
	}
}

// scanForSecret 扫描目录下所有文件是否含明文 secret（含 base64 变体）。
//
// 同时查 base64 是必要的：只查原始字节时，被编码后的密钥会让断言假绿
// （F07 里就遇到过这个假绿）。
func scanForSecret(t *testing.T, root, secret string) []string {
	t.Helper()
	variants := [][]byte{
		[]byte(secret),
		[]byte(base64.StdEncoding.EncodeToString([]byte(secret))),
		[]byte(base64.RawStdEncoding.EncodeToString([]byte(secret))),
		[]byte(base64.URLEncoding.EncodeToString([]byte(secret))),
	}
	var hits []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for _, v := range variants {
			if len(v) > 0 && strings.Contains(string(data), string(v)) {
				hits = append(hits, path)
				break
			}
		}
		return nil
	})
	return hits
}
