// 违规夹具的红证（U07 Step 2 的核心纪律）。
//
// 这个文件回答一个问题：**这些检查真的会红吗？**
// 如果一条检查在故意违规的夹具上也是绿的，那么它对生产页面的通过就没有任何意义。
// 所以每条规则都必须先在这里失败一次，才允许出现在其他 spec 里。
//
// 同时用干净对照页（fixtures/clean/）证明检查不是"见谁咬谁"：
// 干净页必须零发现，否则检查器只是在乱报。
import { test, expect } from "@playwright/test";
import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { resolve } from "node:path";
import { byRule, expectClean, runAudit, tabFocusWalk, REPO_ROOT } from "./lib/audit";
import {
  scanDirectColors,
  scanHardcodedMetrics,
  scanMessageKeys,
  scanMockImports,
  sourceFiles,
} from "./lib/static-scan.mjs";

const FIXTURES = resolve(REPO_ROOT, "frontend/e2e/fixtures");

function fixtureUrl(rel: string): string {
  return pathToFileURL(resolve(FIXTURES, rel)).href;
}

test.describe("违规夹具必须让检查变红", () => {
  test("unnamed-control：没有可访问名称的按钮被点名", async ({ page }) => {
    await page.goto(fixtureUrl("violations/missing-name.html"));
    const result = await runAudit(page, { layout: false });
    const hits = byRule(result, "unnamed-control");
    expect(hits.length, "缺少可访问名称的夹具必须被检出").toBeGreaterThanOrEqual(2);
    // 有名字的按钮不能被误报。
    expect(hits.some((h) => h.target.includes("named"))).toBe(false);
  });

  test("no-visible-focus：键盘 Tab 后没有焦点环的按钮被点名", async ({ page }) => {
    await page.goto(fixtureUrl("violations/no-focus-style.html"));
    const steps = await tabFocusWalk(page, 1);
    expect(steps.length, "Tab 没有落到按钮上").toBe(1);
    expect(steps[0].target, "焦点应落在违规按钮上").toContain("invisible-focus");
    expect(steps[0].ringVisible, "夹具的按钮必须没有可见焦点环（否则这条检查没有意义）").toBe(false);
  });

  test("low-contrast：对比度不足的文字被点名", async ({ page }) => {
    await page.goto(fixtureUrl("violations/low-contrast.html"));
    const result = await runAudit(page, { layout: false });
    const hits = byRule(result, "low-contrast");
    expect(hits.length, "对比度不足必须被检出").toBeGreaterThanOrEqual(1);
    expect(hits[0].target).toContain("faint");
  });

  test("clipped-control / h-scroll：超出视口的控件被点名", async ({ page }) => {
    await page.setViewportSize({ width: 800, height: 600 });
    await page.goto(fixtureUrl("violations/clipped-layout.html"));
    const result = await runAudit(page, { contrast: false });
    expect(byRule(result, "clipped-control").length, "被裁掉的控件必须被检出").toBeGreaterThanOrEqual(1);
    expect(byRule(result, "h-scroll").length, "横向滚动必须被检出").toBeGreaterThanOrEqual(1);
  });

  test("overlap：两个交互元素实质重叠被点名", async ({ page }) => {
    await page.goto(fixtureUrl("violations/overlap.html"));
    const result = await runAudit(page, { contrast: false });
    const hits = byRule(result, "overlap");
    expect(hits.length, "控件重叠必须被检出").toBeGreaterThanOrEqual(1);
  });

  test("truncated-label：控件文案被截断被点名", async ({ page }) => {
    await page.goto(fixtureUrl("violations/truncated-label.html"));
    const result = await runAudit(page, { contrast: false });
    const hits = byRule(result, "truncated-label");
    expect(hits.length, "截断的控件文案必须被检出").toBeGreaterThanOrEqual(1);
    expect(hits[0].target).toContain("long-label");
  });

  test("干净对照页必须零发现，且键盘焦点环可见", async ({ page }) => {
    await page.goto(fixtureUrl("clean/page.html"));
    await expectClean(page, "clean/page.html");
    const steps = await tabFocusWalk(page, 2);
    expect(steps.length, "Tab 没有落到控件上").toBe(2);
    for (const step of steps) {
      expect(step.ringVisible, `干净页的 ${step.target} 必须有可见焦点环`).toBe(true);
    }
  });
});

test.describe("静态扫描必须在违规源码上变红", () => {
  const violationsRoot = resolve(FIXTURES, "violations");
  const cleanRoot = resolve(FIXTURES, "clean");

  test("mock-import：演示数据引用被检出", () => {
    const findings = scanMockImports(violationsRoot, sourceFiles(violationsRoot));
    expect(findings.length, "引用 ./mock/demo-runs 必须被检出").toBeGreaterThanOrEqual(1);
    expect(findings.some((f) => f.file.endsWith("mock-usage.tsx"))).toBe(true);
  });

  test("hardcoded-metric：写死的统计数字被检出", () => {
    const findings = scanHardcodedMetrics(violationsRoot, sourceFiles(violationsRoot));
    expect(findings.length, "1,284 / 98% / 12 次 必须被检出").toBeGreaterThanOrEqual(3);
  });

  test("direct-color：直接颜色与调色板类名被检出", () => {
    const findings = scanDirectColors(violationsRoot, sourceFiles(violationsRoot));
    expect(findings.length, "bg-[#0f141b] / #ff0000 / text-slate-200 必须被检出").toBeGreaterThanOrEqual(3);
  });

  test("message-key-drift：不存在的消息 key 被检出", () => {
    const catalogs = ["zh-CN", "en-US"].map(
      (l) => JSON.parse(readFileSync(resolve(REPO_ROOT, "frontend/src/i18n/locales", `${l}.json`), "utf8")) as object
    );
    const findings = scanMessageKeys(violationsRoot, sourceFiles(violationsRoot), catalogs);
    const keys = findings.map((f) => f.detail);
    expect(keys.some((k) => k.includes("nav.settingss")), "拼错的 key 必须被检出").toBe(true);
    expect(keys.some((k) => k.includes("errors.NO_SUCH_CODE")), "不存在的错误码 key 必须被检出").toBe(true);
    // 真实存在的 key 不能被误报。
    expect(keys.some((k) => k.includes("nav.settings") && !k.includes("nav.settingss"))).toBe(false);
  });

  test("干净对照源码必须零发现", () => {
    const files = sourceFiles(cleanRoot);
    const catalogs = ["zh-CN", "en-US"].map(
      (l) => JSON.parse(readFileSync(resolve(REPO_ROOT, "frontend/src/i18n/locales", `${l}.json`), "utf8")) as object
    );
    expect(scanMockImports(cleanRoot, files)).toEqual([]);
    expect(scanHardcodedMetrics(cleanRoot, files)).toEqual([]);
    expect(scanDirectColors(cleanRoot, files)).toEqual([]);
    expect(scanMessageKeys(cleanRoot, files, catalogs)).toEqual([]);
  });
});
