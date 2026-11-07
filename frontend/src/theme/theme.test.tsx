// 主题解析与应用的测试（U00）。
//
// 守三件事：
//  1. system / light / dark 的解析结果只取决于偏好与系统状态；
//  2. 已保存偏好优先于系统偏好（用户显式选浅色，系统切深色也不翻）；
//  3. 系统配色变化**不重载**就生效（订阅媒体查询，而不是启动时读一次）。

import { describe, expect, it } from "vitest";
import {
  applyTheme,
  initTheme,
  isThemePreference,
  loadThemePreference,
  resolveTheme,
  storeThemePreference,
  THEME_STORAGE_KEY,
  type ThemePreference,
} from "./theme";

interface FakeMedia {
  mql: MediaQueryList;
  setDark(dark: boolean): void;
  listeners(): number;
}

function makeMedia(initialDark: boolean): FakeMedia {
  const set = new Set<(ev: MediaQueryListEvent) => void>();
  const mql = {
    matches: initialDark,
    media: "(prefers-color-scheme: dark)",
    onchange: null,
    addEventListener: (_type: string, cb: (ev: MediaQueryListEvent) => void) => set.add(cb),
    removeEventListener: (_type: string, cb: (ev: MediaQueryListEvent) => void) => set.delete(cb),
    addListener: (cb: (ev: MediaQueryListEvent) => void) => set.add(cb),
    removeListener: (cb: (ev: MediaQueryListEvent) => void) => set.delete(cb),
    dispatchEvent: () => true,
  } as unknown as MediaQueryList;
  return {
    mql,
    setDark(dark: boolean) {
      (mql as unknown as { matches: boolean }).matches = dark;
      for (const cb of [...set]) cb({ matches: dark } as MediaQueryListEvent);
    },
    listeners: () => set.size,
  };
}

function fakeStorage(initial: Record<string, string> = {}) {
  const map = new Map<string, string>(Object.entries(initial));
  return {
    map,
    getItem: (key: string) => map.get(key) ?? null,
    setItem: (key: string, value: string) => {
      map.set(key, value);
    },
  };
}

const themeOf = () => document.documentElement.dataset.theme;

describe("theme resolution", () => {
  it("resolves system/light/dark", () => {
    expect(resolveTheme("system", true)).toBe("dark");
    expect(resolveTheme("system", false)).toBe("light");
    expect(resolveTheme("light", true)).toBe("light");
    expect(resolveTheme("dark", false)).toBe("dark");
  });

  it("applies only light/dark to the DOM, never 'system'", () => {
    const media = makeMedia(true);
    expect(applyTheme("system", media.mql)).toBe("dark");
    expect(themeOf()).toBe("dark");
    // 主题会同时写 color-scheme：滚动条/原生控件也随之切换。
    expect(document.documentElement.style.colorScheme).toBe("dark");

    expect(applyTheme("light", media.mql)).toBe("light");
    expect(themeOf()).toBe("light");
    expect(document.documentElement.style.colorScheme).toBe("light");
  });

  it("treats a missing media query as light", () => {
    expect(applyTheme("system", null)).toBe("light");
    expect(themeOf()).toBe("light");
  });
});

describe("theme preference persistence", () => {
  it("stores and validates the preference", () => {
    const storage = fakeStorage();
    storeThemePreference("dark", storage);
    expect(storage.map.get(THEME_STORAGE_KEY)).toBe("dark");
    expect(loadThemePreference(storage)).toBe("dark");

    storeThemePreference("nonsense" as ThemePreference, storage);
    // 非法值不落盘，也不会被读成"某种主题"。
    expect(isThemePreference("nonsense")).toBe(false);
    expect(loadThemePreference(storage)).toBe("dark");
  });

  it("falls back to system when storage is missing or throws", () => {
    expect(loadThemePreference(null)).toBe("system");
    const throwing = {
      getItem: () => {
        throw new Error("localStorage disabled");
      },
    };
    // 隐私模式下 localStorage 会抛：退回 system，而不是让首屏崩掉。
    expect(loadThemePreference(throwing)).toBe("system");
  });

  it("lets the persisted preference win over the system", () => {
    const media = makeMedia(true); // 系统深色
    const storage = fakeStorage({ [THEME_STORAGE_KEY]: "light" });
    const handle = initTheme({ media: media.mql, storage });
    expect(handle.preference).toBe("light");
    expect(themeOf()).toBe("light");
    handle.dispose();
  });
});

describe("system preference changes", () => {
  it("applies prefers-color-scheme changes without a reload", () => {
    const media = makeMedia(false);
    const handle = initTheme({ media: media.mql, preference: "system", storage: fakeStorage() });
    expect(themeOf()).toBe("light");

    media.setDark(true);
    expect(themeOf()).toBe("dark");
    expect(handle.resolved).toBe("dark");

    media.setDark(false);
    expect(themeOf()).toBe("light");
    handle.dispose();
    expect(media.listeners()).toBe(0);
  });

  it("ignores system changes when the user picked a theme explicitly", () => {
    const media = makeMedia(false);
    const handle = initTheme({ media: media.mql, preference: "light", storage: fakeStorage() });
    media.setDark(true);
    // 显式选择不该被系统覆盖——那会让"我明明选了浅色"变成无法解释的现象。
    expect(themeOf()).toBe("light");

    handle.setPreference("system");
    expect(themeOf()).toBe("dark");
    handle.dispose();
  });

  it("persists preference changes made at runtime", () => {
    const storage = fakeStorage();
    const media = makeMedia(false);
    const handle = initTheme({ media: media.mql, preference: "system", storage });
    handle.setPreference("dark");
    expect(storage.map.get(THEME_STORAGE_KEY)).toBe("dark");
    expect(themeOf()).toBe("dark");
    handle.dispose();
  });
});
