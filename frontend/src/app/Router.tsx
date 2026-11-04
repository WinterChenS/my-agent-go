// 五入口导航。
//
// 不用 react-router：桌面应用没有 URL，路由在这里只是"当前是哪个视图"一个值。
// 引一个路由库来管理一个枚举，多出的是依赖和一层需要理解的抽象，
// 换不到任何东西。真正的收益（深链接、前进后退）在桌面壳里也不存在。
//
// navReducer 是纯函数，导航逻辑可以被直接断言，不必渲染 React 才能测。

import { createContext, useContext, useReducer, type Dispatch, type ReactNode } from "react";

export type ViewId = "workbench" | "resources" | "memory" | "runs" | "settings";

export interface ViewSpec {
  id: ViewId;
  label: string;
  /** 一句话说明这个入口能做什么，用于空白态与悬停提示。 */
  hint: string;
}

/**
 * 五入口的顺序即"日常使用频率"：先干活，再管资源，最后看历史与设置。
 * 顺序在这里是唯一来源，Layout 与测试都从这里取，避免两处各写一遍。
 */
export const VIEWS: readonly ViewSpec[] = [
  { id: "workbench", label: "工作台", hint: "在工作区里和 agent 一起干活" },
  { id: "resources", label: "资源库", hint: "模型、技能与 MCP 服务" },
  { id: "memory", label: "记忆", hint: "跨会话保留的上下文" },
  { id: "runs", label: "运行记录", hint: "历史运行与产物" },
  { id: "settings", label: "设置", hint: "运行环境、工作区与凭证" },
] as const;

export interface RouterState {
  view: ViewId;
  /**
   * 视图内的二级选择（例如运行记录里选中的 run）。
   * 由各视图自行解释，导航层不假设它的含义。
   */
  selection?: string;
}

export const initialRouterState: RouterState = { view: "workbench" };

export type RouterAction =
  | { kind: "go"; view: ViewId; selection?: string }
  | { kind: "select"; selection?: string };

export function navReducer(state: RouterState, action: RouterAction): RouterState {
  switch (action.kind) {
    case "go":
      // 切视图时清掉上一个视图的选择，避免带着别人的 selection 进新视图。
      if (action.selection === undefined) {
        return state.view === action.view ? state : { view: action.view };
      }
      return { view: action.view, selection: action.selection };
    case "select":
      return { ...state, selection: action.selection };
    default:
      return state;
  }
}

/** 视图 id 是否合法。绑定层传来的字符串要先过这里，不能直接当 ViewId 用。 */
export function isViewId(v: string): v is ViewId {
  return VIEWS.some((spec) => spec.id === v);
}

/** 按方向键在五入口之间移动，返回目标视图（用于 tablist 的键盘导航）。 */
export function nextView(current: ViewId, delta: number): ViewId {
  const idx = VIEWS.findIndex((v) => v.id === current);
  const base = idx < 0 ? 0 : idx;
  const len = VIEWS.length;
  return VIEWS[(base + delta + len) % len].id;
}

const RouterContext = createContext<{
  state: RouterState;
  dispatch: Dispatch<RouterAction>;
} | null>(null);

export function RouterProvider({ children }: { children: ReactNode }) {
  const [state, dispatch] = useReducer(navReducer, initialRouterState);
  return <RouterContext.Provider value={{ state, dispatch }}>{children}</RouterContext.Provider>;
}

export function useRouter() {
  const ctx = useContext(RouterContext);
  if (!ctx) {
    // 静默返回一个假值会让"忘了套 Provider"变成难查的运行期怪现象，
    // 直接说清楚。
    throw new Error("useRouter 必须在 RouterProvider 内使用");
  }
  return ctx;
}
