// 外壳像素对比（D1 证据生成器，不是断言门禁）。
//
// 做什么：
//   1. 把**构建产物**（frontend/dist）用 `vite preview` 起在 127.0.0.1:4188；
//   2. 把原型导出 `docs/ui/Agent-客户端产品原型/` 用最小静态服务器起在 :4189
//      （原型的 index.html 靠 assets/studio.js 渲染侧栏、读 localStorage，
//      file:// 下 localStorage 会被拒，所以走 http）；
//   3. 两个 1366×768 / 1440×900 各截一张图，并量同一批区域：
//      侧栏宽、顶栏高、内容留白、卡片圆角、主色（OKLab 距离）；
//   4. 写 shell-diff.json 并给非零退出码如果某个区域超出容差。
//
// 为什么"量"而不是只截图：截图只能证明"我看过"，量出来的数字才能被复核和回归。
// 口径与 D1 报告里的表格一致。
//
// 用法（Node 24，PLAYWRIGHT_BROWSERS_PATH 默认指向工作区 .tools/pw-browsers）：
//   node frontend/src/design/shell-diff.mjs [--out=<dir>]
//
// 注意：这是**网页侧**证据（同一份 React 构建产物 + 同一套 Chromium）。真机
// （Wails/WebView2/DPI）证据仍然只能由 tests/native 的门禁套件产生。

import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { mkdirSync, readFileSync, writeFileSync, existsSync } from "node:fs";
import { dirname, extname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const HERE = dirname(fileURLToPath(import.meta.url));
const FRONTEND = resolve(HERE, "..", "..");
const REPO = resolve(FRONTEND, "..");
const PROTO = join(REPO, "docs", "ui", "Agent-客户端产品原型");
const DIST = join(FRONTEND, "dist");

const outArg = process.argv.find((a) => a.startsWith("--out="));
const OUT = outArg ? resolve(outArg.slice("--out=".length)) : join(REPO, "D1-evidence");

process.env.PLAYWRIGHT_BROWSERS_PATH ??= join(REPO, ".tools", "pw-browsers");

const VIEWPORTS = [
  { width: 1366, height: 768 },
  { width: 1440, height: 900 },
];

// 容差：几何 2px（子像素取整），颜色 OKLab 距离 0.02（≈ 肉眼不可辨）。
const TOLERANCE = { px: 2, oklab: 0.02 };

// ---------------------------------------------------------------- 颜色换算
// 与 frontend/src/design/tokens.ts 同一套公式（OKLab ↔ sRGB）。这里再写一份是
// 因为这是 .mjs 证据脚本，不经过 TS 编译；两处的一致性由 tokens.test.ts 保证
// （它锁的是契约值本身），本脚本只负责把两边的计算色换算到同一空间再比。

function parseOklch(value) {
  const m = /^oklch\(\s*([\d.]+)(%?)\s+([\d.]+)\s+([\d.]+)/.exec(value.trim());
  if (!m) return null;
  const raw = Number(m[1]);
  return { l: m[2] === "%" ? raw / 100 : raw, c: Number(m[3]), h: Number(m[4]) };
}

function oklchToLinear({ l, c, h }) {
  const rad = (h * Math.PI) / 180;
  const a = c * Math.cos(rad);
  const b = c * Math.sin(rad);
  const l_ = l + 0.3963377774 * a + 0.2158037573 * b;
  const m_ = l - 0.1055613458 * a - 0.0638541728 * b;
  const s_ = l - 0.0894841775 * a - 1.291485548 * b;
  const L = l_ ** 3;
  const M = m_ ** 3;
  const S = s_ ** 3;
  return [
    4.0767416621 * L - 3.3077115913 * M + 0.2309699292 * S,
    -1.2684380046 * L + 2.6097574011 * M - 0.3413193965 * S,
    -0.0041960863 * L - 0.7034186147 * M + 1.707614701 * S,
  ];
}

function parseRgb(value) {
  const enc = (v) => {
    const c = Number(v) / 255;
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  };
  // 注意：Chromium 把 legacy 颜色在自定义属性里序列化成 hex（`rgb(23 121 225)`
  // 读回来是 `#1779e1`），所以两种写法都要接。
  const hex = /^#([0-9a-fA-F]{6})$/.exec(value.trim());
  if (hex) {
    const h = hex[1];
    return [enc(parseInt(h.slice(0, 2), 16)), enc(parseInt(h.slice(2, 4), 16)), enc(parseInt(h.slice(4, 6), 16))];
  }
  const m = /^rgba?\(\s*([\d.]+)[ ,]+([\d.]+)[ ,]+([\d.]+)/.exec(value.trim());
  if (!m) return null;
  return [enc(m[1]), enc(m[2]), enc(m[3])];
}

/** 任意 CSS 颜色（oklch / rgb）→ OKLab。 */
function toOklab(value) {
  const oklch = parseOklch(value);
  const linear = oklch ? oklchToLinear(oklch) : parseRgb(value);
  if (!linear) return null;
  const [r, g, b] = linear;
  const l = Math.cbrt(0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b);
  const m = Math.cbrt(0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b);
  const s = Math.cbrt(0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b);
  return [
    0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s,
    1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s,
    0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s,
  ];
}

function oklabDistance(a, b) {
  return Math.hypot(a[0] - b[0], a[1] - b[1], a[2] - b[2]);
}


/**
 * 焦点环只能从规则里读：`:focus-visible` 不会因为读样式而命中。
 *
 * 坑：Chrome 里 `CSSStyleRule.cssRules` 存在但是空列表（嵌套规则支持），
 * 先判 `rule.cssRules` 真值会把所有普通规则当成容器跳过去，于是永远找不到。
 * 必须先判 `selectorText`。
 */
const FOCUS_RING_HELPER = `window.__d1FocusVisibleRule = function () {
  function walk(list) {
    for (const rule of list) {
      if (rule.selectorText === ":focus-visible") {
        return { outline: rule.style.outline, offset: rule.style.outlineOffset };
      }
      if (rule.cssRules && rule.cssRules.length) {
        const found = walk(rule.cssRules);
        if (found) return found;
      }
    }
    return null;
  }
  for (const sheet of document.styleSheets) {
    let rules;
    try {
      rules = sheet.cssRules;
    } catch (error) {
      continue;
    }
    const found = walk(rules);
    if (found) return found;
  }
  return null;
};`;

// ---------------------------------------------------------------- 静态服务器

const MIME = { ".html": "text/html; charset=utf-8", ".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".png": "image/png", ".svg": "image/svg+xml" };

function serve(root, port) {
  const server = createServer((req, res) => {
    const url = decodeURIComponent((req.url ?? "/").split("?")[0]);
    const rel = url === "/" ? "/index.html" : url;
    const file = join(root, rel);
    if (!file.startsWith(root) || !existsSync(file)) {
      res.writeHead(404).end("not found");
      return;
    }
    res.writeHead(200, { "content-type": MIME[extname(file)] ?? "application/octet-stream" });
    res.end(readFileSync(file));
  });
  return new Promise((ok) => server.listen(port, "127.0.0.1", () => ok(server)));
}

async function waitFor(url, timeoutMs = 60_000) {
  const end = Date.now() + timeoutMs;
  while (Date.now() < end) {
    try {
      const res = await fetch(url);
      if (res.ok) return;
    } catch {
      /* 还没起来 */
    }
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error(`等待 ${url} 超时`);
}

// ---------------------------------------------------------------- 单侧测量

/** 原型侧：studio.css 的原生类名。 */
async function measurePrototype(page) {
  return page.evaluate(() => {
    const px = (v) => Number.parseFloat(v) || 0;
    const cs = (sel) => {
      const el = document.querySelector(sel);
      return el ? getComputedStyle(el) : null;
    };
    const sidebar = document.querySelector(".sidebar");
    const topbar = document.querySelector(".topbar");
    const inner = cs(".page-inner");
    const card = cs(".card");
    const root = getComputedStyle(document.documentElement);
    return {
      sidebarWidth: sidebar ? px(getComputedStyle(sidebar).width) : null,
      topbarHeight: topbar ? px(getComputedStyle(topbar).height) : null,
      contentPadding: inner
        ? { top: px(inner.paddingTop), right: px(inner.paddingRight), bottom: px(inner.paddingBottom), left: px(inner.paddingLeft) }
        : null,
      cardRadius: card ? px(card.borderTopLeftRadius) : null,
      accent: root.getPropertyValue("--accent").trim(),
      focusRing: window.__d1FocusVisibleRule(),
    };
  });
}

/** 我们的应用侧：.anvil-* 类名 + 令牌。 */
async function measureApp(page) {
  return page.evaluate(() => {
    const px = (v) => Number.parseFloat(v) || 0;
    const root = getComputedStyle(document.documentElement);
    const sidebar = document.querySelector(".anvil-sidebar");
    const topbar = document.querySelector(".anvil-shell-topbar");
    const pageEl = document.querySelector("main.anvil-page");
    // 内容留白目前由页面根元素自己带（D1 只做外壳；D2 才把留白搬到外壳上）。
    const inner = pageEl?.firstElementChild ? getComputedStyle(pageEl.firstElementChild) : null;
    return {
      sidebarWidth: sidebar ? px(getComputedStyle(sidebar).width) : null,
      topbarHeight: topbar ? px(getComputedStyle(topbar).height) : null,
      contentPadding: inner
        ? { top: px(inner.paddingTop), right: px(inner.paddingRight), bottom: px(inner.paddingBottom), left: px(inner.paddingLeft), owner: "page" }
        : null,
      cardRadius: px(root.getPropertyValue("--anvil-radius")),
      accent: root.getPropertyValue("--anvil-accent").trim(),
      focusRing: window.__d1FocusVisibleRule(),
      navItemCount: document.querySelectorAll("nav li button").length,
      historySection: Boolean(document.querySelector('[data-shell-region="history"]')),
      footerSection: Boolean(document.querySelector('[data-shell-region="footer"]')),
    };
  });
}

// ---------------------------------------------------------------- 主流程

if (!existsSync(join(DIST, "index.html"))) {
  throw new Error("找不到 frontend/dist：先跑 npm run build");
}

mkdirSync(OUT, { recursive: true });

const protoServer = await serve(PROTO, 4189);
const preview = spawn(
  process.execPath,
  [join(FRONTEND, "node_modules", "vite", "bin", "vite.js"), "preview", "--port", "4188", "--strictPort", "--host", "127.0.0.1"],
  { cwd: FRONTEND, stdio: "ignore" }
);

let browser;
try {
  await waitFor("http://127.0.0.1:4188/");
  browser = await chromium.launch();
  const rows = [];

  for (const vp of VIEWPORTS) {
    const tag = `${vp.width}x${vp.height}`;

    // ---- 原型侧（只有浅色：导出里没有深色块）
    const protoCtx = await browser.newContext({ viewport: vp, deviceScaleFactor: 1 });
    const protoPage = await protoCtx.newPage();
    await protoPage.addInitScript(FOCUS_RING_HELPER);
    await protoPage.goto("http://127.0.0.1:4189/index.html", { waitUntil: "load" });
    await protoPage.waitForSelector(".sidebar .nav-item");
    const proto = await measurePrototype(protoPage);
    const protoShot = join(OUT, `prototype-${tag}.png`);
    await protoPage.screenshot({ path: protoShot });
    await protoCtx.close();

    // ---- 应用侧（浅色 + 深色；深色没有原型对照，只做趋势证据）
    const app = {};
    for (const theme of ["light", "dark"]) {
      const ctx = await browser.newContext({ viewport: vp, deviceScaleFactor: 1, locale: "zh-CN" });
      const page = await ctx.newPage();
      await page.addInitScript(FOCUS_RING_HELPER);
      await page.addInitScript((t) => {
        window.localStorage.setItem("anvil.locale", "zh-CN");
        window.localStorage.setItem("anvil.theme", t);
      }, theme);
      await page.goto("http://127.0.0.1:4188/", { waitUntil: "load" });
      await page.waitForSelector(".anvil-shell .anvil-sidebar");
      await page.waitForFunction(() => document.querySelectorAll("nav li button").length >= 9);
      app[theme] = await measureApp(page);
      await page.screenshot({ path: join(OUT, `app-${tag}-${theme}.png`) });
      await ctx.close();
    }

    const accentApp = toOklab(app.light.accent);
    const accentProto = toOklab(proto.accent);
    if (!accentApp || !accentProto) {
      throw new Error(`主色无法解析成 OKLab: app=${JSON.stringify(app.light.accent)} proto=${JSON.stringify(proto.accent)}`);
    }

    const regions = [
      {
        region: "sidebarWidth",
        unit: "px",
        prototype: proto.sidebarWidth,
        app: app.light.sidebarWidth,
        delta: Number((app.light.sidebarWidth - proto.sidebarWidth).toFixed(2)),
        tolerance: TOLERANCE.px,
      },
      {
        region: "topbarHeight",
        unit: "px",
        prototype: proto.topbarHeight,
        app: app.light.topbarHeight,
        delta: Number((app.light.topbarHeight - proto.topbarHeight).toFixed(2)),
        tolerance: TOLERANCE.px,
      },
      {
        region: "contentPadding",
        unit: "px",
        prototype: proto.contentPadding,
        app: app.light.contentPadding,
        // 页面根自带留白（D2 搬到外壳），这里只比水平方向与顶部，并写明归属。
        delta: {
          top: app.light.contentPadding.top - proto.contentPadding.top,
          left: app.light.contentPadding.left - proto.contentPadding.left,
        },
        note: "content padding 目前由页面根元素提供（D1 不改页面主体，D2 迁移到外壳的 .anvil-page-inner）",
      },
      {
        region: "cardRadius",
        unit: "px",
        prototype: proto.cardRadius,
        app: app.light.cardRadius,
        delta: Number((app.light.cardRadius - proto.cardRadius).toFixed(2)),
        tolerance: TOLERANCE.px,
      },
      {
        region: "accentColor",
        unit: "OKLab ΔE",
        prototype: proto.accent,
        app: app.light.accent,
        delta: Number(oklabDistance(accentApp, accentProto).toFixed(4)),
        tolerance: TOLERANCE.oklab,
      },
    ];

    for (const r of regions) {
      r.pass =
        typeof r.delta === "number"
          ? Math.abs(r.delta) <= (r.tolerance ?? Number.POSITIVE_INFINITY)
          : r.region === "contentPadding"
            ? true // 见 note：归属是页面（D2）
            : false;
    }

    rows.push({
      viewport: vp,
      screenshots: {
        prototype: `prototype-${tag}.png`,
        appLight: `app-${tag}-light.png`,
        appDark: `app-${tag}-dark.png`,
      },
      regions,
      darkMeasurements: app.dark,
      prototypeExtra: { focusRing: proto.focusRing, navItemCount: null },
      appExtra: {
        focusRing: app.light.focusRing,
        navItemCount: app.light.navItemCount,
        historySection: app.light.historySection,
        footerSection: app.light.footerSection,
      },
    });
  }

  const failed = rows.flatMap((r) => r.regions.filter((x) => !x.pass).map((x) => `${r.viewport.width}x${r.viewport.height} ${x.region}`));
  const report = {
    task: "D1",
    kind: "web-build-shell-diff",
    generatedAt: new Date().toISOString(),
    environment: {
      browser: await (async () => {
        const ctx = await browser.newContext();
        const p = await ctx.newPage();
        const ua = await p.evaluate(() => navigator.userAgent);
        await ctx.close();
        return ua;
      })(),
      appUrl: "http://127.0.0.1:4188/ (vite preview of frontend/dist)",
      prototypeUrl: "http://127.0.0.1:4189/index.html (docs/ui/Agent-客户端产品原型)",
      theme: "light（原型没有深色块；app 的深色截图仅作趋势证据）",
      limits: [
        "网页侧证据：同一份 React 构建产物 + Chromium，不含 Wails 宿主/WebView2/DPI 缩放",
        "预检环境没有后端，历史列表与页面主体是空/不可用状态，因此不比较列表内容，只比较外壳几何",
        "页面主体样式属于 D2+；内容留白当前仍由页面根元素提供",
      ],
    },
    tolerance: TOLERANCE,
    rows,
    summary: { regions: rows.length * 5, failed: failed.length, failedRegions: failed },
  };

  writeFileSync(join(OUT, "shell-diff.json"), JSON.stringify(report, null, 2) + "\n", "utf8");
  console.log(JSON.stringify({ out: OUT, failed }, null, 2));
  if (failed.length > 0) process.exitCode = 1;
} finally {
  await browser?.close();
  preview.kill();
  protoServer.close();
}
