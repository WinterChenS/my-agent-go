// 会话目录的绑定调用（U01 / C06b）。
//
// 与其余绑定模块同一形态：FQN 集中定义，返回带 error 字段的结构体而不是 reject，
// 因为跨进程错误必须能按 code 分支（CONFIG_CONFLICT 要提示"已被别处修改"并重读）。
//
// 这里没有任何本地状态：会话目录的真相在 SQLite 里。改完必须重新 list，
// 不允许拿 mutation 的返回值拼一份"看起来更新了"的列表。

import { Call } from "@wailsio/runtime";
import type { CallError } from "../../types";

const FQN = "my-agent-go/internal/app.Desktop";

/** 一页会话的条数。与 C06 的默认值一致，翻页时由后端继续收敛。 */
export const HISTORY_PAGE_SIZE = 20;

export interface HistorySession {
  id: string;
  workspaceId: string;
  title: string;
  pinned: boolean;
  archived: boolean;
  kind: string;
  createdAt: string;
  updatedAt: string;
  revision: number;
}

export interface HistoryGroup {
  workspaceId: string;
  sessions: HistorySession[];
}

export interface HistoryPageResult {
  groups: HistoryGroup[];
  nextCursor?: string;
  hasMore: boolean;
  error?: CallError;
}

export interface HistoryMutationResult {
  item?: HistorySession;
  error?: CallError;
}

export interface HistoryOpResult {
  error?: CallError;
}

export interface HistoryQuery {
  workspaceId?: string;
  includeArchived?: boolean;
  limit?: number;
  cursor?: string;
}

export const historyApi = {
  /** 在当前工作区里新建一个普通会话。workspaceId 为空表示用设置里的工作区。 */
  create: (workspaceId?: string): Promise<HistoryMutationResult> =>
    Call.ByName(`${FQN}.CreateSession`, workspaceId ?? ""),

  list: (query: HistoryQuery): Promise<HistoryPageResult> =>
    Call.ByName(`${FQN}.ListSessions`, {
      workspaceId: query.workspaceId ?? "",
      includeArchived: query.includeArchived ?? false,
      limit: query.limit ?? HISTORY_PAGE_SIZE,
      cursor: query.cursor ?? "",
    }),

  rename: (id: string, expectedRevision: number, title: string): Promise<HistoryMutationResult> =>
    Call.ByName(`${FQN}.RenameSession`, id, expectedRevision, title),

  setPinned: (id: string, expectedRevision: number, pinned: boolean): Promise<HistoryMutationResult> =>
    Call.ByName(`${FQN}.PinSession`, id, expectedRevision, pinned),

  setArchived: (id: string, expectedRevision: number, archived: boolean): Promise<HistoryMutationResult> =>
    Call.ByName(`${FQN}.ArchiveSession`, id, expectedRevision, archived),

  remove: (id: string, expectedRevision: number): Promise<HistoryOpResult> =>
    Call.ByName(`${FQN}.DeleteSession`, id, expectedRevision),
};
