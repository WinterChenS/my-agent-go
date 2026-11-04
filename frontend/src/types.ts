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

export interface RunSnapshot {
  runId: string;
  sessionId: string;
  workspaceId: string;
  state: RunState;
  lastSeq: number;
  configRevision: number;
  piVersion: string;
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
  workspace: string;
  isolation: string;
  policyActive: boolean;
  platform: string;
}

export interface Settings {
  piPath: string;
  nodePath: string;
  provider: string;
  baseUrl: string;
  model: string;
  apiKey: string;
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
