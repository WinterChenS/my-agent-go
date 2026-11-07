// 主题解析与应用（U00）。
//
// 三条规则：
//  1. "跟随系统"只是一个选择方式，不是第三种配色：真正的 DOM 上永远只有
//     `data-theme="light"` 或 `"dark"`，颜色 token 只写两套。
//  2. 已保存的偏好优先于系统偏好——用户显式选过浅色，系统切成深色也不该翻过来。
//  3. 系统配色变化必须**不重载**就生效：媒体查询订阅而不是启动时读一次。
//
// 初始化在 React 挂载之前完成（main.tsx 调用 initTheme），否则深色系统下
// 会先闪一帧白底——那是纯视觉问题，但会让人以为界面在抖动。

export type ThemePreference = "system" | "light" | "dark";
export type ResolvedTheme = "light" | "dark";

export const THEME_STORAGE_KEY = "anvil.theme";
export const SUPPORTED_THEMES: readonly ThemePreference[] = ["system", "light", "dark"] as const;

export function isThemePreference(value: unknown): value is ThemePreference {
  return typeof value === "string" && (SUPPORTED_THEMES as readonly string[]).includes(value);
}

/** 系统当前是否深色。media 为 null（无 window，如部分测试环境）时按浅色处理。 */
export function systemPrefersDark(media: MediaQueryList | null): boolean {
  return Boolean(media?.matches);
}

/** 把"偏好 + 系统状态"解析成真正生效的主题。 */
export function resolveTheme(pref: ThemePreference, systemDark: boolean): ResolvedTheme {
  if (pref === "light" || pref === "dark") return pref;
  return systemDark ? "dark" : "light";
}

/**
 * 把主题写到 DOM 上。
 *
 * 同时设置 `data-theme`（给 Tailwind/语义 token 用）与 `color-scheme`
 * （让滚动条、表单控件、原生弹窗跟随，而不是只有自己画的元素变深）。
 */
export function applyTheme(pref: ThemePreference, media: MediaQueryList | null): ResolvedTheme {
  const resolved = resolveTheme(pref, systemPrefersDark(media));
  if (typeof document !== "undefined") {
    document.documentElement.dataset.theme = resolved;
    document.documentElement.style.colorScheme = resolved;
  }
  return resolved;
}

/** 读取已保存的主题偏好；非法/缺失时返回 "system"。 */
export function loadThemePreference(storage?: Pick<Storage, "getItem"> | null): ThemePreference {
  const store = storage ?? safeLocalStorage();
  if (!store) return "system";
  try {
    const raw = store.getItem(THEME_STORAGE_KEY);
    return isThemePreference(raw) ? raw : "system";
  } catch {
    // 隐私模式/被禁用时 localStorage 会抛：退回 system 而不是让首屏崩掉。
    return "system";
  }
}

/** 保存主题偏好（只作为启动时的本地镜像；权威值在 Go 的 Preferences 里）。 */
export function storeThemePreference(
  pref: ThemePreference,
  storage?: Pick<Storage, "setItem"> | null
): void {
  // 非法值不落盘：写进 localStorage 的垃圾会在下次启动被读出来，
  // 变成"主题莫名其妙变成默认"的隐性问题。
  if (!isThemePreference(pref)) return;
  const store = storage ?? safeLocalStorage();
  if (!store) return;
  try {
    store.setItem(THEME_STORAGE_KEY, pref);
  } catch {
    // 存不下不影响本次会话的主题切换。
  }
}

function safeLocalStorage(): Storage | null {
  try {
    if (typeof window === "undefined") return null;
    return window.localStorage;
  } catch {
    return null;
  }
}

export function currentMedia(): MediaQueryList | null {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return null;
  try {
    return window.matchMedia("(prefers-color-scheme: dark)");
  } catch {
    return null;
  }
}

export interface ThemeHandle {
  preference: ThemePreference;
  resolved: ResolvedTheme;
  /** 切换偏好（会同时落本地镜像并应用）。 */
  setPreference(next: ThemePreference): void;
  /** 取消系统配色订阅。 */
  dispose(): void;
}

/**
 * 初始化主题：应用已保存偏好，并订阅系统配色变化。
 *
 * 返回的 handle 交给设置页做"即时切换"；dispose 只在测试/重建时调用。
 */
export function initTheme(options?: {
  preference?: ThemePreference;
  media?: MediaQueryList | null;
  storage?: Pick<Storage, "getItem" | "setItem"> | null;
}): ThemeHandle {
  const media = options?.media ?? currentMedia();
  let preference = options?.preference ?? loadThemePreference(options?.storage ?? null);
  let resolved = applyTheme(preference, media);

  const onChange = () => {
    // 只有"跟随系统"时才需要响应系统变化：显式选了浅色的用户不该被系统改掉。
    if (preference === "system") resolved = applyTheme(preference, media);
  };

  // addEventListener 在旧 WebView/Safari 上不可用，退回 addListener。
  if (media && typeof media.addEventListener === "function") {
    media.addEventListener("change", onChange);
  } else if (media && typeof media.addListener === "function") {
    media.addListener(onChange);
  }

  return {
    get preference() {
      return preference;
    },
    get resolved() {
      return resolved;
    },
    setPreference(next: ThemePreference) {
      preference = isThemePreference(next) ? next : "system";
      resolved = applyTheme(preference, media);
      storeThemePreference(preference, (options?.storage ?? null) as Pick<Storage, "setItem"> | null);
    },
    dispose() {
      if (media && typeof media.removeEventListener === "function") {
        media.removeEventListener("change", onChange);
      } else if (media && typeof media.removeListener === "function") {
        media.removeListener(onChange);
      }
    },
  };
}
