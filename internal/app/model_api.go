package app

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/credentials"
	"my-agent-go/internal/resources"
)

// 本文件是 C02 的绑定层：模型定义、连接测试与凭证。
//
// 与 C01 同样的原则：错误一律结构化返回，前端按 Code 分支。
// 尤其是连接测试——"401 要改凭证"和"429 等一会儿"必须能被区分，
// 否则用户只能对着"连接失败"四个字猜。

// ModelOptions 是前端可选项的唯一来源。
//
// 必须在后端给出：API 类型白名单来自已认证 Pi 的实际分发能力（resources
// 包里有一致性测试对着真实 bundle 核对）。前端自己写一份下拉，
// 迟早会出现"界面能选、运行时按未知值失败"。
type ModelOptions struct {
	APIs           []string `json:"apis"`
	InputKinds     []string `json:"inputKinds"`
	ThinkingLevels []string `json:"thinkingLevels"`
}

// ModelListResult 是模型列表。
type ModelListResult struct {
	Items    []resources.ModelDefinition `json:"items"`
	Revision uint64                      `json:"revision"`
	Options  ModelOptions                `json:"options"`
	Error    *CallError                  `json:"error,omitempty"`
}

// ModelResult 是单条模型操作的结果。
type ModelResult struct {
	Item     resources.ModelDefinition `json:"item"`
	Revision uint64                    `json:"revision"`
	Error    *CallError                `json:"error,omitempty"`
}

// ModelProbeResponse 是连接测试结果。
//
// Result.Error 是 provider 侧的分类错误（401/429/超时…），
// 外层 Error 是"根本没测成"（模型不存在、Pi 未配置）。
// 两者分开：前者说明模型端点有问题，后者说明本机还没准备好测。
type ModelProbeResponse struct {
	Result resources.ProbeResult `json:"result"`
	Error  *CallError            `json:"error,omitempty"`
}

// CurrentModelResult 是"当前使用的模型"。
type CurrentModelResult struct {
	// Source 取 resources / settings / none。
	Source   string                    `json:"source"`
	Ref      string                    `json:"ref"`
	Provider string                    `json:"provider"`
	Model    string                    `json:"model"`
	Item     resources.ModelDefinition `json:"item"`
	Revision uint64                    `json:"revision"`
	Error    *CallError                `json:"error,omitempty"`
}

// ModelAction 是"设为当前模型"的结果。
type ModelAction struct {
	Ref      string     `json:"ref"`
	Deferred bool       `json:"deferred"`
	Error    *CallError `json:"error,omitempty"`
}

// ModelDiagnosisEntry 是一条模型定义在本次诊断里的状态。
//
// Status 只有两种：ok（会进 models.json）/ skipped（不会，且 Reason 说明为什么）。
// "跳过"不是错误——它经常是用户自己禁用的——但不说出来就会变成
// "列表里有一条、运行时却没有它"的悬案。
type ModelDiagnosisEntry struct {
	ModelID    string `json:"modelId"`
	ProviderID string `json:"providerId"`
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
}

// ModelDiagnosisSelection 是当前选中模型的只读快照。
//
// 只有**引用**：credentialRef 是凭证库里的名字，密钥本体不在这条路径上。
type ModelDiagnosisSelection struct {
	ProviderID    string `json:"providerId"`
	ModelID       string `json:"modelId"`
	BaseURL       string `json:"baseUrl"`
	API           string `json:"api"`
	CredentialRef string `json:"credentialRef"`
}

// ModelDiagnosis 回答一个问题：“为什么现在没有可用模型？”
//
// 为什么需要它：保存模型成功、启动却回 CONFIG_MISSING 时，用户无从判断是
// 没设为当前、编译 models.json 时被跳过、还是凭证引用悬空——这几种的修复
// 动作完全不同。诊断把这些事实一次列全，且**不产生副作用**（不写文件、不启动 Pi）。
type ModelDiagnosis struct {
	SelectedRef    string                   `json:"selectedRef"`
	SelectedSource string                   `json:"selectedSource"`
	Selected       *ModelDiagnosisSelection `json:"selected,omitempty"`
	// Models 是逐模型状态（界面直接渲染，不在前端重算规则）。
	Models []ModelDiagnosisEntry `json:"models"`
	// Compiled 是真正进了 models.json 的条数；Skipped 是没进的及原因。
	Compiled int                   `json:"compiled"`
	Skipped  []resources.ModelSkip `json:"skipped"`
	// ModelsJSONPath/Exists 是真正会被 Pi 读的那份文件，以及它当前在不在。
	// 不存在本身不算“不可用”（下一次启动会写），所以它不参与 Ready。
	ModelsJSONPath   string `json:"modelsJsonPath"`
	ModelsJSONExists bool   `json:"modelsJsonExists"`
	Ready            bool   `json:"ready"`
	// Reason 是 Ready=false 时的唯一一句原因（含要动手的那一步）。
	Reason string     `json:"reason"`
	Error  *CallError `json:"error,omitempty"`
}

// DiagnoseModelConfig 诊断模型配置为什么可用/不可用。
//
// 复用 CompilePiModels 的同一套规则（CompilePiModelsDiagnostic 只是把单条
// 失败记成 Skipped）：这里不另写一份"能不能跑"的判定，否则界面说没事、
// 启动却报 CONFIG_MISSING 的偏差会永久存在。
func (d *Desktop) DiagnoseModelConfig() ModelDiagnosis {
	ctx := context.Background()

	d.mu.Lock()
	s := d.settings
	d.mu.Unlock()

	out := ModelDiagnosis{
		SelectedRef:    strings.TrimSpace(s.ModelRef),
		SelectedSource: "none",
		Models:         []ModelDiagnosisEntry{},
		Skipped:        []resources.ModelSkip{},
		ModelsJSONPath: filepath.Join(d.agentDir(), "models.json"),
	}
	if st, err := os.Stat(out.ModelsJSONPath); err == nil && !st.IsDir() {
		out.ModelsJSONExists = true
	}

	svc, err := d.resourceServices()
	if err != nil {
		out.Error = toCallError(err)
		out.Reason = "读不到模型资源服务：" + err.Error()
		return out
	}

	pm, compileErr := svc.CompilePiModelsDiagnostic(ctx)
	if pm.Skipped != nil {
		out.Skipped = pm.Skipped
	}
	skippedReason := make(map[string]string, len(out.Skipped))
	for _, sk := range out.Skipped {
		skippedReason[sk.ModelID] = sk.Reason
	}

	set, listErr := svc.List(ctx)
	if listErr != nil {
		out.Error = toCallError(listErr)
		out.Reason = "读不出模型定义：" + listErr.Error()
		return out
	}
	for _, m := range set.Items {
		entry := ModelDiagnosisEntry{ModelID: m.ID, ProviderID: m.ProviderID, Status: "ok"}
		if reason, ok := skippedReason[m.ID]; ok {
			entry.Status = "skipped"
			entry.Reason = reason
			out.Models = append(out.Models, entry)
			continue
		}
		out.Compiled++
		out.Models = append(out.Models, entry)
	}
	if compileErr != nil {
		out.Error = toCallError(compileErr)
		out.Reason = "编译 models.json 失败：" + compileErr.Error()
		return out
	}

	if out.SelectedRef == "" {
		// 旧字段路径（fixture / 本机端点）：设置里直接写了端点与模型名。
		// 它不经过资源库，所以编译结果与它无关，不算进 Compiled。
		if strings.TrimSpace(s.BaseURL) != "" && strings.TrimSpace(s.Model) != "" {
			provider := strings.TrimSpace(s.Provider)
			if provider == "" {
				provider = "fixture"
			}
			out.SelectedSource = "settings"
			out.Selected = &ModelDiagnosisSelection{
				ProviderID: provider,
				ModelID:    strings.TrimSpace(s.Model),
				BaseURL:    strings.TrimSpace(s.BaseURL),
				API:        "openai-completions",
			}
			out.Ready = true
			return out
		}
		if len(set.Items) == 0 {
			out.Reason = "资源库里没有任何模型定义；请先在设置→模型里新建一个模型并设为当前模型"
			return out
		}
		ids := make([]string, 0, len(set.Items))
		for _, m := range set.Items {
			ids = append(ids, m.ID)
		}
		out.Reason = "资源库里有 " + strconv.Itoa(len(set.Items)) + " 个模型（" + strings.Join(ids, "、") +
			"），但没有一个被设为当前模型；请点“设为当前模型”后再启动"
		return out
	}

	out.SelectedSource = "resources"
	def, defErr := svc.Get(ctx, out.SelectedRef)
	if defErr != nil {
		out.Error = toCallError(defErr)
		out.Reason = "当前模型 " + out.SelectedRef + " 不存在（可能已被删除）；请在设置→模型里重新选择一个模型"
		return out
	}
	out.Selected = &ModelDiagnosisSelection{
		ProviderID:    def.ProviderID,
		ModelID:       def.ModelID,
		BaseURL:       def.BaseURL,
		API:           def.API,
		CredentialRef: def.CredentialRef,
	}
	if def.Enabled != nil && !*def.Enabled {
		out.Reason = "当前模型 " + out.SelectedRef + " 已被禁用；请启用它，或改选其它模型"
		return out
	}
	if reason, ok := skippedReason[def.ID]; ok {
		out.Reason = "当前模型 " + out.SelectedRef + " 没有参与编译：" + reason
		return out
	}
	if len(pm.JSON) == 0 {
		out.Reason = "没有任何可用的模型定义，无法生成 models.json"
		return out
	}
	out.Ready = true
	return out
}

// resourceServices 懒加载模型服务与凭证存储。
//
// 凭证库不可用（例如 Linux 没有 Secret Service）时**不**让整个模型功能瘫痪：
// 没有密钥的本机端点仍然可以定义和运行；只是任何涉及密钥的保存会明确报
// CREDENTIAL_STORE_UNAVAILABLE，而不是退化成明文文件（SEC07）。
func (d *Desktop) resourceServices() (*resources.Service, error) {
	d.svcMu.Lock()
	defer d.svcMu.Unlock()
	if d.modelSvc != nil {
		return d.modelSvc, nil
	}
	store, err := config.NewStore(filepath.Join(d.root, "config"))
	if err != nil {
		return nil, err
	}
	if d.creds == nil {
		if creds, cerr := credentials.NewNativeStore(filepath.Join(d.root, "credentials")); cerr == nil {
			d.creds = creds
		}
	}
	d.modelSvc = resources.New(store, d.creds,
		resources.WithLayout(resources.NewLayout(d.root)),
		// 系统策略扩展的位置由仓库决定；解析失败时不阻塞模型功能，
		// 只在列扩展时缺少那一条（界面会因此没有"系统资源"可展示）。
		resources.WithSystemExtension(d.systemExtensionPath()),
	)
	return d.modelSvc, nil
}

// systemExtensionPath 尽力解析策略扩展路径；找不到返回空串。
func (d *Desktop) systemExtensionPath() string {
	if p, err := findPolicyExtension(); err == nil {
		return p
	}
	return ""
}

func modelOptions() ModelOptions {
	return ModelOptions{
		APIs:           resources.SupportedAPIs(),
		InputKinds:     resources.SupportedInputKinds(),
		ThinkingLevels: resources.SupportedThinkingLevels(),
	}
}

// ListModels 返回资源库里的模型定义。
func (d *Desktop) ListModels() ModelListResult {
	out := ModelListResult{Items: []resources.ModelDefinition{}, Options: modelOptions()}
	svc, err := d.resourceServices()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	set, err := svc.List(context.Background())
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	if set.Items != nil {
		out.Items = set.Items
	}
	out.Revision = set.Revision
	return out
}

// SaveModel 保存模型定义（CAS）。secret 非空时写入 OS 凭证库，配置里只留引用。
func (d *Desktop) SaveModel(expectedRevision uint64, def resources.ModelDefinition, secret string) ModelResult {
	out := ModelResult{Item: def}
	svc, err := d.resourceServices()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	ctx := context.Background()
	doc, err := svc.Save(ctx, expectedRevision, def, secret)
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	// 回读而不是把入参拼回去：校验会归一化字段（inputKinds 顺序等），
	// 界面要显示的是磁盘上真正存下的形态。
	saved, err := svc.Get(ctx, def.ID)
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	out.Item = saved
	out.Revision = doc.Revision
	return out
}

// DeleteModel 删除模型定义。
func (d *Desktop) DeleteModel(expectedRevision uint64, id string) ModelResult {
	out := ModelResult{}
	svc, err := d.resourceServices()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	// 删除当前正在用的模型会让下一次运行指向一个不存在的 ref。
	// 选择"拒绝"而不是"静默取消选择"：用户可能只是想在列表里清理，
	// 而当前模型是他刚刚确认过的决定，替他把这个决定改掉不是清理。
	d.mu.Lock()
	active := d.settings.ModelRef
	d.mu.Unlock()
	if active != "" && active == id {
		out.Error = toCallError(contracts.NewAppError(contracts.CodeConfigConflict,
			"模型 "+id+" 是当前使用的模型；请先切换到别的模型再删除", false))
		return out
	}
	doc, err := svc.Delete(context.Background(), expectedRevision, id)
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	out.Revision = doc.Revision
	return out
}

// TestModel 用临时 Pi 会话真的发一次最小请求。
//
// 这是唯一会真实产生调用费用的操作，且必须由用户点击触发；
// 界面侧要显示"可能计费"。
func (d *Desktop) TestModel(id string) ModelProbeResponse {
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()

	svc, err := d.resourceServices()
	if err != nil {
		return ModelProbeResponse{Error: toCallError(err)}
	}
	def, err := svc.Get(ctx, id)
	if err != nil {
		return ModelProbeResponse{Error: toCallError(err)}
	}
	secret, err := svc.Secret(ctx, def.CredentialRef)
	if err != nil {
		return ModelProbeResponse{Error: toCallError(err)}
	}

	d.mu.Lock()
	s := d.settings
	d.mu.Unlock()
	inst, err := d.resolveInstallation(s)
	if err != nil {
		return ModelProbeResponse{Error: toCallError(err)}
	}

	prober := resources.NewProber(filepath.Join(d.root, "probes"), inst)
	return ModelProbeResponse{Result: prober.Probe(ctx, def, secret)}
}

// SetActiveModel 把某个模型设为"当前模型"（下一次 run 生效）。
//
// 进行中的 run 不受影响：换模型不能把正在跑的一轮连根拔掉。
func (d *Desktop) SetActiveModel(ref string) ModelAction {
	out := ModelAction{Ref: strings.TrimSpace(ref)}
	svc, err := d.resourceServices()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	ctx := context.Background()

	if out.Ref != "" {
		def, err := svc.Get(ctx, out.Ref)
		if err != nil {
			out.Error = toCallError(contracts.NewAppError(contracts.CodeNotFound,
				"模型不存在: "+out.Ref, false))
			return out
		}
		if def.Enabled != nil && !*def.Enabled {
			out.Error = toCallError(contracts.NewAppError(contracts.CodeDependencyMissing,
				"模型 "+out.Ref+" 已禁用，不能设为当前模型", false))
			return out
		}
		// 旧格式条目（只有 "provider/model" 一个字符串）编译进 models.json 会得到空
		// provider 键，直接拒是对的——但"拒绝"必须给出可行的下一步，而不是让用户去猜。
		//
		// 实测（2026-11-08）：用户按 Pi 的习惯只填了 `deepseek-2/deepseek-v4.1-flash`，
		// 保存后点"设为当前模型"得到 UNSUPPORTED，界面也没说该填什么。
		// 这里就地补齐并写回：以最后一个 "/" 切分（模型名本身允许带斜杠），
		// provider 名沿用该模型已有的 Provider（若为空则用前缀）。
		if strings.TrimSpace(def.ProviderID) == "" || strings.TrimSpace(def.ModelID) == "" {
			providerID := strings.TrimSpace(def.ProviderID)
			modelID := strings.TrimSpace(def.ModelID)
			// 用户最常见的填法：只给了模型名字符串 `provider/模型`。先在模型名里切
			// （模型名本身允许带斜杠，所以按**第一个**斜杠切 provider），再退回 ID。
			// 只补供应商名；模型名原样保留（允许含斜杠，那才是发给 API 的名字）。
			if providerID == "" {
				base := modelID
				if base == "" {
					base = out.Ref
				}
				if i := strings.Index(base, "/"); i > 0 {
					providerID = base[:i]
				} else {
					providerID = "custom"
				}
			}
			if modelID == "" {
				modelID = out.Ref
			}
			if providerID == "" || modelID == "" {
				out.Error = toCallError(contracts.NewAppError(contracts.CodeUnsupported,
					"模型 "+out.Ref+" 缺少 provider 名或模型名：请按 <provider>/<模型名> 填写，例如 gateway/deepseek-v4.1-flash", false))
				return out
			}
			def.ProviderID = providerID
			def.ModelID = modelID
			current, listErr := svc.List(ctx)
			if listErr != nil {
				out.Error = toCallError(listErr)
				return out
			}
			if _, err := svc.Save(ctx, current.Revision, def, ""); err != nil {
				out.Error = toCallError(contracts.NewAppError(contracts.CodeConfigConflict,
					"补齐旧格式模型时保存失败（配置已被改动）："+err.Error(), true))
				return out
			}
		}
	}

	d.mu.Lock()
	s := d.settings
	d.mu.Unlock()
	s.ModelRef = out.Ref
	if out.Ref != "" {
		// 切到资源库模型后，设置里的明文密钥不再被任何路径读取。
		// 继续把它留在 settings.json 里，只是把一个不再需要的秘密留在磁盘上。
		s.APIKey = ""
	}
	// 只落盘、不关运行时：正在跑的 run 不受影响，新模型在下一个安全点生效。
	if err := d.persistSettings(s); err != nil {
		out.Error = toCallError(err)
		return out
	}

	// 有 run 在跑时，新模型要等空闲才生效——界面上如实说明，不要假装已切换。
	d.mu.Lock()
	if d.svc != nil && (d.svc.ActiveCount() > 0 || d.svc.QueuedCount() > 0) {
		out.Deferred = true
	}
	d.mu.Unlock()

	// 旧 models.json 里可能还留着上一次运行写下的明文密钥。切到凭证库路径后
	// 它已没有任何读取者，留在磁盘上只会变成 SEC07 的漏洞——顺手覆盖掉。
	d.scrubModelsFile()
	return out
}

// scrubModelsFile 用当前配置重写 models.json，把可能残留的明文密钥覆盖掉。
//
// 只在空闲时做：正在跑的 Pi 已经加载了这份文件，替换它对那一轮没有好处，
// 而下一次重建运行时会重新写一遍。失败也不上报——这是清理，不是切换的一部分；
// 真正要跑的时候写不出来，那一步会自己报错。
func (d *Desktop) scrubModelsFile() {
	d.rtMu.Lock()
	defer d.rtMu.Unlock()

	d.mu.Lock()
	busy := d.svc != nil && (d.svc.ActiveCount() > 0 || d.svc.QueuedCount() > 0)
	d.mu.Unlock()
	if busy {
		return
	}
	setup, err := d.resolveModelSetup(context.Background())
	if err != nil || len(setup.ModelsJSON) == 0 {
		return
	}
	_ = resources.WritePiModels(d.agentDir(), resources.PiModels{JSON: setup.ModelsJSON})
}

// CurrentModel 返回当前使用的模型。
func (d *Desktop) CurrentModel() CurrentModelResult {
	d.mu.Lock()
	s := d.settings
	d.mu.Unlock()

	ref := strings.TrimSpace(s.ModelRef)
	if ref == "" {
		out := CurrentModelResult{Source: "settings", Provider: s.Provider, Model: s.Model}
		if out.Provider == "" && out.Model == "" {
			out.Source = "none"
		}
		return out
	}
	svc, err := d.resourceServices()
	if err != nil {
		return CurrentModelResult{Source: "resources", Ref: ref, Error: toCallError(err)}
	}
	def, err := svc.Get(context.Background(), ref)
	if err != nil {
		return CurrentModelResult{Source: "resources", Ref: ref, Error: toCallError(err)}
	}
	set, _ := svc.List(context.Background())
	return CurrentModelResult{
		Source:   "resources",
		Ref:      ref,
		Provider: def.ProviderID,
		Model:    def.ModelID,
		Item:     def,
		Revision: set.Revision,
	}
}

// ModelsForProfile 返回可用于 profile 选择的模型 ID 列表（供 C01 编辑器用）。
func (d *Desktop) ModelsForProfile() []string {
	set := d.ListModels()
	out := make([]string, 0, len(set.Items))
	for _, m := range set.Items {
		if m.Enabled != nil && !*m.Enabled {
			continue
		}
		out = append(out, m.ID)
	}
	return out
}

// ---------------------------------------------------------------- MP1：模型维护
//
// 用户诉求："自动读取本地已有的模型，并且可以额外添加模型和供应商"。
// 这一层只做转接：发现、探测、导入、供应商 CRUD 的判定都在 resources。
// 明文 HTTP 的放行规则同样在 resources（私网放行并标 insecure，公网要显式确认）。

// ModelDiscoveryResult 是本地端点自动发现的结果。
type ModelDiscoveryResult struct {
	Items []resources.DiscoveredEndpoint `json:"items"`
	Error *CallError                     `json:"error,omitempty"`
}

// ModelProviderProbeResult 是用户显式扫描任意端点的结果。
//
// Probe.Error 是端点侧的分类失败（不可达/超时/HTTP 错误），外层 Error 是
// "这次扫描根本没发起"（端点非法、公网明文未确认、凭证不存在）。
type ModelProviderProbeResult struct {
	Probe resources.ProviderProbe `json:"probe"`
	Error *CallError              `json:"error,omitempty"`
}

// ModelImportResult 是一次导入的结论。
type ModelImportResult struct {
	Imported []string                    `json:"imported"`
	Updated  []string                    `json:"updated"`
	Skipped  []string                    `json:"skipped"`
	Models   []resources.ModelDefinition `json:"models"`
	Revision uint64                      `json:"revision"`
	Error    *CallError                  `json:"error,omitempty"`
}

// ProviderListResult 是供应商列表。
type ProviderListResult struct {
	Items    []resources.Provider `json:"items"`
	Revision uint64               `json:"revision"`
	Error    *CallError           `json:"error,omitempty"`
}

// ProviderResult 是单个供应商操作的结果。
type ProviderResult struct {
	Item     resources.Provider `json:"item"`
	Revision uint64             `json:"revision"`
	Error    *CallError         `json:"error,omitempty"`
}

// DiscoverModels 自动探测回环上的本地模型端点。
//
// 这是静默动作，所以只碰回环；要扫局域网/公网端点必须走 ProbeProviderModels
// （用户显式输入地址并点击）。
func (d *Desktop) DiscoverModels() ModelDiscoveryResult {
	out := ModelDiscoveryResult{Items: []resources.DiscoveredEndpoint{}}
	svc, err := d.resourceServices()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	items, err := svc.DiscoverModels(ctx)
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	if items != nil {
		out.Items = items
	}
	return out
}

// ProbeProviderModels 扫描用户输入的端点（含局域网明文网关）。
//
// allowInsecure 必须是用户的显式确认：公网 + 明文 HTTP 只有它为 true 才放行，
// 且返回的 Insecure 会告诉界面"凭据会以明文经过网络"。
func (d *Desktop) ProbeProviderModels(baseURL, api, credentialRef string, allowInsecure bool) ModelProviderProbeResult {
	svc, err := d.resourceServices()
	if err != nil {
		return ModelProviderProbeResult{Error: toCallError(err)}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	probe, err := svc.ProbeProviderModels(ctx, baseURL, api, credentialRef, allowInsecure)
	if err != nil {
		return ModelProviderProbeResult{Error: toCallError(err)}
	}
	return ModelProviderProbeResult{Probe: probe}
}

// ImportModels 把扫描到的模型导入成模型定义（按 provider+model 幂等）。
func (d *Desktop) ImportModels(providerID, baseURL, api string, modelIDs []string, defaults resources.ImportDefaults, allowInsecure bool) ModelImportResult {
	out := ModelImportResult{Imported: []string{}, Updated: []string{}, Skipped: []string{}, Models: []resources.ModelDefinition{}}
	svc, err := d.resourceServices()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	res, err := svc.ImportModels(context.Background(), resources.ImportSpec{
		ProviderID:    providerID,
		BaseURL:       baseURL,
		API:           api,
		ModelIDs:      modelIDs,
		Defaults:      defaults,
		AllowInsecure: allowInsecure,
	})
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	out.Imported = res.Imported
	out.Updated = res.Updated
	out.Skipped = res.Skipped
	out.Revision = res.Revision
	if res.Models != nil {
		out.Models = res.Models
	}
	return out
}

// ListProviders 列出供应商（显式记录 + 只靠模型出现的 providerId）。
func (d *Desktop) ListProviders() ProviderListResult {
	out := ProviderListResult{Items: []resources.Provider{}}
	svc, err := d.resourceServices()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	set, err := svc.ListProviders(context.Background())
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	if set.Items != nil {
		out.Items = set.Items
	}
	out.Revision = set.Revision
	return out
}

// SaveProvider 新建/更新供应商，并让引用它的模型一起指向新端点。
func (d *Desktop) SaveProvider(id, baseURL, api string, allowInsecure bool) ProviderResult {
	out := ProviderResult{}
	svc, err := d.resourceServices()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	item, revision, err := svc.SaveProvider(context.Background(), id, baseURL, api, allowInsecure)
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	out.Item = item
	out.Revision = revision
	return out
}

// DeleteProvider 删除供应商；仍有模型引用时明确拒绝并列出引用者。
func (d *Desktop) DeleteProvider(id string) ProviderResult {
	out := ProviderResult{}
	svc, err := d.resourceServices()
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	revision, err := svc.DeleteProvider(context.Background(), id)
	if err != nil {
		out.Error = toCallError(err)
		return out
	}
	out.Revision = revision
	return out
}
