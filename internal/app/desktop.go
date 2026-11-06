// Package app 是桌面宿主与运行时之间的绑定层。
//
// 它只做三件事：把设置变成一次可执行的环境（Pi 安装件、隔离 agentDir、
// models.json、策略服务），把 UI 的调用转成 RunService 的方法调用，
// 把 RunService 的事件转成 Wails 事件。业务判断都不在这里。
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"my-agent-go/internal/config"
	"my-agent-go/internal/contracts"
	"my-agent-go/internal/credentials"
	"my-agent-go/internal/harnessmcp"
	"my-agent-go/internal/memory"
	"my-agent-go/internal/piinstall"
	"my-agent-go/internal/policy"
	"my-agent-go/internal/profile"
	"my-agent-go/internal/resources"
	"my-agent-go/internal/run"
	"my-agent-go/internal/runtime/pi"
	"my-agent-go/internal/workspace"
)

// EventName 是推给前端的 Wails 事件名。
const EventName = "myagent:run-event"

// Settings 是桌面端的可编辑设置。
//
// 凭证处理边界：本版把 apiKey 写进 Pi 自己的 models.json（Pi 的原生方式）。
// 后续接入 F07 的 CredentialStore 后，models.json 只留引用、不留明文。
type Settings struct {
	// PiPath 是 Pi 可执行文件；若指向 .js 入口，则用 NodePath 启动。
	PiPath string `json:"piPath"`
	// PiSavedPath 是上一次在候选里选定的 Pi（saved-path 来源）。
	// 与 PiPath（用户显式指定，优先级更高）分开：用户指定的路径失效时仍要报
	// preferred 不可用，而选定的路径失效时要报"上次选的 Pi 不在了"。
	//
	// 它只是**发现来源**，不是运行路径的第三优先级：resolveInstallation 仍是
	// settings.piPath → <managed>/active.json → CONFIG_MISSING，不读这个字段。
	PiSavedPath string `json:"piSavedPath,omitempty"`
	// NodePath 仅在 PiPath 为 .js 时使用。
	NodePath string `json:"nodePath"`
	// Provider 是 models.json 里的 provider 键名。
	Provider string `json:"provider"`
	// BaseURL 是 OpenAI 兼容端点，例如 http://127.0.0.1:8080/v1。
	BaseURL string `json:"baseUrl"`
	// Model 是模型 id。
	Model string `json:"model"`
	// APIKey 交给 Pi 的 models.json。
	//
	// 仅用于"还没有任何模型定义"的旧配置路径（fixture/本机端点）。
	// 一旦 ModelRef 指向资源库里的模型，密钥改从 OS 凭证库取，
	// 这里不再被读到（也不会被写进 models.json 明文）。
	APIKey string `json:"apiKey"`
	// ModelRef 指向资源库中的模型定义；空表示沿用上面四个旧字段。
	ModelRef string `json:"modelRef"`
	// Workspace 是 agent 的工作目录（文件读写的边界）。
	Workspace string `json:"workspace"`
	// ShellAllowed 控制是否授予 shell 权限。
	ShellAllowed bool `json:"shellAllowed"`
}

// Status 是给 UI 的环境概览。
type Status struct {
	AppRoot   string `json:"appRoot"`
	AgentDir  string `json:"agentDir"`
	PiPath    string `json:"piPath"`
	PiVersion string `json:"piVersion"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	ModelRef  string `json:"modelRef"`
	Workspace string `json:"workspace"`
	// RuntimeProvider/Model 是最近一次构建实际交给 Pi 的模型。
	// 与上面的 Provider/Model（设置里的值）分开：设置里写的不一定是正在跑的。
	RuntimeProvider string `json:"runtimeProvider"`
	RuntimeModel    string `json:"runtimeModel"`
	// RuntimePending 为 true 表示模型已改，但还有 run 在跑，等空闲才生效。
	RuntimePending bool `json:"runtimePending"`
	// RuntimeSkills 是最近一次构建**托管且启用**、并通过 --skill 显式交给 Pi 的
	// 技能 ID（RES02）。空表示没有托管技能被显式加载，**不是**"没有技能可用"：
	// Pi 自己的自动发现仍可能加载了它发现的技能。
	RuntimeSkills []string `json:"runtimeSkills"`
	// RuntimeMCP 是最近一次构建实际写进 agentDir/mcp.json 的外部 MCP 服务名。
	// 空表示没有启用任何外部 MCP（自有 harness MCP 不在此列）。
	RuntimeMCP []string `json:"runtimeMCP"`
	// MCPPending 表示 MCP 配置与运行中的集合不一致（下一次安全点才生效）。
	MCPPending bool `json:"mcpPending"`
	// SkillAutoDiscovery 是当前运行参数下 Pi 侧技能自动发现的状态
	// （contracts.SkillDiscoveryEnabled / Disabled）。自动发现到的清单产品拿不到，
	// 所以这里只有开关，没有名次。
	SkillAutoDiscovery string `json:"skillAutoDiscovery"`
	// SkillsPending 表示托管技能的配置与运行中的不一致（内容摘要或集合变了，
	// 要等下一个 run 才生效）。
	SkillsPending bool   `json:"skillsPending"`
	Isolation     string `json:"isolation"`
	PolicyActive  bool   `json:"policyActive"`
	Platform      string `json:"platform"`
}

// Desktop 是绑定到前端的服务对象。
type Desktop struct {
	root     string
	settings Settings

	mu      sync.Mutex
	svc     *run.Service
	emitter func(contracts.Event)
	lastErr string
	// policyExt 是解析到的策略扩展绝对路径，供状态展示复用。
	policyExt string
	// harness 是自有 MCP 服务（应用级单例）。端口与令牌固定，
	// 因此 mcp.json 只需写一次，多个 run 共用同一端点。
	harness *harnessmcp.Server

	// svcMu 保护 C01/C02 服务（工作区/Profile/模型/凭证）的懒加载。
	// 与 d.mu 分开：持有它期间会做磁盘读写，不该连带把 Status 也卡住。
	svcMu      sync.Mutex
	wsSvc      *workspace.Service
	profileSvc *profile.Service
	modelSvc   *resources.Service
	creds      credentials.Store
	// memSvc 是 C05 的记忆服务（应用级单例，同一个 memory.db）。
	memSvc *memory.Service
	// runtimeSessionID 是最近一次构建的 run 会话，记忆用它记录来源。
	runtimeSessionID string

	// workerStarter 允许测试注入假 worker；nil 时 RunService 用真实的 pi.StartWorker。
	// 生产路径不设它。
	workerStarter run.WorkerStarter

	// piRequestOverride 允许测试覆盖 Pi 发现环境（PATH/常见位置/版本探针），
	// 免得测试结论依赖宿主机真实 PATH。生产恒为 nil。
	piRequestOverride func(pi.DiscoveryRequest) pi.DiscoveryRequest

	// piInstallMu 保护 F06 安装服务单例；piInstallOverride 允许测试注入清单/
	// HTTP 客户端/探针（生产恒为 nil，生产路径只用嵌入清单与真实探针）。
	piInstallMu       sync.Mutex
	piInstallSvc      *piinstall.InstallService
	piInstallOverride func(*piinstall.InstallService)

	// rtMu 串行化运行时构建/重建，避免两次 Start 同时建出两个 Pi 运行时。
	rtMu sync.Mutex
	// runtimeKey 描述当前运行时的模型来源；模型或配置变了就要重建。
	runtimeKey        string
	pendingRuntimeKey string
	// runtimeProvider/Model 是最近一次构建实际使用的模型（诊断展示用）。
	runtimeProvider string
	runtimeModel    string
	// runtimeSkills/Digest 是最近一次构建**托管且启用**的技能集合（RES02 的"旧 run 保持快照"）。
	// 它只是"显式 --skill 的那几条"，不是 Pi 实际可见的全部技能。
	// Digest 同时参与运行时签名：编辑 SKILL.md 内容不会改配置 revision，
	// 只有内容摘要能发现"技能变了"。
	runtimeSkills       []string
	runtimeSkillDigest  string
	runtimeExtensionDig string
	// runtimeMCP/runtimeMCPDigest 是最近一次构建交给 Pi 的外部 MCP 集合。
	// digest 参与运行时签名：改服务/改参数/改暴露策略都会重建 runtime，
	// 有 run 在跑时则记 pending（旧 run 不静默变化）。
	runtimeMCP       []string
	runtimeMCPDigest string
}

// New 构造 Desktop。root 是应用数据根目录。
func New(root string) (*Desktop, error) {
	d := &Desktop{root: root}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("创建应用数据目录失败: %w", err)
	}
	s, err := d.loadSettings()
	if err != nil {
		return nil, err
	}
	d.settings = s
	// 默认工作区先建出来，避免首次启动就撞"目录不存在"。
	// 建不出来不阻断启动：真正的原因（只读盘、无权限）会在 Start 时
	// 以更准确的错误码出现，比在启动阶段吞掉它更有用。
	if s.Workspace != "" {
		_ = os.MkdirAll(s.Workspace, 0o755)
	}
	return d, nil
}

// SetEmitter 注入事件出口（Wails 环境下是 app.Event.Emit）。
func (d *Desktop) SetEmitter(fn func(contracts.Event)) {
	d.mu.Lock()
	d.emitter = fn
	d.mu.Unlock()
}

func (d *Desktop) settingsPath() string { return filepath.Join(d.root, "settings.json") }

func (d *Desktop) loadSettings() (Settings, error) {
	s := defaultSettings(d.root)
	if raw, err := os.ReadFile(d.settingsPath()); err == nil {
		// 用户已保存的设置优先；解析失败不静默丢弃，交由调用方看到错误。
		if err := json.Unmarshal(raw, &s); err != nil {
			return s, fmt.Errorf("解析 settings.json 失败: %w", err)
		}
	}
	applyEnvOverrides(&s)
	return s, nil
}

// defaultSettings 给出可用的默认值：优先本机基线的 Node 与隔离安装的 Pi 1.5.0。
//
// 默认工作区路径指向应用数据目录下的 workspace，并会在 New 里创建，
// 但**不会**默认信任：目录存在只代表"这里可以放文件"，
// 允许在其中执行代码必须是用户的显式动作（C01）。
func defaultSettings(root string) Settings {
	s := Settings{
		Provider:  "fixture",
		BaseURL:   "http://127.0.0.1:0/v1",
		Model:     "fixture-model",
		Workspace: filepath.Join(root, "workspace"),
	}
	if runtime.GOOS == "windows" {
		if p := `D:\Program Files\nodejs\node.exe`; fileExists(p) {
			s.NodePath = p
		}
	}
	return s
}

// applyEnvOverrides 允许用环境变量驱动一次运行（自动化验证用，不改变 UI 行为）。
func applyEnvOverrides(s *Settings) {
	set := func(dst *string, keys ...string) {
		for _, k := range keys {
			if v := strings.TrimSpace(os.Getenv(k)); v != "" {
				*dst = v
				return
			}
		}
	}
	set(&s.PiPath, "MYAGENT_PI_PATH")
	set(&s.NodePath, "MYAGENT_NODE_PATH")
	set(&s.Provider, "MYAGENT_PROVIDER")
	set(&s.BaseURL, "MYAGENT_BASE_URL")
	set(&s.Model, "MYAGENT_MODEL")
	set(&s.APIKey, "MYAGENT_API_KEY")
	set(&s.Workspace, "MYAGENT_WORKSPACE")
	if v := strings.TrimSpace(os.Getenv("MYAGENT_SHELL")); v != "" {
		s.ShellAllowed = v == "1" || strings.EqualFold(v, "true")
	}
}

// Settings 返回当前设置（不含任何隐式改写）。
func (d *Desktop) Settings() Settings {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.settings
}

// SaveSettings 持久化设置并丢弃旧的 RunService（下次 Start 用新配置重建）。
//
// 它会关闭运行时，因此会中断正在跑的 run。只改"下一次运行用哪个模型"时
// 不要走这里——用 persistSettings（SetActiveModel 就是这么做的）。
func (d *Desktop) SaveSettings(s Settings) error {
	if err := d.persistSettings(s); err != nil {
		return err
	}
	d.mu.Lock()
	old := d.svc
	d.svc = nil
	d.mu.Unlock()
	if old != nil {
		_ = old.Close(context.Background())
	}
	return nil
}

// persistSettings 只落盘并更新内存里的设置，不动正在跑的运行时。
//
// 模型切换走这条路：运行中换模型不能把当前这一轮连根拔掉，
// 新配置由 ensureRuntime 的签名比较在空闲时生效（MOD04 的安全点）。
func (d *Desktop) persistSettings(s Settings) error {
	if strings.TrimSpace(s.Workspace) == "" {
		return fmt.Errorf("工作目录不能为空")
	}
	abs, err := filepath.Abs(s.Workspace)
	if err != nil {
		return fmt.Errorf("工作目录无法绝对化: %w", err)
	}
	s.Workspace = abs
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return fmt.Errorf("创建工作目录失败: %w", err)
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(d.settingsPath(), raw, 0o600); err != nil {
		return fmt.Errorf("写 settings.json 失败: %w", err)
	}
	d.mu.Lock()
	d.settings = s
	d.mu.Unlock()
	return nil
}

// isolationSpec 是"实际传给 Pi 的隔离参数"的唯一构造点。
//
// Status 展示与 ensureRuntime 启动都必须调用它。这两处曾经各写一份，
// 结果是界面显示 --no-mcp、实际却启用了 harness MCP——
// 诊断信息不准比没有诊断信息更糟，它会把人带去错误的方向。
func isolationSpec(policyExt string) pi.IsolationSpec {
	return pi.IsolationSpec{PolicyExtension: policyExt, HarnessMCP: true}
}

// isolationSpecWith 在隔离参数上追加本次要加载的技能与托管扩展。
//
// 单独一个函数而不是改 isolationSpec 的签名：Status 只需要"隔离参数"，
// 给它多传两个参数会让展示路径与启动路径的构造点再次分叉。
func isolationSpecWith(policyExt string, skills, extraExts []string) pi.IsolationSpec {
	return pi.IsolationSpec{
		PolicyExtension: policyExt,
		HarnessMCP:      true,
		ExtraExtensions: extraExts,
		Skills:          skills,
	}
}

// skillAutoDiscoveryFromArgs 从**实际交给 Pi 的参数**读出技能自动发现的状态。
//
// 判据只有一个：参数里到底有没有抑制发现的 flag（-ns / --no-skills）。
// 不另立一个"设置项"：历史上界面显示 --no-mcp、实际却启用了 MCP，
// 就是因为展示侧和启动侧各写了一份"应该是什么"。这里两边读同一份 args。
func skillAutoDiscoveryFromArgs(args []string) string {
	for _, a := range args {
		switch a {
		case "-ns", "--no-skills":
			return contracts.SkillDiscoveryDisabled
		}
	}
	return contracts.SkillDiscoveryEnabled
}

// Status 返回环境概览。
func (d *Desktop) Status() Status {
	d.mu.Lock()
	s := d.settings
	ext := d.policyExt
	svc := d.svc
	d.mu.Unlock()

	// 已启动时直接读运行时那份 spec —— 展示值与实际启动参数同源，
	// 结构上就不可能出现"显示 --no-mcp、实际启用了 MCP"。
	// 未启动时按同样的构造点推算，并先解析策略扩展路径，
	// 免得显示一串缺 -e <policy> 的参数让人以为策略没生效。
	var args []string
	if svc != nil {
		args = svc.IsolationArgs()
	} else {
		if ext == "" {
			if p, err := findPolicyExtension(); err == nil {
				ext = p
			}
		}
		args = isolationSpec(ext).Args()
	}

	d.mu.Lock()
	runtimeProvider := d.runtimeProvider
	runtimeModel := d.runtimeModel
	runtimeSkills := append([]string{}, d.runtimeSkills...)
	runtimeSkillDigest := d.runtimeSkillDigest
	runtimeMCP := append([]string{}, d.runtimeMCP...)
	runtimeMCPDigest := d.runtimeMCPDigest
	pending := d.pendingRuntimeKey != "" && d.pendingRuntimeKey != d.runtimeKey
	d.mu.Unlock()

	// 自动发现的状态从同一份 args 推出：展示值与实际启动参数同源。
	autoDiscovery := skillAutoDiscoveryFromArgs(args)

	// 运行中的托管技能摘要与当前配置不一致 → 说明改了还没生效。
	// 这里只做"能不能读配置"的降级：读不出来时保守地不说"已生效"。
	//
	// 不随着"自动发现开着"退化成永远 false：自动发现不改变托管技能的台账，
	// 而用户真正要确认的是"我刚启用的那条托管技能到底进了参数没有"。
	skillsPending := false
	if runtimeSkillDigest != "" {
		if plan, planErr := d.compileSkills(); planErr == nil {
			skillsPending = plan.Digest != runtimeSkillDigest
		}
	}
	// MCP 同理：禁用的服务在下一个安全点从 mcp.json 移除；在此之前
	// 界面必须说“还没生效”，而不是让人以为已经断开。
	mcpPending := false
	if runtimeMCPDigest != "" {
		if plan, planErr := d.compileMCP(); planErr == nil {
			mcpPending = plan.Digest != runtimeMCPDigest
		}
	}

	st := Status{
		AppRoot:            d.root,
		AgentDir:           d.agentDir(),
		PiPath:             s.PiPath,
		Provider:           s.Provider,
		Model:              s.Model,
		ModelRef:           s.ModelRef,
		Workspace:          s.Workspace,
		Platform:           runtime.GOOS + "/" + runtime.GOARCH,
		Isolation:          strings.Join(args, " "),
		RuntimeProvider:    runtimeProvider,
		RuntimeModel:       runtimeModel,
		RuntimePending:     pending,
		RuntimeSkills:      runtimeSkills,
		RuntimeMCP:         runtimeMCP,
		MCPPending:         mcpPending,
		SkillAutoDiscovery: autoDiscovery,
		SkillsPending:      skillsPending,
	}
	st.PolicyActive = true
	return st
}

// agentDir 返回该 workspace 的隔离 agent 目录。
func (d *Desktop) agentDir() string {
	store, err := config.NewStore(filepath.Join(d.root, "config"))
	if err != nil {
		return filepath.Join(d.root, "agents", "default")
	}
	return store.AgentDir("ws-default", "profile-default")
}

// modelSetup 描述"这次运行该用哪个模型"，以及它的来源。
type modelSetup struct {
	Provider string
	Model    string
	// ModelsJSON 非空表示用资源库编译出的 models.json（密钥只经 Env 交付）。
	ModelsJSON []byte
	Env        []string
	Revision   uint64
	// Source 是 "settings" 或 "resources"，只用于诊断与运行时签名。
	Source string
	Ref    string
}

// key 是运行时的身份：任一项变化都必须重建，否则改完模型还在跑旧的那个。
func (m modelSetup) key() string {
	return m.Source + "|" + m.Ref + "|" + m.Provider + "|" + m.Model + "|" + strconv.FormatUint(m.Revision, 10)
}

// resolveModelSetup 决定本次运行使用的模型。
//
// 优先 ModelRef（资源库里的定义）：那条路径上密钥只在 OS 凭证库，
// 配置与 models.json 都不含明文。没有 ModelRef 时退回旧的四个设置字段，
// 那是 fixture / 本机端点的路径——它把密钥放在 settings.json 里（界面会告警），
// 但 models.json 仍然只写 $ENV 引用，不在磁盘上多留一份明文。
func (d *Desktop) resolveModelSetup(ctx context.Context) (modelSetup, error) {
	d.mu.Lock()
	s := d.settings
	d.mu.Unlock()

	ref := strings.TrimSpace(s.ModelRef)
	if ref == "" {
		return legacyModelSetup(s)
	}
	svc, err := d.resourceServices()
	if err != nil {
		return modelSetup{}, err
	}
	def, err := svc.Get(ctx, ref)
	if err != nil {
		return modelSetup{}, contracts.NewAppError(contracts.CodeDependencyMissing,
			"当前模型 "+ref+" 不存在（可能已被删除）；请在资源库中重新选择并设为当前模型", false)
	}
	if def.Enabled != nil && !*def.Enabled {
		return modelSetup{}, contracts.NewAppError(contracts.CodeDependencyMissing,
			"当前模型 "+ref+" 已被禁用；请启用它或改选其它模型", false)
	}
	if strings.TrimSpace(def.ProviderID) == "" || strings.TrimSpace(def.ModelID) == "" {
		return modelSetup{}, contracts.NewAppError(contracts.CodeDependencyMissing,
			"当前模型 "+ref+" 是旧格式（缺 provider 名或模型名），无法编译成 models.json；"+
				"请在资源库里编辑补齐后再设为当前模型", false)
	}
	pm, err := svc.CompilePiModels(ctx)
	if err != nil {
		return modelSetup{}, err
	}
	if len(pm.JSON) == 0 {
		return modelSetup{}, contracts.NewAppError(contracts.CodeConfigMissing,
			"没有任何可用的模型定义，无法生成 models.json；"+describeSkipped(pm.Skipped)+
				"请先在资源库里补齐或新建一个模型", false)
	}
	set, err := svc.List(ctx)
	if err != nil {
		return modelSetup{}, err
	}
	return modelSetup{
		Provider:   def.ProviderID,
		Model:      def.ModelID,
		ModelsJSON: pm.JSON,
		Env:        pm.Env,
		Revision:   set.Revision,
		Source:     "resources",
		Ref:        ref,
	}, nil
}

// legacyModelSetup 把旧的四个设置字段（provider/baseUrl/model/apiKey）编译成
// 与资源库完全相同的一份 models.json。
//
// 用同一套编译实现而不是另写一段拼装代码：两段代码一旦分叉，"测过的"和
// "实际跑的"就会是两份不同的配置，而症状要到 provider 报错时才出现。
func legacyModelSetup(s Settings) (modelSetup, error) {
	baseURL := strings.TrimSpace(s.BaseURL)
	model := strings.TrimSpace(s.Model)
	if baseURL == "" || model == "" {
		return modelSetup{}, contracts.NewAppError(contracts.CodeConfigMissing, "缺少 provider 端点或模型 id", false)
	}
	provider := strings.TrimSpace(s.Provider)
	if provider == "" {
		provider = "fixture"
	}
	// Pi 的 models.json schema 要求 apiKey 至少 1 个字符，空值会让**整份**文件
	// 校验失败，症状伪装成"provider 不存在"——占位值由编译层统一填。
	pm, err := resources.CompileLegacy(resources.ModelDefinition{
		ID:            "settings",
		ProviderID:    provider,
		ModelID:       model,
		API:           "openai-completions",
		BaseURL:       baseURL,
		InputKinds:    []string{"text"},
		ContextWindow: 128000,
		MaxOutput:     8192,
	}, s.APIKey)
	if err != nil {
		return modelSetup{}, err
	}
	return modelSetup{
		Provider:   provider,
		Model:      model,
		ModelsJSON: pm.JSON,
		Env:        pm.Env,
		Source:     "settings",
	}, nil
}

// describeSkipped 把"哪些条目没参与编译"说成一句人话。
func describeSkipped(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return "（" + strings.Join(ids, "、") + " 是旧格式，缺少 provider 名或模型名）"
}

// ensureRuntime 按需构建 RunService。
//
// 模型可以在两次 run 之间切换，但不能在 run 进行中切换：那会把正在跑的一轮
// 连根拔掉。这里的规则是——空闲就重建，有 run 在跑就继续用旧运行时，
// 并把新配置记在 pendingRuntimeKey 里，等下一次空闲时生效（MOD04）。
func (d *Desktop) ensureRuntime() (*run.Service, error) {
	d.rtMu.Lock()
	defer d.rtMu.Unlock()

	ctx := context.Background()
	setup, err := d.resolveModelSetup(ctx)
	if err != nil {
		return nil, err
	}
	// 技能与扩展也在签名里：换模型、改技能、开关扩展都必须重建运行时，
	// 否则"界面显示已启用、Pi 根本没加载"这种偏差会一直存在。
	skills, extPlan, err := d.resolveRuntimeResources()
	if err != nil {
		return nil, err
	}
	// MCP 也进运行时签名：启用/禁用/改参数/改暴露策略都必须重建，
	// 否则“界面已禁用、Pi 还在用旧 mcp.json”会长期存在。
	mcpPlan, err := d.compileMCP()
	if err != nil {
		return nil, err
	}
	key := setup.key() + "|sk:" + skills.Digest + "|ex:" + extPlan.Digest + "|mcp:" + mcpPlan.Digest

	d.mu.Lock()
	if d.svc != nil && d.runtimeKey == key {
		svc := d.svc
		d.mu.Unlock()
		return svc, nil
	}
	existing := d.svc
	emitter := d.emitter
	s := d.settings
	workerStarter := d.workerStarter
	d.mu.Unlock()

	if existing != nil {
		if existing.ActiveCount() > 0 || existing.QueuedCount() > 0 {
			d.mu.Lock()
			d.pendingRuntimeKey = key
			d.mu.Unlock()
			return existing, nil
		}
		_ = existing.Close(ctx)
		d.mu.Lock()
		d.svc = nil
		d.runtimeKey = ""
		d.mu.Unlock()
	}

	inst, err := d.resolveInstallation(s)
	if err != nil {
		return nil, err
	}
	agentDir := d.agentDir()
	if err := run.EnsureAgentDir(agentDir); err != nil {
		return nil, err
	}
	// 两条路径都在这里落同一份 models.json：文件里只有 $ENV 引用，
	// 真实密钥经 setup.Env 交给子进程。
	if err := resources.WritePiModels(agentDir, resources.PiModels{JSON: setup.ModelsJSON}); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.Workspace, 0o700); err != nil {
		return nil, fmt.Errorf("创建工作目录失败: %w", err)
	}

	guard, err := policy.NewPathGuard(s.Workspace)
	if err != nil {
		return nil, err
	}
	// 权限只在这里算一次：既交给策略服务，也交给 RunService 作为 per-run 快照
	// 的默认值。两处读同一份事实，不会出现"策略服务允许 shell、而 run 的快照
	// 里写着别的"的分叉。
	permissions := config.Permissions{
		Filesystem: "workspace",
		Shell:      s.ShellAllowed,
		Network:    false,
	}
	pol := policy.New(policy.Options{
		Permissions: permissions,
		PathGuard:   guard,
		ApprovalTTL: 60 * time.Second,
	})

	// 策略扩展是仓库内的 pi-extensions/harness-policy.ts；找不到就拒绝启动，
	// 而不是让 Pi 在没有执行前授权的情况下跑工具。
	ext, err := findPolicyExtension()
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.policyExt = ext
	d.mu.Unlock()

	// 自有 MCP 是应用级单例：端口与令牌在进程内固定，mcp.json 只写一次，
	// 多个 run 共用同一端点。重建运行时（换模型）不重建它：
	// 换模型与换工具通道是两件事，重启 MCP 会白白打断正在用的端点。
	d.mu.Lock()
	harness := d.harness
	d.mu.Unlock()
	if harness == nil {
		// C05：记忆不可用（数据库打不开）时照常启动 MCP，只是不注册记忆工具；
		// 具体错误在记忆页可见。不能因为记忆故障就拦住聊天。
		mem, memoryWS := d.memoryToolsForRuntime(ctx)
		harness, err = harnessmcp.Start(ctx, harnessmcp.Options{
			// 工具注册表只在这里与工具目录共用同一份构造（harnessTools），
			// 两处各写一份会出现“目录里有、实际没注册”的偏差。
			Tools:   harnessTools(s.Workspace, mem, memoryWS),
			Version: d.version(),
		})
		if err != nil {
			return nil, err
		}
		d.mu.Lock()
		d.harness = harness
		d.mu.Unlock()
	}
	if err := writeMCPConfig(agentDir, harness.Endpoint().URL); err != nil {
		return nil, err
	}
	// 外部 MCP 由这张表拥有：禁用的服务会在下一次构建时从 mcp.json 移除，
	// 而不是留着 enabled:false。秘密只以 ${ENV} 引用出现。
	if err := resources.ApplyMCPPlan(agentDir, mcpPlan); err != nil {
		return nil, err
	}

	// 技能+扩展的隔离参数只在这里构造一次：下面既用它启动 Pi，也用它推出
	// "技能自动发现开着还是关着"。两边读同一份 args，展示与执行不会分叉。
	iso := isolationSpecWith(ext, skills.Paths, extPlan.Paths)
	isolationArgs := iso.Args()

	sessionID := contracts.SessionID(fmt.Sprintf("sess-%d", time.Now().Unix()))
	// 这是应用进程里**唯一**的 RunService，也就是唯一的 admission queue：
	// 交互会话、C08 连接器与 C09 调度都往它里面提交，全局活跃上限固定为 2。
	// 因此这里不能按会话建服务，也不传 MaxActive。
	svc := run.New(run.Options{
		WorkspaceID:  "ws-default",
		SessionID:    sessionID,
		WorkspaceDir: s.Workspace,
		AgentDir:     agentDir,
		Installation: inst,
		// HarnessMCP=true 会以 `-e builtin:mcp` 重新启用内置 MCP（-ne 会连带关掉它）。
		// 项目级 .pi/mcp.json 仍被 -na 切断，能连上的只有 agentDir 里我们自己写的那一条。
		Isolation:      iso,
		Provider:       setup.Provider,
		Model:          setup.Model,
		ConfigRevision: setup.Revision,
		ModelEnv:       setup.Env,
		// 快照记的是这一轮通过 --skill 显式加载的托管技能 ID（RES02），
		// 以及 Pi 自己发现技能的那个开关。两者分开：托管为空不等于没有技能。
		ManagedSkillIDs:    skills.IDs,
		SkillAutoDiscovery: skillAutoDiscoveryFromArgs(isolationArgs),
		Policy:             pol,
		PolicySnapshot:     run.PolicySnapshot{Permissions: permissions},
		// 自有 MCP 的环境 + 外部 MCP 的凭证环境（只经子进程环境交付）。
		HarnessEnv:  append(harness.Env(), mcpPlan.Env...),
		StartWorker: workerStarter,
	})
	if emitter != nil {
		// 退订函数在本层还无人调用：桌面宿主目前没有“UI 断开”的显式信号，
		// 订阅者随进程存活。保留返回值语义（F05-B），不伪造断开事件。
		_ = svc.Subscribe(emitter)
	}

	d.mu.Lock()
	d.svc = svc
	d.runtimeKey = key
	d.runtimeSessionID = string(sessionID)
	d.runtimeProvider = setup.Provider
	d.runtimeModel = setup.Model
	d.runtimeSkills = append([]string{}, skills.IDs...)
	d.runtimeSkillDigest = skills.Digest
	d.runtimeExtensionDig = extPlan.Digest
	d.runtimeMCP = append([]string{}, mcpPlan.IDs...)
	d.runtimeMCPDigest = mcpPlan.Digest
	d.pendingRuntimeKey = ""
	d.mu.Unlock()
	return svc, nil
}

// resolveRuntimeResources 计算本次运行要加载的技能与扩展。
//
// 扩展里包含系统策略扩展（它是 CompileExtensions 的第一项）；它已经通过
// IsolationSpec.PolicyExtension 以 -e 传下去了，这里必须排除，否则同一个
// 扩展会被加载两次。
func (d *Desktop) resolveRuntimeResources() (resources.SkillPlan, resources.ExtensionPlan, error) {
	svc, err := d.resourceServices()
	if err != nil {
		return resources.SkillPlan{}, resources.ExtensionPlan{}, err
	}
	skills, err := svc.CompileSkills(context.Background(), defaultWorkspaceID)
	if err != nil {
		return resources.SkillPlan{}, resources.ExtensionPlan{}, err
	}
	svc.SetSystemExtension(d.systemExtensionPath())
	exts, err := svc.CompileExtensions(context.Background())
	if err != nil {
		return resources.SkillPlan{}, resources.ExtensionPlan{}, err
	}
	policy := d.systemExtensionPath()
	kept := make([]string, 0, len(exts.Paths))
	keptIDs := make([]string, 0, len(exts.IDs))
	for i, p := range exts.Paths {
		if policy != "" && strings.EqualFold(filepath.Clean(p), filepath.Clean(policy)) {
			continue
		}
		kept = append(kept, p)
		keptIDs = append(keptIDs, exts.IDs[i])
	}
	exts.Paths = kept
	exts.IDs = keptIDs
	return skills, exts, nil
}

// resolveInstallation 把设置/托管指针转成可启动的安装件。
//
// 解析顺序（每一条都不得静默回退）：
//  1. settings.piPath —— 用户显式指定。它失效就报错，绝不改用一个托管版本；
//  2. <managed>/active.json —— F06 激活的托管安装。使用前**验证**：可执行体存在，
//     且 sha256 等于激活时记录的 executableSha256。不一致就结构化失败
//     （VERIFICATION_FAILED，带指针路径与期望/实际哈希），同样不改用别的托管版本；
//  3. 都没有 —— CONFIG_MISSING（“未配置 Pi 路径”），与“指针坏了”可区分。
//
// 为什么“当前托管版本”由指针而不是排序回答：F08 的托管候选按**目录名**排序，
// versions/1.5.0-win-x64 排在 1.6.0-win-x64 前面——名字顺序不是版本顺序。
func (d *Desktop) resolveInstallation(s Settings) (pi.PiInstallation, error) {
	if path := strings.TrimSpace(s.PiPath); path != "" {
		return resolveUserInstallation(s, path)
	}
	return resolveActiveManagedInstallation(pi.ManagedRoot())
}

// resolveUserInstallation 是“用户显式指定路径”那条路（行为与 F06 之前一致）。
func resolveUserInstallation(s Settings, path string) (pi.PiInstallation, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return pi.PiInstallation{}, err
	}
	if !fileExists(abs) {
		return pi.PiInstallation{}, contracts.NewAppError(contracts.CodeConfigMissing,
			"Pi 路径不存在: "+abs, false)
	}
	inst := pi.PiInstallation{
		ID:           "local:" + abs,
		Executable:   abs,
		Version:      "1.5.0",
		Origin:       pi.OriginExternal,
		Architecture: runtime.GOARCH,
		Runnable:     true,
	}
	// .js 入口（npm 安装形式）需要 node 前缀。这不是"随便找个 node"：
	// 基线要求固定的 Node 版本，路径由设置显式给出。
	if strings.EqualFold(filepath.Ext(abs), ".js") {
		node := strings.TrimSpace(s.NodePath)
		if node == "" {
			return pi.PiInstallation{}, contracts.NewAppError(contracts.CodeConfigMissing,
				"Pi 是 .js 入口，但未配置 Node 路径", false)
		}
		if !fileExists(node) {
			return pi.PiInstallation{}, contracts.NewAppError(contracts.CodeConfigMissing,
				"Node 路径不存在: "+node, false)
		}
		inst.Executable = node
		inst.Args = []string{abs}
	}
	return inst, nil
}

// resolveActiveManagedInstallation 解析 F06 的 active 指针，并在使用前验证可执行体。
//
// 验证复用 internal/piinstall 的现成实现（ReadActive / ActiveExecutable /
// HashFileContent）：哈希口径只有一份，产品算的哈希就是证据里的哈希。
func resolveActiveManagedInstallation(managedRoot string) (pi.PiInstallation, error) {
	if strings.TrimSpace(managedRoot) == "" {
		return pi.PiInstallation{}, contracts.NewAppError(contracts.CodeConfigMissing,
			"未配置 Pi 路径（settings.piPath），且无法确定托管 Pi 根目录；"+
				"请安装托管 Pi 或在设置里指定路径", false)
	}
	pointerPath := filepath.Join(managedRoot, piinstall.ActiveFileName)
	ptr, err := piinstall.ReadActive(managedRoot)
	if err != nil {
		var appErr *contracts.AppError
		if errors.As(err, &appErr) {
			return pi.PiInstallation{}, contracts.NewAppError(appErr.Code,
				"托管 Pi 指针 "+pointerPath+" 不可用: "+appErr.Message+"；不会静默改用其它托管版本", false)
		}
		return pi.PiInstallation{}, contracts.NewAppError(contracts.CodeVerificationFailed,
			"托管 Pi 指针 "+pointerPath+" 不可读: "+err.Error()+"；不会静默改用其它托管版本", false)
	}
	if ptr == nil {
		return pi.PiInstallation{}, contracts.NewAppError(contracts.CodeConfigMissing,
			"未配置 Pi 路径（settings.piPath），且没有已激活的托管 Pi（"+pointerPath+
				" 不存在）；请安装托管 Pi 或在设置里指定路径", false)
	}
	exe, err := piinstall.ActiveExecutable(managedRoot, ptr)
	if err != nil {
		return pi.PiInstallation{}, activePointerError(managedRoot, ptr, err)
	}
	id := strings.TrimSpace(ptr.InstallationID)
	if id == "" {
		// 旧指针没有 installationID：给一个不会和 F08 候选 ID 混淆的兜底值。
		id = "managed:" + exe
	}
	return pi.PiInstallation{
		ID:           id,
		Executable:   exe,
		Version:      ptr.Version,
		Origin:       pi.OriginManaged,
		Architecture: runtime.GOARCH,
		Source:       pi.SourceManaged,
		Runnable:     true,
	}, nil
}

// activePointerError 把指针校验失败说成一个可修复的结构化错误。
//
// 错误信息必须同时给出指针路径、期望哈希与实际哈希：只说“坏了”会让人无从下手，
// 而“重装”和“有人改过这个文件”是两件不同的事。
func activePointerError(managedRoot string, ptr *piinstall.ActivePointer, cause error) error {
	pointerPath := filepath.Join(managedRoot, piinstall.ActiveFileName)
	expected := strings.TrimSpace(ptr.ExecutableSHA256)
	exe := filepath.Join(managedRoot, filepath.FromSlash(ptr.VersionDir), filepath.FromSlash(ptr.Executable))
	actual, actualErr := piinstall.HashFileContent(exe)
	actualText := actual
	if actualErr != nil {
		actualText = "缺失或不可读（" + actualErr.Error() + "）"
	}
	return contracts.NewAppError(contracts.CodeVerificationFailed,
		fmt.Sprintf("托管 Pi 指针 %s 校验失败（版本 %s，期望 sha256 %s，实际 %s）：%v；"+
			"不会静默改用其它托管版本，请重新安装托管 Pi",
			pointerPath, ptr.Version, expected, actualText, cause), false)
}

// Start 启动一次 run。
//
// 启动前先确认工作区存在且已信任。这道检查必须在绑定层，
// 不能只靠 UI 灰掉发送按钮——UI 可以被绕过（直接调绑定、未来的 CLI），
// 而"未信任不执行"是数据层不变量。
//
// 这里把 Desktop 的设置解析成一次**per-run** 提交（身份 + 工作区），
// 而不是依赖 RunService 实例上的某个默认会话：服务只有一个，提交者有很多。
func (d *Desktop) Start(message string) (contracts.RunSnapshot, error) {
	ctx := context.Background()
	workspaceID, err := d.assertWorkspaceReady(ctx)
	if err != nil {
		d.setLastErr(err.Error())
		return contracts.RunSnapshot{}, err
	}
	// 注入必须在启动 Pi 之前完成：Pi 只看到最终这一条 prompt。
	// 记忆是参考数据；注入失败（含短消息 QueryTooShort）不阻断发送。
	message = d.withMemoryContext(ctx, message)
	svc, err := d.ensureRuntime()
	if err != nil {
		d.setLastErr(err.Error())
		return contracts.RunSnapshot{}, err
	}

	d.mu.Lock()
	sessionID := contracts.SessionID(d.runtimeSessionID)
	workspaceDir := strings.TrimSpace(d.settings.Workspace)
	d.mu.Unlock()

	snap, err := svc.Start(ctx, run.StartRunRequest{
		Message:      message,
		SessionID:    sessionID,
		WorkspaceID:  workspaceID,
		WorkspaceDir: workspaceDir,
	})
	if err != nil {
		d.setLastErr(err.Error())
		return snap, err
	}
	return snap, nil
}

// assertWorkspaceReady 把设置里的工作区路径登记成工作区并检查信任，返回工作区 ID。
//
// 每次 Start 都做一次幂等登记，而不是只在设置保存时做：
// 设置里的路径可能已经被删掉、被改成软链接或换了大小写，
// 只在保存时检查会留下一段"设置说 OK、实际目录已经不是那个"的窗口。
func (d *Desktop) assertWorkspaceReady(ctx context.Context) (contracts.WorkspaceID, error) {
	d.mu.Lock()
	path := strings.TrimSpace(d.settings.Workspace)
	d.mu.Unlock()
	if path == "" {
		return "", contracts.NewAppError(
			contracts.CodeConfigMissing,
			"尚未选择工作区：请先在设置里添加一个目录，并信任它之后再发起任务",
			false,
		)
	}
	ws, _, err := d.coreServices()
	if err != nil {
		return "", err
	}
	item, err := ws.CreateWorkspace(ctx, path)
	if err != nil {
		return "", err
	}
	if err := ws.AssertRunnable(ctx, item.ID); err != nil {
		var appErr *contracts.AppError
		// 只给"未信任"补一句修复指引；其它错误原样透出，
		// 重写文案会把配置冲突之类的原因掩盖成"未信任"。
		if errors.As(err, &appErr) && appErr.Code == contracts.CodeUnauthorized {
			return "", contracts.NewAppError(
				appErr.Code,
				"工作区「"+item.Name+"」尚未信任，不能在其中执行代码；请在设置里确认信任后重试",
				false,
			)
		}
		return "", err
	}
	return item.ID, nil
}

// Abort 停止一次 run。
func (d *Desktop) Abort(runID string) error {
	svc, err := d.runtime()
	if err != nil {
		return err
	}
	return svc.Abort(context.Background(), contracts.RunID(runID))
}

// ResolveApproval 提交一次审批结论。
func (d *Desktop) ResolveApproval(runID, approvalID, choice string) error {
	svc, err := d.runtime()
	if err != nil {
		return err
	}
	return svc.ResolveApproval(context.Background(), contracts.RunID(runID), approvalID, choice)
}

// Replay 返回某 run 在 afterSeq 之后的事件（前端重连用）。
func (d *Desktop) Replay(runID string, afterSeq int) ([]contracts.Event, error) {
	svc, err := d.runtime()
	if err != nil {
		return nil, err
	}
	if afterSeq < 0 {
		afterSeq = 0
	}
	return svc.Replay(contracts.RunID(runID), uint64(afterSeq))
}

// LastError 返回最近一次失败原因（便于 UI 展示，不用翻日志）。
func (d *Desktop) LastError() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastErr
}

func (d *Desktop) setLastErr(s string) {
	d.mu.Lock()
	d.lastErr = s
	d.mu.Unlock()
}

func (d *Desktop) runtime() (*run.Service, error) {
	d.mu.Lock()
	svc := d.svc
	d.mu.Unlock()
	if svc == nil {
		return nil, contracts.NewAppError(contracts.CodeNotFound, "尚未启动任何 run", false)
	}
	return svc, nil
}

// Shutdown 关闭运行时。
func (d *Desktop) Shutdown() {
	d.mu.Lock()
	svc := d.svc
	d.svc = nil
	harness := d.harness
	d.harness = nil
	d.mu.Unlock()
	if svc != nil {
		_ = svc.Close(context.Background())
	}
	// 自有 MCP 与应用同寿命；退出时关掉，避免端口比进程活得久。
	if harness != nil {
		_ = harness.Close(context.Background())
	}
	// 记忆库同理：应用退出时关闭连接。
	d.svcMu.Lock()
	mem := d.memSvc
	d.memSvc = nil
	d.svcMu.Unlock()
	if mem != nil {
		_ = mem.Close()
	}
}

// version 返回构建版本，写进 MCP serverInfo 便于从 Pi 日志确认连的是哪个构建。
func (d *Desktop) version() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := strings.TrimSpace(info.Main.Version); v != "" && v != "(devel)" {
			return v
		}
	}
	return "0.1.0-dev"
}

// findPolicyExtension 定位仓库内的策略扩展。
//
// 打包后扩展随可执行文件分发；开发态从当前工作目录向上找。
func findPolicyExtension() (string, error) {
	candidates := []string{}
	if v := strings.TrimSpace(os.Getenv("MYAGENT_POLICY_EXT")); v != "" {
		candidates = append(candidates, v)
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "pi-extensions", "harness-policy.ts"),
			filepath.Join(dir, "..", "pi-extensions", "harness-policy.ts"))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, "pi-extensions", "harness-policy.ts"),
			filepath.Join(wd, "..", "pi-extensions", "harness-policy.ts"))
	}
	for _, c := range candidates {
		if abs, err := filepath.Abs(c); err == nil && fileExists(abs) {
			return abs, nil
		}
	}
	return "", contracts.NewAppError(contracts.CodeConfigMissing,
		"找不到策略扩展 pi-extensions/harness-policy.ts；没有它工具调用没有执行前授权", false)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
