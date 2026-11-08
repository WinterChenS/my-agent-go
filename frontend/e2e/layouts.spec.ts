// 布局预检（U07 Step 4 的网页侧）：五个窗口尺寸 × 九页。
//
// 断言的是"用户点得到"这件事：无横向滚动、无控件横向被裁、无控件实质重叠、
// 滚入视野后都在视口内、控件文案不被截断。
//
// 注意：这里的 880×600 是**CSS 视口**。Windows 150%/200% 系统缩放、
// macOS Retina、Linux X11/Wayland 无法在网页预检里证明，那些属于 native 证据
// （本机只能证 100%；见 ADR 0005 与 U07 报告里的 deferred 清单）。
import { test, expect } from "@playwright/test";
import { expectClean } from "./lib/audit";
import { NINE_PAGES, applyPrefs, openPage, waitShell, type Locale } from "./lib/pages";

const VIEWPORTS = [
  { width: 880, height: 600 },
  { width: 1024, height: 720 },
  { width: 1366, height: 768 },
  { width: 1440, height: 900 },
  { width: 1920, height: 1080 },
] as const;

const LOCALES: Locale[] = ["zh-CN", "en-US"];

for (const locale of LOCALES) {
  for (const vp of VIEWPORTS) {
    test.describe(`${locale} ${vp.width}×${vp.height}`, () => {
      test.use({ viewport: { width: vp.width, height: vp.height } });

      test.beforeEach(async ({ page }) => {
        await applyPrefs(page, locale, "light");
        await page.goto("/");
        await waitShell(page);
      });

      for (const entry of NINE_PAGES) {
        test(`${entry.id} 布局无裁剪/重叠`, async ({ page }) => {
          await openPage(page, locale, entry);
          const result = await expectClean(page, `${entry.id} ${locale} ${vp.width}×${vp.height}`, {
            contrast: false,
          });
          expect(result.facts.viewport.w, "视口宽度必须与请求一致").toBe(vp.width);
          expect(
            result.facts.document.scrollWidth,
            `页面出现横向滚动（${result.facts.document.scrollWidth} > ${result.facts.document.clientWidth}）`
          ).toBeLessThanOrEqual(result.facts.document.clientWidth);
        });
      }
    });
  }
}
