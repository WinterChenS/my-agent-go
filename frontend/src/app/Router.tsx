// 路由状态（U01）。
//
// 桌面壳里没有可分享的 URL，也没有浏览器前进后退：路由只是"当前是哪个页面"。
// 所以这里是一个受控的字符串状态 + 纯归约函数；路径到页面的映射在 navigation.ts。
//
// 归约保持"同路径不制造新状态"：导航到当前页不应该触发一次无意义的重渲染，
// 更不应该把页面里正在编辑的状态重置掉。

import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useReducer,
  type ReactNode,
} from "react";
import { normalizeRoute, resolveRoute, type RouteMatch } from "./navigation";

export interface RouterState {
  /** 规范化后的逻辑路由：路径 + 可选查询串（`/agents?agent=x`）。 */
  path: string;
}

export const initialRouterState: RouterState = { path: "/" };

export type RouterAction = { kind: "navigate"; path: string };

export function navReducer(state: RouterState, action: RouterAction): RouterState {
  switch (action.kind) {
    case "navigate": {
      const next = normalizeRoute(action.path);
      return next === state.path ? state : { path: next };
    }
    default:
      return state;
  }
}

export interface RouterValue {
  path: string;
  match: RouteMatch;
  navigate: (path: string) => void;
}

const RouterContext = createContext<RouterValue | null>(null);

export function RouterProvider({
  children,
  initialPath = "/",
}: {
  children: ReactNode;
  initialPath?: string;
}) {
  const [state, dispatch] = useReducer(navReducer, { path: normalizeRoute(initialPath) });
  const navigate = useCallback((path: string) => dispatch({ kind: "navigate", path }), []);
  const value = useMemo<RouterValue>(
    () => ({ path: state.path, match: resolveRoute(state.path), navigate }),
    [state.path, navigate]
  );
  return <RouterContext.Provider value={value}>{children}</RouterContext.Provider>;
}

export function useRouter(): RouterValue {
  const ctx = useContext(RouterContext);
  if (!ctx) {
    // 静默返回一份默认值会让"忘了套 Provider"变成难查的运行期怪现象。
    throw new Error("useRouter must be used inside RouterProvider");
  }
  return ctx;
}
