// 前端到 Go 的唯一通道（Wails v3 绑定调用）。
//
// 绑定名格式是 <Go package path>.<Type>.<Method>：Wails 用 package path 做 FQN，
// 所以名字里带斜杠。集中在这里定义，避免散落各处的字符串拼错。
import { Call, Events } from "@wailsio/runtime";
import type {
  CurrentModelResult,
  ModelAction,
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
};

/** 订阅 run 事件。返回取消订阅函数。 */
export function onRunEvent(fn: (ev: ProductEvent) => void): () => void {
  return Events.On("myagent:run-event", (event: { data: ProductEvent }) => {
    fn(event.data);
  });
}
