// 设计令牌的唯一来源（D1）。
//
// 为什么要有这个文件：`docs/ui/Agent-客户端产品原型/` 是**视觉契约**（DESIGN-HANDOFF.md
// 的原话："Treat this handoff as a visual contract"）。契约里的颜色是 OKLCH，写在
// `assets/studio.css` 的 `:root` 里。令牌抄错一个数字，整个产品就"不像原型"，
// 而这种偏差没有任何测试会自己发现——所以令牌必须有一份可被解析、可被比对的来源。
//
// 本文件保存**逐字的原型值**（`PROTO_LIGHT`，字符串与 studio.css 完全一致）。
// `frontend/src/index.css` 是它的投影：同一批颜色，序列化成 sRGB。
//
// 为什么投影成 sRGB 而不是直接把 oklch() 写进 CSS：
//   `tests/native/uiaudit.js`（U07 门禁，本任务只读、不可改）用 `parseColor()` 解析
//   `--anvil-*` 与元素计算色，只认 hex / rgb() / rgba()；Chromium 对 oklch() 的
//   计算值序列化成 `oklch(...)`，于是 tokenContrast() 会给出 ratio=null，
//   G3 的对比度断言直接报"无法解析颜色"（实测：见 D1 报告）。换句话说，
//   把 oklch() 直接写进 CSS 会让**对比度门禁整条失效**，那不是 token 问题而是门禁问题，
//   但修门禁属于 `tests/**`（不属于本任务）。所以：契约值保持 OKLCH 逐字，
//   投影层是它的精确 sRGB 等价物，两侧由 `tokens.test.ts` 双向锁定（改任一侧即红）。
//
// 深色：原型**没有**深色块（studio.css 只有一份 `:root`，全文无 prefers-color-scheme /
// data-theme），而产品必须有浅色与深色两套。`PROTO_DARK` 因此是推导值：
// 色相（H）与彩度（C）逐字保留原型的值，只有明度（L）取深色锚点；锚点表在下面显式列出，
// `tokens.test.ts` 会校验：① 深色 token 的 C/H 与浅色一致；② L 等于锚点表；
// ③ 明度序（bg < surface-2 < surface-3 < border < border-strong < muted < fg-2 < fg）
// 与浅色一致；④ 关键前景/背景对达到 WCAG AA。原型将来若真的补了深色块，
// 测试会红（要求把这批锚点换成契约值），而不是让推导值悄悄继续生效。

/** 原型 studio.css `:root` 里的颜色令牌名（去掉 `--` 前缀）。 */
export type ProtoColorName =
  | "bg"
  | "surface"
  | "surface-2"
  | "surface-3"
  | "fg"
  | "fg-2"
  | "muted"
  | "border"
  | "border-strong"
  | "accent"
  | "accent-hover"
  | "accent-soft"
  | "on-accent"
  | "success"
  | "success-soft"
  | "warn"
  | "warn-soft"
  | "danger"
  | "danger-soft";

export type ThemeMode = "light" | "dark";

/**
 * 原型 `:root` 的颜色值，逐字抄自 `docs/ui/Agent-客户端产品原型/assets/studio.css`
 * 第 8–27 行。不要在这里"顺手优化"任何数字：它就是契约。
 */
export const PROTO_LIGHT: Record<ProtoColorName, string> = {
  bg: "oklch(99% 0.002 240)",
  surface: "oklch(100% 0 0)",
  "surface-2": "oklch(97.4% 0.004 250)",
  "surface-3": "oklch(95.6% 0.005 250)",
  fg: "oklch(18% 0.012 250)",
  "fg-2": "oklch(33% 0.012 250)",
  muted: "oklch(54% 0.012 250)",
  border: "oklch(92% 0.005 250)",
  "border-strong": "oklch(85.5% 0.007 250)",
  accent: "oklch(58% 0.18 255)",
  "accent-hover": "oklch(52% 0.18 255)",
  "accent-soft": "oklch(96% 0.02 255)",
  "on-accent": "oklch(99% 0 0)",
  success: "oklch(56% 0.13 152)",
  "success-soft": "oklch(96.5% 0.03 152)",
  warn: "oklch(58% 0.12 72)",
  "warn-soft": "oklch(97% 0.04 78)",
  danger: "oklch(55% 0.17 25)",
  "danger-soft": "oklch(96.5% 0.025 25)",
};

/**
 * 深色锚点：只列明度（L%），彩度与色相沿用 `PROTO_LIGHT` 的同名 token。
 * 这些数字是"深色版该有的明度层级"，不是原型的值——原型没有深色块。
 */
export const DARK_LIGHTNESS: Record<ProtoColorName, number> = {
  bg: 14,
  surface: 18,
  "surface-2": 15.5,
  "surface-3": 22,
  fg: 95,
  "fg-2": 82,
  muted: 66,
  border: 27,
  "border-strong": 35,
  accent: 68,
  "accent-hover": 74,
  "accent-soft": 26,
  "on-accent": 99,
  success: 72,
  "success-soft": 24,
  warn: 76,
  "warn-soft": 26,
  danger: 70,
  "danger-soft": 25,
};

export interface Oklch {
  /** 明度，0–100（与 CSS 的百分比写法同一个尺度）。 */
  l: number;
  /** 彩度，0–0.4。 */
  c: number;
  /** 色相，度。 */
  h: number;
}

/** 解析 `oklch(L% C H)`（也接受 `oklch(L C H)` 里 L 用 0–1 小数）。 */
export function parseOklch(value: string): Oklch {
  const m = /^oklch\(\s*([\d.]+)(%?)\s+([\d.]+)\s+([\d.]+)\s*\)$/.exec(value.trim());
  if (!m) throw new Error(`not a parsable oklch value: ${value}`);
  const raw = Number(m[1]);
  const l = m[2] === "%" ? raw : raw * 100;
  return { l, c: Number(m[3]), h: Number(m[4]) };
}

/** 深色 token 的 OKLCH 值：C/H 沿用原型，L 取锚点。 */
export function darkOklch(name: ProtoColorName): string {
  const light = parseOklch(PROTO_LIGHT[name]);
  return `oklch(${DARK_LIGHTNESS[name]}% ${light.c} ${light.h})`;
}

/** 某个 token 在某个主题下的 OKLCH 值（浅色逐字取自原型）。 */
export function oklchOf(name: ProtoColorName, theme: ThemeMode): string {
  return theme === "light" ? PROTO_LIGHT[name] : darkOklch(name);
}

export interface Rgb {
  r: number;
  g: number;
  b: number;
}

/**
 * OKLCH → sRGB（8 位）。转换公式来自 CSS Color 4（OKLab → linear sRGB 矩阵），
 * 越界通道按 CSS 的默认行为裁剪到 [0,255]。投影结果写进 index.css，
 * 由 `tokens.test.ts` 反向比对。
 */
export function oklchToSrgb(color: Oklch): Rgb {
  const { l, c, h } = color;
  const rad = (h * Math.PI) / 180;
  const a = c * Math.cos(rad);
  const b = c * Math.sin(rad);
  const l_ = l / 100 + 0.3963377774 * a + 0.2158037573 * b;
  const m_ = l / 100 - 0.1055613458 * a - 0.0638541728 * b;
  const s_ = l / 100 - 0.0894841775 * a - 1.291485548 * b;
  const lc = l_ ** 3;
  const mc = m_ ** 3;
  const sc = s_ ** 3;
  const linear: [number, number, number] = [
    4.0767416621 * lc - 3.3077115913 * mc + 0.2309699292 * sc,
    -1.2684380046 * lc + 2.6097574011 * mc - 0.3413193965 * sc,
    -0.0041960863 * lc - 0.7034186147 * mc + 1.707614701 * sc,
  ];
  const encode = (v: number) => {
    const clamped = Math.min(1, Math.max(0, v));
    const gamma = clamped <= 0.0031308 ? 12.92 * clamped : 1.055 * clamped ** (1 / 2.4) - 0.055;
    return Math.round(Math.min(1, Math.max(0, gamma)) * 255);
  };
  return { r: encode(linear[0]), g: encode(linear[1]), b: encode(linear[2]) };
}

/** 某个 token 在某个主题下的 sRGB 三元组（index.css 的投影值）。 */
export function srgbOf(name: ProtoColorName, theme: ThemeMode): Rgb {
  return oklchToSrgb(parseOklch(oklchOf(name, theme)));
}

/**
 * 我们自己消费的 `--anvil-*` 名字 → 原型 token 名。
 *
 * 为什么不是一一对应：`--anvil-*` 是 F3/U00 已经铺进组件的语义层
 * （Tailwind 的 `--color-*` 也接着它们），改名会让每个页面重写一遍 class。
 * 所以语义名保留，值一律来自原型：`--anvil-surface` 指的是"次级面/侧栏面"，
 * 对应原型的 `--surface-2`；卡片面是 `--anvil-surface-raised`，对应 `--surface`。
 */
export const COLOR_ALIASES: Record<string, ProtoColorName> = {
  "anvil-background": "bg",
  "anvil-surface": "surface-2",
  "anvil-surface-2": "surface-2",
  "anvil-surface-raised": "surface",
  "anvil-surface-3": "surface-3",
  "anvil-foreground": "fg",
  "anvil-fg": "fg",
  "anvil-fg-2": "fg-2",
  "anvil-muted": "muted",
  "anvil-border": "border",
  "anvil-border-strong": "border-strong",
  "anvil-accent": "accent",
  "anvil-accent-hover": "accent-hover",
  "anvil-accent-soft": "accent-soft",
  "anvil-accent-surface": "accent-soft",
  "anvil-on-accent": "on-accent",
  "anvil-success": "success",
  "anvil-success-soft": "success-soft",
  "anvil-success-surface": "success-soft",
  "anvil-warning": "warn",
  "anvil-warn": "warn",
  "anvil-warn-soft": "warn-soft",
  "anvil-warning-surface": "warn-soft",
  "anvil-danger": "danger",
  "anvil-danger-soft": "danger-soft",
  "anvil-danger-surface": "danger-soft",
  "anvil-focus": "accent",
};

/**
 * 由原型公式派生、原型没有单独定义的颜色变量。
 *
 * 这些名字 F3 已经在用（`--anvil-*-border` 是徽标描边、`--anvil-overlay` 是遮罩），
 * 值不另立数字，而是照原型的 `color-mix(...)` 写法算出来：徽标描边 22–26%、
 * 遮罩是前景色的半透明混合。这样"换主题"时它们跟着变，不会变成第二套调色板。
 */
export const DERIVED_COLORS: Record<
  string,
  { from: string; opacity: number }
> = {
  // 徽标描边：原型 `.badge-*` 的 22/24/26% 混合。
  "anvil-accent-border": { from: "anvil-accent", opacity: 22 },
  "anvil-success-border": { from: "anvil-success", opacity: 24 },
  "anvil-warning-border": { from: "anvil-warning", opacity: 26 },
  "anvil-danger-border": { from: "anvil-danger", opacity: 24 },
  // 遮罩：浮层背后的压暗层，比例来自原型浮层遮罩的写法（前景色半透明）。
  "anvil-overlay": { from: "anvil-foreground", opacity: 45 },
};

/** 字体栈：逐字取自原型的三个 `--font-*`。 */
export const FONT_STACKS = {
  display:
    '-apple-system, BlinkMacSystemFont, "SF Pro Display", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", system-ui, sans-serif',
  body:
    '-apple-system, BlinkMacSystemFont, "SF Pro Text", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", system-ui, sans-serif',
  mono: '"SF Mono", "JetBrains Mono", "IBM Plex Mono", ui-monospace, Menlo, monospace',
} as const;

/** 圆角与几何：逐字取自原型的 `--r-*` / `--sidebar-w` / `.topbar` / `.page-inner`。 */
export const GEOMETRY = {
  "r-xs": "4px",
  "r-sm": "6px",
  "r-md": "8px",
  "r-lg": "12px",
  "sidebar-w": "236px",
  "topbar-h": "52px",
  "topbar-pad-x": "20px",
  "sidebar-pad-x": "10px",
  "sidebar-pad-y": "14px",
  "page-max": "1240px",
  "page-pad-y": "26px",
  "page-pad-x": "28px",
  "page-pad-b": "56px",
  "space-unit": "4px",
} as const;

/** 动效时长：原型用 .12s/.13s/.14s/.16s/.2s，归成三档。 */
export const MOTION = {
  fast: "120ms",
  base: "140ms",
  slow: "200ms",
  ease: "ease",
} as const;

/** WCAG 相对亮度（sRGB，8 位输入）。 */
export function relativeLuminance(rgb: Rgb): number {
  const channel = (value: number) => {
    const v = value / 255;
    return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * channel(rgb.r) + 0.7152 * channel(rgb.g) + 0.0722 * channel(rgb.b);
}

/** WCAG 对比度。 */
export function contrastRatio(a: Rgb, b: Rgb): number {
  const la = relativeLuminance(a);
  const lb = relativeLuminance(b);
  const hi = Math.max(la, lb);
  const lo = Math.min(la, lb);
  return (hi + 0.05) / (lo + 0.05);
}

/**
 * 门禁口径需要达到 AA 的前景/背景对（与 `tests/native/uiaudit.js` 的 tokenContrast()
 * 与 `ui_acceptance_test.go` 的阈值一致：正文/次级文字 4.5，其余语义色 3.0）。
 */
export const CONTRAST_PAIRS: ReadonlyArray<{
  fg: ProtoColorName;
  bg: ProtoColorName;
  min: number;
}> = [
  { fg: "fg", bg: "bg", min: 4.5 },
  { fg: "fg", bg: "surface", min: 4.5 },
  { fg: "fg", bg: "surface-2", min: 4.5 },
  { fg: "muted", bg: "bg", min: 4.5 },
  { fg: "muted", bg: "surface", min: 4.5 },
  { fg: "muted", bg: "surface-2", min: 4.5 },
  { fg: "accent", bg: "bg", min: 3 },
  { fg: "success", bg: "bg", min: 3 },
  { fg: "warn", bg: "bg", min: 3 },
  { fg: "danger", bg: "bg", min: 3 },
];
