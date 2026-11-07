// Agent/Profile 编辑器的绑定调用（U03 / UI07）。
//
// 与其余 feature 的 api.ts 同一形态：FQN 集中定义，返回带 `error` 字段的结构体
// 而不是 reject——CONFIG_CONFLICT 要能提示"已被别处修改"并重读，
// INVALID_INPUT 要能把"越过权限上限"的结构化原因显示出来。
//
// 这里不保存任何可展示状态：文档、上限、来源、资源清单都来自后端，
// 页面只持有"正在编辑的草稿"。

import { Call } from "@wailsio/runtime";
import type {
  CallError,
  MCPDefinition,
  MemoryScopeState,
  ModelDefinition,
  Profile,
  ProfileConflictResult,
  ProfileListResult,
  ProfileResult,
  SkillResource,
  ToolDescriptor,
} from "../../types";

const FQN = "my-agent-go/internal/app.Desktop";

// ---------------------------------------------------------------- 后端形状

/** 一个值的来源：system / default / user / workspace / unavailable。 */
export interface AgentSource {
  field: string;
  value: string;
  source: string;
  available: boolean;
}

export interface AgentMissingRef {
  kind: string;
  id: string;
}

export interface AgentCeiling {
  filesystem: string;
  network: boolean;
  shell: boolean;
  sources: Record<string, string>;
  /** false = 没有任何配置层显式设置过权限，上限完全来自应用策略。 */
  available: boolean;
}

export interface AgentEditorState {
  profile: Profile;
  ceiling: AgentCeiling;
  sources: AgentSource[];
  missing?: AgentMissingRef[];
  warnings?: string[];
  unavailableFields: string[];
  error?: CallError;
}

export interface AgentCeilingResult {
  ceiling: AgentCeiling;
  unavailableFields: string[];
  error?: CallError;
}

/**
 * 编辑器需要的资源目录。
 *
 * `unavailable` 记录哪一项读不出来：读不出来时界面显示 unavailable，
 * 而不是"0 个模型"——那会让人以为资源库是空的。
 */
export interface AgentResources {
  models: ModelDefinition[];
  skills: SkillResource[];
  mcp: MCPDefinition[];
  tools: ToolDescriptor[];
  memoryScopes: MemoryScopeState[];
  unavailable?: Partial<Record<AgentResourceKind, CallError>>;
}

export type AgentResourceKind = "models" | "skills" | "mcp" | "tools" | "memory";

// ---------------------------------------------------------------- 调用

export const agentsApi = {
  /** 列表（左栏）。 */
  profiles: (): Promise<ProfileListResult> => Call.ByName(`${FQN}.ListProfiles`),

  /** 编辑现场：文档 + 上限 + 来源 + 缺口。 */
  state: (id: string, workspaceId: string): Promise<AgentEditorState> =>
    Call.ByName(`${FQN}.AgentEditorState`, id, workspaceId),

  /** 新建时的上限（还没有文档，但同样要能选中"允许的一档"）。 */
  ceiling: (workspaceId: string): Promise<AgentCeilingResult> =>
    Call.ByName(`${FQN}.AgentProfileCeiling`, workspaceId),

  /** CAS 保存：expectedRevision 必须是"我读到的那一版"。 */
  save: (
    id: string,
    workspaceId: string,
    expectedRevision: number,
    draft: Profile
  ): Promise<ProfileResult> =>
    Call.ByName(`${FQN}.SaveAgentProfile`, id, workspaceId, expectedRevision, draft),

  /** 复制：只带源 ID、新 ID 与工作区，不带任何秘密或 revision。 */
  duplicate: (sourceId: string, newId: string, workspaceId: string): Promise<ProfileResult> =>
    Call.ByName(`${FQN}.DuplicateAgentProfile`, sourceId, newId, workspaceId),

  rename: (
    id: string,
    workspaceId: string,
    expectedRevision: number,
    name: string
  ): Promise<ProfileResult> =>
    Call.ByName(`${FQN}.RenameAgentProfile`, id, workspaceId, expectedRevision, name),

  remove: (id: string, expectedRevision: number): Promise<ProfileResult> =>
    Call.ByName(`${FQN}.DeleteAgentProfile`, id, expectedRevision),

  /** 冲突现场：磁盘当前版本 + 与草稿的逐字段差异。 */
  conflict: (id: string, draft: Profile): Promise<ProfileConflictResult> =>
    Call.ByName(`${FQN}.ProfileConflict`, id, draft),

  /**
   * 资源目录：模型/Skill/MCP/工具/记忆 scope。
   *
   * 逐项读取而不是"一个聚合接口"：其中一项读不出来（例如记忆库没初始化）
   * 不该让整个编辑器变成空白，界面按项显示 unavailable 即可。
   */
  resources: async (): Promise<AgentResources> => {
    const out: AgentResources = { models: [], skills: [], mcp: [], tools: [], memoryScopes: [] };
    const unavailable: Partial<Record<AgentResourceKind, CallError>> = {};

    try {
      const models = await Call.ByName(`${FQN}.ListModels`);
      out.models = models?.items ?? [];
      if (models?.error) unavailable.models = models.error;
    } catch (e) {
      unavailable.models = e as CallError;
    }
    try {
      const skills = await Call.ByName(`${FQN}.ListSkills`);
      out.skills = skills?.items ?? [];
      if (skills?.error) unavailable.skills = skills.error;
    } catch (e) {
      unavailable.skills = e as CallError;
    }
    try {
      const mcp = await Call.ByName(`${FQN}.ListMCP`);
      out.mcp = mcp?.items ?? [];
      if (mcp?.error) unavailable.mcp = mcp.error;
    } catch (e) {
      unavailable.mcp = e as CallError;
    }
    try {
      const tools = await Call.ByName(`${FQN}.ListTools`);
      out.tools = tools?.items ?? [];
      if (tools?.error) unavailable.tools = tools.error;
    } catch (e) {
      unavailable.tools = e as CallError;
    }
    try {
      const memories = await Call.ByName(`${FQN}.ListMemories`);
      out.memoryScopes = memories?.scopes ?? [];
      if (memories?.error) unavailable.memory = memories.error;
    } catch (e) {
      unavailable.memory = e as CallError;
    }

    if (Object.keys(unavailable).length > 0) out.unavailable = unavailable;
    return out;
  },
};
