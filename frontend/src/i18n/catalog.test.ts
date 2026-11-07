// catalog 与 UI 契约的守卫测试（U00）。
//
// 三件事必须一直是绿的：
//  1. 两种语言的 key 集合一模一样（少一条 = 某个语言下会显示 key 本身）；
//  2. **每一个 Go 错误码**都有两种语言的文案（错误码从 errors.go 解析，不手抄）；
//  3. catalog 之外不出现用户可见文案，且 baseline 只在缩小。
//
// 这些断言的价值在于它们会随着代码变化自己更新失败：
// 后端新增一个错误码而前端忘了加文案，这里立刻红，而不是等到英文用户
// 在界面上看到 `errors.SOMETHING` 才发现。

import { afterEach, describe, expect, it } from "vitest";
import zhCN from "./locales/zh-CN.json";
import enUS from "./locales/en-US.json";
import {
  DEFAULT_LOCALE,
  flattenKeys,
  formatError,
  hasKey,
  localeKeyDiff,
  missingErrorMessages,
  resolveLocale,
  setLocale,
  t,
  type Catalog,
} from "./catalog";
import {
  checkAll,
  checkBaseline,
  checkErrorMessages,
  goErrorCodes,
  scanUserCopy,
} from "../../../scripts/check-ui-contract.mjs";

afterEach(() => {
  setLocale(DEFAULT_LOCALE);
});

describe("catalog shape", () => {
  it("keeps locale key sets identical", () => {
    expect(flattenKeys(enUS as Catalog).sort()).toEqual(flattenKeys(zhCN as Catalog).sort());
    expect(localeKeyDiff("en-US", "zh-CN")).toEqual([]);
  });

  it("has no empty message", () => {
    for (const [locale, catalog] of [
      ["zh-CN", zhCN as Catalog],
      ["en-US", enUS as Catalog],
    ] as const) {
      for (const key of flattenKeys(catalog)) {
        expect(t(key, undefined, locale).trim(), `${locale}:${key}`).not.toBe("");
      }
    }
  });

  it("names the product Anvil in both locales", () => {
    // BRAND01 的最底一层：界面标题不能还写着旧名字。
    expect(t("app.name", undefined, "zh-CN")).toBe("Anvil");
    expect(t("app.name", undefined, "en-US")).toBe("Anvil");
  });
});

describe("Go error coverage", () => {
  it("has a message for every Go error code", () => {
    const codes = goErrorCodes();
    // 先证明解析真的读到了东西：空数组会让下面的断言假绿。
    expect(codes.length).toBeGreaterThanOrEqual(30);
    expect(codes).toContain("CONFIG_CONFLICT");
    expect(codes).toContain("INVALID_INPUT");

    expect(missingErrorMessages(codes)).toEqual([]);
    expect(checkErrorMessages(codes)).toEqual([]);
    for (const code of codes) {
      expect(hasKey(`errors.${code}`, "zh-CN"), `zh-CN:${code}`).toBe(true);
      expect(hasKey(`errors.${code}`, "en-US"), `en-US:${code}`).toBe(true);
    }
  });

  it("localizes unknown codes instead of leaking Go's message", () => {
    setLocale("en-US");
    const text = formatError({ code: "SOMETHING_NEW", message: "后端的中文措辞" });
    expect(text).toContain("SOMETHING_NEW");
    // en-US 下不得出现中文：Go 的 message 不属于产品文案。
    expect(/[\u3400-\u9fff]/.test(text)).toBe(false);
  });

  it("substitutes params and keeps unknown placeholders visible", () => {
    setLocale("en-US");
    const text = formatError({
      code: "INVALID_INPUT",
      params: { field: "locale", value: "fr-FR", supported: "zh-CN, en-US" },
    });
    expect(text).toContain("locale");
    expect(text).toContain("fr-FR");
    expect(text).toContain("zh-CN, en-US");

    // 缺参数时保留 {field}：让缺参可见，而不是静默变成半句话。
    // `state.blockedHint` 只有 {reason}，这里用它验证行为。
    expect(t("state.blockedHint")).toBe("{reason}");
  });
});

describe("locale resolution", () => {
  it("prefers saved preference, then system, then zh-CN", () => {
    expect(resolveLocale("en-US", "zh-CN")).toBe("en-US");
    expect(resolveLocale(null, "en-US")).toBe("en-US");
    expect(resolveLocale(undefined, undefined)).toBe("zh-CN");
    // 不受支持的值不能被"猜"成相近语言：用户在设置里选了什么就是什么。
    expect(resolveLocale("en-GB", "zh-CN")).toBe("zh-CN");
    expect(resolveLocale("fr-FR", "en-US")).toBe("en-US");
  });

  it("switches the active catalog for t()", () => {
    setLocale("en-US");
    expect(t("common.retry")).toBe("Retry");
    setLocale("zh-CN");
    expect(t("common.retry")).toBe("重试");
  });
});

describe("user-facing copy stays in the catalogs", () => {
  it("has no user-facing copy outside catalogs", () => {
    const result = checkAll();
    // baseline 只记录历史页面（U01–U07 逐页迁走）；新增文案必须进 catalog。
    expect(result.baselineIssues).toEqual([]);
    expect(result.parityIssues).toEqual([]);
    expect(result.errorIssues).toEqual([]);
    expect(result.ok).toBe(true);
  });

  it("keeps U00's own surface free of literals", () => {
    const problems = scanUserCopy().filter((p) =>
      ["i18n/", "theme/", "components/states/", "components/status/", "components/unsaved/"].some((prefix) =>
        p.file.startsWith(prefix)
      )
    );
    expect(problems).toEqual([]);
  });

  it("fails when a new file introduces copy", () => {
    // 负例：证明门禁真的会拦，而不是恒真。
    const issues = checkBaseline(
      [{ file: "features/brand-new/Page.tsx", line: 1, rule: "jsx-text", text: "新建页面" }],
      { files: {} }
    );
    expect(issues.map((i) => i.kind)).toContain("unlisted-copy");
  });

  it("fails when a legacy file gains copy or the baseline goes stale", () => {
    const problems = [
      { file: "app/App.tsx", line: 1, rule: "jsx-text", text: "a" },
      { file: "app/App.tsx", line: 2, rule: "jsx-text", text: "b" },
    ];
    const increased = checkBaseline(problems, { files: { "app/App.tsx": 1 } });
    expect(increased.map((i) => i.kind)).toContain("copy-increased");

    const stale = checkBaseline([], { files: { "app/App.tsx": 3 } });
    expect(stale.map((i) => i.kind)).toContain("baseline-stale");
  });
});
