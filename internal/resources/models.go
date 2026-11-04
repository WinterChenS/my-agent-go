// Package resources 管理用户定义的模型资源（C02）。
//
// 三条边界：
//   - 模型定义是普通配置（会被导出、贴进 issue、写进日志），因此只保存
//     credentialRef，绝不保存密钥本体；密钥只在 OS 保护存储里（SEC07）。
//   - 交给 Pi 的 models.json 也不写明文密钥：写 "$MYAGENT_CRED_x" 环境引用，
//     真实值经子进程环境变量交付，不落盘。
//   - API 类型白名单来自已认证的 Pi 1.5.0 实际分发分支，不是"猜所有 OpenAI
//     兼容端点都一样"。见 supportedAPIs 与 pi_bundle_test.go 的一致性检查。
package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/credentials"
)

// ModelDefinition 是一条模型定义。
//
// ProviderID 是 models.json 里的 provider 键名，同一个 provider 下的多个模型
// 共用一个 baseUrl/api；ModelID 是真正发给 API 的模型名（可能含斜杠）。
type ModelDefinition struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerId"`
	ModelID    string `json:"modelId"`
	// API 是 Pi 的协议实现名，必须在 supportedAPIs 里。
	API     string `json:"api"`
	BaseURL string `json:"baseUrl"`
	// InputKinds 只允许 text/image，且必须包含 text——本产品的会话需要文本。
	InputKinds    []string `json:"inputKinds"`
	ContextWindow int      `json:"contextWindow"`
	MaxOutput     int      `json:"maxOutput"`
	// CredentialRef 指向 OS 凭证库中的条目；这里永远不出现密钥本体。
	CredentialRef string `json:"credentialRef"`
	// ThinkingLevels 是用户声明的思考等级。空表示该模型不支持思考。
	ThinkingLevels []string `json:"thinkingLevels"`
	// Enabled 省略视为启用；只有显式 false 才是禁用。
	Enabled *bool `json:"enabled,omitempty"`
}

// Set 是一次列举的结果。
//
// Revision 必须一起返回：前端保存时要用它做 CAS，拿列表里的旧 revision 去写
// 会覆盖别人刚做的改动——这正是 CFG02 要防的事。
type Set struct {
	Items    []ModelDefinition `json:"items"`
	Revision uint64            `json:"revision"`
}

// Service 提供模型定义的读写与编译。
type Service struct {
	store *config.Store
	creds credentials.Store
}

// New 构造模型服务。creds 可以为 nil（仅有单元测试会这样用），
// 此时任何涉及密钥的保存都会明确失败，而不是悄悄退化成明文。
func New(store *config.Store, creds credentials.Store) *Service {
	return &Service{store: store, creds: creds}
}

// supportedAPIs 是允许用户选择的协议实现。
//
// 这份清单不是"常见 OpenAI 兼容值"的猜测：每一项都对应 Pi 1.5.0 bundle 里
// 真实存在的 API 分发实现（pi_bundle_test.go 会在装有认证 Pi 的环境里核对）。
// 刻意不收录的：
//   - openai-decisions / typesafe-system-one / cloudflare-workers-ai-system-one
//     是分类器（classifier）接口，不是对话模型；
//   - openrouter-images 是图像生成接口；
//   - pi-messages 是 Pi 自有 radius 后端专用协议，需要 radius 专用凭证，
//     不适合作为用户自填 provider；
//   - llama-cpp-classify 是内置分类辅助，不对应用户配置。
var supportedAPIs = []string{
	"openai-completions",
	"openai-responses",
	"openai-codex-responses",
	"azure-openai-responses",
	"anthropic-messages",
	"google-generative-ai",
	"google-vertex",
	"bedrock-converse-stream",
	"mistral-conversations",
}

// excludedAPIs 记录"Pi 里有、但我们不提供给用户"的协议及其原因。
// 它同时是一致性测试的豁免名单：Pi 升级后新增一个 API 时，测试会失败，
// 逼迫我们复核而不是默默把它漏掉。
var excludedAPIs = map[string]string{
	"openai-decisions":                 "分类器接口，不是对话模型",
	"typesafe-system-one":              "分类器接口，不是对话模型",
	"cloudflare-workers-ai-system-one": "分类器接口，不是对话模型",
	"openrouter-images":                "图像生成接口",
	"pi-messages":                      "Pi 自有 radius 后端专用协议",
	"llama-cpp-classify":               "内置分类辅助",
	"unknown":                          "占位值，不是真实协议",
}

// SupportedAPIs 返回可选的协议清单（副本，调用方改不坏内部状态）。
func SupportedAPIs() []string {
	out := make([]string, len(supportedAPIs))
	copy(out, supportedAPIs)
	return out
}

// apiExists 是唯一的合法性判定。UI 的下拉必须来自 SupportedAPIs()，
// 不要在别处再写一份——两套枚举的后果是界面能选、运行时按未知值静默失败。
func apiExists(v string) bool {
	for _, a := range supportedAPIs {
		if a == v {
			return true
		}
	}
	return false
}

// thinkingLevels 是 Pi 的思考等级键名。
var thinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// SupportedThinkingLevels 返回合法等级。
func SupportedThinkingLevels() []string {
	out := make([]string, len(thinkingLevels))
	copy(out, thinkingLevels)
	return out
}

// SupportedInputKinds 返回合法的输入类型。
func SupportedInputKinds() []string { return []string{"text", "image"} }

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// secretKeyPattern 与 profile 侧保持一致：按键名拦截密钥字段。
var secretKeyPattern = regexp.MustCompile(`(?i)^(api[-_]?key|secret|token|password|passwd|authorization|bearer)$`)

// credentialRefPattern 限制 ref 字符集：ref 会出现在环境变量名里。
var credentialRefPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// List 返回用户层里的全部模型定义。
func (s *Service) List(ctx context.Context) (Set, error) {
	doc, err := s.store.Load(ctx, config.KeyUser)
	if err != nil {
		return Set{}, err
	}
	items, err := decodeModels(doc.Body)
	if err != nil {
		return Set{}, err
	}
	return Set{Items: items, Revision: doc.Revision}, nil
}

// Get 读取单条模型定义。
func (s *Service) Get(ctx context.Context, id string) (ModelDefinition, error) {
	set, err := s.List(ctx)
	if err != nil {
		return ModelDefinition{}, err
	}
	for _, m := range set.Items {
		if m.ID == id {
			return m, nil
		}
	}
	return ModelDefinition{}, contracts.NewAppError(contracts.CodeNotFound, "模型不存在: "+id, false)
}

// Save 保存一条模型定义（CAS）。
//
// secret 非空时先写 OS 凭证库，再保存引用。顺序是刻意的：反过来的话，
// 保存成功而凭证写入失败会留下一个"引用了不存在凭证"的模型，
// 用户看到保存成功却启动不了。
func (s *Service) Save(ctx context.Context, expectedRevision uint64, def ModelDefinition, secret string) (contracts.ConfigDocument, error) {
	normalized, err := normalizeDefinition(def)
	if err != nil {
		return contracts.ConfigDocument{}, err
	}
	if secret != "" {
		if s.creds == nil {
			return contracts.ConfigDocument{}, contracts.NewAppError(
				contracts.CodeCredentialStoreUnavailable,
				"没有可用的 OS 凭证库，拒绝保存密钥（不回退明文）", false)
		}
		if normalized.CredentialRef == "" {
			return contracts.ConfigDocument{}, contracts.NewAppError(
				contracts.CodeUnsupported, "输入了密钥但没有凭证引用名（credentialRef）", false)
		}
		if err := s.creds.Put(ctx, normalized.CredentialRef, []byte(secret)); err != nil {
			return contracts.ConfigDocument{}, err
		}
	}
	if normalized.CredentialRef != "" && s.creds != nil {
		if _, err := s.creds.Get(ctx, normalized.CredentialRef); err != nil {
			return contracts.ConfigDocument{}, contracts.NewAppError(
				contracts.CodeDependencyMissing,
				"凭证引用 "+normalized.CredentialRef+" 不存在；请先保存密钥或改用已有凭证", false)
		}
	}

	doc, err := s.store.Load(ctx, config.KeyUser)
	if err != nil {
		return contracts.ConfigDocument{}, err
	}
	body, err := upsertModel(doc.Body, normalized)
	if err != nil {
		return contracts.ConfigDocument{}, err
	}
	return s.store.CompareAndSwap(ctx, config.KeyUser, expectedRevision, body)
}

// Delete 删除一条模型定义（CAS）。
func (s *Service) Delete(ctx context.Context, expectedRevision uint64, id string) (contracts.ConfigDocument, error) {
	doc, err := s.store.Load(ctx, config.KeyUser)
	if err != nil {
		return contracts.ConfigDocument{}, err
	}
	items, err := rawModels(doc.Body)
	if err != nil {
		return contracts.ConfigDocument{}, err
	}
	kept := make([]json.RawMessage, 0, len(items))
	found := false
	for _, raw := range items {
		if idOf(raw) == id {
			found = true
			continue
		}
		kept = append(kept, raw)
	}
	if !found {
		return contracts.ConfigDocument{}, contracts.NewAppError(contracts.CodeNotFound, "模型不存在: "+id, false)
	}
	body, err := withModels(doc.Body, kept)
	if err != nil {
		return contracts.ConfigDocument{}, err
	}
	return s.store.CompareAndSwap(ctx, config.KeyUser, expectedRevision, body)
}

// PiModels 是编译结果：models.json 正文 + 需要注入子进程的凭证环境变量。
type PiModels struct {
	// JSON 是 models.json 的内容，apiKey 只有 $ENV 引用。
	JSON []byte
	// Env 形如 MYAGENT_CRED_xxxx=secret，只在进程内传递。
	Env []string
	// Providers 是本次编译出的 provider 键（诊断/测试用）。
	Providers []string
	// Skipped 是因缺少 provider/模型名而没参与编译的条目 ID。
	//
	// 旧版本存下的形状（只有 provider/baseUrl/model）会落到这里。
	// 不参与编译，但要能被说出来——静默跳过会让用户以为它在用。
	Skipped []string
}

// CompilePiModels 把用户层的模型定义编译成 Pi 的 models.json。
//
// 同 ProviderID 的多条模型共用一个 provider 条目；若它们的 baseUrl/api 不一致，
// 拒绝编译而不是"取最后一个"：那会让其中一个模型的请求打到另一个端点，
// 而且从界面完全看不出来。
func (s *Service) CompilePiModels(ctx context.Context) (PiModels, error) {
	set, err := s.List(ctx)
	if err != nil {
		return PiModels{}, err
	}
	return compilePiModels(set.Items, func(m ModelDefinition) (string, string, error) {
		if m.CredentialRef == "" {
			return "", "", nil
		}
		if s.creds == nil {
			return "", "", contracts.NewAppError(
				contracts.CodeCredentialStoreUnavailable,
				"没有可用的 OS 凭证库，无法编译模型 "+m.ID, false)
		}
		secret, err := s.creds.Get(ctx, m.CredentialRef)
		if err != nil {
			return "", "", contracts.NewAppError(
				contracts.CodeDependencyMissing,
				"模型 "+m.ID+" 引用的凭证 "+m.CredentialRef+" 不可用: "+err.Error(), false)
		}
		return CredentialEnvName(m.CredentialRef), string(secret), nil
	})
}

// CompileLegacy 把旧的四个设置字段编译成同一份 models.json。
//
// 为什么不让旧路径自己拼一份：拼出来的那份会把明文密钥写进磁盘（C01 的做法），
// 而 SEC07 要求配置、日志、诊断包里对密钥的命中数为 0。旧路径只是"还没迁到
// 模型定义"的入口，不是"可以不守规矩"的豁免——它同样只写 $ENV 引用，
// 真实值经子进程环境变量交付。
func CompileLegacy(def ModelDefinition, secret string) (PiModels, error) {
	return compilePiModels([]ModelDefinition{def}, func(ModelDefinition) (string, string, error) {
		secret = strings.TrimSpace(secret)
		if secret == "" {
			// 本机/内网端点没有密钥是常态，不是错误；占位值由 compilePiModels 填。
			return "", "", nil
		}
		return CredentialEnvName(legacyCredentialRef), secret, nil
	})
}

// legacyCredentialRef 是旧设置路径的固定凭证名，只用于生成环境变量名。
const legacyCredentialRef = "settings-api-key"

// credentialResolver 为一条定义返回（环境变量名, 密钥原文）。
// 变量名为空表示该模型不需要凭证（本机/内网端点）。
//
// 抽成回调是为了让服务与连接测试共用同一套 models.json 生成逻辑：
// 连接测试如果自己拼一份"看起来一样"的配置，测过的就不是真正会跑的那份。
type credentialResolver func(ModelDefinition) (envName, secret string, err error)

// compilePiModels 是 models.json 生成的唯一实现。
func compilePiModels(defs []ModelDefinition, resolve credentialResolver) (PiModels, error) {
	type providerEntry struct {
		name    string
		baseURL string
		api     string
		apiKey  string
		owner   string
		models  []map[string]any
	}
	order := []string{}
	byProvider := map[string]*providerEntry{}
	env := []string{}
	skipped := []string{}
	seenEnv := map[string]bool{}

	for _, m := range defs {
		if m.Enabled != nil && !*m.Enabled {
			continue
		}
		// 空 providerId 会在 providers 里生成一个空键（"": {...}），Pi 那边
		// 要么拒绝整份文件、要么匹配到一个不存在的 provider。跳过它并记进
		// Skipped，而不是让一条旧数据把其它模型的运行一起拖垮——
		// 但也不能默默跳过：调用方与列表都要能看到"这条没参与编译"。
		// C01 之前的旧形状（provider/baseUrl/model）正好落到这个分支。
		if strings.TrimSpace(m.ProviderID) == "" || strings.TrimSpace(m.ModelID) == "" {
			skipped = append(skipped, m.ID)
			continue
		}
		envName, secret, err := resolve(m)
		if err != nil {
			return PiModels{}, err
		}
		apiKey := "local-no-key"
		if envName != "" {
			apiKey = "$" + envName
			if !seenEnv[envName] {
				seenEnv[envName] = true
				env = append(env, envName+"="+secret)
			}
		}
		// 未提供凭证时填占位值：Pi 的 schema 要求 apiKey 至少 1 个字符，
		// 留空会让整份 models.json 校验失败，症状会伪装成"provider 不存在"。

		p, ok := byProvider[m.ProviderID]
		if !ok {
			p = &providerEntry{name: m.ProviderID, baseURL: m.BaseURL, api: m.API, apiKey: apiKey, owner: m.ID}
			byProvider[m.ProviderID] = p
			order = append(order, m.ProviderID)
		} else if p.baseURL != m.BaseURL || p.api != m.API {
			return PiModels{}, contracts.NewAppError(
				contracts.CodeConfigConflict,
				"模型 "+p.owner+" 与 "+m.ID+" 共用 provider "+m.ProviderID+
					"，但 baseUrl/api 不一致（"+p.baseURL+"/"+p.api+" vs "+m.BaseURL+"/"+m.API+"）；"+
					"请改用一个不同的 provider 名或统一这两项",
				false)
		}
		p.models = append(p.models, piModel(m))
	}

	if len(order) == 0 {
		return PiModels{Skipped: skipped}, nil
	}

	providers := map[string]any{}
	for _, name := range order {
		p := byProvider[name]
		providers[name] = map[string]any{
			"name":    p.name,
			"baseUrl": p.baseURL,
			"api":     p.api,
			"apiKey":  p.apiKey,
			"models":  p.models,
		}
	}
	doc := map[string]any{
		"$schema":   "https://pi.dev/schemas/models.schema.json",
		"providers": providers,
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return PiModels{}, contracts.NewAppError(contracts.CodeInternal, "序列化 models.json 失败: "+err.Error(), false)
	}
	sort.Strings(env)
	return PiModels{JSON: raw, Env: env, Providers: order, Skipped: skipped}, nil
}

// WritePiModels 把编译结果写进隔离 agentDir。
//
// 0600 是必要的而不是装饰：文件里虽无明文密钥，但它描述了可用的 provider
// 与端点，仍属于用户配置。
func WritePiModels(agentDir string, pm PiModels) error {
	if len(pm.JSON) == 0 {
		return contracts.NewAppError(contracts.CodeConfigMissing, "没有可写入的模型定义", false)
	}
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "创建 agentDir 失败: "+err.Error(), false)
	}
	path := filepath.Join(agentDir, "models.json")
	if err := os.WriteFile(path, pm.JSON, 0o600); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "写入 models.json 失败: "+err.Error(), false)
	}
	return nil
}

// piModel 把一条定义转成 models.json 里的 models 元素。
func piModel(m ModelDefinition) map[string]any {
	out := map[string]any{
		"id":            m.ModelID,
		"name":          m.ModelID,
		"input":         m.InputKinds,
		"contextWindow": m.ContextWindow,
		"maxTokens":     m.MaxOutput,
		"cost":          map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
	}
	levels := effectiveThinkingLevels(m)
	if len(levels) == 0 {
		out["reasoning"] = false
		return out
	}
	out["reasoning"] = true
	// 只对"参数就是级别名"的 API 下发 thinkingLevelMap（openai 系的
	// reasoning_effort 就是 low/medium/high）。anthropic/google 等用的是
	// 预算或另一种枚举，凭空映射会把错误的值发给 provider——
	// 那种情况宁可不填，让 Pi 用它为各 API 实现好的默认值。
	if effortBasedAPI(m.API) {
		mapping := map[string]any{}
		for _, lv := range thinkingLevels {
			if containsString(levels, lv) {
				mapping[lv] = lv
			} else {
				mapping[lv] = nil
			}
		}
		out["thinkingLevelMap"] = mapping
	}
	return out
}

func effortBasedAPI(api string) bool {
	switch api {
	case "openai-completions", "openai-responses", "openai-codex-responses", "azure-openai-responses":
		return true
	}
	return false
}

// effectiveThinkingLevels 去掉 off：Pi 里 off 表示"不思考"，
// 它不算"支持思考"的证据，否则每个模型都会被标成 reasoning。
func effectiveThinkingLevels(m ModelDefinition) []string {
	out := make([]string, 0, len(m.ThinkingLevels))
	for _, lv := range m.ThinkingLevels {
		if lv != "off" {
			out = append(out, lv)
		}
	}
	return out
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// CredentialEnvName 由 ref 派生环境变量名。
//
// 用哈希而不是 ref 原文：ref 允许 "." 与 "-"，而环境变量名只允许下划线与
// 大写字母（Windows 下 "." 在 cmd/部分运行库里也不可靠）。
func CredentialEnvName(ref string) string {
	sum := sha256.Sum256([]byte(ref))
	return "MYAGENT_CRED_" + strings.ToUpper(hex.EncodeToString(sum[:6]))
}

// normalizeDefinition 校验并归一化一条定义。
//
// 所有"看起来能接受"的值都在这里被拒：静默降级（比如不认识的 api 当成
// openai-completions）会把配置错误推迟到请求阶段，那时的报错与根因无关。
func normalizeDefinition(def ModelDefinition) (ModelDefinition, error) {
	def.ID = strings.TrimSpace(def.ID)
	def.ProviderID = strings.TrimSpace(def.ProviderID)
	def.ModelID = strings.TrimSpace(def.ModelID)
	def.API = strings.TrimSpace(def.API)
	def.BaseURL = strings.TrimSpace(def.BaseURL)
	def.CredentialRef = strings.TrimSpace(def.CredentialRef)
	// 只抹掉冗余的显式 true；false 是"用户禁用"这一事实，必须留住。
	// 这里曾经无条件置 nil，后果是禁用的模型照样被编译进 models.json——
	// 界面显示已禁用、实际仍在跑，是最难被发现的那类偏差。
	if def.Enabled != nil && *def.Enabled {
		def.Enabled = nil
	}

	if !idPattern.MatchString(def.ID) {
		return ModelDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
			"模型 ID 非法: "+def.ID+"（只允许小写字母、数字、点、下划线、连字符，且以字母或数字开头）", false)
	}
	if !idPattern.MatchString(def.ProviderID) {
		return ModelDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
			"provider 名非法: "+def.ProviderID+"（会成为 models.json 的键名，规则同模型 ID）", false)
	}
	if def.ModelID == "" || len(def.ModelID) > 200 {
		return ModelDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
			"模型名（发给 API 的 model）为空或过长", false)
	}
	if strings.ContainsAny(def.ModelID, "\r\n\x00") {
		return ModelDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
			"模型名不得包含换行或 NUL", false)
	}
	if !apiExists(def.API) {
		return ModelDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
			"不支持的 API 类型: "+def.API+"（可选: "+strings.Join(supportedAPIs, ", ")+"）", false)
	}
	if err := validateBaseURL(def.BaseURL); err != nil {
		return ModelDefinition{}, err
	}
	if def.ContextWindow <= 0 {
		return ModelDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
			"contextWindow 必须为正数", false)
	}
	if def.ContextWindow > 10_000_000 {
		return ModelDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
			"contextWindow 超过 10,000,000，疑似填错单位", false)
	}
	if def.MaxOutput <= 0 {
		return ModelDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
			"maxOutput 必须为正数", false)
	}
	if def.MaxOutput > def.ContextWindow {
		return ModelDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
			"maxOutput 不能大于 contextWindow（"+strconv.Itoa(def.MaxOutput)+" > "+strconv.Itoa(def.ContextWindow)+"）", false)
	}

	kinds, err := normalizeInputKinds(def.InputKinds)
	if err != nil {
		return ModelDefinition{}, err
	}
	def.InputKinds = kinds

	if def.CredentialRef != "" && !credentialRefPattern.MatchString(def.CredentialRef) {
		return ModelDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
			"凭证引用名非法: "+def.CredentialRef+"（规则同模型 ID）", false)
	}
	levels, err := normalizeLevels(def.ThinkingLevels)
	if err != nil {
		return ModelDefinition{}, err
	}
	def.ThinkingLevels = levels

	if err := rejectSecretFields(def); err != nil {
		return ModelDefinition{}, err
	}
	return def, nil
}

// validateBaseURL 只接受 http/https，且不接受内嵌用户名密码。
//
// userinfo（https://user:pass@host）是最容易被忽略的明文密钥：
// 它会被原样写进配置并被日志打印，所以在这里直接拒绝。
func validateBaseURL(raw string) error {
	if raw == "" {
		return contracts.NewAppError(contracts.CodeUnsupported, "baseUrl 为空", false)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return contracts.NewAppError(contracts.CodeUnsupported, "baseUrl 无法解析: "+err.Error(), false)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return contracts.NewAppError(contracts.CodeUnsupported,
			"baseUrl 只支持 http/https: "+raw, false)
	}
	if u.Host == "" {
		return contracts.NewAppError(contracts.CodeUnsupported, "baseUrl 缺少主机名: "+raw, false)
	}
	if u.User != nil {
		return contracts.NewAppError(contracts.CodeUnauthorized,
			"baseUrl 不能内嵌用户名或密码；请改用凭证引用", false)
	}
	for key := range u.Query() {
		if secretKeyPattern.MatchString(key) {
			return contracts.NewAppError(contracts.CodeUnauthorized,
				"baseUrl 的查询参数 "+key+" 看起来是密钥；请改用凭证引用", false)
		}
	}
	return nil
}

func normalizeInputKinds(in []string) ([]string, error) {
	if len(in) == 0 {
		return []string{"text"}, nil
	}
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "text" && v != "image" {
			return nil, contracts.NewAppError(contracts.CodeUnsupported,
				"inputKinds 只允许 text/image，收到: "+v, false)
		}
		seen[v] = true
	}
	if !seen["text"] {
		return nil, contracts.NewAppError(contracts.CodeUnsupported,
			"inputKinds 必须包含 text：本产品的会话需要文本输入", false)
	}
	out := []string{"text"}
	if seen["image"] {
		out = append(out, "image")
	}
	return out, nil
}

func normalizeLevels(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if !containsString(thinkingLevels, v) {
			return nil, contracts.NewAppError(contracts.CodeUnsupported,
				"未知思考等级: "+v+"（可选: "+strings.Join(thinkingLevels, ", ")+"）", false)
		}
		seen[v] = true
	}
	out := make([]string, 0, len(seen))
	for _, lv := range thinkingLevels {
		if seen[lv] {
			out = append(out, lv)
		}
	}
	return out, nil
}

// rejectSecretFields 按键名拦掉密钥字段。
//
// 与 profile 同一策略：拦不住把密钥写进 name 的绕法，但能拦住所有正常误用。
// 真正的保证来自"模型定义里根本没有密钥字段"这个结构事实。
func rejectSecretFields(def ModelDefinition) error {
	raw, err := json.Marshal(def)
	if err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "序列化模型定义失败: "+err.Error(), false)
	}
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return contracts.NewAppError(contracts.CodeInternal, "解析模型定义失败: "+err.Error(), false)
	}
	if path, ok := findSecretKey(probe, "model"); ok {
		return contracts.NewAppError(contracts.CodeUnauthorized,
			"模型定义不能保存密钥（发现字段 "+path+"）；请改用凭证引用", false)
	}
	return nil
}

func findSecretKey(v any, path string) (string, bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			here := path + "." + k
			if secretKeyPattern.MatchString(k) {
				if s, ok := child.(string); !ok || strings.TrimSpace(s) != "" {
					return here, true
				}
			}
			if p, ok := findSecretKey(child, here); ok {
				return p, true
			}
		}
	case []any:
		for i, child := range t {
			if p, ok := findSecretKey(child, fmt.Sprintf("%s[%d]", path, i)); ok {
				return p, true
			}
		}
	}
	return "", false
}

// decodeModels 从配置层 body 解出模型数组。
func decodeModels(body json.RawMessage) ([]ModelDefinition, error) {
	raws, err := rawModels(body)
	if err != nil {
		return nil, err
	}
	out := make([]ModelDefinition, 0, len(raws))
	for _, raw := range raws {
		var def ModelDefinition
		if err := json.Unmarshal(raw, &def); err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal,
				"模型定义解析失败: "+err.Error(), false)
		}
		if def.ID == "" {
			return nil, contracts.NewAppError(contracts.CodeInternal, "模型定义缺少 id", false)
		}
		if len(def.InputKinds) == 0 {
			def.InputKinds = []string{"text"}
		}
		out = append(out, def)
	}
	return out, nil
}

func rawModels(body json.RawMessage) ([]json.RawMessage, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, contracts.NewAppError(contracts.CodeConfigConflict,
			"用户配置解析失败: "+err.Error(), false)
	}
	raw, ok := fields["models"]
	if !ok {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, contracts.NewAppError(contracts.CodeConfigConflict,
			"用户配置的 models 不是数组: "+err.Error(), false)
	}
	return items, nil
}

// idOf 取一条原始资源的 id，用于按 ID 替换。
func idOf(raw json.RawMessage) string {
	var v struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	return v.ID
}

// upsertModel 在保留同层其它字段的前提下按 ID 写入一条模型。
func upsertModel(old json.RawMessage, def ModelDefinition) (json.RawMessage, error) {
	items, err := rawModels(old)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(def)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "序列化模型定义失败: "+err.Error(), false)
	}
	out := make([]json.RawMessage, 0, len(items)+1)
	replaced := false
	for _, raw := range items {
		if idOf(raw) == def.ID {
			out = append(out, encoded)
			replaced = true
			continue
		}
		out = append(out, raw)
	}
	if !replaced {
		out = append(out, encoded)
	}
	return withModels(old, out)
}

// withModels 把 models 数组写回 body，保留其它顶层字段。
func withModels(old json.RawMessage, items []json.RawMessage) (json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if len(old) > 0 {
		if err := json.Unmarshal(old, &fields); err != nil {
			return nil, contracts.NewAppError(contracts.CodeConfigConflict,
				"用户配置解析失败: "+err.Error(), false)
		}
	}
	if _, ok := fields["schemaVersion"]; !ok {
		v, err := json.Marshal(config.ConfigSchemaVersion)
		if err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "序列化 schemaVersion 失败: "+err.Error(), false)
		}
		fields["schemaVersion"] = v
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "序列化模型列表失败: "+err.Error(), false)
	}
	fields["models"] = encoded
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, contracts.NewAppError(contracts.CodeInternal, "序列化用户配置失败: "+err.Error(), false)
	}
	return out, nil
}
