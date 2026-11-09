// 设置页的绑定调用（U06 / UI10）。
//
// 与其他 feature 的 api.ts 同一形态：FQN 集中定义，返回值里带 `error` 的
// 结构体按 code 分支处理，不解析 Go 的 message 文案。
//
// 这里刻意**没有**应用更新的绑定：F09 尚未实现，界面因此只能渲染
// `unavailable` 与原因（见 UpdateSettings.tsx），不能显示进度、更不能提供
// "退出并更新"。等 F09 的 `UpdateStatus/CheckForUpdate/DownloadUpdate/
// UpdateBlockers/ExitAndUpdate` 落地后，在这里补齐调用即可。
//
// Pi 的安装进度是**事件**（只承载阶段），权威状态一律来自 InstallPi 的返回值：
// 界面不得从"下载到第几阶段"推断安装成功。

import { Call, Events } from "@wailsio/runtime";
import type {
  CallError,
  CurrentModelResult,
  EffectiveConfigResult,
  ModelAction,
  ModelListResult,
  ModelProbeResponse,
  Preferences,
  PreferencesResult,
} from "../../types";

const FQN = "my-agent-go/internal/app.Desktop";

// ---------------------------------------------------------------- Pi（F08 / F06）

/** Pi 安装件的发现来源（与 internal/runtime/pi 的 PiInstallation 对应）。 */
export interface PiInstallation {
  id: string;
  executable: string;
  args?: string[] | null;
  version?: string;
  /** external | managed：这是谁装的（与候选来源不是同一件事）。 */
  origin: string;
  architecture: string;
  source?: string;
  runnable: boolean;
  note?: string;
}

/** `preferred | saved | managed | path | common`。 */
export type PiCandidateOrigin = "preferred" | "saved" | "managed" | "path" | "common";

/** `standalone | npm`：npm 表示启动器只是 Node shim。 */
export type PiDistribution = "standalone" | "npm";

/** F08 的兼容状态（界面只展示，不推断）。 */
export type PiCompatibilityState =
  | "discovering"
  | "ready_verified"
  | "ready_compatible"
  | "upgrade_available"
  | "incompatible"
  | "missing"
  | "installing"
  | "rollback_available"
  | "failed";

export interface PiCandidate {
  installation: PiInstallation;
  origin: string;
  distribution: string;
  /** 启动器 sha256（空串表示没能算出来）。 */
  executableHash: string;
  launcherPath: string;
  /** 真正被执行的二进制：standalone 是启动器自身，npm 是 Node。 */
  runtimeExecutable: string;
  runtimeHash: string;
  compatibility: PiCompatibilityState | string;
  /** 这个候选**真的跑完并通过**了六项契约探测（快速发现恒为 false）。 */
  contractVerified?: boolean;
  diagnostics?: string[] | null;
}

/** 这次发现对契约验证到什么程度（与 internal/runtime/pi 的 DiscoveryVerification 对应）。 */
export type PiDiscoveryVerification = "contract_probed" | "version_only";

export interface PiDiscoveryResult {
  state: PiCompatibilityState | string;
  selected?: PiCandidate | null;
  candidates: PiCandidate[] | null;
  diagnostics?: string[] | null;
  /** contract_probed = 选中候选跑过六项契约探测；version_only = 只探测了版本与指纹。 */
  verification?: PiDiscoveryVerification | string;
}

/** 一次安装/回滚/取消的权威结果（字段与 internal/piinstall.Result 对应）。 */
export interface PiInstallResult {
  installId?: string;
  state: string;
  managedRoot: string;
  version?: string;
  targetId?: string;
  executable?: string;
  executableSha256?: string;
  installationId?: string;
  previousVersion?: string;
  rollbackAvailable: boolean;
  autoRolledBack?: boolean;
  failedStage?: string;
  error?: string;
  eventName: string;
}

/** 安装阶段（进度事件只给这一段）。 */
export type PiInstallStage = "download" | "verify" | "extract" | "probe" | "activate" | "validate";

export interface PiInstallProgress {
  installId?: string;
  stage: PiInstallStage | string;
}

/** 订阅 Pi 安装进度。返回取消订阅函数。 */
export function onPiInstallProgress(fn: (ev: PiInstallProgress) => void): () => void {
  return Events.On("myagent:pi-install", (event: { data: PiInstallProgress }) => {
    fn(event.data);
  });
}

// ---------------------------------------------------------------- 权限上限（C03）

/** profile 可用的权限上限（系统 ∩ 工作区）；Sources 逐字段说明是谁定的。 */
export interface AgentCeiling {
  filesystem: string;
  network: boolean;
  shell: boolean;
  sources: Record<string, string> | null;
  available: boolean;
}

export interface AgentCeilingResult {
  ceiling: AgentCeiling;
  unavailableFields: string[] | null;
  error?: CallError;
}

// ---------------------------------------------------------------- 调用

export const settingsApi = {
  /** 展示偏好（语言/主题）。revision 是保存时必须带回来的 CAS 凭据。 */
  getPreferences: (): Promise<PreferencesResult> => Call.ByName(`${FQN}.GetPreferences`),

  savePreferences: (expectedRevision: number, preferences: Preferences): Promise<PreferencesResult> =>
    Call.ByName(`${FQN}.SavePreferences`, expectedRevision, preferences),

  // ---- 模型 ----
  listModels: (): Promise<ModelListResult> => Call.ByName(`${FQN}.ListModels`),
  currentModel: (): Promise<CurrentModelResult> => Call.ByName(`${FQN}.CurrentModel`),
  /** 会真实调用模型（可能计费），只能由用户点击触发。 */
  testModel: (id: string): Promise<ModelProbeResponse> => Call.ByName(`${FQN}.TestModel`, id),
  setActiveModel: (ref: string): Promise<ModelAction> => Call.ByName(`${FQN}.SetActiveModel`, ref),

  /**
   * 有效配置与来源（M1ENV）。这里只取模型那一段：provider/model/baseUrl/apiKeyRef
   * 分别是谁给的（settings / env / default），设置页据此显示来源。
   */
  effectiveConfig: (): Promise<EffectiveConfigResult> =>
    Call.ByName(`${FQN}.EffectiveConfig`),

  // ---- 权限上限 ----
  agentProfileCeiling: (workspaceId: string): Promise<AgentCeilingResult> =>
    Call.ByName(`${FQN}.AgentProfileCeiling`, workspaceId),

  // ---- Pi 生命周期（F08 / F06）----
  /**
   * 完整发现：每个可用候选都会跑六项隔离契约探测（spawn 真实 Pi，单个候选秒级）。
   * 只给用户显式的“验证契约”动作用，不要挂在页面挂载上。
   */
  discoverPi: (): Promise<PiDiscoveryResult> => Call.ByName(`${FQN}.DiscoverPi`),
  /** 快速发现：只探测版本与指纹，不跑契约探测（挂载/列表用）。 */
  discoverPiFast: (): Promise<PiDiscoveryResult> => Call.ByName(`${FQN}.DiscoverPiFast`),
  selectPi: (installationId: string): Promise<PiDiscoveryResult> =>
    Call.ByName(`${FQN}.SelectPi`, installationId),
  /** 只给版本：下载地址与哈希来自随二进制下发的受信清单，UI 不能给 URL。 */
  installPi: (version: string): Promise<PiInstallResult> => Call.ByName(`${FQN}.InstallPi`, version),
  cancelPiInstall: (installId: string): Promise<void> =>
    Call.ByName(`${FQN}.CancelPiInstall`, installId),
  rollbackPi: (installationId: string): Promise<PiInstallResult> =>
    Call.ByName(`${FQN}.RollbackPi`, installationId),
};
