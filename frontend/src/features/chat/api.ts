// 对话页的绑定调用（U02 / C06、C07）。
//
// 与其余 feature 的 api.ts 同一形态：FQN 集中定义在这里，返回带 `error` 字段的
// 结构体而不是 reject（跨进程错误必须能按 code 分支：CONFIG_CONFLICT 要提示
// "已被别处修改"并重读，而不是把 Go 的 message 显示给用户）。
//
// 这里刻意**没有**第二份状态：会话、run、审批、澄清、产物的真相都在后端
// （SQLite / Pi transcript / 文件系统）。本模块只做"转发 + 结构化结果"。
//
// 字段全部手写对齐 Go 侧 DTO（与 types.ts 同一策略：手写，字段改错由 typecheck 抓）。

import { Call } from "@wailsio/runtime";
import type {
  CallError,
  ProductEvent,
  RunSnapshot,
  RunState,
  SkillAutoDiscovery,
} from "../../types";

const FQN = "my-agent-go/internal/app.Desktop";

// ---------------------------------------------------------------- 附件（C06c）

/** 一次附件请求：只有路径与显示名。路径是"候选定位串"，不是权威。 */
export interface ChatAttachmentRequest {
  path: string;
  displayName: string;
}

/** 后端已经落地并核对过的附件（有 ID、大小、sha256；没有可写路径）。 */
export interface ChatAttachmentMeta {
  id: string;
  displayName: string;
  size: number;
  sha256: string;
  mediaType: string;
  status: string;
  expiresAt: string;
}

/** 单个文件被拒的结构化原因。前端必须逐条显示，不能折叠成"全部失败"。 */
export interface ChatAttachmentRejection {
  path: string;
  displayName: string;
  code: string;
  message: string;
}

export interface ChatAttachmentsResult {
  sessionId: string;
  items: ChatAttachmentMeta[];
  rejected: ChatAttachmentRejection[];
  error?: CallError;
}

/**
 * 启动一轮之后拿到的快照（与 contracts.RunSnapshot 对齐的字段子集）。
 *
 * configRevision/provider/model/managedSkills 都是启动那一刻的事实：
 * 用户中途换模型不会改写这一轮已经用了什么。
 */
export interface ChatRunSnapshot extends RunSnapshot {
  state: RunState;
  provider?: string;
  model?: string;
  managedSkills?: string[];
  skillAutoDiscovery?: SkillAutoDiscovery;
}

// ---------------------------------------------------------------- 澄清（C06f）

/**
 * 一次澄清答复。
 *
 * 与审批的区别是这条通道的**全部**语义：它只回到 Pi 的对应 UI request，
 * 不经过 ResolveApproval、不签发 session grant、不产生任何工具许可。
 */
export interface ChatClarificationResponse {
  /** select / input / editor：文本答案。 */
  value?: { text: string };
  /** confirm：是/否。 */
  confirmed?: boolean;
  /** 显式放弃回答（会以 cancelled 收口）。 */
  cancelled?: boolean;
}

// ---------------------------------------------------------------- 导出（C06e）

export type ChatExportFormat = "html" | "markdown" | "json";

export interface ChatExportResult {
  sessionId: string;
  format: string;
  /** 导出目录里的真实路径（后端写完后才有）。 */
  path: string;
  bytes: number;
  sha256: string;
  records: number;
  messages: number;
  redactions: number;
  writtenAt: string;
  error?: CallError;
}

// ---------------------------------------------------------------- 产物与运行详情（C07）

/** 一次磁盘核对：登记时的 sha256 与这次读到的 observedSha256 分开给。 */
export interface ChatVerification {
  status: string;
  observedSha256?: string;
  observedSize?: number;
  reason?: string;
  checkedAt: string;
}

export interface ChatArtifact {
  id: string;
  workspaceId: string;
  runId?: string;
  sessionId?: string;
  displayName: string;
  canonicalPath: string;
  sha256: string;
  size: number;
  mediaType: string;
  createdAt: string;
  updatedAt: string;
  /** 只有 GetArtifact 会带：列表不带，因为"没核对"不能显示成"已核对"。 */
  verification?: ChatVerification;
}

export interface ChatArtifactListResult {
  items: ChatArtifact[];
  error?: CallError;
}

export interface ChatArtifactResult {
  item: ChatArtifact;
  error?: CallError;
}

/** run 用量事实：指针字段缺失 = 不知道，不是 0。 */
export interface ChatUsageFacts {
  availability: string;
  inputTokens?: number;
  outputTokens?: number;
  totalTokens?: number;
  costUsd?: number;
  priceSource?: string;
  estimated?: boolean;
}

export interface ChatRunRecord {
  runId: string;
  sessionId?: string;
  workspaceId?: string;
  state?: string;
  piVersion?: string;
  provider?: string;
  model?: string;
  configRevision: number;
  lastSeq: number;
  startedAt?: string;
  updatedAt?: string;
  usage: ChatUsageFacts;
  budget?: {
    maxToolCalls: number;
    maxDurationMs: number;
    toolCalls: number;
    exceeded: boolean;
    exceededReason?: string;
  };
  error?: { code: string; message: string; retryable: boolean };
}

/** 一次工具执行（来自事件日志）。 */
export interface ChatToolCall {
  seq: number;
  eventId: string;
  type: string;
  occurredAt: string;
  toolCallId?: string;
  toolName?: string;
  isError?: boolean;
}

/** 一次审批请求/答复（来自事件日志）。 */
export interface ChatApprovalFact {
  seq: number;
  eventId: string;
  type: string;
  occurredAt: string;
  approvalId?: string;
  toolName?: string;
  choice?: string;
}

export interface ChatArtifactRef {
  artifactId: string;
  displayName: string;
  mediaType: string;
  size: number;
  sha256: string;
  status: string;
}

/** 一段"这次答不出来"的说明。界面显示 unavailable + 原因，不显示空数组。 */
export interface ChatUnavailableSection {
  section: string;
  reason: string;
}

export interface ChatRunDetails {
  runId: string;
  record?: ChatRunRecord;
  usage: {
    availability: string;
    runs: number;
    reportedRuns: number;
    unreportedRuns: number;
    tokens: {
      availability: string;
      inputTokens?: number;
      outputTokens?: number;
      totalTokens?: number;
      reportedRuns: number;
      unreportedRuns: number;
    };
    cost: {
      availability: string;
      costUsd?: number;
      priceSource?: string;
      estimated?: boolean;
      knownRuns: number;
      unknownRuns: number;
    };
    note?: string;
  };
  artifacts: ChatArtifactRef[];
  tools: ChatToolCall[];
  approvals: ChatApprovalFact[];
  compactions: ChatEventFact[];
  diagnostics: ChatEventFact[];
  errors: ChatEventFact[];
  eventCount: number;
  lastEventId?: string;
  unavailable: ChatUnavailableSection[];
}

export interface ChatEventFact {
  seq: number;
  eventId: string;
  type: string;
  occurredAt: string;
  kind?: string;
  message?: string;
}

export interface ChatRunDetailsResult {
  details: ChatRunDetails;
  error?: CallError;
}

// ---------------------------------------------------------------- 调用

export const chatApi = {
  /**
   * 会话级提交：消息 + 已解析的附件能力 ID。
   *
   * 用 StartSessionRun 而不是 Start：会话归属、附件、记忆注入都在后端这一条路径上，
   * 前端不自己拼一份 prompt，也不把附件路径塞进文本。
   */
  startSessionRun: (
    sessionId: string,
    message: string,
    attachmentIds: string[] = []
  ): Promise<ChatRunSnapshot> =>
    Call.ByName(`${FQN}.StartSessionRun`, sessionId, message, attachmentIds),

  /** 停止一轮运行（后端接受之后才由上层把界面切到"停止中"）。 */
  abort: (runId: string): Promise<void> => Call.ByName(`${FQN}.Abort`, runId),

  /** 审批结论。choice ∈ once|session|deny。 */
  resolveApproval: (runId: string, approvalId: string, choice: string): Promise<void> =>
    Call.ByName(`${FQN}.ResolveApproval`, runId, approvalId, choice),

  /** 澄清答复：独立的通道，不进入审批。 */
  answerClarification: (
    sessionId: string,
    requestId: string,
    response: ChatClarificationResponse
  ): Promise<void> =>
    Call.ByName(`${FQN}.AnswerClarification`, sessionId, requestId, response),

  /** 重放某个 run 在 afterSeq 之后的事件（重连/恢复）。 */
  replay: (runId: string, afterSeq: number): Promise<ProductEvent[]> =>
    Call.ByName(`${FQN}.Replay`, runId, afterSeq),

  /**
   * 把用户选中的附件交给后端解析。
   *
   * 返回的 Items 里没有路径——前端传的路径字符串不是权威，只有后端复核后的
   * ID/size/sha256 才算数；被拒的每一个文件都在 Rejected 里带着原因。
   */
  resolveAttachments: (
    sessionId: string,
    requests: ChatAttachmentRequest[]
  ): Promise<ChatAttachmentsResult> =>
    Call.ByName(`${FQN}.ResolveAttachments`, sessionId, requests),

  /** 导出一份会话（内容是后端从 transcript 生成的，前端只拿路径与指纹）。 */
  exportSession: (
    sessionId: string,
    format: ChatExportFormat,
    target: string
  ): Promise<ChatExportResult> =>
    Call.ByName(`${FQN}.ExportSession`, sessionId, format, target),

  /** 某一轮登记的产物（索引层结论，不做磁盘核对）。 */
  runArtifacts: (runId: string): Promise<ChatArtifactListResult> =>
    Call.ByName(`${FQN}.ListRunArtifacts`, runId),

  /** 单个产物：带这次读的核对结论（status / observedSha256）。 */
  artifact: (id: string): Promise<ChatArtifactResult> =>
    Call.ByName(`${FQN}.GetArtifact`, id),

  /** 一个 run 的可观测事实；答不出的段落在 unavailable 里。 */
  runDetails: (runId: string): Promise<ChatRunDetailsResult> =>
    Call.ByName(`${FQN}.RunDetails`, runId),
};
