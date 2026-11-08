// 页面审计的 Playwright 适配层。
//
// 判定逻辑只有一份（tests/native/uiaudit.js），这里只负责：
//   1. 把那份源码注入页面；
//   2. 调用 audit() 并返回结构化结果；
//   3. 提供"逐条失败"的断言，让失败信息里带规则名和目标元素。
//
// 为什么不在这里用 axe-core：U07 要的判定（对比度、可见焦点、截断、可达、重叠）
// 必须与 built-Wails 门禁完全一致；引一个第三方库就变成两套口径，而两套口径
// 的差异最后总是以"门禁不过但预检过了"的形式出现。
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, type Page } from "@playwright/test";

const here = dirname(fileURLToPath(import.meta.url));
export const REPO_ROOT = resolve(here, "../../..");
export const AUDIT_SOURCE = readFileSync(resolve(REPO_ROOT, "tests/native/uiaudit.js"), "utf8");

export interface Finding {
  rule: string;
  target: string;
  detail: string;
}

export interface AuditResult {
  findings: Finding[];
  facts: {
    locale: string;
    theme: string;
    themeTokens: Record<string, string>;
    tokenContrast: Array<{ pair: string; ratio: number | null; reason?: string }>;
    viewport: { w: number; h: number; dpr: number };
    controls: number;
    states: string[];
    headings: string[];
    document: { scrollWidth: number; clientWidth: number; scrollHeight: number };
  };
  summary: { total: number; byRule: Record<string, number> };
}

export interface AuditOptions {
  contrast?: boolean;
  layout?: boolean;
}

/** 把审计器注入页面（幂等）。 */
export async function installAudit(page: Page): Promise<void> {
  const has = await page.evaluate(() => Boolean((window as unknown as { __u07audit?: unknown }).__u07audit));
  if (has) return;
  await page.addScriptTag({ content: AUDIT_SOURCE });
  const ok = await page.evaluate(() => Boolean((window as unknown as { __u07audit?: unknown }).__u07audit));
  expect(ok, "审计器注入失败：页面里没有 __u07audit").toBe(true);
}

/** 运行审计。 */
export async function runAudit(page: Page, opts: AuditOptions = {}): Promise<AuditResult> {
  await installAudit(page);
  return (await page.evaluate(
    (o) => (window as unknown as { __u07audit: { audit: (x: unknown) => AuditResult } }).__u07audit.audit(o),
    opts
  )) as AuditResult;
}

/** 只取某一类发现，便于逐条断言。 */
export function byRule(result: AuditResult, rule: string): Finding[] {
  return result.findings.filter((f) => f.rule === rule);
}

/** 断言审计零发现，失败时按规则分组打印（避免 200 行重复文案）。 */
export async function expectClean(page: Page, label: string, opts: AuditOptions = {}): Promise<AuditResult> {
  const result = await runAudit(page, opts);
  if (result.findings.length > 0) {
    const grouped: Record<string, string[]> = {};
    for (const f of result.findings) {
      grouped[f.rule] = grouped[f.rule] ?? [];
      if (grouped[f.rule].length < 8) grouped[f.rule].push(`${f.target} — ${f.detail}`);
    }
    const lines = Object.entries(grouped).map(([rule, items]) => `[${rule}] ${items.length}\n    ${items.join("\n    ")}`);
    throw new Error(`${label} 有 ${result.findings.length} 条失败:\n  ${lines.join("\n  ")}`);
  }
  return result;
}

export interface FocusStep {
  target: string;
  tag: string;
  focusVisible: boolean;
  outline: string;
  boxShadow: string;
  ringVisible: boolean;
}

/**
 * 用真实 Tab 键走一遍键盘焦点，返回每一步的可见焦点证据。
 *
 * 为什么必须用键盘：程序化 focus() 不会让 Chromium 进入键盘模态，
 * :focus-visible 不命中，用它判定会把"只在键盘聚焦时画焦点环"的控件
 * 全部误报（实测侧栏 8 个入口）。这里同时要求两项：
 *   1. 浏览器认为该元素处于 :focus-visible（键盘模态）；
 *   2. 确实画出了 outline 或 box-shadow（有可见焦点环）。
 */
export async function tabFocusWalk(page: Page, steps: number): Promise<FocusStep[]> {
  await installAudit(page);
  const out: FocusStep[] = [];
  for (let i = 0; i < steps; i++) {
    await page.keyboard.press("Tab");
    const step = (await page.evaluate(() =>
      (window as unknown as { __u07audit: { tabFocusStep: () => FocusStep | null } }).__u07audit.tabFocusStep()
    )) as FocusStep | null;
    if (step) out.push(step);
  }
  return out;
}

/** token 对比度断言：正文色 token 必须在它实际配的背景上达到 AA。 */
export function expectTokenContrast(result: AuditResult, label: string): void {
  for (const entry of result.facts.tokenContrast) {
    if (entry.ratio === null) {
      throw new Error(`${label}: token 对 ${entry.pair} 无法解析颜色（${entry.reason ?? ""}）`);
    }
    // 大字号/图标可用 3.0；这里按正文 4.5 要求前景/柔化前景，其余按 3.0。
    const need = /^(foreground|muted)\//.test(entry.pair) ? 4.5 : 3.0;
    expect(entry.ratio, `${label}: token 对比度 ${entry.pair} = ${entry.ratio} < ${need}`).toBeGreaterThanOrEqual(need);
  }
}
