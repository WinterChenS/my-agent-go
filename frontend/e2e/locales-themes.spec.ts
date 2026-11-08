// 两语言 × 两主题的页面矩阵（U07 Step 3 的网页侧）。
//
// 断言四件事：
//   1. 语言/主题真的作用到 DOM（<html lang> / data-theme），不是只改了设置页的草稿；
//   2. 侧栏九个入口的文案与该语言的 catalog 完全一致（消息表生效）；
//   3. 当前语言下**不出现另一种语言的导航文案**（半边中文半边英文是最常见的回归）；
//   4. en-US 下正文不出现中文硬编码（Go 的中文 message、写死的文案都算）。
//
// 第 4 条是本任务里最会抓到东西的一条：它把"某个分区还在写死中文"变成红。
// option 里的语言名（简体中文）与带 lang 属性的元素属于正常情况，排除在外。
import { test, expect } from "@playwright/test";
import { expectClean, expectTokenContrast } from "./lib/audit";
import { NINE_PAGES, applyPrefs, message, openPage, waitShell, type Locale, type ThemeChoice } from "./lib/pages";

const LOCALES: Locale[] = ["zh-CN", "en-US"];
const THEMES: ThemeChoice[] = ["light", "dark"];

/** 页面上出现的、不属于当前语言的中文文案（排除 option 与显式 lang 元素）。 */
async function cjkLeaks(page: import("@playwright/test").Page): Promise<string[]> {
  return page.evaluate(() => {
    const out: string[] = [];
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    while (walker.nextNode()) {
      const node = walker.currentNode;
      const text = (node.nodeValue ?? "").trim();
      if (!text) continue;
      if (!/[\u3400-\u9fff]/.test(text)) continue;
      const parent = node.parentElement;
      if (!parent) continue;
      if (parent.closest("option, title, script, style")) continue;
      // <html lang> 是所有节点的祖先：把它算成"显式别的语言"会让整页文本被排除，
      // 检查于是永远为绿（预检侧踩过同一个坑）。
      const langEl = parent.closest("[lang]");
      if (langEl && langEl !== document.documentElement) continue;
      out.push(text.slice(0, 80));
    }
    return Array.from(new Set(out)).slice(0, 20);
  });
}

for (const locale of LOCALES) {
  for (const theme of THEMES) {
    test.describe(`${locale} / ${theme}`, () => {
      test.beforeEach(async ({ page }) => {
        await applyPrefs(page, locale, theme);
        await page.goto("/");
        await waitShell(page);
      });

      test("语言与主题真的应用到 DOM", async ({ page }) => {
        const facts = await page.evaluate(() => ({
          lang: document.documentElement.lang,
          theme: document.documentElement.dataset.theme,
        }));
        expect(facts.lang).toBe(locale);
        expect(facts.theme).toBe(theme);
      });

      test("侧栏九个入口的文案来自当前语言的 catalog", async ({ page }) => {
        const labels = await page.evaluate(() =>
          Array.from(document.querySelectorAll("nav li button, aside li button")).map((b) =>
            (b.textContent ?? "").replace(/\s+/g, " ").trim()
          )
        );
        for (const entry of NINE_PAGES) {
          const expected = message(locale, entry.navKey);
          expect(labels, `侧栏里缺少「${expected}」`).toContain(expected);
        }
        // 另一语言的文案不该同时出现在侧栏里。
        const other: Locale = locale === "zh-CN" ? "en-US" : "zh-CN";
        for (const entry of NINE_PAGES) {
          const otherLabel = message(other, entry.navKey);
          if (otherLabel === message(locale, entry.navKey)) continue;
          expect(labels, `侧栏里混进了另一种语言的「${otherLabel}」`).not.toContain(otherLabel);
        }
      });

      for (const entry of NINE_PAGES) {
        test(`${entry.id} 无混合语言/无对比度问题`, async ({ page }) => {
          await openPage(page, locale, entry);
          const result = await expectClean(page, `${entry.id} ${locale}/${theme}`);
          expectTokenContrast(result, `${entry.id} ${locale}/${theme}`);
          if (locale === "en-US") {
            const leaks = await cjkLeaks(page);
            expect(leaks, `${entry.id} 在 en-US 下出现中文硬编码: ${JSON.stringify(leaks)}`).toEqual([]);
          }
        });
      }
    });
  }
}
