// 路由解析测试（U01 / UI05）。
//
// 这一层看起来"不可能出错"，但它决定用户能不能到达设置页——
// 设置页是修复"工作区未信任"的唯一入口，路由回落到白屏会把整个应用锁死。
// 所以断言不是"有个函数返回了东西"，而是：每个生产路由都指向一个真实组件，
// 未知路径也有回落，anvil:// 逻辑地址与页面路径走同一个解析器。

import { describe, expect, it } from "vitest";
import {
  ROUTES,
  initialRouteFrom,
  navGroups,
  normalizeRoute,
  parseAnvilUrl,
  resolveRoute,
  searchPages,
} from "./navigation";
import { initialRouterState, navReducer } from "./Router";
import { t } from "../i18n";

/** 规格 §5.1 列出的九个生产路由。 */
const REQUIRED_PATTERNS = [
  "/",
  "/chat/:id",
  "/agents",
  "/skills",
  "/mcp",
  "/connectors",
  "/schedules",
  "/library",
  "/settings",
  "/observability",
] as const;

function concrete(pattern: string): string {
  return pattern.replace(":id", "sess-1");
}

describe("resolveRoute", () => {
  it.each(REQUIRED_PATTERNS)("每个生产路由都有真实组件: %s", (pattern) => {
    const match = resolveRoute(concrete(pattern));
    expect(match).toBeDefined();
    // 不能是"未知路径回落到工作台"的那条路：那样看起来每个路由都通了，
    // 实际上点进去全是同一个页面。
    expect(match.fallback).toBe(false);
    expect(match.spec.pattern).toBe(pattern);
    expect(typeof match.spec.component).toBe("function");
    // 导航文案必须来自 catalog（U00 的扫描器只拦字面量，这里拦"key 不存在"）。
    expect(t(match.spec.labelKey)).not.toBe(match.spec.labelKey);
  });

  it("未知路径回落到工作台，但仍然有真实组件（不能白屏）", () => {
    const match = resolveRoute("/no-such-page");
    expect(match.fallback).toBe(true);
    expect(match.spec.id).toBe("workbench");
    expect(typeof match.spec.component).toBe("function");
    expect(resolveRoute("").spec.id).toBe("workbench");
  });

  it("解析 /chat/:id 的参数并保留尾部查询串", () => {
    const match = resolveRoute("/chat/sess-42");
    expect(match.spec.id).toBe("chat");
    expect(match.params.id).toBe("sess-42");
    expect(match.path).toBe("/chat/sess-42");

    const agents = resolveRoute("/agents?agent=agent-1");
    expect(agents.spec.id).toBe("agents");
    expect(agents.path).toBe("/agents");
    expect(agents.query.get("agent")).toBe("agent-1");
  });

  it("尾部斜杠与大小写不会造出另一个页面", () => {
    expect(resolveRoute("/settings/").spec.id).toBe("settings");
    expect(resolveRoute("/SETTINGS").spec.id).toBe("settings");
  });

  it("每个路由只对应一个页面 id（不会两条规则抢同一路径）", () => {
    const ids = new Set(ROUTES.map((r) => r.id));
    expect(ids.size).toBe(ROUTES.length);
    const solved = REQUIRED_PATTERNS.map((p) => resolveRoute(concrete(p)).spec.id);
    expect(new Set(solved).size).toBe(REQUIRED_PATTERNS.length);
  });
});

describe("anvil:// 逻辑地址", () => {
  it("映射到与页面路径相同的解析器", () => {
    expect(parseAnvilUrl("anvil://chat/sess-9")).toBe("/chat/sess-9");
    expect(parseAnvilUrl("anvil://settings")).toBe("/settings");
    expect(parseAnvilUrl("anvil://library?artifact=a1")).toBe("/library?artifact=a1");
    expect(parseAnvilUrl("anvil://")).toBe("/");
    // 不是 anvil:// 的一律不解析（不能把 http(s) 当内部地址）。
    expect(parseAnvilUrl("https://example.com/settings")).toBe(null);
    expect(parseAnvilUrl("/settings")).toBe(null);
  });

  it("resolveRoute 直接接受 anvil:// 地址", () => {
    expect(resolveRoute("anvil://skills").spec.id).toBe("skills");
    expect(resolveRoute("anvil://chat/sess-1").params.id).toBe("sess-1");
  });
});

describe("anvil:// 作为启动地址", () => {
  it("宿主传入的初始地址与页面路径等价", () => {
    expect(initialRouteFrom("?route=anvil://settings", "")).toBe("/settings");
    expect(initialRouteFrom("?route=/chat/sess-1", "")).toBe("/chat/sess-1");
    expect(initialRouteFrom("?route=anvil://library?artifact=a1", "")).toBe("/library?artifact=a1");
    expect(initialRouteFrom("", "#/skills")).toBe("/skills");
    // 不认识的地址回落到首页，而不是把用户丢到一个白屏路由上。
    expect(initialRouteFrom("?route=anvil://nope", "")).toBe("/");
    expect(initialRouteFrom("", "")).toBe("/");
  });
});

describe("导航状态", () => {
  it("默认停在工作台", () => {
    expect(initialRouterState.path).toBe("/");
  });

  it("同路径不制造新状态（不会重置页面内编辑状态）", () => {
    const state = { path: "/settings" };
    expect(navReducer(state, { kind: "navigate", path: "/settings/" })).toBe(state);
  });

  it("导航保留查询串并规范化路径", () => {
    const next = navReducer(initialRouterState, { kind: "navigate", path: "/agents/?agent=a1" });
    expect(next.path).toBe("/agents?agent=a1");
    const match = resolveRoute(next.path);
    expect(match.spec.id).toBe("agents");
    expect(match.query.get("agent")).toBe("a1");
  });

  it("anvil:// 地址在导航层也能用", () => {
    expect(normalizeRoute("anvil://chat/sess-2")).toBe("/chat/sess-2");
  });
});

describe("导航目录", () => {
  it("三个主入口 + 工具与自动化分组", () => {
    const groups = navGroups();
    expect(groups.map((g) => g.id)).toEqual(["primary", "tools"]);
    expect(groups[0].items.map((i) => i.id)).toEqual(["workbench", "chat", "agents"]);
    // 工具分组必须覆盖资源库、记忆、Skill、MCP、连接器、定时任务与设置。
    expect(groups[1].items.map((i) => i.id)).toEqual([
      "library",
      "memory",
      "skills",
      "mcp",
      "connectors",
      "schedules",
      "settings",
      "observability",
    ]);
    for (const group of groups) {
      for (const item of group.items) {
        expect(t(item.labelKey)).not.toBe(item.labelKey);
        expect(item.path.startsWith("/")).toBe(true);
      }
    }
  });

  it("页面搜索来自静态目录（不经过后端索引）", () => {
    const all = searchPages("");
    expect(all.length).toBeGreaterThanOrEqual(10);
    expect(searchPages("设置").some((p) => p.route === "/settings")).toBe(true);
    expect(searchPages("skill").some((p) => p.route === "/skills")).toBe(true);
    // 没有命中时返回空数组，不是 null。
    expect(searchPages("不存在的东西")).toEqual([]);
  });
});
