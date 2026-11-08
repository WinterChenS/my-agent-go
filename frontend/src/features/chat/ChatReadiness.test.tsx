// 对话页的发送前提（UX1）。
//
// 这一组用例守的是"别等点完才报错"：
//   - 挂载时就检查 Pi / 工作区 / 模型，把每一条不满足写成阻塞原因；
//   - 每条原因都能跳到对应设置分区（Pi / 权限（工作区）/ 模型）；
//   - 有阻塞时输入区禁用并有可读解释，发送不会打到后端（onSendToSession 不被调用）；
//   - 发送前会重新检查一次（不是只看挂载时那份快照）；
//   - 内置 fixture 有明确标记，换成真 provider 后消失。
//
// 这里不 mock Wails runtime：检查走注入的 discovery 替身（与 browserApprovals
// 同一模式），测的是页面行为，不是我对绑定的伪造。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { t } from "../../i18n";
import { initialState, type UiState } from "../../runState";
import type { CurrentModelResult, Settings, Workspace } from "../../types";
import { ShellProvider, type ShellValue } from "../../app/shell";
import type { PiDiscoveryResult } from "../settings/api";

const backend = vi.hoisted(() => ({
  list: null as unknown as (...a: any[]) => Promise<any>,
}));

vi.mock("./api", () => ({
  chatApi: {
    startSessionRun: vi.fn(),
    abort: vi.fn(),
    resolveApproval: vi.fn(),
    answerClarification: vi.fn(),
    replay: vi.fn(),
    resolveAttachments: vi.fn(),
    exportSession: vi.fn(),
    runArtifacts: vi.fn(),
    runDetails: vi.fn(),
    artifact: vi.fn(),
  },
}));

vi.mock("../history/api", () => ({
  HISTORY_PAGE_SIZE: 20,
  historyApi: {
    list: (...a: any[]) => backend.list(...a),
    create: vi.fn(),
    rename: vi.fn(),
    setPinned: vi.fn(),
    setArchived: vi.fn(),
    remove: vi.fn(),
  },
}));

import { ChatPage, type ReadinessBackend } from "./ChatPage";
import type { BrowserApprovalBackend } from "./browserApproval";

const SESSION = {
  id: "sess-1",
  workspaceId: "ws-1",
  title: "验收会话",
  pinned: false,
  archived: false,
  kind: "chat",
  createdAt: "2026-11-06T00:00:00Z",
  updatedAt: "2026-11-06T00:00:00Z",
  revision: 3,
};

const WORKSPACE: Workspace = {
  id: "ws-1",
  name: "demo",
  canonicalPath: "D:/work/demo",
  trusted: true,
  createdAt: "2026-11-01T00:00:00Z",
};

const SETTINGS: Settings = {
  piPath: "",
  nodePath: "",
  provider: "openai",
  baseUrl: "https://api.example.com/v1",
  model: "gpt-5",
  apiKey: "",
  modelRef: "model-1",
  workspace: "D:/work/demo",
  shellAllowed: false,
};

const RESOURCE_MODEL: CurrentModelResult = {
  source: "resources",
  ref: "model-1",
  provider: "openai",
  model: "gpt-5",
  item: {
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
  } as CurrentModelResult["item"],
  revision: 3,
};

function candidate(overrides: Record<string, unknown> = {}) {
  return {
    installation: {
      id: "managed:1.5.0",
      executable: "D:/anvil/pi/1.5.0/pi.exe",
      origin: "managed",
      architecture: "x86_64",
      source: "managed",
      runnable: true,
      version: "1.5.0",
    },
    origin: "managed",
    distribution: "standalone",
    executableHash: "a".repeat(64),
    launcherPath: "D:/anvil/pi/1.5.0/pi.exe",
    runtimeExecutable: "D:/anvil/pi/1.5.0/pi.exe",
    runtimeHash: "b".repeat(64),
    compatibility: "ready_verified",
    diagnostics: [],
    ...overrides,
  } as unknown as NonNullable<PiDiscoveryResult["candidates"]>[number];
}

const READY_DISCOVERY: PiDiscoveryResult = {
  state: "ready_verified",
  selected: candidate(),
  candidates: [candidate()],
  diagnostics: [],
};

const MISSING_DISCOVERY: PiDiscoveryResult = {
  state: "missing",
  selected: null,
  candidates: [],
  diagnostics: [],
};

/**
 * 旧字段路径（source=settings）的当前模型：Go 侧只填 source/provider/model，
 * item 与 revision 是零值，这里如实表达而不是编一份假的模型定义。
 */
const FIXTURE_MODEL: CurrentModelResult = {
  source: "settings",
  ref: "",
  provider: "fixture",
  model: "fixture-model",
  item: {} as CurrentModelResult["item"],
  revision: 0,
};

const FIXTURE_SETTINGS: Settings = {
  ...SETTINGS,
  provider: "fixture",
  baseUrl: "http://127.0.0.1:0/v1",
  model: "fixture-model",
  modelRef: "",
};

/** 极简 UI 状态：这个文件只关心发送前提，不重放事件。 */
const UI: UiState = initialState;

function shellValue(overrides: Partial<ShellValue> = {}): ShellValue {
  const noop = () => undefined;
  return {
    backend: {
      status: null,
      settings: SETTINGS,
      workspaces: [WORKSPACE],
      currentWorkspace: WORKSPACE,
      profiles: [],
      models: [],
      modelRevision: 0,
      modelOptions: { apis: [], inputKinds: [], thinkingLevels: [] },
      currentModel: RESOURCE_MODEL,
      skills: [],
      skillRevision: 0,
      skillsPending: false,
      skillsActiveRun: false,
      extensions: [],
      extensionRevision: 0,
      loading: false,
      error: null,
    },
    status: null,
    settings: SETTINGS,
    workspaces: [WORKSPACE],
    currentWorkspace: WORKSPACE,
    profiles: [],
    models: [],
    modelOptions: { apis: [], inputKinds: [], thinkingLevels: [] },
    modelRevision: 0,
    currentModel: RESOURCE_MODEL,
    skills: [],
    skillRevision: 0,
    skillsPending: false,
    skillsActiveRun: false,
    extensions: [],
    extensionRevision: 0,
    ui: UI,
    input: "",
    onInput: noop,
    onSend: noop,
    onStop: noop,
    onDecide: noop,
    onSendToSession: noop,
    onAnswerClarification: noop,
    onReplayRun: noop,
    busy: false,
    savingSettings: false,
    actionError: null,
    saveSettings: async () => undefined,
    trustWorkspace: async () => undefined,
    addWorkspace: async () => undefined,
    refreshProfiles: noop,
    refreshModels: noop,
    refreshSkills: noop,
    onNewSession: async () => undefined,
    ...overrides,
  };
}

const NO_BROWSER_APPROVALS: BrowserApprovalBackend = {
  pending: async () => [],
  resolve: async () => undefined,
};

function routeFor(id: string) {
  return {
    spec: { id: "chat", pattern: "/chat/:id", component: ChatPage, labelKey: "nav.chat", hintKey: "nav.hints.chat" },
    path: `/chat/${id}`,
    params: { id },
    query: new URLSearchParams(),
    fallback: false,
  } as unknown as Parameters<typeof ChatPage>[0]["route"];
}

interface Harness {
  discoverPi: ReturnType<typeof vi.fn>;
  readiness: ReadinessBackend;
}

function readinessBackend(result: PiDiscoveryResult | Error): Harness {
  const discoverPi = vi.fn(async () => {
    if (result instanceof Error) throw result;
    return result;
  });
  return { discoverPi, readiness: { discoverPi } as unknown as ReadinessBackend };
}

function renderPage(
  shell: ShellValue,
  readiness: ReadinessBackend | undefined,
  onNavigate: (route: string) => void = () => undefined
) {
  return render(
    <ShellProvider value={shell}>
      <ChatPage
        onNavigate={onNavigate}
        route={routeFor("sess-1")}
        browserApprovals={NO_BROWSER_APPROVALS}
        browserApprovalPollMs={0}
        readiness={readiness}
      />
    </ShellProvider>
  );
}

function blockerCodes(): string[] {
  return Array.from(document.querySelectorAll("[data-readiness-blocker]")).map(
    (el) => el.getAttribute("data-readiness-blocker") ?? ""
  );
}

beforeEach(() => {
  backend.list = async () => ({
    groups: [{ workspaceId: "ws-1", sessions: [SESSION] }],
    hasMore: false,
  });
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("对话页发送前提", () => {
  it("全新状态：挂载即列出三条阻塞原因，输入区禁用，发送不打到后端", async () => {
    const user = userEvent.setup();
    const harness = readinessBackend(MISSING_DISCOVERY);
    const onSendToSession = vi.fn();
    const navigate = vi.fn();
    renderPage(
      shellValue({
        currentWorkspace: { ...WORKSPACE, trusted: false },
        settings: FIXTURE_SETTINGS,
        currentModel: FIXTURE_MODEL,
        input: "这条消息不该被发出去",
        onSendToSession,
      }),
      harness.readiness,
      navigate
    );

    // 挂载时检查一次，不需要用户先点任何东西。
    await waitFor(() => expect(harness.discoverPi).toHaveBeenCalled());
    await waitFor(() => expect(document.querySelector("[data-readiness-panel]")).not.toBeNull());

    expect(blockerCodes()).toEqual(["PI_MISSING", "WORKSPACE_UNTRUSTED", "MODEL_FIXTURE"]);

    // 每条都有自己的跳转目标，且真的导航到那个分区。
    const piJump = document.querySelector('[data-readiness-jump="PI_MISSING"]');
    expect(piJump).not.toBeNull();
    await user.click(piJump as HTMLElement);
    expect(navigate).toHaveBeenCalledWith("/settings?section=pi");
    await user.click(document.querySelector('[data-readiness-jump="WORKSPACE_UNTRUSTED"]') as HTMLElement);
    expect(navigate).toHaveBeenCalledWith("/settings?section=permissions");
    await user.click(document.querySelector('[data-readiness-jump="MODEL_FIXTURE"]') as HTMLElement);
    expect(navigate).toHaveBeenCalledWith("/settings?section=model");

    // 输入区禁用 + 可读解释（不是只有一个红 toast）。
    const input = screen.getByLabelText(t("chat.composer.placeholder")) as HTMLTextAreaElement;
    expect(input.disabled).toBe(true);
    expect(document.querySelector("[data-composer-blocked]")).not.toBeNull();
    expect(screen.getByText(t("chat.composer.blocked"))).toBeInTheDocument();

    // 发送按钮也是禁用的：点了不会有请求。
    const send = screen.getByRole("button", { name: t("chat.composer.send") }) as HTMLButtonElement;
    expect(send.disabled).toBe(true);
    await user.click(send);
    expect(onSendToSession).not.toHaveBeenCalled();
  });

  it("检查还在飞时不锁输入：只显示轻量“检测中”，结论到达后才按事实阻塞", async () => {
    const user = userEvent.setup();
    // 可控的发现 Promise：先让页面挂载完成，再决定结论什么时候到。
    let release: (value: PiDiscoveryResult) => void = () => undefined;
    const pending = new Promise<PiDiscoveryResult>((resolve) => {
      release = resolve;
    });
    const discoverPi = vi.fn(() => pending);
    const onSendToSession = vi.fn();
    renderPage(
      shellValue({ input: "", onSendToSession }),
      { discoverPi } as unknown as ReadinessBackend
    );

    // 检查在飞：输入区必须可用（页面不能被一次探测卡死），并且有一条轻量状态。
    const input = (await waitFor(() => {
      const el = screen.getByLabelText(t("chat.composer.placeholder"));
      return el as HTMLTextAreaElement;
    }));
    await waitFor(() => expect(discoverPi).toHaveBeenCalled());
    expect(input.disabled).toBe(false);
    const checking = document.querySelector("[data-composer-checking]") as HTMLElement;
    expect(checking).not.toBeNull();
    expect(checking.textContent).toBe(t("chat.composer.checkingReadiness"));
    // “还没检查完”不是“你缺东西”：此时不该出现阻塞文案。
    expect(document.querySelector("[data-composer-blocked]")).toBeNull();

    // 结论到达（没有 Pi）：这时才按事实阻塞输入并给出可读解释。
    release(MISSING_DISCOVERY);
    await waitFor(() => expect(document.querySelector("[data-composer-blocked]")).not.toBeNull());
    expect(document.querySelector("[data-composer-checking]")).toBeNull();
    expect((screen.getByLabelText(t("chat.composer.placeholder")) as HTMLTextAreaElement).disabled).toBe(true);
    // 用户点禁用后的发送按钮不发请求。
    await user.click(screen.getByRole("button", { name: t("chat.composer.send") }));
    expect(onSendToSession).not.toHaveBeenCalled();
  });

  it("readiness 用快速发现（不跑契约探测）也照样能判定可发送", async () => {
    // 快速发现的形状：verification=version_only、候选 contractVerified=false，
    // 但状态是 ready_compatible —— 判定层不该因此把用户挡在门外。
    const fast = readinessBackend({
      state: "ready_compatible",
      selected: candidate({ compatibility: "ready_compatible", contractVerified: false }),
      candidates: [candidate({ compatibility: "ready_compatible", contractVerified: false })],
      diagnostics: [],
      verification: "version_only",
    });
    renderPage(shellValue({ input: "可以发" }), fast.readiness);

    await waitFor(() => expect(fast.discoverPi).toHaveBeenCalled());
    await waitFor(() => expect(document.querySelector("[data-readiness-panel]")).toBeNull());
    const input = screen.getByLabelText(t("chat.composer.placeholder")) as HTMLTextAreaElement;
    expect(input.disabled).toBe(false);
    expect(document.querySelector("[data-composer-blocked]")).toBeNull();
  });

  it("Pi 存在但低于最低版本：原因里写清版本与升级路径", async () => {
    const old = candidate({
      installation: {
        id: "path:1.4.0",
        executable: "C:/tools/pi/pi.exe",
        origin: "external",
        architecture: "x86_64",
        runnable: true,
        version: "1.4.0",
      },
      origin: "path",
      compatibility: "upgrade_available",
    });
    const harness = readinessBackend({
      state: "upgrade_available",
      selected: null,
      candidates: [old],
      diagnostics: ["path:1.4.0: 低于最低支持版本"],
    });
    renderPage(shellValue({ input: "hi" }), harness.readiness);

    await waitFor(() => expect(document.querySelector('[data-readiness-blocker="PI_TOO_OLD"]')).not.toBeNull());
    const item = document.querySelector('[data-readiness-blocker="PI_TOO_OLD"]') as HTMLElement;
    expect(item.textContent).toContain("1.4.0");
    expect(item.textContent).toContain("1.5.0");
    // 升级路径在设置 · Pi 分区，不在别处。
    expect(item.querySelector('[data-readiness-jump="PI_TOO_OLD"]')).not.toBeNull();
  });

  it("工作区未信任：原因带目录与信任动作", async () => {
    const harness = readinessBackend(READY_DISCOVERY);
    renderPage(
      shellValue({
        currentWorkspace: { ...WORKSPACE, trusted: false },
        workspaces: [{ ...WORKSPACE, trusted: false }],
        input: "hi",
      }),
      harness.readiness
    );

    await waitFor(() =>
      expect(document.querySelector('[data-readiness-blocker="WORKSPACE_UNTRUSTED"]')).not.toBeNull()
    );
    const item = document.querySelector('[data-readiness-blocker="WORKSPACE_UNTRUSTED"]') as HTMLElement;
    expect(item.textContent).toContain("D:/work/demo");
    const jump = item.querySelector('[data-readiness-jump="WORKSPACE_UNTRUSTED"]') as HTMLElement;
    expect(jump.textContent).toContain(t("chat.readiness.section.permissions"));
  });

  it("全部满足：面板消失、输入区可用，发送直接用当前结论（不再逐条重探测）", async () => {
    const user = userEvent.setup();
    const harness = readinessBackend(READY_DISCOVERY);
    const onSendToSession = vi.fn();
    renderPage(shellValue({ input: "看看这个仓库", onSendToSession }), harness.readiness);

    await waitFor(() => expect(document.querySelector("[data-readiness-panel]")).toBeNull());
    const input = screen.getByLabelText(t("chat.composer.placeholder")) as HTMLTextAreaElement;
    expect(input.disabled).toBe(false);

    const before = harness.discoverPi.mock.calls.length;
    await user.click(screen.getByRole("button", { name: t("chat.composer.send") }));
    await waitFor(() => expect(onSendToSession).toHaveBeenCalledWith("sess-1", "看看这个仓库", []));
    // 发送不再重跑 DiscoverPi：那是要 spawn 探针的重活，每发一条都跑一次既慢
    // 又会让界面闪一次"正在检查发送前提"（2026-11-08 用户反馈）。
    // 结论只在 Pi/工作区/模型变化或用户点"重新检查"时更新。
    expect(harness.discoverPi.mock.calls.length).toBe(before);
  });

  it("内置 fixture + 活着的端点：不阻塞发送，只打徽标（脚本化回放）", async () => {
    // 这条守住"一键演示"路径：run-fixture.bat 会把 baseUrl 指向一个真实监听的
    // 本地 fixture 端点。它当然不是真模型，所以徽标必须显示；但把它也当阻塞项，
    // 就等于自己把整条链路演示挡死（2026-11-08 实测）。
    const harness = readinessBackend(READY_DISCOVERY);
    const liveSettings: Settings = { ...FIXTURE_SETTINGS, baseUrl: "http://127.0.0.1:64648/v1" };
    const onSendToSession = vi.fn();
    const user = userEvent.setup();
    renderPage(
      shellValue({
        settings: liveSettings,
        currentModel: { ...FIXTURE_MODEL, source: "settings" },
        input: "hi",
        onSendToSession,
      }),
      harness.readiness
    );

    await waitFor(() => expect(document.querySelector("[data-fixture-badge]")).not.toBeNull());
    expect(document.querySelector("[data-readiness-panel]")).toBeNull();
    const input = screen.getByLabelText(t("chat.composer.placeholder")) as HTMLTextAreaElement;
    expect(input.disabled).toBe(false);
    await user.click(screen.getByRole("button", { name: t("chat.composer.send") }));
    await waitFor(() => expect(onSendToSession).toHaveBeenCalled());
  });

  it("内置 fixture 显示徽标；换成真 provider 后消失", async () => {
    const harness = readinessBackend(READY_DISCOVERY);
    const fixtureShell = shellValue({ settings: FIXTURE_SETTINGS, currentModel: FIXTURE_MODEL, input: "hi" });
    const { rerender } = render(
      <ShellProvider value={fixtureShell}>
        <ChatPage
          onNavigate={() => undefined}
          route={routeFor("sess-1")}
          browserApprovals={NO_BROWSER_APPROVALS}
          browserApprovalPollMs={0}
          readiness={harness.readiness}
        />
      </ShellProvider>
    );

    await waitFor(() => expect(document.querySelector("[data-fixture-badge]")).not.toBeNull());
    expect(document.querySelector("[data-fixture-badge]")?.textContent).toContain(t("chat.composer.fixtureBadge"));

    rerender(
      <ShellProvider value={shellValue({ input: "hi" })}>
        <ChatPage
          onNavigate={() => undefined}
          route={routeFor("sess-1")}
          browserApprovals={NO_BROWSER_APPROVALS}
          browserApprovalPollMs={0}
          readiness={harness.readiness}
        />
      </ShellProvider>
    );
    await waitFor(() => expect(document.querySelector("[data-fixture-badge]")).toBeNull());
  });

  it("Pi 探测失败时不假装就绪，也不把原因吞掉", async () => {
    const harness = readinessBackend(new Error("probe failed"));
    renderPage(shellValue({ input: "hi" }), harness.readiness);

    await waitFor(() => expect(document.querySelector('[data-readiness-blocker="PI_UNKNOWN"]')).not.toBeNull());
    expect((screen.getByLabelText(t("chat.composer.placeholder")) as HTMLTextAreaElement).disabled).toBe(true);
  });
});
