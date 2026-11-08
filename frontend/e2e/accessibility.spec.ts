// 可访问性预检（U07 Step 2/4 的网页侧）。
//
// 覆盖：九页 × 两语言 × 两主题的可访问名称、可见焦点、对比度、可达性与截断。
// 预检跑的是构建产物在没有 Wails 宿主时的诚实降级（每页都是真实状态，不是假数据），
// 因此这里的绿只说明"样式与结构与文案在这一层没坏"，不等于 G3 通过。
// 真机证据在 tests/native/ui_acceptance_test.go。
import { test, expect } from "@playwright/test";
import { expectClean, expectTokenContrast, tabFocusWalk } from "./lib/audit";
import { NINE_PAGES, applyPrefs, openPage, waitShell, type Locale, type ThemeChoice } from "./lib/pages";

const LOCALES: Locale[] = ["zh-CN", "en-US"];
const THEMES: ThemeChoice[] = ["light", "dark"];

for (const locale of LOCALES) {
  for (const theme of THEMES) {
    test.describe(`可访问性 ${locale} / ${theme}`, () => {
      test.beforeEach(async ({ page }) => {
        await applyPrefs(page, locale, theme);
        await page.goto("/");
        await waitShell(page);
      });

      for (const entry of NINE_PAGES) {
        test(`${entry.id} 零可访问性发现`, async ({ page }) => {
          await openPage(page, locale, entry);
          const result = await expectClean(page, `${entry.id} ${locale}/${theme}`);
          expectTokenContrast(result, `${entry.id} ${locale}/${theme}`);
          expect(result.facts.theme, "主题必须真的应用到 DOM 上").toBe(theme);
          expect(result.facts.locale, "语言必须真的应用到 <html lang>").toBe(locale);
          expect(result.facts.controls, "页面上应当有可交互控件").toBeGreaterThan(0);
        });
      }
    });
  }
}

test.describe("键盘可达性", () => {
  test("Tab 走完一圈：每一步都处于键盘模态且有可见焦点环", async ({ page }) => {
    await applyPrefs(page, "zh-CN", "light");
    await page.goto("/");
    await waitShell(page);

    const steps = await tabFocusWalk(page, 14);
    expect(steps.length, "Tab 没有到达任何元素").toBeGreaterThan(8);
    const noRing = steps.filter((s) => !s.ringVisible);
    expect(
      noRing.map((s) => `${s.target}（${s.outline} / ${s.boxShadow}）`),
      "以下控件的键盘焦点没有可见焦点环"
    ).toEqual([]);
    const notKbdModal = steps.filter((s) => !s.focusVisible);
    expect(
      notKbdModal.map((s) => s.target),
      "以下控件在键盘 Tab 后没有进入 :focus-visible"
    ).toEqual([]);
    // 侧栏入口必须真的能被 Tab 到（不能只靠鼠标）。
    expect(steps.some((s) => s.target.includes("工作台")), "Tab 到不了侧栏入口").toBe(true);
  });

  test("焦点顺序从顶栏搜索开始，且不跳过可交互控件（前 14 步无 body 空步）", async ({ page }) => {
    await applyPrefs(page, "en-US", "dark");
    await page.goto("/");
    await waitShell(page);
    const steps = await tabFocusWalk(page, 14);
    expect(steps.length, "有 Tab 步没有落到任何元素（焦点跑到 body）").toBe(14);
    expect(steps[0].tag, "第一个 Tab 目标应是顶栏搜索输入框").toBe("input");
  });
});
