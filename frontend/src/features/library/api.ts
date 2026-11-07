// 资源库的绑定调用（U04 / UI08、RES05）。
//
// 与其他 feature 的 api.ts 同一形态：FQN 集中定义在这里，返回带 `error` 字段的
// 结构体而不是 reject（跨进程错误要按 code 分支）。
//
// 这一层不做任何"看起来成功"的推断：导入、新建、启停、删除、诊断的结论都来自
// Go 侧返回值；拒绝原因逐条渲染（规则 ID + 诊断参数），文案在 catalog 里。

import { Call } from "@wailsio/runtime";
import type {
  CallError,
  MCPDefinition,
  MCPListResult,
  MCPStatusResult,
  ProfileListResult,
  SkillContentResult,
  SkillListResult,
  SkillResource,
} from "../../types";

const FQN = "my-agent-go/internal/app.Desktop";

/** 本地新建技能的返回。 */
export interface SkillCreateResult {
  item?: SkillResource;
  revision: number;
  pending: boolean;
  error?: CallError;
}

/** 一条链接检查的拒绝原因。Rule 是稳定 ID，Params 只放诊断细节。 */
export interface SkillLinkRejection {
  rule: string;
  params?: Record<string, string>;
}

/** 链接检查结果。有 rejections 时 stagedPath 一定为空。 */
export interface SkillLinkInspection {
  url: string;
  finalUrl: string;
  form: string;
  name: string;
  description: string;
  requires: string[] | null;
  disableModelInvocation: boolean;
  hash: string;
  bytes: number;
  files: number;
  stagedPath: string;
  redirects: string[] | null;
  warnings: string[] | null;
  rejections: SkillLinkRejection[] | null;
  error?: CallError;
}

/** 单条 MCP 操作的返回（启用/禁用/删除）。 */
export interface MCPCreateResult {
  item?: MCPDefinition;
  revision: number;
  pending: boolean;
  error?: CallError;
}

/** 试运行的权限事实。 */
export interface SkillDryRunPolicy {
  filesystem: string;
  network: boolean;
  shell: boolean;
  approvals: string;
  deniedCapabilities: string[] | null;
}

export interface SkillDryRunDenial {
  toolName: string;
  rule: string;
}

export interface SkillDryRunResult {
  itemId: string;
  /** completed / failed / timeout / blocked */
  state: string;
  output: string;
  policy: SkillDryRunPolicy;
  denials: SkillDryRunDenial[] | null;
  isolationArgs: string[] | null;
  tempCleaned: boolean;
  skillUnchanged: boolean;
  userStateUnchanged: boolean;
  durationMs: number;
  limitations: string[] | null;
  error?: CallError;
}

export const libraryApi = {
  skills: (): Promise<SkillListResult> => Call.ByName(`${FQN}.ListSkills`),

  createSkill: (
    name: string,
    scope: string,
    workspaceId: string
  ): Promise<SkillCreateResult> => Call.ByName(`${FQN}.CreateSkill`, name, scope, workspaceId),

  /** 检查链接：只解析清单并暂存，不执行任何脚本、不碰托管目录。 */
  inspectSkillLink: (url: string): Promise<SkillLinkInspection> =>
    Call.ByName(`${FQN}.InspectSkillLink`, url),

  importSkill: (path: string, scope: string, workspaceId: string): Promise<SkillCreateResult> =>
    Call.ByName(`${FQN}.ImportSkill`, path, scope, workspaceId),

  setSkillEnabled: (
    id: string,
    expectedRevision: number,
    enabled: boolean
  ): Promise<SkillCreateResult> => Call.ByName(`${FQN}.SetSkillEnabled`, id, expectedRevision, enabled),

  deleteSkill: (expectedRevision: number, id: string): Promise<SkillCreateResult> =>
    Call.ByName(`${FQN}.DeleteSkill`, expectedRevision, id),

  readSkill: (id: string): Promise<SkillContentResult> => Call.ByName(`${FQN}.ReadSkill`, id),

  saveSkill: (id: string, expectedRevision: number, content: string): Promise<SkillCreateResult> =>
    Call.ByName(`${FQN}.SaveSkill`, id, expectedRevision, content),

  /** 隔离试运行：用户显式触发，结束后后端负责清理临时目录。 */
  dryRunSkill: (id: string, prompt: string): Promise<SkillDryRunResult> =>
    Call.ByName(`${FQN}.DryRunSkill`, id, prompt),

  profiles: (): Promise<ProfileListResult> => Call.ByName(`${FQN}.ListProfiles`),

  mcpServers: (): Promise<MCPListResult> => Call.ByName(`${FQN}.ListMCP`),

  setMCPEnabled: (
    id: string,
    expectedRevision: number,
    enabled: boolean
  ): Promise<MCPCreateResult> => Call.ByName(`${FQN}.SetMCPEnabled`, id, expectedRevision, enabled),

  deleteMCP: (expectedRevision: number, id: string): Promise<MCPCreateResult> =>
    Call.ByName(`${FQN}.DeleteMCP`, expectedRevision, id),

  /** 只有协议握手与工具列表都成功，状态才会是 connected。 */
  diagnoseMCP: (id: string): Promise<MCPStatusResult> => Call.ByName(`${FQN}.DiagnoseMCP`, id),
};

export type LibraryMCPDefinition = MCPDefinition;
