// 模型维护的绑定调用（MP1）。
//
// 单独一份而不是继续往根 api.ts 里堆：模型页签需要的不只是 CRUD，
// 还有发现/探测/导入/供应商，这些是同一组"模型维护"动作。
//
// 与其他 feature 的 api.ts 同一形态：返回带 `error` 字段的结构体而不是 reject。
// 连接测试与扫描是显式动作，只有用户点击时才会发生。

import { Call } from "@wailsio/runtime";
import type {
  CallError,
  CurrentModelResult,
  ModelAction,
  ModelDefinition,
  ModelListResult,
  ModelProbeResponse,
  ModelResult,
} from "../../types";

const FQN = "my-agent-go/internal/app.Desktop";

/** 端点报告的一条模型：ID 是发给 API 的模型名，不是本产品的定义 ID。 */
export interface DiscoveredModel {
  id: string;
  name?: string;
  size?: number;
}

/** 发现/探测的稳定失败描述。 */
export interface DiscoveryFailure {
  code: string;
  message?: string;
  detail?: string;
}

/** 一个端点的扫描结论。 */
export interface DiscoveredEndpoint {
  kind: string;
  /** 写进模型定义的 baseUrl（Ollama 是 /v1）。 */
  baseUrl: string;
  api: string;
  reachable: boolean;
  latencyMs: number;
  /** true 表示这是到非回环地址的明文 HTTP 端点。 */
  insecure?: boolean;
  models: DiscoveredModel[] | null;
  error?: DiscoveryFailure;
}

export interface ModelDiscoveryResult {
  items: DiscoveredEndpoint[] | null;
  error?: CallError;
}

export interface ProviderProbe {
  baseUrl: string;
  api: string;
  reachable: boolean;
  latencyMs: number;
  insecure?: boolean;
  models: DiscoveredModel[] | null;
  error?: DiscoveryFailure;
}

export interface ProviderProbeResult {
  probe: ProviderProbe;
  error?: CallError;
}

/** 供应商：providerId + baseUrl + api。Insecure 与 modelIds 是派生字段。 */
export interface ProviderSummary {
  id: string;
  baseUrl: string;
  api: string;
  insecure: boolean;
  modelIds: string[] | null;
}

export interface ProviderListResult {
  items: ProviderSummary[] | null;
  revision: number;
  error?: CallError;
}

export interface ProviderResult {
  item?: ProviderSummary;
  revision: number;
  error?: CallError;
}

export interface ImportDefaults {
  contextWindow: number;
  maxOutput: number;
  inputKinds: string[];
}

export interface ModelImportResult {
  imported: string[] | null;
  updated: string[] | null;
  skipped: string[] | null;
  models: ModelDefinition[] | null;
  revision: number;
  error?: CallError;
}

/** 导入默认容量：用户自己的判断优先，导入只给一个能跑的起点。 */
export const IMPORT_DEFAULTS: ImportDefaults = {
  contextWindow: 128000,
  maxOutput: 8192,
  inputKinds: ["text"],
};

export const modelsApi = {
  list: (): Promise<ModelListResult> => Call.ByName(`${FQN}.ListModels`),

  save: (
    expectedRevision: number,
    def: ModelDefinition,
    secret: string
  ): Promise<ModelResult> => Call.ByName(`${FQN}.SaveModel`, expectedRevision, def, secret),

  remove: (expectedRevision: number, id: string): Promise<ModelResult> =>
    Call.ByName(`${FQN}.DeleteModel`, expectedRevision, id),

  /** 会真实调用模型（可能计费），必须由用户点击触发。 */
  test: (id: string): Promise<ModelProbeResponse> => Call.ByName(`${FQN}.TestModel`, id),

  setActive: (ref: string): Promise<ModelAction> => Call.ByName(`${FQN}.SetActiveModel`, ref),

  current: (): Promise<CurrentModelResult> => Call.ByName(`${FQN}.CurrentModel`),

  /** 自动发现：只探回环端点。 */
  discover: (): Promise<ModelDiscoveryResult> => Call.ByName(`${FQN}.DiscoverModels`),

  /** 用户显式扫描任意端点；allowInsecure 是对公网明文 HTTP 的确认。 */
  probeProvider: (
    baseUrl: string,
    api: string,
    credentialRef: string,
    allowInsecure: boolean
  ): Promise<ProviderProbeResult> =>
    Call.ByName(`${FQN}.ProbeProviderModels`, baseUrl, api, credentialRef, allowInsecure),

  /** 按 provider+model 幂等导入。 */
  importModels: (
    providerId: string,
    baseUrl: string,
    api: string,
    modelIds: string[],
    defaults: ImportDefaults,
    allowInsecure: boolean
  ): Promise<ModelImportResult> =>
    Call.ByName(`${FQN}.ImportModels`, providerId, baseUrl, api, modelIds, defaults, allowInsecure),

  providers: (): Promise<ProviderListResult> => Call.ByName(`${FQN}.ListProviders`),

  saveProvider: (
    id: string,
    baseUrl: string,
    api: string,
    allowInsecure: boolean
  ): Promise<ProviderResult> =>
    Call.ByName(`${FQN}.SaveProvider`, id, baseUrl, api, allowInsecure),

  deleteProvider: (id: string): Promise<ProviderResult> => Call.ByName(`${FQN}.DeleteProvider`, id),
};
