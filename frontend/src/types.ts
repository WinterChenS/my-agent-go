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
