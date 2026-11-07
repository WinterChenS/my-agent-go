// 消息表与 `t()`（U00）。
//
// 纪律：
//   - **所有用户可见文案都在 locale catalog 里**，组件只写 key；
//     `scripts/check-ui-contract.mjs` 会拦住散落在组件里的字面量文案。
//   - 文案只在前端发生：Go 侧返回稳定的 `code` + `params`，
//     en-US 下绝不显示 Go 的中文 message（那是后端内部措辞，不是产品文案）。
//   - 用户、模型、工具的原样文本不翻译：它们不是"我们的文案"。
//
// 不引入 i18n 库：V1 只有 zh-CN / en-US 两个语言、一张静态消息表，
// 一个 `t()` 加 `Intl` 足够；多一层依赖只会多一个需要版本对齐的东西。

import zhCN from "./locales/zh-CN.json";
import enUS from "./locales/en-US.json";

export type Locale = "zh-CN" | "en-US";

/** 消息参数：只允许标量，避免把对象塞进文案里。 */
export type MessageParams = Record<string, string | number>;

/** catalog 的嵌套结构（JSON）。 */
export type Catalog = { [key: string]: string | Catalog };

export const DEFAULT_LOCALE: Locale = "zh-CN";
export const SUPPORTED_LOCALES: readonly Locale[] = ["zh-CN", "en-US"] as const;

const catalogs: Record<Locale, Catalog> = {
  "zh-CN": zhCN as Catalog,
  "en-US": enUS as Catalog,
};

/**
 * 当前语言。
 *
 * 放在模块级而不是 React context：`formatError` / `formatBytes` 这类纯函数
 * 在非组件上下文（事件归约、日志、测试）里也要能拿到同一种语言；
 * 组件重渲染负责"切换后立刻生效"。
 */
let activeLocale: Locale = DEFAULT_LOCALE;

export function isLocale(value: unknown): value is Locale {
  return typeof value === "string" && (SUPPORTED_LOCALES as readonly string[]).includes(value);
}

/**
 * 解析初始化语言，顺序：已保存偏好 → 系统语言 → zh-CN。
 *
 * 只认精确的受支持语言；`en-GB` 之类不被猜成 en-US——猜错的后果是
 * 界面语言和用户在设置里选的不一致，而这种不一致很难被当成 bug 报出来。
 */
export function resolveLocale(preference?: string | null, system?: string | null): Locale {
  if (isLocale(preference)) return preference;
  if (isLocale(system)) return system;
  return DEFAULT_LOCALE;
}

export function getLocale(): Locale {
  return activeLocale;
}

export function setLocale(locale: Locale): void {
  if (!isLocale(locale)) return;
  activeLocale = locale;
  if (typeof document !== "undefined") {
    document.documentElement.lang = locale;
  }
}

export function catalogOf(locale: Locale): Catalog {
  const c = catalogs[locale];
  if (!c) {
    // 调用方传了未支持的语言：退回默认语言而不是抛错。
    // 一个缺失的翻译不该让整页白屏。
    return catalogs[DEFAULT_LOCALE];
  }
  return c;
}

/** 把嵌套 catalog 摊平成点分 key（`errors.NOT_FOUND`）。 */
export function flattenKeys(catalog: Catalog, prefix = ""): string[] {
  const out: string[] = [];
  for (const [key, value] of Object.entries(catalog)) {
    const path = prefix ? `${prefix}.${key}` : key;
    if (typeof value === "string") out.push(path);
    else out.push(...flattenKeys(value, path));
  }
  return out;
}

function lookup(locale: Locale, key: string): string | undefined {
  const parts = key.split(".");
  let node: string | Catalog | undefined = catalogOf(locale);
  for (const part of parts) {
    if (typeof node !== "object" || node === null) return undefined;
    node = node[part];
  }
  return typeof node === "string" ? node : undefined;
}

/** 当前语言下是否存在这条消息。 */
export function hasKey(key: string, locale: Locale = activeLocale): boolean {
  return lookup(locale, key) !== undefined;
}

/**
 * 取消息并做 `{param}` 替换。
 *
 * 未知 key 原样返回 key：界面上出现 `errors.FOO` 是一眼可见的 bug 信号，
 * 比显示"操作失败"然后让人查日志好得多。
 * 未提供的参数保留 `{param}` 原样：同样是为了让缺参可见，而不是静默吞掉。
 */
export function t(key: string, params?: MessageParams, locale: Locale = activeLocale): string {
  const template = lookup(locale, key);
  if (template === undefined) {
    // 当前语言缺失时退回默认语言，再不行才返回 key。
    const fallback = lookup(DEFAULT_LOCALE, key);
    if (fallback === undefined) return key;
    return substitute(fallback, params);
  }
  return substitute(template, params);
}

function substitute(template: string, params?: MessageParams): string {
  if (!params) return template;
  return template.replace(/\{([A-Za-z0-9_]+)\}/g, (whole, name: string) => {
    const v = params[name];
    return v === undefined || v === null ? whole : String(v);
  });
}

/** 跨边界错误的最小形状（与 Go 的 CallError / 事件里的 error 兼容）。 */
export interface ErrorLike {
  code: string;
  message?: string;
  params?: Record<string, string>;
  operation?: string;
}

/**
 * 把结构化错误转成当前语言的文案。
 *
 * 注意**不使用** `error.message`：那是 Go 侧的中文措辞，en-US 下显示中文
 * 是明确禁止的（规格 §5.1）。需要原始细节时由调用方另外展示 `message`
 * （诊断区），文案本身永远来自 catalog。
 */
export function formatError(error: ErrorLike | null | undefined): string {
  if (!error) return "";
  const key = `errors.${error.code}`;
  const params: MessageParams = { ...(error.params ?? {}) };
  if (hasKey(key)) return t(key, params);
  return t("errors._unknown", { code: error.code });
}

/** 没有消息的 Go 错误码（catalog 覆盖缺口的检查入口）。 */
export function missingErrorMessages(codes: readonly string[]): string[] {
  const missing: string[] = [];
  for (const code of codes) {
    for (const locale of SUPPORTED_LOCALES) {
      if (!hasKey(`errors.${code}`, locale)) missing.push(`${locale}:errors.${code}`);
    }
  }
  return missing;
}

/** 两个语言的 key 集合差异（空数组 = 完全一致）。 */
export function localeKeyDiff(a: Locale = "en-US", b: Locale = "zh-CN"): string[] {
  const ka = new Set(flattenKeys(catalogOf(a)));
  const kb = new Set(flattenKeys(catalogOf(b)));
  const out: string[] = [];
  for (const k of ka) if (!kb.has(k)) out.push(`${a} has only ${k}`);
  for (const k of kb) if (!ka.has(k)) out.push(`${b} has only ${k}`);
  return out.sort();
}

/**
 * 初始化语言：已保存偏好 → 系统语言 → zh-CN，并写到 <html lang>。
 *
 * 由 main.tsx 在挂载 React 之前调用一次；后续切换语言走 setLocale。
 */
export function initLocale(options?: { preference?: string | null; system?: string | null }): Locale {
  const system =
    options?.system ??
    (typeof navigator !== "undefined" && navigator.language ? navigator.language : null);
  const locale = resolveLocale(options?.preference, system);
  setLocale(locale);
  return locale;
}

