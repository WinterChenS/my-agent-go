// 静态路由与导航目录（U01 / UI05）。
//
// 这里是"哪个路径是哪个页面"的唯一来源：
//   - 左侧栏的分组与顺序来自 navGroups()；
//   - 路由解析 resolveRoute() 对每个生产路由给一个真实组件，未知路径回落到工作台；
//   - 全局搜索的"页面与导航动作"也来自这里（后端索引只覆盖会话/Agent/Skill/产物）；
//   - anvil:// 是**应用内逻辑地址**，与页面路径共用同一个解析器（规格 §4/§5.1）。
//
// 为什么不引路由库：桌面壳里没有可分享的 URL，也没有浏览器前进后退；
// 路由只是"当前是哪个页面"一个值，一个纯函数足够，而且能被直接断言。
//
// 文案只放 key：labelKey/hintKey 必须存在于 catalog（测试会验证），
// 组件用 t() 取文案。

import { createElement, type ComponentType } from "react";
import { t } from "../i18n";
import { WorkbenchPage } from "../features/workbench/WorkbenchPage";
import {
  AgentsPage,
  ChatPage,
  LibraryPage,
  MCPPage,
  MemoryPage,
  NotImplementedPage,
  RunsPage,
  SettingsPage,
  SkillsPage,
} from "./pages";

export type RouteId =
  | "workbench"
  | "chat"
  | "chat-index"
  | "agents"
  | "skills"
  | "mcp"
  | "connectors"
  | "schedules"
  | "library"
  | "memory"
  | "runs"
  | "settings";

/** 页面组件统一签名：路由信息 + 导航动作。 */
export interface PageProps {
  onNavigate: (route: string) => void;
  route?: RouteMatch;
}

export type RouteComponent = ComponentType<PageProps>;

export interface RouteSpec {
  id: RouteId;
  /** 路由模式；`:id` 是单个路径参数。 */
  pattern: string;
  component: RouteComponent;
  labelKey: string;
  hintKey: string;
}

// 连接器/定时任务在 C08/C09 合入前只有共享的 not_implemented 状态，
// 这里用包装组件固定 feature，页面本身不接受任何额外参数（也就没有动作）。
const ConnectorsRoute: RouteComponent = () =>
  createElement(NotImplementedPage, { feature: "connectors" });
const SchedulesRoute: RouteComponent = () =>
  createElement(NotImplementedPage, { feature: "schedules" });

export const ROUTES: readonly RouteSpec[] = [
  { id: "workbench", pattern: "/", component: WorkbenchPage, labelKey: "nav.workbench", hintKey: "nav.hints.workbench" },
  { id: "chat", pattern: "/chat/:id", component: ChatPage, labelKey: "nav.chat", hintKey: "nav.hints.chat" },
  { id: "chat-index", pattern: "/chat", component: ChatPage, labelKey: "nav.chat", hintKey: "nav.hints.chat" },
  { id: "agents", pattern: "/agents", component: AgentsPage, labelKey: "nav.agents", hintKey: "nav.hints.agents" },
  { id: "library", pattern: "/library", component: LibraryPage, labelKey: "nav.library", hintKey: "nav.hints.library" },
  { id: "memory", pattern: "/memory", component: MemoryPage, labelKey: "nav.memory", hintKey: "nav.hints.memory" },
  { id: "skills", pattern: "/skills", component: SkillsPage, labelKey: "nav.skills", hintKey: "nav.hints.skills" },
  { id: "mcp", pattern: "/mcp", component: MCPPage, labelKey: "nav.mcp", hintKey: "nav.hints.mcp" },
  { id: "connectors", pattern: "/connectors", component: ConnectorsRoute, labelKey: "nav.connectors", hintKey: "nav.hints.connectors" },
  { id: "schedules", pattern: "/schedules", component: SchedulesRoute, labelKey: "nav.schedules", hintKey: "nav.hints.schedules" },
  { id: "runs", pattern: "/runs", component: RunsPage, labelKey: "nav.runs", hintKey: "nav.hints.runs" },
  { id: "settings", pattern: "/settings", component: SettingsPage, labelKey: "nav.settings", hintKey: "nav.hints.settings" },
] as const;

export interface RouteMatch {
  spec: RouteSpec;
  /** 不含查询串的规范化路径。 */
  path: string;
  params: Record<string, string>;
  query: URLSearchParams;
  /** true 表示路径不认识、回落到工作台（不是"这个路由真的存在"）。 */
  fallback: boolean;
}

/** 规范化路径：去尾部斜杠、补前导斜杠、空串即首页。 */
export function normalizePath(raw: string): string {
  let path = (raw ?? "").trim();
  const q = path.indexOf("?");
  if (q >= 0) path = path.slice(0, q);
  const h = path.indexOf("#");
  if (h >= 0) path = path.slice(0, h);
  if (path === "") return "/";
  if (!path.startsWith("/")) path = "/" + path;
  while (path.length > 1 && path.endsWith("/")) path = path.slice(0, -1);
  return path;
}

/**
 * 规范化一个"逻辑路由"（页面路径或 anvil:// 地址），保留查询串。
 *
 * 路由器存的是这个形态：路径部分规范化，查询串原样保留（搜索结果用
 * `?agent=` / `?artifact=` 定位具体条目，丢了这个就不是"同一个页面状态"了）。
 */
export function normalizeRoute(raw: string): string {
  const value = (raw ?? "").trim();
  const logical = value.toLowerCase().startsWith("anvil://") ? parseAnvilUrl(value) ?? value : value;
  const qIndex = logical.indexOf("?");
  const path = normalizePath(qIndex >= 0 ? logical.slice(0, qIndex) : logical);
  if (qIndex < 0) return path;
  const query = logical.slice(qIndex + 1).split("#")[0];
  return query ? `${path}?${query}` : path;
}

/**
 * 把 anvil:// 逻辑地址转成页面路径。
 *
 * 只接受 anvil:// 前缀；http(s) 等外部地址一律返回 null——把外部地址当内部
 * 路由会让"诊断里贴的一个网页链接"变成一次页面跳转。
 */
export function parseAnvilUrl(raw: string): string | null {
  const prefix = "anvil://";
  const value = (raw ?? "").trim();
  if (!value.toLowerCase().startsWith(prefix)) return null;
  const rest = value.slice(prefix.length);
  if (rest === "" || rest === "/") return "/";
  return "/" + rest.replace(/^\/+/, "");
}

/** 单段匹配：`:name` 捕获一段，其余大小写不敏感。 */
function matchPattern(pattern: string, path: string): Record<string, string> | null {
  const want = pattern.split("/").filter(Boolean);
  const got = path.split("/").filter(Boolean);
  if (want.length !== got.length) return null;
  const params: Record<string, string> = {};
  for (let i = 0; i < want.length; i++) {
    const seg = want[i];
    if (seg.startsWith(":")) {
      const name = seg.slice(1);
      try {
        params[name] = decodeURIComponent(got[i]);
      } catch {
        params[name] = got[i];
      }
      continue;
    }
    if (seg.toLowerCase() !== got[i].toLowerCase()) return null;
  }
  return params;
}

/**
 * 解析一个路由（页面路径或 anvil:// 地址）。
 *
 * 永不返回 undefined：未知路径回落到工作台并标记 fallback。
 * 白屏比"回落到首页"糟得多——用户至少要知道自己落在了哪里。
 */
export function resolveRoute(route: string): RouteMatch {
  const value = (route ?? "").trim();
  const logical = value.toLowerCase().startsWith("anvil://") ? parseAnvilUrl(value) : null;
  const source = logical ?? value;
  const qIndex = source.indexOf("?");
  const query = new URLSearchParams(qIndex >= 0 ? source.slice(qIndex + 1) : "");
  const path = normalizePath(source);

  for (const spec of ROUTES) {
    const params = matchPattern(spec.pattern, path);
    if (params) return { spec, path, params, query, fallback: false };
  }
  const home = ROUTES.find((r) => r.id === "workbench") ?? ROUTES[0];
  return { spec: home, path: "/", params: {}, query, fallback: true };
}

/**
 * 宿主传入的初始路由（U01 接的 anvil:// 逻辑地址入口）。
 *
 * 支持两种来源：`?route=<路径或 anvil:// 地址>` 与 `#<路径>`。两者都过一遍
 * 同一个解析器：不认识的地址回落到首页——宁可落在工作台，也不要白屏。
 */
export function initialRouteFrom(search: string, hash: string): string {
  const params = new URLSearchParams(search.startsWith("?") ? search.slice(1) : search);
  const fromQuery = params.get("route") ?? "";
  const fromHash = (hash ?? "").replace(/^#/, "");
  const raw = (fromQuery || fromHash).trim();
  if (!raw) return "/";
  const match = resolveRoute(raw);
  if (match.fallback) return "/";
  const qIndex = raw.indexOf("?");
  const query = qIndex >= 0 ? raw.slice(qIndex + 1).split("#")[0] : "";
  return query ? `${match.path}?${query}` : match.path;
}

export interface NavItem {
  id: RouteId;
  path: string;
  labelKey: string;
  hintKey: string;
}

export interface NavGroup {
  id: "primary" | "tools";
  labelKey: string;
  items: NavItem[];
}

function item(id: RouteId, path: string): NavItem {
  const spec = ROUTES.find((r) => r.id === id);
  return {
    id,
    path,
    labelKey: spec?.labelKey ?? `nav.${id}`,
    hintKey: spec?.hintKey ?? `nav.hints.${id}`,
  };
}

/**
 * 左侧栏分组。
 *
 * 主入口只有三个（工作台/对话/Agent），其余收进"工具与自动化"：
 * 日常反复点的入口不该跟一周看一次的资源库挤在同一行。
 */
export function navGroups(): NavGroup[] {
  return [
    {
      id: "primary",
      labelKey: "nav.primaryLabel",
      items: [item("workbench", "/"), item("chat", "/chat"), item("agents", "/agents")],
    },
    {
      id: "tools",
      labelKey: "nav.toolsLabel",
      items: [
        item("library", "/library"),
        item("memory", "/memory"),
        item("skills", "/skills"),
        item("mcp", "/mcp"),
        item("connectors", "/connectors"),
        item("schedules", "/schedules"),
        item("settings", "/settings"),
      ],
    },
  ];
}

export interface PageSearchItem {
  kind: "page";
  title: string;
  route: string;
  labelKey: string;
}

/**
 * 全局搜索里的"页面与导航动作"。
 *
 * 它来自静态目录而不是后端索引：索引是派生的、可能还没建，
 * 但"跳去设置"这种动作永远应该能查到。
 */
export function searchPages(query: string): PageSearchItem[] {
  const q = (query ?? "").trim().toLowerCase();
  const out: PageSearchItem[] = [];
  for (const group of navGroups()) {
    for (const it of group.items) {
      const label = t(it.labelKey);
      if (q !== "" && !label.toLowerCase().includes(q) && !it.path.toLowerCase().includes(q)) continue;
      out.push({ kind: "page", title: label, route: it.path, labelKey: it.labelKey });
    }
  }
  return out;
}
