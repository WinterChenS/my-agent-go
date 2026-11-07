// 全局搜索的绑定调用（U01 / SRCH01）。
//
// availability 是独立的字段，不是 error：索引未建立既不是后端崩了，也不是
// "搜过了没有命中"。前端必须分开渲染，否则用户会以为"我的会话没了"。
//
// route 由后端生成（稳定 id），前端只负责解析它——不在前端拼路由，
// 否则同一类结果会出现两种路径形状。

import { Call } from "@wailsio/runtime";
import type { CallError } from "../../types";

const FQN = "my-agent-go/internal/app.Desktop";

export type SearchAvailability = "available" | "unavailable";

export interface SearchItem {
  kind: "session" | "agent" | "skill" | "artifact";
  refId: string;
  workspaceId?: string;
  route: string;
  title: string;
  subtitle?: string;
  updatedAt: string;
  rank: number;
}

export interface SearchResult {
  query: string;
  availability: SearchAvailability;
  indexBuiltAt?: string;
  items: SearchItem[];
  error?: CallError;
}

export interface SearchRebuildResult {
  availability: SearchAvailability;
  builtAt?: string;
  summary: {
    builtAt?: string;
    sessions: number;
    agents: number;
    skills: number;
    artifacts: number;
    unavailable?: string[];
  };
  error?: CallError;
}

export const searchApi = {
  search: (query: string, kinds: string[] = [], limit = 20): Promise<SearchResult> =>
    Call.ByName(`${FQN}.Search`, query, kinds, limit),

  rebuildIndex: (): Promise<SearchRebuildResult> => Call.ByName(`${FQN}.RebuildSearchIndex`),

  indexStatus: (): Promise<SearchRebuildResult> => Call.ByName(`${FQN}.SearchIndexStatus`),
};
