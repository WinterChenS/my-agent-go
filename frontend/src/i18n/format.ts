// 数值/日期/体积/费用的本地化格式（U00）。
//
// 只用 `Intl`，不加依赖。四个函数都接受显式 locale，默认取当前语言——
// 显式传参是为了让"这个数字按哪种语言格式化"在代码里可读，
// 也让测试不必依赖全局状态。

import { getLocale, t, type Locale } from "./catalog";

type DateInput = Date | string | number;

function toDate(value: DateInput): Date | null {
  const d = value instanceof Date ? value : new Date(value);
  return Number.isNaN(d.getTime()) ? null : d;
}

/**
 * 日期时间。
 *
 * 解析失败返回空串而不是 "Invalid Date"：界面上出现 "Invalid Date" 是
 * 内部错误泄漏，用户既看不懂也不知道该怎么办。
 */
export function formatDate(
  value: DateInput,
  options?: Intl.DateTimeFormatOptions,
  locale: Locale = getLocale()
): string {
  const d = toDate(value);
  if (!d) return "";
  return new Intl.DateTimeFormat(locale, options ?? { dateStyle: "medium", timeStyle: "short" }).format(d);
}

/** 纯日期（不含时间）。 */
export function formatDay(
  value: DateInput,
  locale: Locale = getLocale()
): string {
  const d = toDate(value);
  if (!d) return "";
  return new Intl.DateTimeFormat(locale, { dateStyle: "medium" }).format(d);
}

export function formatNumber(
  value: number,
  options?: Intl.NumberFormatOptions,
  locale: Locale = getLocale()
): string {
  if (!Number.isFinite(value)) return "";
  return new Intl.NumberFormat(locale, options).format(value);
}

const BYTE_UNITS = ["byte", "kilobyte", "megabyte", "gigabyte", "terabyte"] as const;

/**
 * 字节数（1024 进制）。
 *
 * 单位符号来自 catalog（`units.*`），数字按当前语言格式化：
 * 直接拼 "1.5 GB" 会在不同语言下丢掉千分位/小数点差异。
 */
export function formatBytes(bytes: number, locale: Locale = getLocale()): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "";
  let value = bytes;
  let unitIndex = 0;
  while (value >= 1024 && unitIndex < BYTE_UNITS.length - 1) {
    value /= 1024;
    unitIndex += 1;
  }
  const digits = unitIndex === 0 ? 0 : value < 10 ? 1 : 0;
  const formatted = new Intl.NumberFormat(locale, {
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  }).format(value);
  return t("format.bytes", { value: formatted, unit: t(`units.${BYTE_UNITS[unitIndex]}`, undefined, locale) }, locale);
}

/**
 * 费用。
 *
 * 金额与币种由后端给出（模型 usage 是 provider 的计价口径），
 * 这里只负责按语言格式化；币种缺失时不猜——直接按普通数字显示，
 * 不编一个"$"出来。
 */
export function formatCost(
  amount: number,
  currency?: string | null,
  locale: Locale = getLocale()
): string {
  if (!Number.isFinite(amount)) return "";
  if (!currency) {
    return t("format.cost", { value: formatNumber(amount, { maximumFractionDigits: 4 }, locale) }, locale);
  }
  try {
    return new Intl.NumberFormat(locale, {
      style: "currency",
      currency,
      maximumFractionDigits: 4,
    }).format(amount);
  } catch {
    // 非法币种代码（例如上游给了空串或占位符）：退回普通数字，不抛错。
    return formatNumber(amount, { maximumFractionDigits: 4 }, locale);
  }
}

/** 相对时长（例如 "3 分钟"）：用于运行耗时展示。 */
export function formatDuration(ms: number, locale: Locale = getLocale()): string {
  if (!Number.isFinite(ms) || ms < 0) return "";
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) {
    return formatNumber(seconds, { style: "unit", unit: "second", unitDisplay: "short" }, locale);
  }
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) {
    return formatNumber(minutes, { style: "unit", unit: "minute", unitDisplay: "short" }, locale);
  }
  const hours = Math.round(minutes / 60);
  return formatNumber(hours, { style: "unit", unit: "hour", unitDisplay: "short" }, locale);
}
