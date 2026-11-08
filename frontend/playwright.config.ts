// Playwright 预检配置（U07）。
//
// 两件与门禁无关但必须写清楚的事：
//   1. 浏览器二进制放在工具目录 `.tools/pw-browsers`（不进仓库、不进安装包）。
//      `npm run test:e2e` 之前需要 `npx playwright install chromium`，见 docs/evidence/。
//   2. 预检**不产出**任何门禁证据。G3 的 UI/I18N/THEME 证据只能来自构建后的
//      Wails 应用（tests/native/ui_acceptance_test.go + devtools native runner）。
//      这里的价值是：把静态扫描、违规夹具、两语言×两主题与布局回归压到十几秒，
//      让改样式的人先撞上自己的错误，而不是等真机矩阵。
import { defineConfig, devices } from "@playwright/test";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, "..");

// 必须在 Playwright 解析浏览器路径之前设置（config 先于启动执行）。
process.env.PLAYWRIGHT_BROWSERS_PATH = resolve(repoRoot, ".tools", "pw-browsers");

const PORT = 4179;
export const BASE_URL = `http://127.0.0.1:${PORT}`;

export default defineConfig({
  testDir: "./e2e",
  // 串行：同一台机器上同时起多个 vite preview + Chromium 只会让失败变得难以复现。
  fullyParallel: false,
  workers: 1,
  forbidOnly: true,
  retries: 0,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  reporter: [["list"]],
  use: {
    baseURL: BASE_URL,
    viewport: { width: 1366, height: 768 },
    screenshot: "off",
    trace: "off",
    video: "off",
  },
  webServer: {
    // 预检跑的是**构建产物**（vite build → vite preview），不是 dev server：
    // dev server 的模块热替换会把注入/挂载顺序变成另一回事。
    command: `npm run build && npm run preview -- --port ${PORT} --strictPort --host 127.0.0.1`,
    url: BASE_URL,
    reuseExistingServer: false,
    timeout: 180_000,
    stdout: "pipe",
    stderr: "pipe",
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"], viewport: { width: 1366, height: 768 } },
    },
  ],
});
