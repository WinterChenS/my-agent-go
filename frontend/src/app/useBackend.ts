// 后端数据的加载与刷新。
//
// 一条纪律：加载失败不要抛到 React 边界上，而是变成一个可渲染的状态。
// 桌面应用里"白屏 + 控制台有一行错"是最没用的失败形态——
// 用户既看不到原因，也没有可以点的下一步。

import { useCallback, useEffect, useState } from "react";
import { api } from "../api";
import type { CallError, Profile, Settings, Status, Workspace } from "../types";

export interface BackendState {
  status: Status | null;
  settings: Settings | null;
  workspaces: Workspace[];
  /** 设置里那个路径对应的工作区；没配或路径无效时为 null。 */
  currentWorkspace: Workspace | null;
  profiles: Profile[];
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
    const [statusRes, settingsRes, wsRes, profileRes] = await Promise.allSettled([
      api.status(),
      api.settings(),
      api.workspaces(),
      api.profiles(),
    ]);

    const next: BackendState = {
      status: statusRes.status === "fulfilled" ? statusRes.value : null,
      settings: settingsRes.status === "fulfilled" ? settingsRes.value : null,
      workspaces: [],
      currentWorkspace: null,
      profiles: [],
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

  /** 只刷新 Profile，用于保存后同步 revision。 */
  const refreshProfiles = useCallback(async () => {
    const res = await api.profiles();
    setState((prev) => ({
      ...prev,
      profiles: res.error ? prev.profiles : res.items ?? [],
      error: res.error ?? prev.error,
    }));
  }, []);

  return { ...state, reload, refreshWorkspaces, refreshProfiles };
}
