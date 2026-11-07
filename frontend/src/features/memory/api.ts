// 记忆页的绑定调用（U05 / UI09）。
//
// 与其他 feature 的 api.ts 同一形态：FQN 集中定义，返回带 `error` 的结构体。
// 这里多一个字段：availability —— 记忆库本身是否可用。
// "没有记忆"和"记忆读不出来"是两件事，前者是空态，后者是故障。

import { Call } from "@wailsio/runtime";
import type {
  CallError,
  MemoryItem as BaseMemoryItem,
  MemoryScope,
  MemoryScopeState,
} from "../../types";

const FQN = "my-agent-go/internal/app.Desktop";

export type MemoryItem = BaseMemoryItem;
export type { MemoryScope, MemoryScopeState };

/** 记忆子系统可用性。available=false 时 list 里一定没有编造的条目。 */
export interface MemoryAvailability {
  available: boolean;
  reasonCode?: string;
  detail?: string;
}

export interface MemoryListResult {
  items: MemoryItem[] | null;
  scopes: MemoryScopeState[] | null;
  availability?: MemoryAvailability;
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
  json: string;
  itemCount: number;
  error?: CallError;
}

export interface MemoryScopeResult {
  state?: MemoryScopeState;
  error?: CallError;
}

export const memoryApi = {
  list: (): Promise<MemoryListResult> => Call.ByName(`${FQN}.ListMemories`),

  save: (content: string, kind: string, scope: MemoryScope): Promise<MemoryResult> =>
    Call.ByName(`${FQN}.SaveMemory`, content, kind, scope),

  confirm: (id: string): Promise<MemoryResult> => Call.ByName(`${FQN}.ConfirmMemory`, id),

  /** 删除只影响后续注入：已经发出的历史消息无法撤回。 */
  remove: (id: string): Promise<MemoryResult> => Call.ByName(`${FQN}.DeleteMemory`, id),

  search: (query: string): Promise<MemorySearchResult> => Call.ByName(`${FQN}.SearchMemories`, query),

  exportJson: (): Promise<MemoryExportResult> => Call.ByName(`${FQN}.ExportMemories`),

  /** 按 scope 关闭/打开注入；不删除任何条目。 */
  setScopeEnabled: (scope: MemoryScope, enabled: boolean): Promise<MemoryScopeResult> =>
    Call.ByName(`${FQN}.SetMemoryScopeEnabled`, scope, enabled),
};
