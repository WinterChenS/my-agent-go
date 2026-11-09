// 后端数据的加载与刷新。
//
// 一条纪律：加载失败不要抛到 React 边界上，而是变成一个可渲染的状态。
// 桌面应用里"白屏 + 控制台有一行错"是最没用的失败形态——
// 用户既看不到原因，也没有可以点的下一步。

import { useCallback, useEffect, useState } from "react";
import { api } from "../api";
import type {
  CallError,
  CurrentModelResult,
  Extension,
  ModelDefinition,
  ModelOptions,
  Profile,
  Settings,
  SkillResource,
  Status,
  Workspace,
} from "../types";

export interface BackendState {
  status: Status | null;
  settings: Settings | null;
  workspaces: Workspace[];
  /** 设置里那个路径对应的工作区；没配或路径无效时为 null。 */
  currentWorkspace: Workspace | null;
  profiles: Profile[];
  /** 资源库里的模型定义。 */
  models: ModelDefinition[];
  /** 模型配置文档的 revision：保存时用它做 CAS。 */
  modelRevision: number;
  modelOptions: ModelOptions;
  /** 当前使用的模型（可能来自设置而不是资源库）。 */
  currentModel: CurrentModelResult | null;
  /** 技能与扩展（C03）。 */
  skills: SkillResource[];
  skillRevision: number;
  /** 技能改动还没被运行中的 worker 用上。 */
  skillsPending: boolean;
  skillsActiveRun: boolean;
  extensions: Extension[];
  extensionRevision: number;
  /** 首次加载尚未完成。 */
  loading: boolean;
  error: CallError | null;
}

const emptyState: BackendState = {
  status: null,
  settings: null,
  workspaces: [],
  currentWorkspace: null,
  profiles: [],
  models: [],
  modelRevision: 0,
  modelOptions: { apis: [], inputKinds: [], thinkingLevels: [] },
  currentModel: null,
  skills: [],
  skillRevision: 0,
  skillsPending: false,
  skillsActiveRun: false,
  extensions: [],
  extensionRevision: 0,
  loading: true,
  error: null,
};

/**
 * 把任意异常收敛成 CallError。
 *
 * Wails 把 Go error 转成字符串，所以这里拿不到 code——这种情况下用
 * UNKNOWN 而不是硬编一个 INTERNAL_ERROR：前者诚实表示"我不知道类型"，
 * 后者会让排查者以为是后端主动报的内部错误。
 */
export function toCallError(e: unknown): CallError {
  if (typeof e === "object" && e !== null && "code" in e && "message" in e) {
    const ce = e as CallError;
    return { code: String(ce.code), message: String(ce.message), retryable: Boolean(ce.retryable) };
  }
  const message = e instanceof Error ? e.message : String(e);
  // Go 侧 AppError.Error() 形如 "CODE: message"，尽量把 code 摘出来。
  const idx = message.indexOf(": ");
  if (idx > 0) {
    const maybeCode = message.slice(0, idx);
    if (/^[A-Z][A-Z0-9_]+$/.test(maybeCode)) {
      return { code: maybeCode, message: message.slice(idx + 2), retryable: false };
    }
  }
  return { code: "UNKNOWN", message, retryable: false };
}

export function useBackend() {
  const [state, setState] = useState<BackendState>(emptyState);

  const reload = useCallback(async () => {
    setState((prev) => ({ ...prev, loading: true, error: null }));

    // 先单独解析当前工作区：它会在后端做一次幂等登记，
    // 必须等它完成再列列表。两者并行时，列列表可能先返回，
    // 于是左栏显示"还没有工作区"、主区域却说"工作区尚未信任"——同一屏两处事实互相矛盾。
    const curRes = await api.currentWorkspace().catch((e: unknown) => ({
      item: undefined as never,
      error: toCallError(e),
    }));

    // 其余四项并行，互不阻塞：设置读不到不该拖住工作区列表。
    const [
      statusRes,
      settingsRes,
      wsRes,
      profileRes,
      modelRes,
      curModelRes,
      skillRes,
      extRes,
    ] = await Promise.allSettled([
      api.status(),
      api.settings(),
      api.workspaces(),
      api.profiles(),
      api.models(),
      api.currentModel(),
      api.skills(),
      api.extensions(),
    ]);

    const next: BackendState = {
      status: statusRes.status === "fulfilled" ? statusRes.value : null,
      settings: settingsRes.status === "fulfilled" ? settingsRes.value : null,
      workspaces: [],
      currentWorkspace: null,
      profiles: [],
      models: [],
      modelRevision: 0,
      modelOptions: { apis: [], inputKinds: [], thinkingLevels: [] },
      currentModel: null,
      skills: [],
      skillRevision: 0,
      skillsPending: false,
      skillsActiveRun: false,
      extensions: [],
      extensionRevision: 0,
      loading: false,
      error: null,
    };

    if (curRes && !curRes.error) {
      next.currentWorkspace = curRes.item;
    }

    if (wsRes.status === "fulfilled") {
      if (wsRes.value.error) next.error = wsRes.value.error;
      else next.workspaces = wsRes.value.items ?? [];
    } else {
      next.error = toCallError(wsRes.reason);
    }

    if (profileRes.status === "fulfilled") {
      if (profileRes.value.error && !next.error) next.error = profileRes.value.error;
      else next.profiles = profileRes.value.items ?? [];
    } else if (!next.error) {
      next.error = toCallError(profileRes.reason);
    }

    if (modelRes.status === "fulfilled") {
      const r = modelRes.value;
      if (r.error && !next.error) next.error = r.error;
      next.models = r.items ?? [];
      next.modelRevision = r.revision;
      next.modelOptions = r.options;
    } else if (!next.error) {
      next.error = toCallError(modelRes.reason);
    }

    if (curModelRes.status === "fulfilled" && !curModelRes.value.error) {
      next.currentModel = curModelRes.value;
    }

    if (skillRes.status === "fulfilled") {
      const r = skillRes.value;
      if (r.error && !next.error) next.error = r.error;
      next.skills = r.items ?? [];
      next.skillRevision = r.revision;
      next.skillsPending = r.pending;
      next.skillsActiveRun = r.activeRun;
    } else if (!next.error) {
      next.error = toCallError(skillRes.reason);
    }

    if (extRes.status === "fulfilled") {
      if (extRes.value.error && !next.error) next.error = extRes.value.error;
      next.extensions = extRes.value.items ?? [];
      next.extensionRevision = extRes.value.revision;
    } else if (!next.error) {
      next.error = toCallError(extRes.reason);
    }

    // status 读不到通常是应用壳还没就绪；这属于致命失败，必须显示出来。
    if (statusRes.status === "rejected" && !next.error) {
      next.error = toCallError(statusRes.reason);
    }

    setState(next);
  }, []);

  useEffect(() => {
    void reload();
  }, [reload]);

  /**
   * 刷新工作区列表与当前工作区。
   *
   * 两个都要刷：左栏读列表、工作台的权限提示读 currentWorkspace。
   * 只刷列表的话，信任按钮的 aria-label 会变（读列表），
   * 而"尚未信任"的提示仍挂着（读 currentWorkspace）——同一屏自相矛盾。
   */
  const refreshWorkspaces = useCallback(async () => {
    const [res, cur] = await Promise.all([api.workspaces(), api.currentWorkspace()]);
    setState((prev) => ({
      ...prev,
      workspaces: res.error ? prev.workspaces : res.items ?? [],
      currentWorkspace: cur.error ? prev.currentWorkspace : cur.item,
      error: res.error ?? prev.error,
    }));
  }, []);

  /** 刷新模型列表、当前模型与设置（模型切换会改设置里的 modelRef）。 */
  const refreshModels = useCallback(async () => {
    const [res, cur, settings] = await Promise.all([
      api.models(),
      api.currentModel(),
      api.settings().catch(() => null),
    ]);
    setState((prev) => ({
      ...prev,
      models: res.error ? prev.models : res.items ?? [],
      modelRevision: res.error ? prev.modelRevision : res.revision,
      modelOptions: res.error ? prev.modelOptions : res.options,
      currentModel: cur.error ? prev.currentModel : cur,
      settings: settings ?? prev.settings,
      error: res.error ?? prev.error,
    }));
  }, []);

  /**
   * 只刷新 Status。
   *
   * 为什么需要：运行时（以及它实际用的 provider/model）是在 Start 时才构建的，
   * 而整页 reload 只在挂载时跑一次。不刷的话，设置页的诊断会在一次真实运行之后
   * 仍然写着"尚未启动"——一个本来用于说明事实的面板，反而在撒谎。
   */
  const refreshStatus = useCallback(async () => {
    const res = await api.status().catch(() => null);
    if (res) setState((prev) => ({ ...prev, status: res }));
  }, []);

  /**
   * 定期重读 Status。
   *
   * 为什么需要：侧栏的运行时状态点读的是 `status.piVersion`，而这个值在启动时
   * 还是空的——Pi 版本要跑一次发现才有。只刷一次的话，用户会一直看到
   * "未检测到 Pi"，即便 Pi 就在那里、会话也能启动（实测踩到）。
   * 5 秒一次、窗口不可见时不跑：代价是一次 Wails 调用，换来状态点不再撒谎。
   */
  useEffect(() => {
    let alive = true;
    const tick = () => {
      if (!alive || document.visibilityState !== "visible") return;
      void refreshStatus();
    };
    const timer = window.setInterval(tick, 5000);
    const onVisible = () => {
      if (document.visibilityState === "visible") void refreshStatus();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      alive = false;
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [refreshStatus]);

  /**
   * 刷新技能与扩展。
   *
   * 同时刷 Status：技能是否"已生效"只有对比运行中的摘要才知道，
   * 而这个结论在 Status 里。只刷列表会让"下次生效"的提示停留在旧值上。
   */
  const refreshSkills = useCallback(async () => {
    const [res, ext, status] = await Promise.all([
      api.skills(),
      api.extensions(),
      api.status().catch(() => null),
    ]);
    setState((prev) => ({
      ...prev,
      skills: res.error ? prev.skills : res.items ?? [],
      skillRevision: res.error ? prev.skillRevision : res.revision,
      skillsPending: res.error ? prev.skillsPending : res.pending,
      skillsActiveRun: res.error ? prev.skillsActiveRun : res.activeRun,
      extensions: ext.error ? prev.extensions : ext.items ?? [],
      extensionRevision: ext.error ? prev.extensionRevision : ext.revision,
      status: status ?? prev.status,
      error: res.error ?? prev.error,
    }));
  }, []);

  /** 只刷新 Profile，用于保存后同步 revision。 */
  const refreshProfiles = useCallback(async () => {
    const res = await api.profiles();
    setState((prev) => ({
      ...prev,
      profiles: res.error ? prev.profiles : res.items ?? [],
      error: res.error ?? prev.error,
    }));
  }, []);

  return {
    ...state,
    reload,
    refreshWorkspaces,
    refreshProfiles,
    refreshModels,
    refreshStatus,
    refreshSkills,
  };
}
