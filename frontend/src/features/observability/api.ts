// 观测页的绑定调用（U06 / UI11）。
//
// 只消费 C07 的三个绑定：`UsageSummary`、`RunDetails`、`ExportDiagnostics`。
// 三个纪律：
//   - "不知道"原样传出去：`availability` 与指针字段直接来自 Go，
//     前端把 nil 显示成 `unavailable`，绝不折算成 0（RUN06 / ART03）；
//   - 时间范围用 RFC3339 字符串，空串表示不限：不在这里补一份"本月"假设；
//   - 导出只写本机诊断目录，不上传；界面只展示后端返回的路径/hash/脱敏计数。
//
// 缺的绑定：**没有"列 run"的接口**（Plan 里写的 `RunChain` 在 Go 侧实际是
// `RunDetails(runId)`）。所以这一页只能按 run id 打开一条 run 的事实，
// 无法列出"最近的错误"。缺口以 unavailable 呈现，并在 U06 报告里给出
// 需要的 DTO（见 U06-report.md）。

import { Call } from "@wailsio/runtime";
import type { CallError } from "../../types";

const FQN = "my-agent-go/internal/app.Desktop";

/** `reported | unavailable`（与 contracts.Usage* 一致）。 */
export type Availability = "reported" | "unavailable" | string;

export interface UsageRange {
  from?: string;
  to?: string;
  workspaceId?: string;
  sessionId?: string;
  runIds?: string[] | null;
}

export interface TokenUsage {
  availability: Availability;
  inputTokens?: number;
  outputTokens?: number;
  totalTokens?: number;
  reportedRuns: number;
  unreportedRuns: number;
}

export interface CostUsage {
  availability: Availability;
  costUsd?: number;
  priceSource?: string;
  estimated?: boolean;
  knownRuns: number;
  unknownRuns: number;
}

export interface UsageSummary {
  availability: Availability;
  range: UsageRange;
  runs: number;
  reportedRuns: number;
  unreportedRuns: number;
  tokens: TokenUsage;
  cost: CostUsage;
  note?: string;
}

export interface UsageSummaryResult {
  summary: UsageSummary;
  error?: CallError;
}

/** 单 run 的用量事实（指针缺失 = 不知道，不是 0）。 */
export interface UsageFacts {
  availability: Availability;
  inputTokens?: number;
  outputTokens?: number;
  totalTokens?: number;
  costUsd?: number;
  priceSource?: string;
  estimated?: boolean;
}

export interface BudgetFacts {
  maxToolCalls: number;
  maxDurationMs: number;
  toolCalls: number;
  exceeded: boolean;
  exceededReason?: string;
}

export interface ErrorFacts {
  code: string;
  message: string;
  retryable: boolean;
}

export interface RunRecord {
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
  usage: UsageFacts;
  budget?: BudgetFacts;
  error?: ErrorFacts;
}

export interface ArtifactRef {
  artifactId: string;
  displayName: string;
  mediaType: string;
  size: number;
  sha256: string;
  status: string;
}

export interface ToolCallFact {
  seq: number;
  eventId: string;
  type: string;
  occurredAt: string;
  toolCallId?: string;
  toolName?: string;
  isError?: boolean;
}

export interface ApprovalFact {
  seq: number;
  eventId: string;
  type: string;
  occurredAt: string;
  approvalId?: string;
  toolName?: string;
  choice?: string;
}

export interface EventFact {
  seq: number;
  eventId: string;
  type: string;
  occurredAt: string;
  kind?: string;
  message?: string;
}

export interface UnavailableSection {
  section: string;
  reason: string;
}

/** 一次 run 的可观测事实集合（run 链路来自 tools/approvals/errors/diagnostics）。 */
export interface RunDetails {
  runId: string;
  record?: RunRecord;
  usage: UsageSummary;
  artifacts: ArtifactRef[] | null;
  tools: ToolCallFact[] | null;
  approvals: ApprovalFact[] | null;
  compactions: EventFact[] | null;
  diagnostics: EventFact[] | null;
  errors: EventFact[] | null;
  eventCount: number;
  lastEventId?: string;
  unavailable: UnavailableSection[] | null;
}

export interface RunDetailsResult {
  details: RunDetails;
  error?: CallError;
}

export interface UsageSummaryQuery {
  from: string;
  to: string;
  workspaceId: string;
  sessionId: string;
  runIds: string[];
}

export interface DiagnosticsExportRequest {
  runIds: string[];
  sessionId: string;
  workspaceId: string;
  from: string;
  to: string;
  target: string;
}

export interface DiagnosticsExportResult {
  path: string;
  bytes: number;
  sha256: string;
  redactions: number;
  runs: number;
  events: number;
  artifacts: number;
  schemaVersion: number;
  generatedAt: string;
  error?: CallError;
}

export const observabilityApi = {
  usageSummary: (q: UsageSummaryQuery): Promise<UsageSummaryResult> =>
    Call.ByName(`${FQN}.UsageSummary`, q),
  runDetails: (runId: string): Promise<RunDetailsResult> => Call.ByName(`${FQN}.RunDetails`, runId),
  exportDiagnostics: (req: DiagnosticsExportRequest): Promise<DiagnosticsExportResult> =>
    Call.ByName(`${FQN}.ExportDiagnostics`, req),
};
