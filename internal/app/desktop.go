// Package app 是桌面宿主与运行时之间的绑定层。
//
// 它只做三件事：把设置变成一次可执行的环境（Pi 安装件、隔离 agentDir、
// models.json、策略服务），把 UI 的调用转成 RunService 的方法调用，
// 把 RunService 的事件转成 Wails 事件。业务判断都不在这里。
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	anvil "my-agent-go"
	"my-agent-go/internal/config"
	"my-agent-go/internal/connectors"
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
	"my-agent-go/internal/scheduler"
	"my-agent-go/internal/session"
	"my-agent-go/internal/storage"
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
	//
	// M1ENV：这就是模型的 base URL 字段（internal/config 里没有 Settings，也没有
	// 等价字段），因此不新增 Settings.ModelBaseURL——同一件事有两个字段，迟早会分叉。
	BaseURL string `json:"baseUrl"`
	// Model 是模型 id。
	Model string `json:"model"`
	// APIKey 交给 Pi 的 models.json。
	//
	// 仅用于"还没有任何模型定义"的旧配置路径（fixture/本机端点）。
	// 一旦 ModelRef 指向资源库里的模型，密钥改从 OS 凭证库取，
	// 这里不再被读到（也不会被写进 models.json 明文）。
	//
	// 环境变量 ANVIL_API_KEY 能补这个字段，但补上的值**不落盘**（见 stripEnvDerived）。
	APIKey string `json:"apiKey"`
	// ModelAPIKeyRef 是密钥的**引用**（形如 env:ANVIL_MODEL_API_KEY），不是密钥本身。
	//
	// 为什么要有引用而不是直接存 key：环境变量给的密钥必须能被使用、又能被安全地
	// 描述出来——引用可以落盘、可以显示在设置页、可以进诊断包，明文不行。
	// 明文只在进程内存里，经子进程环境交给 Pi（models.json 里只有 $ENV 引用）。
	ModelAPIKeyRef string `json:"modelAPIKeyRef,omitempty"`
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
	// modelConfig 是当前生效模型配置的**来源**（设置 / 环境 / 默认）。
	// 与 settings 同一时刻更新：界面据此显示"这个值是谁给的"，
	// 不允许显示设置值而实际跑环境值。
	modelConfig ModelConfig
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
	// sessionDB/sessionCat 是 C06 的会话目录（应用级单例，固定在
	// <appRoot>/sessions.db）。会话索引只有这一个写者入口，别的包应当复用同一个
	// 句柄，而不是各开一份（run 索引的装配尚未接到这里，见 C06b 报告）。
	sessionDB  *storage.DB
	sessionCat *session.Catalog
	// attachMu 保护 C06c 的附件存储（能力登记表必须在应用生命周期内是单例）。
	// 与 svcMu 分开：初始化它会经由 sessionCatalog 拿 svcMu，同一把锁会自锁。
	attachMu    sync.Mutex
	attachStore *session.AttachmentStore
	// runtimeSessionID 是最近一次构建的 run 会话，记忆用它记录来源。
	runtimeSessionID string

	// workerStarter 允许测试注入假 worker；nil 时 RunService 用真实的 pi.StartWorker。
	// 生产路径不设它。
	workerStarter run.WorkerStarter

	// piRequestOverride 允许测试覆盖 Pi 发现环境（PATH/常见位置/版本探针），
	// 免得测试结论依赖宿主机真实 PATH。生产恒为 nil。
	piRequestOverride func(pi.DiscoveryRequest) pi.DiscoveryRequest

	// piContractMu 保护运行前契约验证的缓存（内存 + <appRoot>/pi-contract-cache.json）。
	//
	// 与 d.mu 分开：写缓存会做磁盘 I/O，不该连带把 Status/Start 的短锁拖长。
	// 锁序恒为 piContractMu → d.mu（piContractNow 读注入时钟时短暂摸 d.mu）。
	piContractMu     sync.Mutex
	piContractCache  map[string]piContractCacheEntry
	piContractLoaded bool

	// piContractProbe 是契约探测的注入点（生产恒为 nil = 真实 pi.ContractProbe）：
	// 探测要 spawn 六个隔离子进程，测试注入可计数的替身来证明“命中不探测”。
	piContractProbe func(context.Context, pi.PiInstallation) (pi.ContractReport, error)
	// piContractClock 允许测试推进时间以验证 TTL（生产恒为 nil = time.Now）。
	piContractClock func() time.Time

	// piInstallMu 保护 F06 安装服务单例；piInstallOverride 允许测试注入清单/
	// HTTP 客户端/探针（生产恒为 nil，生产路径只用嵌入清单与真实探针）。
	piInstallMu       sync.Mutex
	piInstallSvc      *piinstall.InstallService
	piInstallOverride func(*piinstall.InstallService)

	// credStoreProbe 是 OS 凭证库的唯一探测入口（生产 = credentials.Probe）。
	// 测试注入一个打不开的探测，用来证明"不可用"路径给出稳定错误码；生产恒为 nil。
	credStoreProbe func(context.Context, string) (credentials.Store, error)

	// settingsMu 串行化 settings.json 的读-比-写（CAS）。
	//
	// 写者不止一个：SaveSettings / SaveSettingsWithRevision / persistPiSelection。
	// 不在同一把锁里做"读 revision 再写"，两个写者就会都以为自己是最新的。
	settingsMu sync.Mutex

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
	runtimeSkills      []string
	runtimeSkillDigest string
	// piVersion 是侧栏状态点用的缓存（见 cachedPiVersion）：Status 高频轮询不能 spawn。
	piVersion           string
	piVersionKey        string
	runtimeExtensionDig string
	// connectorMu 保护 C08a 连接器服务单例与待审批请求。
	// 与 d.mu 分开：连接器健康检查会做网络往返，不该连带把 Status 卡住。
	connectorMu      sync.Mutex
	connectorSvc     *connectors.Service
	connectorDB      *storage.DB
	connectorPending map[string]connectors.PendingApproval

	// schedulerMu 保护 C09 调度器单例与它的库。
	// 与 d.mu 分开：首次打开任务页会构建运行时并迁移数据库。
	schedulerMu  sync.Mutex
	schedulerSvc *scheduler.Service
	schedulerDB  *storage.DB

	// persistentSubs 是"跨运行时重建仍然有效"的 run 事件订阅者（C09 调度器）。
	//
	// run.Service 会因模型/技能/MCP 变化被重建（空闲时），旧订阅随旧服务消失。
	// 订阅者登记在这里，每次构建新运行时重新挂上；否则调度器会一直监听已关闭的
	// 旧服务，occurrence 永远停在 running 或只能落到"未知"。
	persistentSubs []func(contracts.Event)

	// runtimeMCP/runtimeMCPDigest 是最近一次构建交给 Pi 的外部 MCP 集合。
	// digest 参与运行时签名：改服务/改参数/改暴露策略都会重建 runtime，
	// 有 run 在跑时则记 pending（旧 run 不静默变化）。
	runtimeMCP       []string
	runtimeMCPDigest string

	// unsavedMu / unsavedScopes 是 U00 的未保存改动登记表（F09 的阻止项来源）。
	// 与 d.mu 分开：编辑器每次按键都会上报，拿 d.mu 会和 Status/Start 互相拖慢。
	unsavedMu     sync.Mutex
	unsavedScopes map[string]struct{}
}

// New 构造 Desktop。root 是应用数据根目录。
func New(root string) (*Desktop, error) {
	d := &Desktop{root: root}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("创建应用数据目录失败: %w", err)
	}
	// 产品自带的 Pi 扩展（策略/澄清/附件）随二进制分发：打包后的可执行文件旁
	// 没有 pi-extensions/，所以启动时就把嵌入内容物化到 <appRoot>/extensions/product。
	// 不物化的话，发布包里每一次 run 都会停在"找不到策略扩展"——也就是没有
	// 执行前授权，工具一律不可用。
	setProductExtensionsDir(productExtensionsDirFor(root))
	if err := materializeProductExtensions(); err != nil {
		// 不在这里让应用整体起不来：解析时还会再试一次（并重写被改过的副本），
		// 真拿不到策略扩展时 run 入口会以 CONFIG_MISSING 明确拒绝。先把原因留痕，
		// 免得"为什么突然没有执行前授权"没有任何线索。
		d.setLastErr("产品扩展物化失败：" + err.Error())
	}
	s, cfg, err := d.loadSettings()
	if err != nil {
		return nil, err
	}
	d.settings = s
	d.modelConfig = cfg
	// 默认工作区先建出来，避免首次启动就撞"目录不存在"。
	// 建不出来不阻断启动：真正的原因（只读盘、无权限）会在 Start 时
	// 以更准确的错误码出现，比在启动阶段吞掉它更有用。
	if s.Workspace != "" {
		_ = os.MkdirAll(s.Workspace, 0o755)
	}
	// C09：定时任务的后台循环必须在启动时就跑起来，而不是等用户点开任务页。
	// 它不需要先建好运行时（真正需要执行时才通过委托件去取 RunService），
	// 所以这里可以安全地提前拉起；失败只记原因，不阻断启动——
	// 一个打不开的调度库不该让聊天也一起打不开。
	if _, schedErr := d.schedulerService(); schedErr != nil {
		d.setLastErr("定时任务不可用（不会按时触发）：" + schedErr.Error())
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

// readSettingsFile 读 settings.json 的原始字节；文件不存在返回 (nil, nil)。
//
// 返回字节而不是结构：CAS 凭据是从**文件内容**派生的（见 settingsRevisionOf），
// 所以读与写必须用同一份字节，不能一边读结构一边读文件。
func (d *Desktop) readSettingsFile() ([]byte, error) {
	raw, err := os.ReadFile(d.settingsPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 settings.json 失败: %w", err)
	}
	return raw, nil
}

// settingsRevisionOf 把磁盘上的设置字节折算成 CAS 凭据。
//
// 为什么不是"文件里存一个递增计数"：写 settings.json 的入口不止一个
// （SaveSettings / SaveSettingsWithRevision / 选 Pi / 用户手工编辑 / 品牌迁移）。
// 任何一个忘了加一，另一个拿着旧凭据的写者就会静默覆盖它。
// 内容派生的凭据不需要任何写者配合：文件一变，旧凭据自然失效。
//
// 0 保留给"没有这个文件"；非空文件永远不会得到 0（碰撞概率 2^-64）。
func settingsRevisionOf(raw []byte) uint64 {
	if len(raw) == 0 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write(raw)
	rev := h.Sum64()
	if rev == 0 {
		rev = 1
	}
	return rev
}

// writeSettings 原子写出设置（tmp → fsync → rename）。
//
// 不用 os.WriteFile：写到一半崩溃会留下半个 JSON，下次启动就要求用户重配。
// rename 在同一目录内是原子的，所以崩溃只会留下 .tmp 残片。
func (d *Desktop) writeSettings(s Settings) (uint64, error) {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return 0, err
	}
	path := d.settingsPath()
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, fmt.Errorf("写 settings.json 失败: %w", err)
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return 0, fmt.Errorf("写 settings.json 失败: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return 0, fmt.Errorf("写 settings.json 失败: %w", err)
	}
	if err := f.Close(); err != nil {
		return 0, fmt.Errorf("写 settings.json 失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return 0, fmt.Errorf("写 settings.json 失败: %w", err)
	}
	return settingsRevisionOf(raw), nil
}

func (d *Desktop) loadSettings() (Settings, ModelConfig, error) {
	raw, err := d.readSettingsFile()
	doc, docErr := parseSettingsDocument(raw)
	if err != nil {
		// 读不出来（权限/磁盘）也要给出一份可用的设置：
		// 环境变量与默认值仍然说得清楚，不必让整个应用起不来。
		s, cfg := d.resolveSettings(doc)
		return s, cfg, err
	}
	if docErr != nil {
		// 用户手改坏了配置文件：不静默丢弃，交由调用方看到错误。
		s, cfg := d.resolveSettings(doc)
		return s, cfg, fmt.Errorf("解析 settings.json 失败: %w", docErr)
	}
	s, cfg := d.resolveSettings(doc)
	return s, cfg, nil
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

// applyEnvOverrides 已被 resolveSettings 取代（见 settings_env.go）。
//
// 旧实现是无条件覆盖：环境变量永远赢过设置文件。后果是脚本一设 ANVIL_MODEL，
// 用户在设置里改的模型就永远不生效，且界面上看不出是谁在生效。
// 现行规则是"设置 > 环境 > 默认"，并且补位出来的值不落盘。

// Settings 返回当前设置（不含任何隐式改写）。
func (d *Desktop) Settings() Settings {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.settings
}

// SettingsResult 是带 CAS 凭据的设置读写结果。
//
// Revision 是 settings.json 内容的并发凭据（没有文件为 0）。它不是一个递增序号：
// 任何写入（包括手工编辑与其它入口）都会换掉它，CAS 只需要"相等/不等"。
type SettingsResult struct {
	Settings Settings   `json:"settings"`
	Revision uint64     `json:"revision"`
	Error    *CallError `json:"error,omitempty"`
}

// SettingsWithRevision 从磁盘读设置并带出 CAS 凭据。
//
// 与 GetPreferences 同一纪律：每次现读磁盘，不缓存——配置允许外部编辑，
// 缓存与磁盘不一致时没人说得清哪个是真相。
// 注意：读出来的值已应用环境变量覆盖（与 Settings()/Status 同口径）；
// 把整份快照写回去会把本次进程的环境值固化进用户配置文件，这是旧 SaveSettings 已有的行为，
// 本轮不改变它，但界面应只改用户真正编辑过的字段。
func (d *Desktop) SettingsWithRevision() SettingsResult {
	d.settingsMu.Lock()
	raw, err := d.readSettingsFile()
	d.settingsMu.Unlock()
	if err != nil {
		return SettingsResult{Settings: d.Settings(), Error: toCallError(err)}
	}
	doc, docErr := parseSettingsDocument(raw)
	if docErr != nil {
		return SettingsResult{Settings: d.Settings(),
			Revision: settingsRevisionOf(raw),
			Error: toCallError(contracts.NewAppError(contracts.CodeInternal,
				"设置文件内容不是合法 JSON", false).
				WithOperation("settings.get").WithCause(docErr.Error()))}
	}
	// 这是**读**路径：按磁盘 + 环境算一份给界面编辑的快照，
	// 不改内存里的生效设置（否则界面一刷新就把没保存的编辑吞掉）。
	s, _ := d.resolveSettings(doc)
	return SettingsResult{Settings: s, Revision: settingsRevisionOf(raw)}
}

// SaveSettingsWithRevision 以 CAS 保存设置：expectedRevision 必须等于当前文件的凭据。
//
// 拒绝条件（不建目录、不写文件、不改内存）：
//   - 凭据不一致（另一个窗口/另一个入口先写了）→ CONFIG_CONFLICT，
//     并把磁盘上的当前凭据回带，调用方可重读后重试；
//   - 工作目录为空 → INVALID_INPUT。
//
// 成功时与 SaveSettings 一样会丢弃旧 RunService（设置变了，下一次 Start 重建运行时）。
func (d *Desktop) SaveSettingsWithRevision(expectedRevision uint64, s Settings) SettingsResult {
	rev, err := d.saveSettings(s, &expectedRevision)
	if err != nil {
		return SettingsResult{Settings: d.Settings(), Revision: rev, Error: toCallError(err)}
	}
	d.resetRuntime()
	d.mu.Lock()
	cur := d.settings
	d.mu.Unlock()
	return SettingsResult{Settings: cur, Revision: rev}
}

// SaveSettings 持久化设置并丢弃旧的 RunService（下次 Start 用新配置重建）。
//
// 它会关闭运行时，因此会中断正在跑的 run。只改"下一次运行用哪个模型"时
// 不要走这里——用 persistSettings（SetActiveModel 就是这么做的）。
//
// 这是不检查 revision 的旧入口（保持既有语义不变）。它写出的内容同样会换掉 CAS 凭据，
// 否则它就成了绕过 CAS 的后门：拿着旧凭据的写者会把旧入口的修改悄悄覆盖掉。
func (d *Desktop) SaveSettings(s Settings) error {
	if _, err := d.saveSettings(s, nil); err != nil {
		return err
	}
	d.resetRuntime()
	return nil
}

// resetRuntime 丢弃旧 RunService（下一次 ensureRuntime 用新设置重建）。
//
// 关闭失败不向上报：运行时已经不再可用，关闭错误改变不了这个事实；
// 报出去反而会让"设置已保存"变成一个看起来失败的操作。
func (d *Desktop) resetRuntime() {
	d.mu.Lock()
	old := d.svc
	d.svc = nil
	d.mu.Unlock()
	if old != nil {
		_ = old.Close(context.Background())
	}
}

// persistSettings 只落盘并更新内存里的设置，不动正在跑的运行时。
//
// 模型切换走这条路：运行中换模型不能把当前这一轮连根拔掉，
// 新配置由 ensureRuntime 的签名比较在空闲时生效（MOD04 的安全点）。
func (d *Desktop) persistSettings(s Settings) error {
	_, err := d.saveSettings(s, nil)
	return err
}

// saveSettings 是设置落盘的**唯一**写点（含 CAS），返回写入后的凭据。
//
// expected 为 nil 表示不检查 revision（旧 SaveSettings / 内部只改一字段的写者）。
// 非 nil 且与磁盘不一致时返回 CONFIG_CONFLICT：不建目录、不写文件、不改内存，
// 让调用方先去重读（静默覆盖另一份编辑比报一个错难查得多）。
func (d *Desktop) saveSettings(s Settings, expected *uint64) (uint64, error) {
	// 先做无副作用的入参校验："空工作目录"不是冲突，不应该被 revision 报错盖掉。
	if strings.TrimSpace(s.Workspace) == "" {
		return 0, contracts.NewAppError(contracts.CodeInvalidInput, "工作目录不能为空", false).
			WithOperation("settings.save")
	}

	d.settingsMu.Lock()
	defer d.settingsMu.Unlock()

	raw, err := d.readSettingsFile()
	if err != nil {
		return 0, err
	}
	current := settingsRevisionOf(raw)
	if expected != nil && *expected != current {
		return current, contracts.NewAppError(contracts.CodeConfigConflict,
			fmt.Sprintf("设置已被别的入口改过（凭据不一致：期望 %d，实际 %d），拒绝覆盖",
				*expected, current), false).
			WithParams(map[string]string{
				"expected": strconv.FormatUint(*expected, 10),
				"actual":   strconv.FormatUint(current, 10),
			}).
			WithOperation("settings.save")
	}

	abs, err := filepath.Abs(s.Workspace)
	if err != nil {
		return current, fmt.Errorf("工作目录无法绝对化: %w", err)
	}
	s.Workspace = abs
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return current, fmt.Errorf("创建工作目录失败: %w", err)
	}

	// 环境变量补位出来的值不落盘（M1ENV）：否则一次保存就把预览/CI 的值
	// 钉进用户配置，环境变量消失后行为会莫名变化。
	doc, docErr := parseSettingsDocument(raw)
	if docErr != nil {
		// 磁盘上那份解析不了（用户手改坏了）：按"什么都没写"处理。
		// 宁可多保留一条环境值不落盘，也不要拿它当"用户写过"。
		doc = settingsDocument{present: map[string]bool{}}
	}
	persist, stripped := stripEnvDerived(s, doc)

	next, err := d.writeSettings(persist)
	if err != nil {
		return current, err
	}
	// 内存里的生效设置按"刚落盘的文档 + 环境变量"重新解析：
	// 环境值仍然生效（下一次 run 行为不变），但磁盘上不会出现它们。
	memDoc := settingsDocumentOf(persist)
	for key := range stripped {
		delete(memDoc.present, key)
	}
	mem, cfg := d.resolveSettings(memDoc)
	d.mu.Lock()
	d.settings = mem
	d.modelConfig = cfg
	d.mu.Unlock()
	return next, nil
}

// CredentialStoreStatusResult 是 OS 凭证库的可用性结论（不含任何秘密）。
//
// 设置页用它回答"我的密钥存在哪、能不能安全保存"。不可用时 Backend 仍是平台真实后端名，
// Detail 说明原因，Error 给稳定错误码 CREDENTIAL_STORE_UNAVAILABLE；不会返回任何凭证内容，
// 也不暗示回退到明文（本产品没有明文回退，只有显式的会话内存库）。
type CredentialStoreStatusResult struct {
	Available bool       `json:"available"`
	Backend   string     `json:"backend"`
	Detail    string     `json:"detail,omitempty"`
	Error     *CallError `json:"error,omitempty"`
}

// CredentialStoreStatus 探测 OS 凭证库是否可用。
//
// 只探测能力，不写探测凭证（往用户凭证库里写测试项本身就是污染）。
// 探测失败一律报 CREDENTIAL_STORE_UNAVAILABLE：对调用方而言问题只有一个
// ——"现在不能安全地保存凭证"，具体原因在 Detail 里。
func (d *Desktop) CredentialStoreStatus() CredentialStoreStatusResult {
	d.mu.Lock()
	probe := d.credStoreProbe
	d.mu.Unlock()
	if probe == nil {
		probe = credentials.Probe
	}
	store, err := probe(context.Background(), filepath.Join(d.root, "credentials"))
	st := credentials.StatusFromOpen(store, err)
	res := CredentialStoreStatusResult{Available: st.Available, Backend: st.Backend, Detail: st.Detail}
	if !st.Available {
		res.Error = toCallError(contracts.NewAppError(contracts.CodeCredentialStoreUnavailable,
			"OS 凭证保护存储不可用，凭证不会落盘（不回退明文）: "+st.Detail, false).
			WithOperation("credentials.status"))
	}
	return res
}

// effectivePermissions 是权限上限的唯一计算点。
//
// 运行时策略服务与连接器服务（C08a）共用它：两处各算一份迟早会出现
// "连接器按 A 判断、run 按 B 执行"的分叉。
func effectivePermissions(s Settings) config.Permissions {
	return config.Permissions{
		Filesystem: "workspace",
		Shell:      s.ShellAllowed,
		Network:    false,
	}
}

// isolationSpec 是"实际传给 Pi 的隔离参数"的唯一构造点。
//
// Status 展示与 ensureRuntime 启动都必须调用它。这两处曾经各写一份，
// 结果是界面显示 --no-mcp、实际却启用了 harness MCP——
// 诊断信息不准比没有诊断信息更糟，它会把人带去错误的方向。
func isolationSpec(policyExt string) pi.IsolationSpec {
	return pi.IsolationSpec{
		PolicyExtension: policyExt,
		HarnessMCP:      true,
		// C06f：产品自带、不可关闭的系统扩展（内容澄清 + 附件只读投递）。
		// 由产品以绝对路径 -e 加载，不从用户配置目录发现——它们是产品能力，
		// 不是用户可以关掉的插件。
		ExtraExtensions: productExtensionPaths(),
	}
}

// isolationSpecWith 在隔离参数上追加本次要加载的技能与托管扩展。
//
// 单独一个函数而不是改 isolationSpec 的签名：Status 只需要"隔离参数"，
// 给它多传两个参数会让展示路径与启动路径的构造点再次分叉。
func isolationSpecWith(policyExt string, skills, extraExts []string) pi.IsolationSpec {
	// 系统扩展永远在最前面：托管扩展再多也不能把产品自带的澄清/附件扩展挤掉。
	exts := append(productExtensionPaths(), extraExts...)
	return pi.IsolationSpec{
		PolicyExtension: policyExt,
		HarnessMCP:      true,
		ExtraExtensions: exts,
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
		PiVersion:          d.cachedPiVersion(),
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
		// 旧字段路径（fixture / 本机端点）：密钥可能是 env: 引用，也可能是旧明文。
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
//
// 密钥原文由 modelSecret 现取（env: 引用读环境变量，否则退回旧明文字段），
// 只经子进程环境交给 Pi；models.json 里写的是 $ENV 引用，磁盘上不多留一份明文。
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
	}, modelSecret(s))
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
	// 产品自带扩展也进签名：它们决定"澄清桥开不开"与"附件能不能投递"，
	// 路径变化（开发态换了仓库位置、发布包换了布局）必须重建运行时，
	// 否则"界面说能澄清、实际没加载扩展"会长期存在。
	key := setup.key() + "|sk:" + skills.Digest + "|ex:" + extPlan.Digest +
		"|mcp:" + mcpPlan.Digest + "|pext:" + strings.Join(productExtensionPaths(), ",")

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

	// FIX2 闸门：解析安装件之外还要证明这个二进制真的会说我们的协议。
	// 放在这里（而不是在每次 Start 的开头）是故意的：已有运行时且配置签名没变时
	// 会在上面提前返回，不会为了“再发一条消息”去哈希/探测一遍二进制。
	inst, err := d.resolveVerifiedInstallation(ctx, s)
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
	// 两处读同一份事实，不会出现"策略服务允许 shell、而某个 run 的声明快照里
	// 也写着 shell 却没人核对"的分叉。
	// 权限上限只在这里算一次：既交给策略服务，也交给 RunService 作为
	// per-run 快照的天花板。连接器服务（C08a）用同一个函数取同一份事实。
	permissions := effectivePermissions(s)
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
			// 工具注册表只在这里与工具目录共用同一份构造（harnessToolset），
			// 两处各写一份会出现“目录里有、实际没注册”的偏差。
			Tools:   d.harnessToolset(s.Workspace, mem, memoryWS),
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
	// C07：诊断记录器（run 快照与产品事件落库）。诊断是**派生**数据：
	// 库打不开不能拦住聊天，但也不能静默：原因写进 lastErr，界面可见。
	//
	// 它同时是 run.Options.History（usage/预算/失败结论）与事件订阅者
	// （工具/审批/压缩/诊断轨迹）。两者都只从**本服务**消费，不依赖 UI 的
	// emitter 是否装配——没有界面的自动化运行也要留下可定位的事实。
	recorder, recErr := d.diagnosticsRecorder()
	if recErr != nil {
		d.setLastErr("诊断记录不可用（usage/事件不会落库）：" + recErr.Error())
		recorder = nil
	}
	// 两个消费者共用 run.Options.History 这一个槽位：
	//   - history：sessions/runs 索引与 transcript 定位键（历史列表与"重开历史"靠它）；
	//   - diagnostics：run_usage/run_events（usage、预算、失败结论、诊断包）。
	// 早先只装了一个，结果索引没有写者——真实运行过的会话在列表里查不到。
	historySink, sinkErr := d.runHistorySinks(recorder)
	if sinkErr != nil {
		// 索引不可用要说出来，但不能拦住聊天：诊断仍有记录，索引这份缺失由
		// 读取路径（历史列表）表现为"没有记录"，而不是"记录了空内容"。
		d.setLastErr("运行索引不可用（会话历史不会落库）：" + sinkErr.Error())
	}

	// 这是应用进程里**唯一**的 RunService，也就是唯一的 admission queue：
	// 交互会话、C08 连接器与 C09 调度都往它里面提交，全局活跃上限固定为 2。
	// 因此这里不能按会话建服务，也不传 MaxActive。
	svc := run.New(run.Options{
		WorkspaceID:  "ws-default",
		SessionID:    sessionID,
		WorkspaceDir: s.Workspace,
		AgentDir:     agentDir,
		Installation: inst,
		// C07：run 快照落库（usage/预算/失败结论）。nil 表示诊断不可用，
		// run 的其它行为不受影响。
		History: historySink,
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
		// 权限天花板与默认快照：per-run 请求只能在这个天花板之下收紧。
		PolicyCeiling: run.PolicyCeiling{
			Permissions: permissions,
			Ref:         "ceiling:workspace",
		},
		PolicySnapshot: run.PolicySnapshot{Permissions: permissions},
		// 自有 MCP 的环境 + 外部 MCP 的凭证环境（只经子进程环境交付）。
		HarnessEnv: append(harness.Env(), mcpPlan.Env...),
		// C06f：附件只经 app 托管的 blob store 读取（run 只认识这一个入口），
		// 澄清按产品自带扩展的身份建立桥。
		Attachments:   desktopAttachmentReader{d: d},
		Clarification: d.clarificationOptions(),
		StartWorker:   workerStarter,
	})
	if emitter != nil {
		// 退订函数在本层还无人调用：桌面宿主目前没有“UI 断开”的显式信号，
		// 订阅者随进程存活。保留返回值语义（F05-B），不伪造断开事件。
		_ = svc.Subscribe(emitter)
	}
	if recorder != nil {
		// 事件日志是诊断包“故障可定位”的来源。与 emitter 分开订阅：
		// 界面没接上不等于不需要留痕。
		_ = svc.Subscribe(recorder.RecordEvent)
	}

	d.mu.Lock()
	d.svc = svc
	persistent := append([]func(contracts.Event){}, d.persistentSubs...)
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

	// 跨运行时的订阅者（C09 调度器）挂到新实例上：它们的语义是"这个进程的
	// run 事件"，不是"某一个 run.Service 实例的事件"。
	for _, fn := range persistent {
		_ = svc.Subscribe(fn)
	}
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
// cachedPiVersion 返回"侧栏那颗状态点"用的 Pi 版本。
//
// 为什么不是每次现算：Status 会被 UI 高频轮询，而算出真实版本要么解析托管指针、
// 要么 spawn 一次 `pi --version`——放在 Status 里就是"界面越刷越卡"。
// 所以第一次调用先返回空（UI 显示"未检测到"），后台算完下一次轮询就正确。
// 缓存键含设置路径与环境变量，改选 Pi 之后下一次自动重算。
func (d *Desktop) cachedPiVersion() string {
	d.mu.Lock()
	version, key := d.piVersion, d.piVersionKey
	s := d.settings
	d.mu.Unlock()

	want := strings.TrimSpace(s.PiPath)
	if want == "" {
		want, _ = envLookup("PI_PATH")
	}
	want += "|" + strings.TrimSpace(s.PiSavedPath)
	if key == want {
		return version
	}
	d.mu.Lock()
	if d.piVersionKey != want {
		d.piVersionKey = want
		d.piVersion = ""
	}
	d.mu.Unlock()
	go d.refreshPiVersion(want)
	return ""
}

func (d *Desktop) refreshPiVersion(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := pi.DiscoverFast(ctx, d.piDiscoveryRequest())
	if err != nil {
		return
	}
	version := ""
	for _, cand := range res.Candidates {
		if cand.Installation.Version != "" {
			version = cand.Installation.Version
			break
		}
	}
	d.mu.Lock()
	if d.piVersionKey == key {
		d.piVersion = version
	}
	d.mu.Unlock()
}

func (d *Desktop) resolveInstallation(s Settings) (pi.PiInstallation, error) {
	if path := strings.TrimSpace(s.PiPath); path != "" {
		return resolveUserInstallation(s, path)
	}
	// 环境变量覆盖（ANVIL_PI_PATH / MYAGENT_PI_PATH），排在设置之后、托管副本之前。
	//
	// 为什么需要：预览包、CI、以及"我想先试一个不在 PATH 上的 Pi"这类场景，都不该
	// 去改用户设置文件——settings.json 是用户资产，脚本改写它会在真实使用中丢配置。
	// 优先级仍然确定：设置里显式选了路径就以设置为准，环境变量只是"没选时的默认"。
	if path, _ := envLookup("PI_PATH"); path != "" {
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
	sessionDB := d.sessionDB
	d.sessionDB = nil
	d.sessionCat = nil
	d.svcMu.Unlock()
	// 附件能力登记表随应用退出一起失效：blob 还在磁盘上，但引用不再可读。
	d.attachMu.Lock()
	d.attachStore = nil
	d.attachMu.Unlock()
	if mem != nil {
		_ = mem.Close()
	}
	// 会话目录库同样与应用同寿命。
	if sessionDB != nil {
		_ = sessionDB.Close()
	}
	// 调度器（任务定义 + occurrence）同样与应用同寿命：先停后台循环再关库。
	d.schedulerMu.Lock()
	schedulerSvc := d.schedulerSvc
	schedulerDB := d.schedulerDB
	d.schedulerSvc = nil
	d.schedulerDB = nil
	d.schedulerMu.Unlock()
	if schedulerSvc != nil {
		_ = schedulerSvc.Close(context.Background())
	}
	if schedulerDB != nil {
		_ = schedulerDB.Close()
	}
	// 连接器库（定义 + 追加式审计）同样与应用同寿命。
	d.connectorMu.Lock()
	connectorDB := d.connectorDB
	d.connectorDB = nil
	d.connectorSvc = nil
	d.connectorPending = nil
	d.connectorMu.Unlock()
	if connectorDB != nil {
		_ = connectorDB.Close()
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

// productExtensionsSubdir 是产品自带扩展在 <appRoot>/extensions 下的命名空间。
//
// 为什么不直接放 <appRoot>/extensions/<name>：那个目录是**用户可以安装/启用/删除**
// 的第三方扩展目录（resources.Layout.ExtensionsDir，扩展页扫的就是它）。把自带扩展
// 放进去会有两个后果：其一，策略扩展会以"可关闭的第三方扩展"出现在扩展页，
// 而它事实上不可关闭；其二，澄清/附件扩展会被 CompileExtensions 当成托管扩展
// 再 -e 一次，同一份扩展被加载两遍。子目录不带 index.ts，resources 的扫描按
// 结构跳过它——产品目录与用户目录各归各的。
const productExtensionsSubdir = "product"

var (
	productExtMu sync.Mutex
	// productExtDir 是已物化的产品扩展目录（<appRoot>/extensions/product）。
	// 由 New 设置：解析入口是包级函数（Status/技能试运行/run 装配共用），
	// 每个 Desktop 只是同一个应用数据根的一个视图。
	productExtDir string
)

// productExtensionAsset 是嵌入内容的读取入口。
//
// 生产恒为 anvil.ProductExtension；测试用钩子模拟"这个构建没带某条扩展"，
// 否则只能改嵌入本身，"嵌入缺失"这条分支就无从验证。
var productExtensionAsset = anvil.ProductExtension

// productExtensionsDirFor 返回某个应用数据根下的产品扩展目录。
func productExtensionsDirFor(root string) string {
	return filepath.Join(root, "extensions", productExtensionsSubdir)
}

func setProductExtensionsDir(dir string) {
	productExtMu.Lock()
	productExtDir = dir
	productExtMu.Unlock()
}

func productExtensionsDir() string {
	productExtMu.Lock()
	dir := productExtDir
	productExtMu.Unlock()
	return dir
}

// findPolicyExtension 定位产品自带的策略扩展。
//
// 找不到就是 CONFIG_MISSING：没有它工具调用没有执行前授权，宁可拒绝启动。
func findPolicyExtension() (string, error) {
	p, ok := findProductExtension("harness-policy.ts", "POLICY_EXT")
	if !ok {
		return "", contracts.NewAppError(contracts.CodeConfigMissing,
			"找不到策略扩展 pi-extensions/harness-policy.ts；没有它工具调用没有执行前授权", false)
	}
	return p, nil
}

// findProductExtension 按"env 覆盖 → 物化副本 → 开发态布局"的顺序定位一个产品扩展。
//
// envName 是**不带前缀**的变量名（经 envLookup 解析，ANVIL_* 优先）。
// 只做文件定位，不读内容、不执行：扩展是有权限的代码，加载决定权在隔离参数里。
//
// 物化副本那一步会先校正盘上内容（缺失或被改动就重写）：策略扩展被换成
// "永远 allow"的版本比找不到它更危险——run 会照常跑起来，授权却已经没了。
func findProductExtension(name, envName string) (string, bool) {
	if envName != "" {
		if v, _ := envLookup(envName); v != "" {
			if abs, err := filepath.Abs(v); err == nil && fileExists(abs) {
				return abs, true
			}
		}
	}
	// 物化副本：打包后的唯一可靠来源。解析时也重写被改过的文件，
	// 而不是只在启动时校验一次。
	if p, err := materializeProductExtension(name); err == nil && p != "" {
		return p, true
	}
	// 开发态便利：仓库布局（可执行文件旁 → 上级 → 当前目录 → 上级）。
	// 发布包里走不到这里（没有 pi-extensions/），但它让 `go run` 与
	// 直接跑测试二进制时不必先物化。
	candidates := []string{}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "pi-extensions", name),
			filepath.Join(dir, "..", "pi-extensions", name))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, "pi-extensions", name),
			filepath.Join(wd, "..", "pi-extensions", name))
	}
	for _, c := range candidates {
		if abs, err := filepath.Abs(c); err == nil && fileExists(abs) {
			return abs, true
		}
	}
	return "", false
}

// materializeProductExtension 保证 <appRoot>/extensions/product/<name> 与嵌入内容一致。
//
// 返回空路径表示"这条扩展不在嵌入里"（这个构建没带它）；err 只在真写不下去时非 nil。
// 内容相同就完全不写：每次启动都重写会让 mtime 抖动，测试与安装包校验都会被噪音干扰。
func materializeProductExtension(name string) (string, error) {
	body, ok := productExtensionAsset(name)
	if !ok || len(body) == 0 {
		return "", nil
	}
	dir := productExtensionsDir()
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("产品扩展目录未初始化（Desktop 尚未构造）")
	}
	target := filepath.Join(dir, name)
	if current, err := os.ReadFile(target); err == nil && bytes.Equal(current, body) {
		return target, nil
	}
	if err := writeProductExtension(target, body); err != nil {
		return "", err
	}
	return target, nil
}

// materializeProductExtensions 把嵌入的产品扩展全部写到位（启动时调用一次）。
func materializeProductExtensions() error {
	var errs []error
	for _, name := range anvil.ProductExtensionNames() {
		if _, err := materializeProductExtension(name); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// writeProductExtension 原子写出一个产品扩展（tmp → fsync → rename）。
//
// 不用 os.WriteFile：Pi 可能在写到一半时读到半个扩展文件，那种失败会以
// "扩展语法错误"出现，离真正的原因很远。rename 在同一目录内是原子的。
func writeProductExtension(target string, body []byte) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建产品扩展目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-ext-*")
	if err != nil {
		return fmt.Errorf("写产品扩展临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	// rename 成功后 Remove 是 no-op；失败时清掉残片，不在应用数据目录里留垃圾。
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("写产品扩展 %s 失败: %w", target, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("同步产品扩展 %s 失败: %w", target, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭产品扩展临时文件失败: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("设置产品扩展权限失败: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("替换产品扩展 %s 失败: %w", target, err)
	}
	return nil
}

// productExtensionPaths 返回产品自带、不可关闭的系统扩展（只含实际存在的）。
//
// 找不到就不加：宁可在诊断里少一条"已加载"，也不要给 Pi 一个不存在的 -e 路径
// 让它在启动阶段直接失败。缺失的事实由 Status/LastError 说明（见 ProductExtensions）。
func productExtensionPaths() []string {
	out := make([]string, 0, 2)
	for _, name := range []string{"anvil-clarification.ts", "anvil-attachment.ts"} {
		if p, ok := findProductExtension(name, productExtensionEnvKey(name)); ok {
			out = append(out, p)
		}
	}
	return out
}

// productExtensionEnvKey 是产品扩展路径的测试/发布覆盖入口（与 POLICY_EXT 同口径）。
//
// 返回不带前缀的变量名，统一由 envLookup 解析。
func productExtensionEnvKey(name string) string {
	switch name {
	case "anvil-clarification.ts":
		return "CLARIFICATION_EXT"
	case "anvil-attachment.ts":
		return "ATTACHMENT_EXT"
	}
	return ""
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
