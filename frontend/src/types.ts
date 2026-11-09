// 与 Go 侧 DTO 一一对应的前端类型。
//
// 这里刻意手写而不是依赖生成的绑定文件：DTO 只有一个来源（internal/contracts），
// 生成器版本一变就会产生噪音 diff；字段名改动会在这里被 typecheck 抓到。

export type RunState =
  | "queued"
  | "starting"
  | "running"
  | "waiting_approval"
  | "stopping"
  | "completed"
  | "failed"
  | "cancelled"
  | "interrupted";

/**
 * Pi 技能自动发现的开关状态。
 *
 * 2026-11-04 起产品不再用 -ns 抑制 Pi 的本地技能发现，Pi 会自己加载
 * $HOME/.agents/skills 等位置里它发现的技能。那份清单产品拿不到，
 * 只有开关状态可信；缺失表示这次 run/这份状态没记录（界面显示“未知”）。
 */
export type SkillAutoDiscovery = "enabled" | "disabled";

export interface RunSnapshot {
  runId: string;
  sessionId: string;
  workspaceId: string;
  state: RunState;
  lastSeq: number;
  configRevision: number;
  piVersion: string;
  /** 这次 run 实际使用的 provider 键与模型名（MOD04）。 */
  provider?: string;
  model?: string;
  /**
   * 这一轮通过 --skill 显式加载的托管技能 ID（RES02：旧 run 保持快照）。
   *
   * 空数组 = 确实没有显式加载托管技能，**不是**“这一轮没有任何技能”：
   * Pi 自动发现的技能不在这个列表里（见 skillAutoDiscovery）。
   */
  managedSkills?: string[];
  /**
   * 这一轮 Pi 侧技能自动发现的开关。字段缺失表示没记录，界面必须显示“未知”。
   * 自动发现到的清单产品无法可信列出——不能把它当成空列表。
   */
  skillAutoDiscovery?: SkillAutoDiscovery;
  error?: { code: string; message: string };
}

export interface ProductEvent {
  schemaVersion: number;
  eventId: string;
  workspaceId: string;
  sessionId: string;
  runId: string;
  seq: number;
  type: string;
  occurredAt: string;
  payload: unknown;
}

export interface Status {
  appRoot: string;
  agentDir: string;
  piPath: string;
  piVersion: string;
  provider: string;
  model: string;
  modelRef: string;
  workspace: string;
  isolation: string;
  policyActive: boolean;
  platform: string;
  /** 最近一次构建实际交给 Pi 的模型；与上面的设置值可能不同。 */
  runtimeProvider: string;
  runtimeModel: string;
  /** 模型已改但还有 run 在跑，等空闲才生效。 */
  runtimePending: boolean;
  /**
   * 最近一次构建**托管且启用**、通过 --skill 显式交给 Pi 的技能 ID（RES02）。
   * 空 = 没有托管技能被显式加载；不是“没有技能可用”——Pi 自动发现仍可能加载了技能。
   */
  runtimeSkills: string[];
  /** 当前参数下 Pi 侧技能自动发现的开关（enabled / disabled；缺失=未知）。 */
  skillAutoDiscovery?: SkillAutoDiscovery;
  /** 托管技能的配置与运行中的不一致（内容摘要或集合变了）。 */
  skillsPending: boolean;
  /**
   * 最近一次构建实际写进隔离 agentDir/mcp.json 的外部 MCP 服务名。
   * 空 = 没有启用任何外部 MCP（自有 harness MCP 不在此列）。
   */
  runtimeMCP?: string[];
  /** MCP 配置与运行中的集合不一致：下一次安全点（ensureRuntime 重建）才生效。 */
  mcpPending?: boolean;
}

export interface Settings {
  piPath: string;
  nodePath: string;
  provider: string;
  /** 模型的 OpenAI 兼容端点（M1ENV 复用这个字段，不另建 ModelBaseURL）。 */
  baseUrl: string;
  model: string;
  apiKey: string;
  /**
   * 密钥的**引用**（形如 `env:ANVIL_MODEL_API_KEY`），不是密钥本身。
   * 环境变量给的密钥只以引用形式存在：明文不进 settings.json，也不进这里。
   */
  modelAPIKeyRef?: string;
  /** 指向资源库里的模型定义；空表示沿用上面几个旧字段。 */
  modelRef: string;
  workspace: string;
  shellAllowed: boolean;
}

/** 一个配置字段的生效来源（与 Go 侧 ConfigSource 一一对应）。 */
export type ConfigSource = "settings" | "env" | "default" | "none";

export interface ConfigFieldSource {
  value: string;
  source: ConfigSource;
  /** 仅 source=env 时出现：命中的完整变量名（ANVIL_* 或旧 MYAGENT_*）。 */
  envVar?: string;
}

/**
 * 当前生效的模型配置与每项来源（M1ENV）。
 *
 * 界面必须把它显示出来：环境变量只在设置里"没写这个字段"时才生效，
 * 但"生效了"与"没生效"在屏幕上必须看得出区别——否则用户改了设置而模型没变，
 * 和"脚本静默顶掉了我的配置"完全无法区分。
 */
export interface ModelConfigProvenance {
  provider: ConfigFieldSource;
  model: ConfigFieldSource;
  baseUrl: ConfigFieldSource;
  apiKeyRef: ConfigFieldSource;
  /** 被环境变量补上的字段名（provider/model/baseUrl/apiKeyRef）。 */
  overriddenByEnv?: string[] | null;
}

/** EffectiveConfig 的绑定结果里界面关心的那一段。 */
export interface EffectiveConfigResult {
  model?: ModelConfigProvenance | null;
  error?: CallError | null;
}

// 取值必须与 Go 侧 policy.ApprovalChoice 一致（once / session_scope / deny）。
// 曾经写过 "session"，后端回 UNSUPPORTED: 未知审批选项: session（2026-11-08 实测）。
export type ApprovalChoice = "once" | "session_scope" | "deny";

// ---- 消息模型（UI 侧）----

export interface ToolCard {
  /**
   * 这张卡属于哪一次 run。
   *
   * 必须有它：toolCallId 只在**单个 run 内**唯一，不同 run 复用同一个 id 是常态
   * （受控 fixture provider 每次都发 call_fixture_1，真实模型在重试时也可能复用）。
   * 只按 toolCallId 回填，会把上一轮已经"失败"的卡片改成这一轮的"完成"。
   */
  runId: string;
  toolCallId: string;
  toolName: string;
  args: string;
  result?: string;
  isError?: boolean;
  running: boolean;
}

export interface ChatMessage {
  id: string;
  role: "user" | "assistant";
  text: string;
  tools: ToolCard[];
  streaming: boolean;
}

export interface ApprovalItem {
  approvalId: string;
  toolName: string;
  toolCallId: string;
  args: string;
  expiresAtUnix: number;
}

// ---- C01：工作区与 Profile ----

/** 绑定层统一错误。前端按 code 分支，不解析 message 文案。 */
export interface CallError {
  code: string;
  message: string;
  retryable: boolean;
  /**
   * 本地化参数（U00）：前端把它填进 catalog 消息，例如
   * `{"field":"locale","value":"fr-FR"}`。
   * 没有参数时字段缺失（不是空对象）。
   */
  params?: Record<string, string>;
  /** 失败的操作标识（如 `preferences.save`），用于排查与埋点。 */
  operation?: string;
  /** 脱敏后的原因摘要（单行）；不含堆栈，不含秘密。 */
  cause?: string;
}

// ---- U00：展示偏好（语言 / 主题）----

/** 与 Go 的 app.Preferences 对应：只有这两项，不进 Settings。 */
export interface Preferences {
  locale: string;
  theme: string;
}

/** preferences 的 CAS 结果：revision 是下次保存必须带回来的凭据。 */
export interface PreferencesResult {
  preferences: Preferences;
  revision: number;
  error?: CallError;
}

export interface Workspace {
  id: string;
  name: string;
  canonicalPath: string;
  trusted: boolean;
  createdAt: string;
}

/** 工具策略的文件系统等级，取值必须与 Go 侧 config.NormalizeFilesystem 一致。 */
export type FilesystemScope = "none" | "read" | "workspace" | "unrestricted";

export interface ToolPolicy {
  filesystem: FilesystemScope;
  network: boolean;
  shell: boolean;
}

export interface MemoryPolicy {
  enabled: boolean;
  maxEntries: number;
}

export interface Profile {
  id: string;
  name: string;
  modelRef: string;
  skillIDs: string[] | null;
  mcpIDs: string[] | null;
  toolPolicy: ToolPolicy;
  memoryPolicy: MemoryPolicy;
  revision: number;
}

export interface WorkspaceResult {
  item: Workspace;
  error?: CallError;
}

export interface WorkspaceListResult {
  items: Workspace[] | null;
  error?: CallError;
}

export interface ProfileResult {
  item: Profile;
  error?: CallError;
}

export interface ProfileListResult {
  items: Profile[] | null;
  error?: CallError;
}

/** 一处字段差异：我的草稿 vs 磁盘上的当前版本。 */
export interface ProfileFieldDiff {
  field: string;
  mine: string;
  theirs: string;
}

export interface ProfileConflictResult {
  current: Profile;
  diff: ProfileFieldDiff[] | null;
  error?: CallError;
}

// ---- C02：模型定义与连接测试 ----

/**
 * 模型定义。
 *
 * 没有 apiKey 字段——密钥只存在于 OS 凭证库，这里只有 credentialRef。
 * 这不是"暂时没加"：模型定义会被导出、被贴进 issue，不能有放密钥的位置。
 */
export interface ModelDefinition {
  id: string;
  providerId: string;
  /** 真正发给 API 的模型名。 */
  modelId: string;
  api: string;
  baseUrl: string;
  inputKinds: string[];
  contextWindow: number;
  maxOutput: number;
  credentialRef: string;
  thinkingLevels: string[] | null;
  /** 只有显式 false 才是禁用；省略视为启用。 */
  enabled?: boolean | null;
}

/** 能力三态：unknown 表示"这次没测"，不是"不支持"。 */
export type Capability = "unknown" | "supported" | "unsupported";

export interface ProbeResult {
  reachable: boolean;
  toolCalling: Capability;
  vision: Capability;
  latencyMs: number;
  providerId: string;
  modelId: string;
  /** provider 侧的分类错误（401/429/超时…）；可达时为空。 */
  error?: CallError;
}

/** 下拉选项由后端给出：API 白名单来自已认证 Pi 的实际分发能力。 */
export interface ModelOptions {
  apis: string[];
  inputKinds: string[];
  thinkingLevels: string[];
}

export interface ModelListResult {
  items: ModelDefinition[] | null;
  revision: number;
  options: ModelOptions;
  error?: CallError;
}

export interface ModelResult {
  item: ModelDefinition;
  revision: number;
  error?: CallError;
}

export interface ModelProbeResponse {
  result: ProbeResult;
  /** "根本没测成"（模型不存在 / Pi 未配置），与 result.error 分开。 */
  error?: CallError;
}

export interface CurrentModelResult {
  source: "resources" | "settings" | "none";
  ref: string;
  provider: string;
  model: string;
  item: ModelDefinition;
  revision: number;
  error?: CallError;
}

export interface ModelAction {
  ref: string;
  /** 有 run 在跑：新模型要等空闲才生效。 */
  deferred: boolean;
  error?: CallError;
}

// ---- C03：技能与扩展 ----

export type ResourceScope = "user" | "workspace";

export interface SkillResource {
  id: string;
  name: string;
  description: string;
  /** 托管目录里 SKILL.md 的绝对路径。 */
  path: string;
  scope: ResourceScope;
  workspaceId?: string;
  enabled: boolean;
  origin: string;
  revision: number;
  hash: string;
  dependencies: string[] | null;
  /** 声明了但在本机 PATH 上找不到的依赖。 */
  missingDependencies: string[] | null;
  /** false 表示不会被交给 Pi，原因在 unavailableReason。 */
  available: boolean;
  unavailableReason?: string;
  disableModelInvocation: boolean;
  bytes: number;
}

export interface SkillListResult {
  items: SkillResource[] | null;
  revision: number;
  /** 当前有 run 在跑（改动要等它结束）。 */
  activeRun: boolean;
  /** 配置里的技能集合与运行中的不一致。 */
  pending: boolean;
  /**
   * 运行中的 worker **显式加载的托管技能**（--skill）。
   * 不含 Pi 自动发现的技能：那份清单没有可信来源。
   */
  runtimeSkills: string[] | null;
  error?: CallError;
}

export interface SkillResult {
  item: SkillResource;
  revision: number;
  pending: boolean;
  error?: CallError;
}

export interface SkillContentResult {
  item: SkillResource;
  content: string;
  error?: CallError;
}

export interface Extension {
  id: string;
  name: string;
  path: string;
  /** 系统资源：不可关闭。 */
  system: boolean;
  /** 扩展本身就是在 Pi 进程里执行的代码。 */
  executable: boolean;
  enabled: boolean;
  origin: "system" | "local" | string;
  canDisable: boolean;
  /** 信任边界说明，直接展示给用户。 */
  note: string;
  revision: number;
  hash: string;
}

export interface ExtensionListResult {
  items: Extension[] | null;
  revision: number;
  error?: CallError;
}

export interface ExtensionResult {
  item: Extension;
  revision: number;
  error?: CallError;
}

// ---- C04：MCP 与统一工具目录 ----

export type MCPTransport = "stdio" | "http";
/**
 * 工具暴露策略。刻意不提供 Pi 的默认 codemode：
 * V1 只支持 direct / deferred / hidden 三种可验证的语义。
 */
export type MCPExposure = "direct" | "deferred" | "hidden";

export interface MCPDefinition {
  id: string;
  transport: MCPTransport;
  /** stdio：只能是一个可执行文件；参数放 args。 */
  command?: string;
  args?: string[] | null;
  /** 子进程环境变量名 → 凭证库 ref（不是秘密本体）。 */
  envRefs?: Record<string, string> | null;
  workingDir?: string;
  /** http：Streamable HTTP 端点。 */
  url?: string;
  /** HTTP 头名 → 凭证库 ref；凭证内容是完整头值（如 `Bearer x`）。 */
  headerRefs?: Record<string, string> | null;
  enabled: boolean;
  exposure: MCPExposure;
}

export interface MCPListResult {
  items: MCPDefinition[] | null;
  revision: number;
  /** 运行中的 worker 实际拿到的外部 MCP 服务名。 */
  runtimeServers: string[] | null;
  /** 配置与运行中的集合不一致（有 run 在跑时本轮继续用旧集合）。 */
  pending: boolean;
  activeRun: boolean;
  /** 是否具备启动 Pi 诊断的条件（Pi 可执行文件可用）。 */
  diagnoseReady: boolean;
  error?: CallError;
}

export interface MCPResult {
  item?: MCPDefinition;
  revision: number;
  pending: boolean;
  error?: CallError;
}

export type MCPState =
  | "connected"
  | "failed"
  | "timeout"
  | "auth_required"
  | "disabled"
  | "unavailable";

export interface MCPToolStatus {
  name: string;
  exposure: MCPExposure;
  /** hidden 时为 false：服务连上了但工具不可达。 */
  enabled: boolean;
  capabilityState: string;
}

export interface MCPStatus {
  id: string;
  configured: boolean;
  transport: MCPTransport;
  enabled: boolean;
  exposure: MCPExposure;
  state: MCPState;
  tools: MCPToolStatus[] | null;
  error?: CallError;
  detail?: string;
  source: string;
  /** V1 没有实现 MCP OAuth；true 只可能来自未来版本。 */
  oauthSupported: boolean;
  checkedAt: string;
}

export interface MCPStatusResult {
  status: MCPStatus;
  error?: CallError;
}

export interface MCPStatusListResult {
  items: MCPStatus[] | null;
  error?: CallError;
}

export interface MCPCredentialResult {
  ref: string;
  error?: CallError;
}

export type ToolSource = "builtin" | "extension" | "mcp" | "harness";
export type ToolPolicyKind = "approval" | "auto" | "denied";

export interface ToolDescriptor {
  name: string;
  source: ToolSource | string;
  description?: string;
  inputSchema?: Record<string, unknown> | null;
  /** 模型当前可达且未被禁用；hidden/disabled/failed 一律 false。 */
  enabled: boolean;
  policy: ToolPolicyKind | string;
  /** available | deferred | hidden | unknown | unavailable | disabled */
  capabilityState: string;
  /** source=mcp 时的服务名。 */
  server?: string;
}

export interface ToolListResult {
  items: ToolDescriptor[] | null;
  error?: CallError;
}

// ---- C05：长期记忆 ----

export type MemoryScope = "user" | "workspace";
export type MemoryStatus = "candidate" | "active" | "deleted";

/**
 * 一条记忆。
 *
 * expiresAt 为空表示不过期；已过期条目仍在列表里（便于删除/查看），
 * 但不会出现在检索结果与下一次 run 的注入中。
 */
export interface MemoryItem {
  id: string;
  scope: MemoryScope;
  workspaceId: string;
  kind: string;
  content: string;
  /** 该记忆是在哪个 run 记下的；空串表示来源未记录。 */
  sourceSessionId: string;
  status: MemoryStatus;
  createdAt: string;
  updatedAt: string;
  expiresAt?: string | null;
}

/** 某个 scope 的注入开关；关闭只影响检索/注入，不删除条目。 */
export interface MemoryScopeState {
  scope: MemoryScope;
  workspaceId: string;
  enabled: boolean;
}

export interface MemoryListResult {
  items: MemoryItem[] | null;
  scopes: MemoryScopeState[] | null;
  error?: CallError;
}

export interface MemoryResult {
  item?: MemoryItem;
  error?: CallError;
}

export interface MemorySearchResult {
  items: MemoryItem[] | null;
  error?: CallError;
}

export interface MemoryExportResult {
  /** 可读 JSON；只在本地显示/另存，不默认上传。 */
  json: string;
  itemCount: number;
  error?: CallError;
}

export interface MemoryScopeResult {
  state: MemoryScopeState;
  error?: CallError;
}
