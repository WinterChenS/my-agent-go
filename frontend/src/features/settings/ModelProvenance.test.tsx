// 模型来源面板（M1ENV）。
//
// 这一块的价值全在"看得见"：环境变量只在设置没写对应字段时才生效，
// 如果界面不标出来，用户改完模型发现没变化，就分不清"我的设置没保存"、
// "环境变量顶掉了我的设置"和"这条绑定根本没接"。所以这里钉两件事：
//   - env 补位的字段打上环境变量名，绝不明晃晃地冒充"设置"；
//   - 密钥只显示引用（env:ANVIL_MODEL_API_KEY），界面上永远不出现密钥值。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { setLocale, t } from "../../i18n";

const fake = vi.hoisted(() => ({
  effectiveConfig: null as unknown as (...a: any[]) => Promise<any>,
}));

vi.mock("./api", () => ({
  settingsApi: fake,
  onPiInstallProgress: () => () => undefined,
}));

import { ModelProvenance } from "./ModelProvenance";

beforeEach(() => {
  fake.effectiveConfig = vi.fn(async () => ({
    model: {
      provider: { value: "envprov", source: "env", envVar: "ANVIL_PROVIDER" },
      model: { value: "envmodel", source: "env", envVar: "ANVIL_MODEL" },
      baseUrl: { value: "http://127.0.0.1:9/v1", source: "env", envVar: "ANVIL_BASE_URL" },
      apiKeyRef: { value: "env:ANVIL_MODEL_API_KEY", source: "env", envVar: "ANVIL_MODEL_API_KEY" },
      overriddenByEnv: ["provider", "model", "baseUrl", "apiKeyRef"],
    },
  }));
});

afterEach(() => {
  setLocale("zh-CN");
});

describe("ModelProvenance", () => {
  it("marks env-filled fields with the variable that supplied them", async () => {
    render(<ModelProvenance />);

    const provider = await screen.findByTestId("model-provenance-provider");
    expect(within(provider).getByText("envprov")).toBeTruthy();
    expect(within(provider).getByText(/ANVIL_PROVIDER/)).toBeTruthy();
    expect(within(provider).getByText(new RegExp(t("settings.model.env.source.env")))).toBeTruthy();

    const warning = screen.getByTestId("model-provenance-env-warning");
    expect(warning.textContent).toContain("provider");
    expect(warning.textContent).toContain("settings.json");
  });

  it("never renders the key itself, only the reference", async () => {
    render(<ModelProvenance />);
    const ref = await screen.findByTestId("model-provenance-apiKeyRef");
    expect(ref.textContent).toContain("env:ANVIL_MODEL_API_KEY");
    // 密钥值不在返回结构里，更没有可显示的位置：整块面板里不出现任何 sk- 形态的串。
    expect(document.body.textContent).not.toMatch(/sk-[A-Za-z0-9]/);
  });

  it("shows settings as the source with no env warning when settings win", async () => {
    fake.effectiveConfig = vi.fn(async () => ({
      model: {
        provider: { value: "openai", source: "settings" },
        model: { value: "gpt-5", source: "settings" },
        baseUrl: { value: "https://api.example.com/v1", source: "settings" },
        apiKeyRef: { value: "", source: "none" },
        overriddenByEnv: [],
      },
    }));
    render(<ModelProvenance />);

    const provider = await screen.findByTestId("model-provenance-provider");
    expect(within(provider).getByText(new RegExp(t("settings.model.env.source.settings")))).toBeTruthy();
    expect(screen.queryByTestId("model-provenance-env-warning")).toBeNull();
  });

  it("surfaces a read failure instead of pretending everything is fine", async () => {
    fake.effectiveConfig = vi.fn(async () => ({
      error: { code: "CONFIG_MISSING", message: "no layers", retryable: false },
    }));
    render(<ModelProvenance />);

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain(t("settings.model.env.unavailable", { reason: "" }).split("{reason}")[0]);
    expect(screen.queryByTestId("model-provenance")).toBeNull();
  });
});
