// 工作台的绑定调用（U01）。
//
// 与其余绑定模块同一形态：FQN 集中定义，返回带 error 字段的结构体而不是 reject。
// 这里不缓存、不合并、不推算：工作台上的每个数字都必须来自这一次调用。

import { Call } from "@wailsio/runtime";
import type { CallError } from "../../types";

const FQN = "my-agent-go/internal/app.Desktop";

/** 一段的可用性；unavailable 时 count 一定缺失（不是 0）。 */
export interface WorkbenchSection {
  availability: "available" | "unavailable";
  reasonCode?: string;
  count?: number;
}

export interface WorkbenchRun {
  runId: string;
  sessionId: string;
  workspaceId?: string;
  state: string;
  sessionTitle?: string;
  updatedAt: string;
}

export interface WorkbenchApproval {
  runId: string;
  sessionId: string;
  approvalId: string;
  toolName?: string;
  requestedAt: string;
}

export interface WorkbenchSession {
  sessionId: string;
  workspaceId: string;
  title: string;
  pinned: boolean;
  updatedAt: string;
}

export interface WorkbenchArtifact {
  artifactId: string;
  workspaceId: string;
  displayName: string;
  size: number;
  updatedAt: string;
}

export interface WorkbenchSchedule {
  scheduleId: string;
  name: string;
  enabled: boolean;
  nextScheduledAt?: string;
  consecutiveFailures: number;
}

export interface WorkbenchHealthInfo {
  activeRuns: number;
  completed24h: number;
  failed24h: number;
  lastRunId?: string;
  lastState?: string;
  lastErrorCode?: string;
  lastUpdatedAt?: string;
}

export interface WorkbenchSummaryResult {
  generatedAt: string;
  currentRuns: WorkbenchSection;
  pendingApprovals: WorkbenchSection;
  recentSessions: WorkbenchSection;
  recentArtifacts: WorkbenchSection;
  schedules: WorkbenchSection;
  health: WorkbenchSection;
  runs: WorkbenchRun[];
  approvals: WorkbenchApproval[];
  sessions: WorkbenchSession[];
  artifacts: WorkbenchArtifact[];
  schedule: WorkbenchSchedule[];
  healthInfo: WorkbenchHealthInfo;
  error?: CallError;
}

export const workbenchApi = {
  summary: (): Promise<WorkbenchSummaryResult> => Call.ByName(`${FQN}.WorkbenchSummary`),
};
