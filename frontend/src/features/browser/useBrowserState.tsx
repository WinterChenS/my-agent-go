// 右栏浏览器的状态与动作（B07）。
//
// 分工：
//   - **状态只有一份**：`BrowserState()` 的快照（tab、控制状态、可见性、能力）。
//     组件不自己推导 tab 列表，也不缓存"点了就算成功"的结果；每个动作之后重新读。
//   - **几何**：这里只做测量与上报（每帧合并 + 去重，见 slotGeometry.ts）。
//     React 不拿原生句柄，也不摆原生视图。
//   - **失败如实显示**：动作返回结构化 CallError 就留在 hint 里，由面板按 code 显示，
//     不吞成"没反应"。

import { useCallback, useEffect, useRef, useState, type RefObject } from "react";
import { toCallError } from "../../app/useBackend";
import type { CallError } from "../../types";
import { browserApi, type BrowserBackendApi, type BrowserSlotGeometry, type BrowserStateView } from "./api";
import { createSlotReporter, type SlotReporter } from "./slotGeometry";

/** 面板的动作。全部是"转发给后端 + 重新读状态"，没有本地业务状态。 */
export interface BrowserActions {
  refresh(): void;
  selectTab(tabId: string): void;
  closeTab(tabId: string): void;
  navigate(url: string): void;
  back(): void;
  reload(): void;
  takeOver(): void;
  resume(): void;
}

export interface UseBrowserStateOptions {
  api?: BrowserBackendApi;
  /** 测试注入的几何通道（产品里走 api.updateSlot）。 */
  onGeometry?: (slot: BrowserSlotGeometry) => void;
  /** 轮询间隔（毫秒）；<=0 关闭轮询。agent 驱动的变化靠它被发现。 */
  pollIntervalMs?: number;
}

export interface BrowserStateHandle {
  state: BrowserStateView | null;
  /** 最近一次动作的失败（按 code 显示），成功后清空。 */
  hint: CallError | null;
  actions: BrowserActions;
  slotRef: RefObject<HTMLDivElement>;
}

const DEFAULT_POLL_MS = 2000;

/** 动作的统一返回值：结构化错误，或带 error 字段的结果（接管/恢复）。 */
type BrowserActionResult = Promise<CallError | null | { error?: CallError }>;

export function useBrowserState(options: UseBrowserStateOptions = {}): BrowserStateHandle {
  const api = options.api ?? browserApi;
  const onGeometry = options.onGeometry;
  const pollMs = options.pollIntervalMs ?? DEFAULT_POLL_MS;

  const [state, setState] = useState<BrowserStateView | null>(null);
  const [hint, setHint] = useState<CallError | null>(null);

  const slotRef = useRef<HTMLDivElement>(null);
  const reporterRef = useRef<SlotReporter | null>(null);
  // 回调用最新一次的 api/onGeometry，避免把旧闭包挂在定时器上。
  const emitRef = useRef<(slot: BrowserSlotGeometry) => void>(() => undefined);
  emitRef.current = (slot) => {
    if (onGeometry) {
      onGeometry(slot);
      return;
    }
    // 两种失败都要变成 hint：**拒绝**（没有 Wails runtime / 绑定不存在）与
    // 结构化 CallError（几何被后端拒绝，例如落进工具条保留区）。
    // 只给 then(onFulfilled) 会把拒绝变成未处理的 rejection：面板看上去“没反应”，
    // 而控制台里才看得到原因。
    void api.updateSlot(slot).then(
      (err) => {
        if (err) setHint(err);
      },
      (err: unknown) => setHint(toCallError(err))
    );
  };

  const refresh = useCallback(() => {
    void api.state().then(
      (next) => setState(next),
      (err: unknown) => setHint(toCallError(err))
    );
  }, [api]);

  useEffect(() => {
    refresh();
    if (pollMs <= 0) return;
    const timer = setInterval(refresh, pollMs);
    return () => clearInterval(timer);
  }, [pollMs, refresh]);

  // 几何上报：挂载时测一次，之后按引用 + 窗口尺寸变化测。
  useEffect(() => {
    const node = slotRef.current;
    if (!node) return;
    const reporter = createSlotReporter({
      emit: (message) => emitRef.current(message),
    });
    reporterRef.current = reporter;

    const measure = () => {
      const rect = node.getBoundingClientRect();
      reporter.report({ left: rect.left, top: rect.top, width: rect.width, height: rect.height });
    };
    measure();

    let observer: ResizeObserver | null = null;
    if (typeof ResizeObserver !== "undefined") {
      observer = new ResizeObserver(measure);
      observer.observe(node);
    }
    window.addEventListener("resize", measure);
    return () => {
      window.removeEventListener("resize", measure);
      observer?.disconnect();
      // 面板消失 → 原生视图必须让位（隐藏而不是销毁：tab 状态不能丢）。
      reporter.hide();
      reporter.flush();
      reporterRef.current = null;
    };
  }, []);

  // 控制类动作的统一包装：调用 → 失败留 hint → 重新读状态。
  const run = useCallback(
    (action: () => BrowserActionResult) => {
      void action().then(
        (result) => {
          const error = result && "error" in result ? result.error : result;
          if (error) {
            setHint(error as CallError);
            return;
          }
          setHint(null);
          refresh();
        },
        (err: unknown) => setHint(toCallError(err))
      );
    },
    [refresh]
  );

  const activeTab = state?.activeTab ?? "";

  const actions: BrowserActions = {
    refresh,
    selectTab: (tabId) => run(() => api.selectTab(tabId)),
    closeTab: (tabId) => run(() => api.closeTab(tabId)),
    navigate: (url) => {
      if (!activeTab) {
        setHint({ code: "NOT_IMPLEMENTED", message: "no active tab selected", retryable: false });
        return;
      }
      run(() => api.navigate(activeTab, url));
    },
    back: () => {
      if (!activeTab) return;
      run(() => api.back(activeTab));
    },
    reload: () => {
      if (!activeTab) return;
      run(() => api.reload(activeTab));
    },
    takeOver: () => {
      if (!activeTab) return;
      run(() => api.takeOver(activeTab));
    },
    resume: () => {
      if (!activeTab) return;
      run(() => api.resume(activeTab));
    },
  };

  return { state, hint, actions, slotRef };
}
