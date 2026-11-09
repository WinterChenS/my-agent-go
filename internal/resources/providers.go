package resources

// 供应商与导入模型（MP1）。
//
// 用户原话："可以额外添加模型和供应商"。在这个产品里，供应商不是一个
// 需要单独"登录"的实体，而是 models.json 里 provider 键的配置来源：
//   - 一条供应商记录 = providerId + baseUrl + api；
//   - 模型可以在 baseUrl/api 为空时继承供应商的值；
//   - models.json 仍然由 compilePiModels 一处生成（分组也还在那里）。
//
// 删除语义选择"拒绝"：只要还有模型引用某供应商，DeleteProvider 就返回
// CONFIG_CONFLICT 并列出引用者 ID。理由：模型是用户真正在用的东西，
// 静默迁移（把模型挪到一个临时 provider 名）会让正在跑的配置无声变形；
// 让用户先决定这些模型怎么办更诚实。

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
)

// providersField 是用户配置文档里供应商数组的字段名。
const providersField = "providers"

// Provider 是一个供应商（派生的一等对象）。
//
// Insecure 与 ModelIDs 是派生字段，不落盘：前者由 baseUrl 算出来，
// 后者由模型列表算出来。把它们写进配置只会出现"两份真相"。
type Provider struct {
	ID      string `json:"id"`
	BaseURL string `json:"baseUrl"`
	API     string `json:"api"`
	// Insecure 表示这是到非回环地址的明文 HTTP 端点。
	Insecure bool `json:"insecure"`
	// ModelIDs 是引用这个供应商的模型定义 ID。
	ModelIDs []string `json:"modelIds"`
}

// ProviderSet 是一次列举的结果（带 CAS 用的 revision）。
type ProviderSet struct {
	Items    []Provider `json:"items"`
	Revision uint64     `json:"revision"`
}

// providerRecord 是落盘形状：只有事实，没有派生字段。
type providerRecord struct {
	ID      string `json:"id"`
	BaseURL string `json:"baseUrl"`
	API     string `json:"api"`
}

// ImportDefaults 是导入时新建模型的容量默认值。
type ImportDefaults struct {
	ContextWindow int      `json:"contextWindow"`
	MaxOutput     int      `json:"maxOutput"`
	InputKinds    []string `json:"inputKinds"`
}

// ImportSpec 是一次导入请求。
type ImportSpec struct {
	ProviderID string `json:"providerId"`
	BaseURL    string `json:"baseUrl"`
	API        string `json:"api"`
	// ModelIDs 是端点报告的模型名（发给 API 的那个，不是本产品里的定义 ID）。
	ModelIDs []string       `json:"modelIds"`
	Defaults ImportDefaults `json:"defaults"`
	// AllowInsecure 是用户对"公网 + 明文 HTTP"的显式确认。
	AllowInsecure bool `json:"allowInsecure"`
}

// ImportResult 是一次导入的结论。
type ImportResult struct {
	// Imported 是本次新建的模型定义 ID；Updated 是被更新的。
	Imported []string `json:"imported"`
	Updated  []string `json:"updated"`
	// Skipped 是名字为空/无法构成合法 ID 的条目（不静默丢弃）。
	Skipped []string `json:"skipped"`
	// Models 是导入后的完整模型列表（已按供应商解析端点）。
	Models   []ModelDefinition `json:"models"`
	Revision uint64            `json:"revision"`
}

// ListProviders 列出供应商。
//
// 来源有两份：显式保存的 providers 记录，以及只靠模型出现的 providerId
// （旧配置）。两份合并，模型引用数由模型列表算出来。
func (s *Service) ListProviders(ctx context.Context) (ProviderSet, error) {
	doc, err := s.store.Load(ctx, config.KeyUser)
	if err != nil {
		return ProviderSet{}, err
	}
	records, err := decodeProviders(doc.Body)
	if err != nil {
		return ProviderSet{}, err
	}
	models, err := decodeModels(doc.Body)
	if err != nil {
		return ProviderSet{}, err
	}
	return ProviderSet{Items: mergeProviders(records, models), Revision: doc.Revision}, nil
}

// SaveProvider 新建/更新供应商，并把引用它的模型一起指向新端点。
//
// 为什么要把已有模型的 baseUrl/api 一起改：models.json 里同一个 provider
// 只能有一个端点，模型与供应商不一致会让编译直接 CONFIG_CONFLICT。
// 用户改的是"这个供应商在哪"，继续让模型停在旧地址不是他的意思。
func (s *Service) SaveProvider(ctx context.Context, id, baseURL, api string, allowInsecure bool) (Provider, uint64, error) {
	id = strings.TrimSpace(id)
	baseURL = strings.TrimSpace(baseURL)
	api = strings.TrimSpace(api)
	if !idPattern.MatchString(id) {
		return Provider{}, 0, contracts.NewAppError(contracts.CodeUnsupported,
			"provider 名非法: "+id+"（只允许小写字母、数字、点、下划线、连字符，且以字母或数字开头）", false)
	}
	if !apiExists(api) {
		return Provider{}, 0, contracts.NewAppError(contracts.CodeUnsupported,
			"不支持的 API 类型: "+api+"（可选: "+strings.Join(supportedAPIs, ", ")+"）", false)
	}
	insecure, err := parseEndpoint(baseURL, allowInsecure)
	if err != nil {
		return Provider{}, 0, err
	}
	if err := validateBaseURL(baseURL); err != nil {
		return Provider{}, 0, err
	}

	doc, err := s.store.Load(ctx, config.KeyUser)
	if err != nil {
		return Provider{}, 0, err
	}
	raws, err := rawModels(doc.Body)
	if err != nil {
		return Provider{}, 0, err
	}
	modelIDs, updated, err := reprovidersModels(raws, id, baseURL, api)
	if err != nil {
		return Provider{}, 0, err
	}
	body, err := withModels(doc.Body, updated)
	if err != nil {
		return Provider{}, 0, err
	}
	body, err = upsertEncoded(body, providersField, id, mustEncode(providerRecord{ID: id, BaseURL: baseURL, API: api}))
	if err != nil {
		return Provider{}, 0, err
	}
	saved, err := s.store.CompareAndSwap(ctx, config.KeyUser, doc.Revision, body)
	if err != nil {
		return Provider{}, 0, err
	}
	return Provider{ID: id, BaseURL: baseURL, API: api, Insecure: insecure, ModelIDs: modelIDs}, saved.Revision, nil
}

// DeleteProvider 删除供应商；仍有模型引用时拒绝。
func (s *Service) DeleteProvider(ctx context.Context, id string) (uint64, error) {
	id = strings.TrimSpace(id)
	doc, err := s.store.Load(ctx, config.KeyUser)
	if err != nil {
		return 0, err
	}
	models, err := decodeModels(doc.Body)
	if err != nil {
		return 0, err
	}
	refs := []string{}
	for _, m := range models {
		if m.ProviderID == id {
			refs = append(refs, m.ID)
		}
	}
	if len(refs) > 0 {
		sort.Strings(refs)
		return 0, contracts.NewAppError(contracts.CodeConfigConflict,
			"供应商 "+id+" 仍被模型引用: "+strings.Join(refs, ", ")+"；请先删除或迁移这些模型", false)
	}
	body, removed, err := removeEncoded(doc.Body, providersField, id)
	if err != nil {
		return 0, err
	}
	if !removed {
		// 没有 providers 记录但模型也不引用：可能只存在于模型列表里，
		// 也可能从来不存在。两种都不该报"删掉了"。
		return 0, contracts.NewAppError(contracts.CodeNotFound, "供应商不存在: "+id, false)
	}
	saved, err := s.store.CompareAndSwap(ctx, config.KeyUser, doc.Revision, body)
	if err != nil {
		return 0, err
	}
	return saved.Revision, nil
}

// ImportModels 把端点报告的模型导入成模型定义。
//
// 幂等键是 providerId + modelId：同一个 provider 下的同名模型只会有一条。
// 重复导入会更新 baseUrl/api（端点可能换了端口/协议），但**不覆盖**
// 用户自己改过的 contextWindow/maxOutput——那是用户对"这个模型能吃多少"
// 的判断，不该被一次扫描抹掉。
func (s *Service) ImportModels(ctx context.Context, spec ImportSpec) (ImportResult, error) {
	providerID := strings.TrimSpace(spec.ProviderID)
	baseURL := strings.TrimSpace(spec.BaseURL)
	api := strings.TrimSpace(spec.API)
	if !idPattern.MatchString(providerID) {
		return ImportResult{}, contracts.NewAppError(contracts.CodeUnsupported,
			"provider 名非法: "+providerID, false)
	}
	if api == "" {
		api = "openai-completions"
	}
	if !apiExists(api) {
		return ImportResult{}, contracts.NewAppError(contracts.CodeUnsupported,
			"不支持的 API 类型: "+api, false)
	}
	if _, err := parseEndpoint(baseURL, spec.AllowInsecure); err != nil {
		return ImportResult{}, err
	}
	if err := validateBaseURL(baseURL); err != nil {
		return ImportResult{}, err
	}

	doc, err := s.store.Load(ctx, config.KeyUser)
	if err != nil {
		return ImportResult{}, err
	}
	raws, err := rawModels(doc.Body)
	if err != nil {
		return ImportResult{}, err
	}
	existing, err := decodeModels(doc.Body)
	if err != nil {
		return ImportResult{}, err
	}

	result := ImportResult{Imported: []string{}, Updated: []string{}, Skipped: []string{}}
	byModel := map[string]ModelDefinition{}
	for _, m := range existing {
		byModel[m.ProviderID+"\x00"+m.ModelID] = m
	}
	usedIDs := map[string]ModelDefinition{}
	for _, m := range existing {
		usedIDs[m.ID] = m
	}

	defaults := normalizeImportDefaults(spec.Defaults)
	for _, rawID := range spec.ModelIDs {
		modelID := strings.TrimSpace(rawID)
		if modelID == "" {
			continue
		}
		key := providerID + "\x00" + modelID
		if prev, ok := byModel[key]; ok {
			next := prev
			next.BaseURL = baseURL
			next.API = api
			if next.ContextWindow <= 0 {
				next.ContextWindow = defaults.ContextWindow
			}
			if next.MaxOutput <= 0 {
				next.MaxOutput = defaults.MaxOutput
			}
			if len(next.InputKinds) == 0 {
				next.InputKinds = defaults.InputKinds
			}
			if next.ContextWindow < next.MaxOutput {
				// 旧值可能是 0/异常组合；导入不该产生一条必然编译失败的条目。
				next.MaxOutput = next.ContextWindow
			}
			normalized, err := normalizeDefinition(next)
			if err != nil {
				return ImportResult{}, err
			}
			raws = replaceModel(raws, normalized)
			result.Updated = append(result.Updated, normalized.ID)
			byModel[key] = normalized
			usedIDs[normalized.ID] = normalized
			continue
		}

		def := ModelDefinition{
			ID:            uniqueModelID(providerID, modelID, usedIDs),
			ProviderID:    providerID,
			ModelID:       modelID,
			API:           api,
			BaseURL:       baseURL,
			InputKinds:    defaults.InputKinds,
			ContextWindow: defaults.ContextWindow,
			MaxOutput:     defaults.MaxOutput,
		}
		normalized, err := normalizeDefinition(def)
		if err != nil {
			return ImportResult{}, err
		}
		raw, err := json.Marshal(normalized)
		if err != nil {
			return ImportResult{}, contracts.NewAppError(contracts.CodeInternal, "序列化模型定义失败: "+err.Error(), false)
		}
		raws = append(raws, raw)
		result.Imported = append(result.Imported, normalized.ID)
		byModel[key] = normalized
		usedIDs[normalized.ID] = normalized
	}
	if len(result.Imported) == 0 && len(result.Updated) == 0 {
		// 什么都没变：不写盘，也不假装 revision 前进了。
		set, err := s.List(ctx)
		if err != nil {
			return ImportResult{}, err
		}
		result.Models = set.Items
		result.Revision = set.Revision
		return result, nil
	}
	body, err := withModels(doc.Body, raws)
	if err != nil {
		return ImportResult{}, err
	}
	body, err = upsertEncoded(body, providersField, providerID, mustEncode(providerRecord{ID: providerID, BaseURL: baseURL, API: api}))
	if err != nil {
		return ImportResult{}, err
	}
	saved, err := s.store.CompareAndSwap(ctx, config.KeyUser, doc.Revision, body)
	if err != nil {
		return ImportResult{}, err
	}
	set, err := s.List(ctx)
	if err != nil {
		return ImportResult{}, err
	}
	result.Models = set.Items
	result.Revision = saved.Revision
	return result, nil
}

// normalizeImportDefaults 给默认值兜底：0 会让 normalizeDefinition 直接拒绝。
func normalizeImportDefaults(in ImportDefaults) ImportDefaults {
	if in.ContextWindow <= 0 {
		in.ContextWindow = 128000
	}
	if in.MaxOutput <= 0 {
		in.MaxOutput = 8192
	}
	if in.MaxOutput > in.ContextWindow {
		in.MaxOutput = in.ContextWindow
	}
	kinds, err := normalizeInputKinds(in.InputKinds)
	if err != nil {
		kinds = []string{"text"}
	}
	in.InputKinds = kinds
	return in
}

// replaceModel 用新定义替换同 ID 的原始条目；返回新数组。
func replaceModel(items []json.RawMessage, def ModelDefinition) []json.RawMessage {
	encoded := mustEncode(def)
	out := make([]json.RawMessage, 0, len(items))
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
	return out
}

// reprovidersModels 把某供应商下所有模型的端点改成新值。
func reprovidersModels(items []json.RawMessage, providerID, baseURL, api string) ([]string, []json.RawMessage, error) {
	ids := []string{}
	out := make([]json.RawMessage, 0, len(items))
	for _, raw := range items {
		var def ModelDefinition
		if err := json.Unmarshal(raw, &def); err != nil {
			return nil, nil, contracts.NewAppError(contracts.CodeInternal, "模型定义解析失败: "+err.Error(), false)
		}
		if def.ProviderID == providerID {
			def.BaseURL = baseURL
			def.API = api
			ids = append(ids, def.ID)
		}
		out = append(out, mustEncode(def))
	}
	return ids, out, nil
}

// uniqueModelID 由 providerId + 模型名派生一条稳定、合法的定义 ID。
//
// 重复导入必须落到同一个 ID 上（幂等），所以不能带随机/时间成分。
func uniqueModelID(providerID, modelID string, used map[string]ModelDefinition) string {
	base := providerID + "-" + slugifyModelID(modelID)
	if _, taken := used[base]; !taken {
		return base
	}
	for i := 2; i < 1000; i++ {
		candidate := base + "-" + itoa(i)
		if _, taken := used[candidate]; !taken {
			return candidate
		}
	}
	return base
}

func slugifyModelID(modelID string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(modelID) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		out = "model"
	}
	return out
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	buf := [8]byte{}
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// mergeProviders 合并显式记录与模型派生出来的供应商。
func mergeProviders(records []providerRecord, models []ModelDefinition) []Provider {
	order := []string{}
	byID := map[string]*Provider{}
	for _, r := range records {
		if _, ok := byID[r.ID]; ok {
			continue
		}
		p := &Provider{ID: r.ID, BaseURL: r.BaseURL, API: r.API, ModelIDs: []string{}}
		if insecure, err := parseEndpoint(r.BaseURL, true); err == nil {
			p.Insecure = insecure
		}
		byID[r.ID] = p
		order = append(order, r.ID)
	}
	for _, m := range models {
		id := strings.TrimSpace(m.ProviderID)
		if id == "" {
			continue
		}
		p, ok := byID[id]
		if !ok {
			p = &Provider{ID: id, BaseURL: m.BaseURL, API: m.API, ModelIDs: []string{}}
			if insecure, err := parseEndpoint(m.BaseURL, true); err == nil {
				p.Insecure = insecure
			}
			byID[id] = p
			order = append(order, id)
		}
		p.ModelIDs = append(p.ModelIDs, m.ID)
	}
	out := make([]Provider, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out
}

func decodeProviders(body json.RawMessage) ([]providerRecord, error) {
	raws, err := rawField(body, providersField)
	if err != nil {
		return nil, err
	}
	out := make([]providerRecord, 0, len(raws))
	for _, raw := range raws {
		var rec providerRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, contracts.NewAppError(contracts.CodeInternal, "供应商记录解析失败: "+err.Error(), false)
		}
		if strings.TrimSpace(rec.ID) == "" {
			return nil, contracts.NewAppError(contracts.CodeInternal, "供应商记录缺少 id", false)
		}
		out = append(out, rec)
	}
	return out, nil
}

func mustEncode(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		// 这里的类型都是本包定义的固定形状，序列化失败属于内部错误；
		// 返回一个非法值会在下游变成更难定位的解析错误，所以 panic 不是选项，
		// 用空对象让调用方在 next 步的校验里明确失败。
		return json.RawMessage(`{}`)
	}
	return raw
}

// providerByID 按 ID 找供应商记录（先到先得，与 mergeProviders 一致）。
func providerByID(records []providerRecord, id string) (providerRecord, bool) {
	for _, r := range records {
		if r.ID == id {
			return r, true
		}
	}
	return providerRecord{}, false
}

// resolveProviders 把留空的 baseUrl/api 用供应商的值补齐（副本，不改入参）。
func resolveProviders(models []ModelDefinition, records []providerRecord) []ModelDefinition {
	out := make([]ModelDefinition, len(models))
	copy(out, models)
	for i := range out {
		rec, ok := providerByID(records, out[i].ProviderID)
		if !ok {
			continue
		}
		if strings.TrimSpace(out[i].BaseURL) == "" {
			out[i].BaseURL = rec.BaseURL
		}
		if strings.TrimSpace(out[i].API) == "" {
			out[i].API = rec.API
		}
	}
	return out
}

// normalizeDefinitionWithProviders 是 Save 的校验入口：
// 先用供应商补齐留空的 baseUrl/api 再校验，但落盘时仍保留"留空"这个事实。
func normalizeDefinitionWithProviders(def ModelDefinition, records []providerRecord) (ModelDefinition, error) {
	providerID := strings.TrimSpace(def.ProviderID)
	// 用户最常见的填法（2026-11-08 实测）：模型名写成 `provider/模型`，供应商留空。
	// 以前这里会一路走到"provider 名非法: （空）"，用户只看到 UNSUPPORTED。
	// 现在按第一个斜杠切出供应商名；没有斜杠就用模型 ID 当供应商，保证键名合法。
	if providerID == "" {
		// 只切出**供应商名**，模型名保持原样：ModelID 是真正发给 API 的名字，
		// 允许含斜杠（models.go:38 的约定）。把 `deepseek-2/x` 改写成 `x` 会让网关
		// 收到一个不存在的模型名——那是比"存不进去"更糟的错。
		modelID := strings.TrimSpace(def.ModelID)
		if i := strings.Index(modelID, "/"); i > 0 {
			providerID = modelID[:i]
		} else {
			providerID = "custom"
		}
		def.ProviderID = providerID
	}
	rec, hasProvider := providerByID(records, providerID)
	rawBase := strings.TrimSpace(def.BaseURL)
	rawAPI := strings.TrimSpace(def.API)
	// 只填了 baseUrl（绝大多数自建网关都是 OpenAI 兼容）：协议按 openai-completions 处理，
	// 而不是回一句"api 为空"把人挡住。显式声明了别的协议仍然以声明为准。
	if rawAPI == "" && rawBase != "" {
		rawAPI = "openai-completions"
	}

	resolved := def
	resolved.API = rawAPI
	if rawBase == "" {
		if !hasProvider || strings.TrimSpace(rec.BaseURL) == "" {
			return ModelDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
				"baseUrl 为空，且供应商 "+providerID+" 没有提供端点；请填写 baseUrl 或先添加供应商", false)
		}
		resolved.BaseURL = rec.BaseURL
	}
	if rawAPI == "" {
		if !hasProvider || strings.TrimSpace(rec.API) == "" {
			return ModelDefinition{}, contracts.NewAppError(contracts.CodeUnsupported,
				"api 为空，且供应商 "+providerID+" 没有声明协议；请选择 API 类型或先添加供应商", false)
		}
		resolved.API = rec.API
	}
	normalized, err := normalizeDefinition(resolved)
	if err != nil {
		return ModelDefinition{}, err
	}
	if rawBase == "" {
		normalized.BaseURL = ""
	}
	if rawAPI == "" {
		normalized.API = ""
	}
	return normalized, nil
}
