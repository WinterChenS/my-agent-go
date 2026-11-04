// 前端到 Go 的唯一通道（Wails v3 绑定调用）。
//
// 绑定名格式是 <Go package path>.<Type>.<Method>：Wails 用 package path 做 FQN，
// 所以名字里带斜杠。集中在这里定义，避免散落各处的字符串拼错。
import { Call, Events } from "@wailsio/runtime";
import type {
  CurrentModelResult,
  ExtensionListResult,
  ExtensionResult,
  SkillContentResult,
  SkillListResult,
  SkillResult,
  ModelAction,
  MCPCredentialResult,
  MCPDefinition,
  MCPListResult,
  MCPResult,
  MCPStatusListResult,
  MCPStatusResult,
  ModelDefinition,
  ModelListResult,
  ModelProbeResponse,
  ModelResult,
  ProductEvent,
  Profile,
  ProfileConflictResult,
  ProfileListResult,
  ProfileResult,
  RunSnapshot,
  Settings,
  Status,
  ToolListResult,
  WorkspaceListResult,
  WorkspaceResult,
} from "./types";

const FQN = "my-agent-go/internal/app.Desktop";

export const api = {
  status: (): Promise<Status> => Call.ByName(`${FQN}.Status`),

  settings: (): Promise<Settings> => Call.ByName(`${FQN}.Settings`),

  saveSettings: (s: Settings): Promise<void> =>
    Call.ByName(`${FQN}.SaveSettings`, s),

  start: (message: string): Promise<RunSnapshot> =>
    Call.ByName(`${FQN}.Start`, message),

  abort: (runId: string): Promise<void> => Call.ByName(`${FQN}.Abort`, runId),

  resolveApproval: (
    runId: string,
    approvalId: string,
    choice: string
  ): Promise<void> =>
    Call.ByName(`${FQN}.ResolveApproval`, runId, approvalId, choice),

  lastError: (): Promise<string> => Call.ByName(`${FQN}.LastError`),

  // ---- C01：工作区与 Profile ----
  //
  // 这些调用返回带 error 字段的结构体而不是 reject：跨进程错误必须能被
  // 按 code 分支处理（冲突要展示差异、未信任要给修复路径），
  // 依赖 Promise reject 的字符串 message 判断类型是不可靠的。

  workspaces: (): Promise<WorkspaceListResult> =>
    Call.ByName(`${FQN}.ListWorkspaces`),

  createWorkspace: (path: string): Promise<WorkspaceResult> =>
    Call.ByName(`${FQN}.CreateWorkspace`, path),

  currentWorkspace: (): Promise<WorkspaceResult> =>
    Call.ByName(`${FQN}.CurrentWorkspace`),

  setWorkspaceTrust: (
    id: string,
    trusted: boolean
  ): Promise<WorkspaceResult> =>
    Call.ByName(`${FQN}.SetWorkspaceTrust`, id, trusted),

  profiles: (): Promise<ProfileListResult> => Call.ByName(`${FQN}.ListProfiles`),

  profile: (id: string): Promise<ProfileResult> =>
    Call.ByName(`${FQN}.GetProfile`, id),

  saveProfile: (
    id: string,
    workspaceId: string,
    expectedRevision: number,
    p: Profile
  ): Promise<ProfileResult> =>
    Call.ByName(`${FQN}.SaveProfile`, id, workspaceId, expectedRevision, p),

  profileConflict: (
    id: string,
    draft: Profile
  ): Promise<ProfileConflictResult> =>
    Call.ByName(`${FQN}.ProfileConflict`, id, draft),

  // ---- C02：模型定义与连接测试 ----

  models: (): Promise<ModelListResult> => Call.ByName(`${FQN}.ListModels`),

  /** secret 单独传：它不进 ModelDefinition（那份结构会被导出与展示）。 */
  saveModel: (
    expectedRevision: number,
    def: ModelDefinition,
    secret: string
  ): Promise<ModelResult> =>
    Call.ByName(`${FQN}.SaveModel`, expectedRevision, def, secret),

  deleteModel: (expectedRevision: number, id: string): Promise<ModelResult> =>
    Call.ByName(`${FQN}.DeleteModel`, expectedRevision, id),

  /** 会真实调用模型（可能计费），必须由用户点击触发。 */
  testModel: (id: string): Promise<ModelProbeResponse> =>
    Call.ByName(`${FQN}.TestModel`, id),

  setActiveModel: (ref: string): Promise<ModelAction> =>
    Call.ByName(`${FQN}.SetActiveModel`, ref),

  currentModel: (): Promise<CurrentModelResult> =>
    Call.ByName(`${FQN}.CurrentModel`),

  // ---- C03：技能与扩展 ----

  skills: (): Promise<SkillListResult> => Call.ByName(`${FQN}.ListSkills`),

  /** 只接受本地目录（复制进产品托管目录，不改源目录）。 */
  importSkill: (
    path: string,
    scope: string,
    workspaceId: string
  ): Promise<SkillResult> => Call.ByName(`${FQN}.ImportSkill`, path, scope, workspaceId),

  overwriteSkill: (
    id: string,
    path: string,
    expectedRevision: number
  ): Promise<SkillResult> =>
    Call.ByName(`${FQN}.OverwriteSkill`, id, path, expectedRevision),

  readSkill: (id: string): Promise<SkillContentResult> =>
    Call.ByName(`${FQN}.ReadSkill`, id),

  saveSkill: (
    id: string,
    expectedRevision: number,
    content: string
  ): Promise<SkillResult> =>
    Call.ByName(`${FQN}.SaveSkill`, id, expectedRevision, content),

  setSkillEnabled: (
    id: string,
    expectedRevision: number,
    enabled: boolean
  ): Promise<SkillResult> =>
    Call.ByName(`${FQN}.SetSkillEnabled`, id, expectedRevision, enabled),

  deleteSkill: (expectedRevision: number, id: string): Promise<SkillResult> =>
    Call.ByName(`${FQN}.DeleteSkill`, expectedRevision, id),

  extensions: (): Promise<ExtensionListResult> =>
    Call.ByName(`${FQN}.ListExtensions`),

  setExtensionEnabled: (
    id: string,
    expectedRevision: number,
    enabled: boolean
  ): Promise<ExtensionResult> =>
    Call.ByName(`${FQN}.SetExtensionEnabled`, id, expectedRevision, enabled),

  // ---- C04：MCP 与统一工具目录 ----

  mcpServers: (): Promise<MCPListResult> => Call.ByName(`${FQN}.ListMCP`),

  /** 保存配置不启动任何脚本/连接；校验失败会带可读错误码。 */
  saveMCP: (
    expectedRevision: number,
    def: MCPDefinition
  ): Promise<MCPResult> => Call.ByName(`${FQN}.SaveMCP`, expectedRevision, def),

  deleteMCP: (expectedRevision: number, id: string): Promise<MCPResult> =>
    Call.ByName(`${FQN}.DeleteMCP`, expectedRevision, id),

  setMCPEnabled: (
    id: string,
    expectedRevision: number,
    enabled: boolean
  ): Promise<MCPResult> =>
    Call.ByName(`${FQN}.SetMCPEnabled`, id, expectedRevision, enabled),

  /** 只写 OS 凭证库，配置里只留 ref。 */
  saveMCPCredential: (ref: string, secret: string): Promise<MCPCredentialResult> =>
    Call.ByName(`${FQN}.SaveMCPCredential`, ref, secret),

  /**
   * 诊断会启动一个短命的 `pi mcp list --json`（连接由 Pi 拥有）。
   * 需要 OAuth 的服务在这里返回 auth_required，不会是 connected。
   */
  diagnoseMCP: (id: string): Promise<MCPStatusResult> =>
    Call.ByName(`${FQN}.DiagnoseMCP`, id),

  diagnoseAllMCP: (): Promise<MCPStatusListResult> =>
    Call.ByName(`${FQN}.DiagnoseAllMCP`),

  /** 统一工具目录；MCP 工具来自 Pi 的诊断输出。 */
  tools: (): Promise<ToolListResult> => Call.ByName(`${FQN}.ListTools`),
};

/** 订阅 run 事件。返回取消订阅函数。 */
export function onRunEvent(fn: (ev: ProductEvent) => void): () => void {
  return Events.On("myagent:run-event", (event: { data: ProductEvent }) => {
    fn(event.data);
  });
}
