// 设置页测试（U06 / UI10）。
//
// 这一页最容易骗人的地方不是布局，而是"保存"：
//   - 失败不能看起来像已保存（旧实现点完按钮就显示"已保存"）；
//   - CAS 冲突必须显示 CONFIG_CONFLICT，而不是静默覆盖另一个窗口的修改；
//   - Pi 的每个候选都要给出真实路径/版本/来源/指纹/探测状态；
//   - F09（应用更新）不存在，更新分区只能是 unavailable，且不能有"退出并更新"。

import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { setLocale, t } from "../../i18n";

const fake = vi.hoisted(() => ({
  getPreferences: null as unknown as (...a: any[]) => Promise<any>,
  savePreferences: null as unknown as (...a: any[]) => Promise<any>,
  listModels: null as unknown as (...a: any[]) => Promise<any>,
  currentModel: null as unknown as (...a: any[]) => Promise<any>,
  testModel: null as unknown as (...a: any[]) => Promise<any>,
  setActiveModel: null as unknown as (...a: any[]) => Promise<any>,
  agentProfileCeiling: null as unknown as (...a: any[]) => Promise<any>,
  discoverPi: null as unknown as (...a: any[]) => Promise<any>,
  selectPi: null as unknown as (...a: any[]) => Promise<any>,
  installPi: null as unknown as (...a: any[]) => Promise<any>,
  cancelPiInstall: null as unknown as (...a: any[]) => Promise<any>,
  rollbackPi: null as unknown as (...a: any[]) => Promise<any>,
  // MP1：模型维护现在挂在设置页，走的是 features/models 的绑定。
  mList: null as unknown as (...a: any[]) => Promise<any>,
  mSave: null as unknown as (...a: any[]) => Promise<any>,
  mRemove: null as unknown as (...a: any[]) => Promise<any>,
  mTest: null as unknown as (...a: any[]) => Promise<any>,
  mSetActive: null as unknown as (...a: any[]) => Promise<any>,
  mCurrent: null as unknown as (...a: any[]) => Promise<any>,
  mDiscover: null as unknown as (...a: any[]) => Promise<any>,
  mProbeProvider: null as unknown as (...a: any[]) => Promise<any>,
  mImportModels: null as unknown as (...a: any[]) => Promise<any>,
  mProviders: null as unknown as (...a: any[]) => Promise<any>,
  mSaveProvider: null as unknown as (...a: any[]) => Promise<any>,
  mDeleteProvider: null as unknown as (...a: any[]) => Promise<any>,
}));

const progress = vi.hoisted(() => ({
  emit: null as null | ((ev: any) => void),
  unsubscribe: vi.fn(),
}));

vi.mock("../models/api", () => ({
  modelsApi: {
    list: (...a: any[]) => fake.mList(...a),
    save: (...a: any[]) => fake.mSave(...a),
    remove: (...a: any[]) => fake.mRemove(...a),
    test: (...a: any[]) => fake.mTest(...a),
    setActive: (...a: any[]) => fake.mSetActive(...a),
    current: (...a: any[]) => fake.mCurrent(...a),
    discover: (...a: any[]) => fake.mDiscover(...a),
    probeProvider: (...a: any[]) => fake.mProbeProvider(...a),
    importModels: (...a: any[]) => fake.mImportModels(...a),
    providers: (...a: any[]) => fake.mProviders(...a),
    saveProvider: (...a: any[]) => fake.mSaveProvider(...a),
    deleteProvider: (...a: any[]) => fake.mDeleteProvider(...a),
  },
  IMPORT_DEFAULTS: { contextWindow: 128000, maxOutput: 8192, inputKinds: ["text"] },
}));

vi.mock("./api", () => ({
  settingsApi: fake,
  onPiInstallProgress: (fn: (ev: any) => void) => {
    progress.emit = fn;
    return progress.unsubscribe;
  },
}));

const shellState = vi.hoisted(() => ({ value: null as any }));
vi.mock("../../app/shell", () => ({ useShell: () => shellState.value }));

import { SettingsPage } from "./SettingsPage";
import { UpdateSettings } from "./UpdateSettings";

const SETTINGS = {
  piPath: "",
  nodePath: "",
  provider: "openai",
  baseUrl: "http://127.0.0.1:8080/v1",
  model: "fixture-model",
  apiKey: "",
  modelRef: "model-1",
  workspace: "D:/work",
  shellAllowed: false,
};

function model(overrides: Record<string, any> = {}) {
  return {
    id: "model-1",
    providerId: "openai",
    modelId: "gpt-5",
    api: "openai-responses",
    baseUrl: "https://api.example.com/v1",
    inputKinds: ["text"],
    contextWindow: 128000,
    maxOutput: 8192,
    credentialRef: "cred-1",
    thinkingLevels: null,
    ...overrides,
  };
}

function candidate(overrides: Record<string, any> = {}) {
  return {
    installation: {
      id: "managed:1.5.0",
      executable: "D:/anvil/pi/versions/1.5.0-win-x64/pi.exe",
      origin: "managed",
      architecture: "x86_64",
      source: "managed",
      runnable: true,
      version: "1.5.0",
    },
    origin: "managed",
    distribution: "standalone",
    executableHash: "a".repeat(64),
    launcherPath: "D:/anvil/pi/versions/1.5.0-win-x64/pi.exe",
    runtimeExecutable: "D:/anvil/pi/versions/1.5.0-win-x64/pi.exe",
    runtimeHash: "b".repeat(64),
    compatibility: "ready_verified",
    diagnostics: [],
    ...overrides,
  };
}

function discovery(overrides: Record<string, any> = {}) {
  return {
    state: "ready_verified",
    selected: candidate(),
    candidates: [candidate()],
    diagnostics: [],
    ...overrides,
  };
}

function shell(overrides: Record<string, any> = {}) {
  return {
    settings: { ...SETTINGS },
    status: { piVersion: "1.5.0", platform: "windows/amd64" },
    workspaces: [
      { id: "ws-1", name: "demo", canonicalPath: "D:/work", trusted: true, createdAt: "2026-11-01T00:00:00Z" },
    ],
    currentWorkspace: { id: "ws-1", name: "demo", canonicalPath: "D:/work", trusted: true, createdAt: "2026-11-01T00:00:00Z" },
    savingSettings: false,
    actionError: null,
    saveSettings: vi.fn(async () => {}),
    trustWorkspace: vi.fn(async () => {}),
    backend: { loading: false, error: null },
    ...overrides,
  };
}

beforeEach(() => {
  fake.getPreferences = vi.fn(async () => ({
    preferences: { locale: "zh-CN", theme: "system" },
    revision: 3,
  }));
  fake.savePreferences = vi.fn(async (_rev: number, p: any) => ({ preferences: p, revision: 4 }));
  fake.listModels = vi.fn(async () => ({
    items: [model()],
    revision: 7,
    options: { apis: [], inputKinds: [], thinkingLevels: [] },
  }));
  fake.currentModel = vi.fn(async () => ({
    source: "resources",
    ref: "model-1",
    provider: "openai",
    model: "gpt-5",
    item: model(),
    revision: 7,
  }));
  fake.testModel = vi.fn(async () => ({
    result: {
      reachable: true,
      toolCalling: "unknown",
      vision: "unknown",
      latencyMs: 42,
      providerId: "openai",
      modelId: "gpt-5",
    },
  }));
  fake.setActiveModel = vi.fn(async (ref: string) => ({ ref, deferred: false }));
  fake.mList = vi.fn(async () => ({
    items: [model()],
    revision: 7,
    options: { apis: ["openai-responses"], inputKinds: ["text"], thinkingLevels: ["off"] },
  }));
  fake.mCurrent = vi.fn(async () => ({
    source: "resources",
    ref: "model-1",
    provider: "openai",
    model: "gpt-5",
    item: model(),
    revision: 7,
  }));
  fake.mProviders = vi.fn(async () => ({ items: [], revision: 7 }));
  fake.mTest = vi.fn(async () => ({
    result: {
      reachable: true,
      toolCalling: "unknown",
      vision: "unknown",
      latencyMs: 42,
      providerId: "openai",
      modelId: "gpt-5",
    },
  }));
  fake.mSetActive = vi.fn(async (ref: string) => ({ ref, deferred: false }));
  fake.mDiscover = vi.fn(async () => ({ items: [] }));
  fake.mProbeProvider = vi.fn(async () => ({ probe: null }));
  fake.mImportModels = vi.fn(async () => ({ imported: [], updated: [], models: [], revision: 8 }));
  fake.mSaveProvider = vi.fn(async () => ({ revision: 8 }));
  fake.mDeleteProvider = vi.fn(async () => ({ revision: 8 }));
  fake.mSave = vi.fn(async () => ({ revision: 8 }));
  fake.mRemove = vi.fn(async () => ({ revision: 8 }));
  fake.agentProfileCeiling = vi.fn(async () => ({
    ceiling: {
      filesystem: "workspace",
      network: false,
      shell: false,
      sources: {
        "permissions.filesystem": "system",
        "permissions.network": "system",
        "permissions.shell": "system",
      },
      available: false,
    },
    unavailableFields: [],
  }));
  fake.discoverPi = vi.fn(async () => discovery());
  fake.selectPi = vi.fn(async () => discovery());
  fake.installPi = vi.fn(async () => ({
    state: "rollback_available",
    managedRoot: "D:/anvil/pi",
    version: "1.5.0",
    targetId: "win-x64",
    executable: "D:/anvil/pi/versions/1.5.0-win-x64/pi.exe",
    executableSha256: "c".repeat(64),
    installationId: "managed:1.5.0",
    previousVersion: "1.4.9",
    rollbackAvailable: true,
    eventName: "myagent:pi-install",
  }));
  fake.cancelPiInstall = vi.fn(async () => {});
  fake.rollbackPi = vi.fn(async () => ({
    state: "ready_verified",
    managedRoot: "D:/anvil/pi",
    version: "1.4.9",
    rollbackAvailable: false,
    eventName: "myagent:pi-install",
  }));
  progress.emit = null;
  shellState.value = shell();
});

afterEach(() => {
  // 保存语言偏好会真的切到 en-US；不还原会让后续用例的 t() 变成英文。
  setLocale("zh-CN");
});

describe("设置页 · 通用（语言与主题）", () => {
  it("读取已保存偏好，保存时带回 revision，并按后端重新读一次确认落盘", async () => {
    render(<SettingsPage onNavigate={() => {}} />);

    const locale = await screen.findByLabelText(t("locale.label"));
    expect(locale).toHaveValue("zh-CN");

    await userEvent.selectOptions(locale, "en-US");
    await userEvent.selectOptions(screen.getByLabelText(t("theme.label")), "dark");
    await userEvent.click(screen.getByRole("button", { name: t("settings.general.save") }));

    await waitFor(() =>
      expect(fake.savePreferences).toHaveBeenCalledWith(3, { locale: "en-US", theme: "dark" })
    );
    expect(await screen.findByTestId("settings-general-saved")).toBeInTheDocument();
    // 持久化+重载：保存后必须再从后端读一次，界面显示的是磁盘上的值。
    await waitFor(() => expect(fake.getPreferences).toHaveBeenCalledTimes(2));
    expect(document.documentElement.lang).toBe("en-US");
    expect(document.documentElement.dataset.theme).toBe("dark");
  });

  it("CAS 冲突显示 CONFIG_CONFLICT，且不显示已保存", async () => {
    fake.savePreferences = vi.fn(async () => ({
      preferences: { locale: "zh-CN", theme: "system" },
      revision: 3,
      error: { code: "CONFIG_CONFLICT", message: "配置已被其它窗口修改", retryable: false },
    }));
    render(<SettingsPage onNavigate={() => {}} />);

    await userEvent.selectOptions(await screen.findByLabelText(t("locale.label")), "en-US");
    await userEvent.click(screen.getByRole("button", { name: t("settings.general.save") }));

    expect(await screen.findByText(t("errors.CONFIG_CONFLICT"))).toBeInTheDocument();
    expect(screen.queryByTestId("settings-general-saved")).toBeNull();
    // 冲突时保留用户的选择，并给出重新读取的动作（不静默覆盖另一个窗口）。
    expect(screen.getByLabelText(t("locale.label"))).toHaveValue("en-US");
    expect(screen.getByText(t("settings.general.conflictHint"))).toBeInTheDocument();
  });

  it("写入失败不会被当成已保存", async () => {
    fake.savePreferences = vi.fn(async () => ({
      preferences: { locale: "zh-CN", theme: "system" },
      revision: 3,
      error: { code: "INTERNAL_ERROR", message: "写 preferences.json 失败", retryable: true },
    }));
    render(<SettingsPage onNavigate={() => {}} />);

    await userEvent.selectOptions(await screen.findByLabelText(t("theme.label")), "light");
    await userEvent.click(screen.getByRole("button", { name: t("settings.general.save") }));

    expect(await screen.findByText(t("errors.INTERNAL_ERROR"))).toBeInTheDocument();
    expect(screen.queryByTestId("settings-general-saved")).toBeNull();
  });

  it("偏好读不出来时显示错误与重试，重试会再读一次", async () => {
    fake.getPreferences = vi
      .fn()
      .mockResolvedValueOnce({
        preferences: { locale: "zh-CN", theme: "system" },
        revision: 0,
        error: { code: "INTERNAL_ERROR", message: "配置存储打不开", retryable: true },
      })
      .mockResolvedValueOnce({ preferences: { locale: "zh-CN", theme: "system" }, revision: 1 });
    render(<SettingsPage onNavigate={() => {}} />);

    const general = await screen.findByTestId("settings-general");
    expect(await within(general).findByText(t("errors.INTERNAL_ERROR"))).toBeInTheDocument();

    await userEvent.click(within(general).getByRole("button", { name: t("common.retry") }));
    expect(await within(general).findByLabelText(t("locale.label"))).toBeInTheDocument();
    expect(fake.getPreferences).toHaveBeenCalledTimes(2);
  });
});

describe("设置页 · 模型", () => {
  it("列出资源库模型、真实凭证状态与当前模型来源", async () => {
    render(<SettingsPage onNavigate={() => {}} />);
    const section = await screen.findByTestId("settings-model");

    const row = await within(section).findByTestId("model-model-1");
    expect(row).toHaveTextContent("openai/gpt-5");
    expect(row).toHaveTextContent(t("library.models.labels.credential", { ref: "cred-1" }));
    expect(within(section).getByTestId("model-current")).toHaveTextContent(
      t("settings.model.source.resources")
    );
  });

  it("连接测试显示真实延迟，失败时按 code 显示", async () => {
    fake.mTest = vi.fn(async () => ({
      result: {
        reachable: false,
        toolCalling: "unknown",
        vision: "unknown",
        latencyMs: 0,
        providerId: "openai",
        modelId: "gpt-5",
        error: { code: "PROVIDER_UNREACHABLE", message: "429", retryable: true },
      },
    }));
    render(<SettingsPage onNavigate={() => {}} />);
    const section = await screen.findByTestId("settings-model");
    await userEvent.click(
      await within(section).findByRole("button", { name: t("library.models.actions.test") })
    );

    const out = await within(section).findByTestId("model-test-model-1");
    expect(out).toHaveTextContent(t("library.models.labels.unreachable"));
    expect(out).toHaveTextContent("PROVIDER_UNREACHABLE");
  });

  it("设为当前模型走真实绑定，deferred 时说明下次运行才生效", async () => {
    fake.mList = vi.fn(async () => ({
      items: [model(), model({ id: "model-2", providerId: "local", modelId: "qwen" })],
      revision: 7,
      options: { apis: ["openai-responses"], inputKinds: ["text"], thinkingLevels: ["off"] },
    }));
    fake.mSetActive = vi.fn(async (ref: string) => ({ ref, deferred: true }));
    render(<SettingsPage onNavigate={() => {}} />);
    const section = await screen.findByTestId("settings-model");
    await userEvent.click(
      await within(section).findByRole("button", { name: t("library.models.actions.setActive") })
    );

    await waitFor(() => expect(fake.mSetActive).toHaveBeenCalledWith("model-2"));
    expect(
      await within(section).findByText(t("library.models.notices.deferred"))
    ).toBeInTheDocument();
  });
});

describe("设置页 · 权限", () => {
  it("显示真实上限与来源，并把不可用的网络权限标成不可用", async () => {
    render(<SettingsPage onNavigate={() => {}} />);
    const section = await screen.findByTestId("settings-permissions");

    expect(await within(section).findByTestId("permissions-filesystem-ceiling")).toHaveTextContent(
      "workspace"
    );
    expect(within(section).getByTestId("permissions-network-ceiling")).toHaveTextContent(
      t("settings.permissions.denied")
    );
    expect(within(section).getByTestId("permissions-network-note")).toHaveTextContent(
      t("settings.permissions.networkUnavailable")
    );
    // 上限来源逐字段显示（不是"系统决定"这种笼统说法）。
    expect(within(section).getByTestId("permissions-shell-source")).toHaveTextContent(
      t("settings.permissions.sources.system")
    );
  });

  it("shell 默认值保存失败时不会显示已保存", async () => {
    const save = vi.fn(async (next: any) => {
      // 真实 App 把失败收敛成 actionError，快照保持不变。
      shellState.value = { ...shellState.value, actionError: { code: "CONFIG_CONFLICT", message: "x", retryable: false } };
      void next;
    });
    shellState.value = shell({ saveSettings: save });
    const view = render(<SettingsPage onNavigate={() => {}} />);

    const section = await screen.findByTestId("settings-permissions");
    await userEvent.click(await within(section).findByLabelText(t("settings.permissions.shellDefault")));
    await waitFor(() => expect(save).toHaveBeenCalledWith({ ...SETTINGS, shellAllowed: true }));

    view.rerender(<SettingsPage onNavigate={() => {}} />);
    expect(await within(section).findByText(t("errors.CONFIG_CONFLICT"))).toBeInTheDocument();
    expect(within(section).queryByTestId("settings-permissions-saved")).toBeNull();
  });

  it("shell 默认值保存成功后按后端快照显示已保存", async () => {
    const save = vi.fn(async (next: any) => {
      shellState.value = { ...shellState.value, actionError: null, settings: { ...next } };
    });
    shellState.value = shell({ saveSettings: save });
    const view = render(<SettingsPage onNavigate={() => {}} />);

    const section = await screen.findByTestId("settings-permissions");
    await userEvent.click(await within(section).findByLabelText(t("settings.permissions.shellDefault")));
    await waitFor(() => expect(save).toHaveBeenCalled());

    view.rerender(<SettingsPage onNavigate={() => {}} />);
    expect(await within(section).findByTestId("settings-permissions-saved")).toBeInTheDocument();
  });

  it("工作区信任来自真实列表并可切换", async () => {
    render(<SettingsPage onNavigate={() => {}} />);
    const section = await screen.findByTestId("settings-permissions");
    const row = await within(section).findByTestId("workspace-ws-1");
    expect(row).toHaveTextContent(t("settings.permissions.trusted"));

    await userEvent.click(within(row).getByRole("button", { name: t("settings.permissions.untrust") }));
    expect(shellState.value.trustWorkspace).toHaveBeenCalledWith("ws-1", false);
  });
});

describe("设置页 · Pi 运行时", () => {
  it("每个候选都显示真实路径/版本/发行形式/来源/指纹与探测状态", async () => {
    const managed = candidate();
    const pathCandidate = candidate({
      installation: {
        id: "path:1.4.0",
        executable: "C:/Users/me/AppData/Roaming/npm/pi.cmd",
        origin: "external",
        architecture: "any",
        source: "path",
        runnable: true,
        version: "1.4.0",
      },
      origin: "path",
      distribution: "npm",
      executableHash: "d".repeat(64),
      launcherPath: "C:/Users/me/AppData/Roaming/npm/pi.cmd",
      runtimeExecutable: "C:/Program Files/nodejs/node.exe",
      runtimeHash: "e".repeat(64),
      compatibility: "upgrade_available",
      diagnostics: ['kind:"version-too-old" 低于最低支持版本 1.5.0'],
    });
    const missing = candidate({
      installation: {
        id: "saved:gone",
        executable: "D:/gone/pi.exe",
        origin: "external",
        architecture: "unknown",
        source: "saved-path",
        runnable: false,
        version: "",
      },
      origin: "saved",
      compatibility: "missing",
      executableHash: "",
      diagnostics: ['kind:"missing-path" saved 路径不存在，已失效'],
    });
    fake.discoverPi = vi.fn(async () =>
      discovery({ state: "ready_verified", candidates: [managed, pathCandidate, missing] })
    );

    render(<SettingsPage onNavigate={() => {}} />);
    const section = await screen.findByTestId("settings-pi");

    const managedRow = await within(section).findByTestId("pi-candidate-managed:1.5.0");
    expect(managedRow).toHaveTextContent("D:/anvil/pi/versions/1.5.0-win-x64/pi.exe");
    expect(managedRow).toHaveTextContent("1.5.0");
    expect(managedRow).toHaveTextContent(t("settings.pi.origin.managed"));
    expect(managedRow).toHaveTextContent(t("settings.pi.distribution.standalone"));
    expect(within(managedRow).getByTestId("pi-state")).toHaveTextContent(
      t("settings.pi.states.ready_verified")
    );
    expect(within(managedRow).getByTestId("pi-executable-hash")).toHaveTextContent("a".repeat(64));

    const pathRow = within(section).getByTestId("pi-candidate-path:1.4.0");
    expect(pathRow).toHaveTextContent(t("settings.pi.origin.path"));
    expect(pathRow).toHaveTextContent(t("settings.pi.distribution.npm"));
    expect(within(pathRow).getByTestId("pi-runtime")).toHaveTextContent(
      "C:/Program Files/nodejs/node.exe"
    );
    expect(within(pathRow).getByTestId("pi-state")).toHaveTextContent(
      t("settings.pi.states.upgrade_available")
    );
    expect(pathRow).toHaveTextContent('kind:"version-too-old"');
    // 低于最低支持版本的候选不可选（升级路径不是"选它继续用"）。
    expect(within(pathRow).getByRole("button", { name: t("settings.pi.select") })).toBeDisabled();

    const missingRow = within(section).getByTestId("pi-candidate-saved:gone");
    expect(within(missingRow).getByTestId("pi-state")).toHaveTextContent(
      t("settings.pi.states.missing")
    );
    expect(within(missingRow).getByRole("button", { name: t("settings.pi.select") })).toBeDisabled();
  });

  it("选用可用候选走 SelectPi，并显示后端返回的选中项", async () => {
    const second = candidate({
      installation: {
        id: "path:1.5.0",
        executable: "C:/tools/pi/pi.exe",
        origin: "external",
        architecture: "x86_64",
        source: "path",
        runnable: true,
        version: "1.5.0",
      },
      origin: "path",
      compatibility: "ready_compatible",
    });
    fake.discoverPi = vi.fn(async () => discovery({ candidates: [candidate(), second] }));
    fake.selectPi = vi.fn(async () =>
      discovery({
        state: "ready_compatible",
        selected: second,
        candidates: [candidate(), second],
      })
    );

    render(<SettingsPage onNavigate={() => {}} />);
    const section = await screen.findByTestId("settings-pi");
    const row = await within(section).findByTestId("pi-candidate-path:1.5.0");
    await userEvent.click(within(row).getByRole("button", { name: t("settings.pi.select") }));

    await waitFor(() => expect(fake.selectPi).toHaveBeenCalledWith("path:1.5.0"));
    expect(await within(row).findByTestId("pi-selected")).toBeInTheDocument();
  });

  it("安装走 InstallPi(version) 并把权威结果与进度分开显示", async () => {
    render(<SettingsPage onNavigate={() => {}} />);
    const section = await screen.findByTestId("settings-pi");
    const version = await within(section).findByLabelText(t("settings.pi.installVersion"));

    // 进度事件只承载阶段，不是结论。
    progress.emit?.({ installId: "inst-1", stage: "verify" });
    expect(await within(section).findByTestId("pi-install-stage")).toHaveTextContent(
      t("settings.pi.stage.verify")
    );
    expect(within(section).queryByTestId("pi-install-result")).toBeNull();

    await userEvent.type(version, "1.5.0");
    await userEvent.click(within(section).getByRole("button", { name: t("settings.pi.install") }));

    await waitFor(() => expect(fake.installPi).toHaveBeenCalledWith("1.5.0"));
    const result = await within(section).findByTestId("pi-install-result");
    expect(result).toHaveAttribute("data-state", "rollback_available");
    expect(within(result).getByTestId("pi-install-previous")).toHaveTextContent("1.4.9");
    expect(within(result).getByTestId("pi-install-hash")).toHaveTextContent("c".repeat(64));
  });

  it("安装失败显示失败阶段，不显示成功", async () => {
    fake.installPi = vi.fn(async () => ({
      state: "failed",
      managedRoot: "D:/anvil/pi",
      version: "1.5.0",
      rollbackAvailable: false,
      failedStage: "verify",
      error: "sha256 不匹配",
      eventName: "myagent:pi-install",
    }));
    render(<SettingsPage onNavigate={() => {}} />);
    const section = await screen.findByTestId("settings-pi");
    await userEvent.type(
      await within(section).findByLabelText(t("settings.pi.installVersion")),
      "1.5.0"
    );
    await userEvent.click(within(section).getByRole("button", { name: t("settings.pi.install") }));

    const result = await within(section).findByTestId("pi-install-result");
    expect(result).toHaveAttribute("data-state", "failed");
    expect(within(result).getByTestId("pi-install-failed-stage")).toHaveTextContent(
      t("settings.pi.stage.verify")
    );
    expect(within(result).getByTestId("pi-install-detail")).toHaveTextContent("sha256 不匹配");
    expect(within(section).queryByTestId("pi-install-previous")).toBeNull();
  });

  it("只有后端报告 rollbackAvailable 时才允许回滚，并把 installationId 原样传回", async () => {
    render(<SettingsPage onNavigate={() => {}} />);
    const section = await screen.findByTestId("settings-pi");
    const rollback = await within(section).findByRole("button", { name: t("settings.pi.rollback") });
    expect(rollback).toBeDisabled();

    await userEvent.type(
      within(section).getByLabelText(t("settings.pi.installVersion")),
      "1.5.0"
    );
    await userEvent.click(within(section).getByRole("button", { name: t("settings.pi.install") }));
    await within(section).findByTestId("pi-install-result");

    expect(within(section).getByRole("button", { name: t("settings.pi.rollback") })).toBeEnabled();
    await userEvent.click(within(section).getByRole("button", { name: t("settings.pi.rollback") }));
    await waitFor(() => expect(fake.rollbackPi).toHaveBeenCalledWith("managed:1.5.0"));
  });

  it("探测失败显示错误码与重试，不渲染任何候选", async () => {
    fake.discoverPi = vi.fn(async () => {
      throw { code: "COMPATIBILITY", message: "探测超时", retryable: true };
    });
    render(<SettingsPage onNavigate={() => {}} />);
    const section = await screen.findByTestId("settings-pi");
    expect(await within(section).findByText(t("errors.COMPATIBILITY"))).toBeInTheDocument();
    expect(within(section).queryByTestId("pi-candidates")).toBeNull();
  });
});

describe("设置页 · 应用更新（F09 未实现）", () => {
  it("渲染 unavailable 与原因，且没有任何可点的更新动作", async () => {
    render(<UpdateSettings />);
    const section = screen.getByTestId("settings-update");
    expect(section).toHaveAttribute("data-state", "unavailable");
    expect(within(section).getByText(t("settings.update.unavailableReason"))).toBeInTheDocument();
    expect(within(section).getByText(t("settings.update.noFakeActions"))).toBeInTheDocument();
    // 没有"退出并更新"、没有进度、没有假状态。
    expect(within(section).queryAllByRole("button")).toHaveLength(0);
    expect(section).not.toHaveTextContent("%");
    expect(section.querySelector('[role="progressbar"]')).toBeNull();
  });
});

describe("设置页 · 安全", () => {
  it("逐个模型报告凭证是否存在，说明旧字段明文兜底，且不回显密钥", async () => {
    fake.listModels = vi.fn(async () => ({
      items: [model(), model({ id: "model-2", providerId: "local", credentialRef: "" })],
      revision: 7,
      options: { apis: [], inputKinds: [], thinkingLevels: [] },
    }));
    shellState.value = shell({ settings: { ...SETTINGS, modelRef: "", apiKey: "sk-plain" } });
    render(<SettingsPage onNavigate={() => {}} />);

    const section = await screen.findByTestId("settings-security");
    expect(await within(section).findByTestId("security-cred-model-1")).toHaveTextContent(
      t("settings.security.credentialPresent")
    );
    expect(within(section).getByTestId("security-cred-model-2")).toHaveTextContent(
      t("settings.security.credentialMissing")
    );
    // 凭证库本身没有状态绑定：显示 unavailable，而不是假装"安全"。
    expect(within(section).getByTestId("security-store-status")).toHaveTextContent(
      t("settings.security.storeUnavailable")
    );
    expect(within(section).getByTestId("security-legacy")).toHaveTextContent(
      t("settings.security.legacyActive")
    );
    // 界面不提供任何输入框回填/重写已保存的密钥。
    expect(section.querySelector('input[type="password"]')).toBeNull();
    expect(section.querySelector("input")).toBeNull();
  });
});

describe("设置页 · 导航与结构", () => {
  it("七个分区都渲染，并给出观测页入口", async () => {
    const onNavigate = vi.fn();
    render(<SettingsPage onNavigate={onNavigate} />);
    for (const id of ["general", "model", "permissions", "pi", "update", "security", "diagnostics"]) {
      expect(await screen.findByTestId(`settings-${id}`)).toBeInTheDocument();
    }
    await userEvent.click(screen.getByRole("button", { name: t("settings.diagnostics.openPage") }));
    expect(onNavigate).toHaveBeenCalledWith("/observability");
  });
});
