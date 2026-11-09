// 设置页的分区定位（UX1）。
//
// 对话页的阻塞面板给的是 `/settings?section=pi|permissions|model`。落地这一跳
// 必须真的把用户带到那个分区：滚到那里、给出焦点、并说清"你是被哪一项带过来的"。
// 不认识的 section 不猜：宁可停在页首，也不要把用户丢到一个随便挑的分区。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { setLocale, t } from "../i18n";
import { resolveRoute } from "./navigation";

const fake = vi.hoisted(() => ({
  discoverPi: vi.fn(async () => ({ state: "missing", selected: null, candidates: [], diagnostics: [] })),
  selectPi: vi.fn(),
  installPi: vi.fn(),
  cancelPiInstall: vi.fn(),
  rollbackPi: vi.fn(),
  getPreferences: vi.fn(async () => ({ preferences: { locale: "zh-CN", theme: "system" }, revision: 1 })),
  savePreferences: vi.fn(),
  listModels: vi.fn(async () => ({ items: [], revision: 1, options: { apis: [], inputKinds: [], thinkingLevels: [] } })),
  currentModel: vi.fn(async () => ({ source: "none", ref: "", error: undefined })),
  // M1ENV：设置页的模型分区会读一次有效配置来源；这里给一份"全都来自设置"的默认值。
  effectiveConfig: vi.fn(async () => ({
    model: {
      provider: { value: "openai", source: "settings" },
      model: { value: "gpt-5", source: "settings" },
      baseUrl: { value: "https://api.example.com/v1", source: "settings" },
      apiKeyRef: { value: "", source: "none" },
      overriddenByEnv: [],
    },
  })),
  testModel: vi.fn(),
  setActiveModel: vi.fn(),
  agentProfileCeiling: vi.fn(async () => ({
    ceiling: { filesystem: "workspace", network: false, shell: false, sources: null, available: true },
    unavailableFields: null,
  })),
}));

vi.mock("../features/settings/api", () => ({
  settingsApi: fake,
  onPiInstallProgress: () => () => undefined,
}));

const shellState = vi.hoisted(() => ({ value: null as any }));
vi.mock("./shell", () => ({ useShell: () => shellState.value }));

import { SettingsRoute } from "./SettingsRoute";

const WORKSPACE = {
  id: "ws-1",
  name: "demo",
  canonicalPath: "D:/work/demo",
  trusted: true,
  createdAt: "2026-11-01T00:00:00Z",
};

beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn();
  shellState.value = {
    settings: {
      piPath: "",
      nodePath: "",
      provider: "openai",
      baseUrl: "https://api.example.com/v1",
      model: "gpt-5",
      apiKey: "",
      modelRef: "",
      workspace: "D:/work/demo",
      shellAllowed: false,
    },
    status: { piVersion: "", platform: "windows/amd64" },
    workspaces: [WORKSPACE],
    currentWorkspace: WORKSPACE,
    savingSettings: false,
    actionError: null,
    saveSettings: vi.fn(),
    trustWorkspace: vi.fn(),
    backend: { loading: false, error: null },
  };
});

afterEach(() => {
  setLocale("zh-CN");
  vi.clearAllMocks();
});

function renderAt(path: string) {
  return render(<SettingsRoute onNavigate={() => undefined} route={resolveRoute(path)} />);
}

describe("设置页分区定位", () => {
  it("?section=pi：滚到 Pi 分区、给出焦点并说明来意", async () => {
    renderAt("/settings?section=pi");

    const section = await screen.findByTestId("settings-pi");
    await waitFor(() => expect(section.getAttribute("data-focused")).toBe("true"));
    expect(Element.prototype.scrollIntoView).toHaveBeenCalled();
    expect(section.getAttribute("tabindex")).toBe("-1");
    expect(
      await screen.findByText(t("settings.jumpNotice", { section: t("settings.pi.title") }))
    ).toBeInTheDocument();
  });

  it("?section=model 指向模型分区，Pi 分区不被标成当前目标", async () => {
    renderAt("/settings?section=model");

    const model = await screen.findByTestId("settings-model");
    await waitFor(() => expect(model.getAttribute("data-focused")).toBe("true"));
    expect(screen.getByTestId("settings-pi").getAttribute("data-focused")).toBeNull();
  });

  it("未知 section 不猜：没有焦点标记、也没有跳转说明", async () => {
    renderAt("/settings?section=does-not-exist");

    await screen.findByTestId("settings-pi");
    expect(document.querySelector("[data-focused='true']")).toBeNull();
    expect(screen.queryByText(/settings\.jumpNotice/)).toBeNull();
  });
});
