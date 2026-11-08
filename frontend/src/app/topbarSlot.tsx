// 顶栏插槽（D2c）。
//
// 为什么要有这个东西：原型 chat.html 的对话页只有**一个** 52px 的页头——它就在
// `<header class="topbar">` 里（`.chat-head-main` + `.chat-head-actions`）。我们的壳层
// 顶栏（D1 的 `.anvil-shell-topbar`）已经是那一条，但如果对话页自己再画一条
// `.achat-head`，整页就比原型低 52px，看起来是两个头叠着（D2b 实测 thread.top=104，
// 原型 52）。
//
// 所以壳层把顶栏里的这层容器暴露给路由：
//
//   Layout:  <header class="anvil-shell-topbar"><div data-topbar-slot ref=… />…</header>
//   ChatPage: createPortal(页头内容, slot.element)
//
// 纪律：
//   - 路由只是**借用**壳层的顶栏，不是再画一条；拿不到插槽时（例如单测直接渲染
//     页面、没有壳层）就不渲染页头，而不是退回"页内自己再长一条带子"——那正是
//     本任务要修掉的缺陷；
//   - 占用要显式声明（claim），壳层据此让出自己的 crumb / 搜索 / 状态区：一条
//     52px 的带子里塞进两套页头内容，仍然算两个头；
//   - 占用的 token 由路由自己持有：路由切换时旧页面的清理不能把新页面的占用清掉
//     （否则顶栏会闪一下 crumb）。

import { createContext, useContext, type ReactNode } from "react";
// 插槽自己的几何/层叠规则（空槽不产生盒子、占用时钉住顶栏行高），见该文件头。
import "./topbarSlot.css";

export interface TopbarSlot {
  /** 插槽容器节点。壳层挂载完成前是 null；路由用 createPortal 把页头渲进去。 */
  element: HTMLElement | null;
  /**
   * 声明「我占用了顶栏」/「我让出顶栏」。
   *
   * token 是调用方自己持有的稳定对象：`claim(token,false)` 只在自己仍然持有占用时
   * 才清除，因此路由切换时"新页面占用 → 旧页面清理"的顺序不会把占用抹掉。
   */
  claim: (token: object, claimed: boolean) => void;
}

/** 不在壳层里时的空插槽：没有容器、占用是空操作（见文件头纪律）。 */
const OUTSIDE_SHELL: TopbarSlot = { element: null, claim: () => undefined };

const TopbarSlotContext = createContext<TopbarSlot | null>(null);

export function TopbarSlotProvider({
  value,
  children,
}: {
  value: TopbarSlot;
  children: ReactNode;
}) {
  return <TopbarSlotContext.Provider value={value}>{children}</TopbarSlotContext.Provider>;
}

/**
 * 取顶栏插槽。
 *
 * 与 `useShell` 刻意不同：缺 Provider 不抛错，返回空插槽。页面在壳层外渲染是
 * 合法场景（组件测试、以后可能的独立窗口），那时"没有顶栏可借"是事实；
 * 抛错会把这类渲染变成崩溃，而静默回退成页内页头又会长出第二条带子。
 */
export function useTopbarSlot(): TopbarSlot {
  return useContext(TopbarSlotContext) ?? OUTSIDE_SHELL;
}
