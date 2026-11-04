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
}

export interface Settings {
  piPath: string;
  nodePath: string;
  provider: string;
  baseUrl: string;
  model: string;
  apiKey: string;
  /** 指向资源库里的模型定义；空表示沿用上面几个旧字段。 */
  modelRef: string;
  workspace: string;
  shellAllowed: boolean;
}

export type ApprovalChoice = "once" | "session" | "deny";

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
