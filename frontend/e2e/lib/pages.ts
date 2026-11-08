// 九个生产页面在预检里的入口。
//
// 导航文案从 locale catalog 里读，不在这里抄一份：抄一份的后果是"文案改了、
// 预检还在点旧文案"，于是预检悄悄点到别处（或者直接失败）而没人知道为什么。
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { expect, type Page } from "@playwright/test";
import { REPO_ROOT } from "./audit";

export type Locale = "zh-CN" | "en-US";
export type ThemeChoice = "light" | "dark";

export interface PageEntry {
  /** 稳定 id（与 v1-gates / 追踪矩阵里的页面名对应）。 */
  id: string;
  /** catalog 里的导航 key。 */
  navKey: string;
}

/** U07 计划要求的九页：工作台、对话、Agent、Skill、MCP、连接器、定时任务、资源库、设置。 */
export const NINE_PAGES: readonly PageEntry[] = [
  { id: "workbench", navKey: "nav.workbench" },
  { id: "chat", navKey: "nav.chat" },
  { id: "agents", navKey: "nav.agents" },
  { id: "skills", navKey: "nav.skills" },
  { id: "mcp", navKey: "nav.mcp" },
  { id: "connectors", navKey: "nav.connectors" },
  { id: "schedules", navKey: "nav.schedules" },
  { id: "library", navKey: "nav.library" },
  { id: "settings", navKey: "nav.settings" },
] as const;

type Catalog = { [k: string]: string | Catalog };

function loadCatalog(locale: Locale): Catalog {
  const path = resolve(REPO_ROOT, "frontend/src/i18n/locales", `${locale}.json`);
  return JSON.parse(readFileSync(path, "utf8")) as Catalog;
}

/** 按点分 key 取文案。 */
export function message(locale: Locale, key: string): string {
  const parts = key.split(".");
  let node: string | Catalog | undefined = loadCatalog(locale);
  for (const part of parts) {
    if (typeof node !== "object" || node === null) return "";
    node = node[part];
  }
  return typeof node === "string" ? node : "";
}

/**
 * 等壳层就绪：侧栏里至少有九个导航入口。
 *
 * 为什么不直接等 `h1`：任一页面在"后端不可用"的状态下都可以不渲染 h1
 * （例如工作台整体落在错误态），那会让等待永远超时。侧栏是壳层的一部分，
 * 它的存在与数据无关。
 */
export async function waitShell(page: Page): Promise<void> {
  await expect
    .poll(async () => page.locator("nav li button, aside li button").count(), { timeout: 20_000 })
    .toBeGreaterThanOrEqual(9);
}

/** 进入页面：点击左侧栏里文案与 catalog 一致的那个入口。 */
export async function openPage(page: Page, locale: Locale, entry: PageEntry): Promise<void> {
  const label = message(locale, entry.navKey);
  expect(label, `catalog 里没有 ${entry.navKey}`).not.toBe("");
  const nav = page.locator("nav, aside").first();
  const button = nav.getByRole("button", { name: label, exact: true }).first();
  await expect(button, `侧栏里找不到「${label}」`).toBeVisible();
  await button.click();
  // 页面切换是 React 状态更新，不是导航。两个信号都必须成立（不 sleep）：
  //   1. 侧栏入口变成 aria-current="page"（当前页高亮）；
  //   2. <main> 的 aria-label 变成该页文案（读屏软件据此知道这是哪一页）。
  await expect.poll(async () => button.getAttribute("aria-current"), { timeout: 10_000 }).toBe("page");
  await expect.poll(async () => page.locator("main").first().getAttribute("aria-label"), { timeout: 10_000 }).toBe(label);
}

/** 切换语言/主题：通过 localStorage 镜像 + reload（预检专用，不需要后端）。 */
export async function applyPrefs(page: Page, locale: Locale, theme: ThemeChoice): Promise<void> {
  await page.addInitScript(
    ([loc, th]) => {
      window.localStorage.setItem("anvil.locale", loc);
      window.localStorage.setItem("anvil.theme", th);
    },
    [locale, theme] as const
  );
}
