// 对话页与消息流的组件测试（U02 / UI06）。
//
// 这里守的不是“画得像原型”，而是三件会真的出错的事：
//
//  1. 事件顺序：流式文本 → 工具卡 → 审批卡，按到达顺序出现，不按“好看的顺序”重排；
//  2. 失败就是失败：失败的一轮不能出现“已完成”的气泡，结论只来自终局事件；
//  3. 点击不等于完成：审批卡点击后仍在原地（只标“已提交”），澄清回答不产生审批。
//
// 界面状态全部由真实形状的后端事件经 runState 归约器算出来，测试里不手搓
// “看起来对”的 UiState——那样测的就不是事件驱动的行为了。

import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { t } from "../../i18n";
import { initialState, reduce, type UiState } from "../../runState";
import type { CallError, ProductEvent } from "../../types";
import { ShellProvider, type ShellValue } from "../../app/shell";
import { Layout } from "../../app/Layout";
import { readContract } from "../../design/tokens.files.mjs";
import { BrowserOcclusionProvider } from "../../app/BrowserOcclusionCoordinator";
import { browserApi } from "../browser/api";
import { browserStateFixture } from "../browser/testFixtures";

const backend = vi.hoisted(() => ({
  list: null as unknown as (...a: any[]) => Promise<any>,
  rename: null as unknown as (...a: any[]) => Promise<any>,
  setPinned: null as unknown as (...a: any[]) => Promise<any>,
  setArchived: null as unknown as (...a: any[]) => Promise<any>,
  remove: null as unknown as (...a: any[]) => Promise<any>,
  resolveAttachments: null as unknown as (...a: any[]) => Promise<any>,
  exportSession: null as unknown as (...a: any[]) => Promise<any>,
  runArtifacts: null as unknown as (...a: any[]) => Promise<any>,
  runDetails: null as unknown as (...a: any[]) => Promise<any>,
  artifact: null as unknown as (...a: any[]) => Promise<any>,
}));

vi.mock("./api", () => ({
  chatApi: {
    startSessionRun: vi.fn(),
    abort: vi.fn(),
    resolveApproval: vi.fn(),
    answerClarification: vi.fn(),
    replay: vi.fn(),
    resolveAttachments: (...a: any[]) => backend.resolveAttachments(...a),
    exportSession: (...a: any[]) => backend.exportSession(...a),
    runArtifacts: (...a: any[]) => backend.runArtifacts(...a),
    runDetails: (...a: any[]) => backend.runDetails(...a),
    artifact: (...a: any[]) => backend.artifact(...a),
  },
}));

vi.mock("../history/api", () => ({
  HISTORY_PAGE_SIZE: 20,
  historyApi: {
    list: (...a: any[]) => backend.list(...a),
    create: vi.fn(),
    rename: (...a: any[]) => backend.rename(...a),
    setPinned: (...a: any[]) => backend.setPinned(...a),
    setArchived: (...a: any[]) => backend.setArchived(...a),
    remove: (...a: any[]) => backend.remove(...a),
  },
}));

import { ChatPage } from "./ChatPage";
import { browserApprovalApi, type BrowserApprovalBackend, type BrowserApprovalItem } from "./browserApproval";

// ---------------------------------------------------------------- 事件 fixture

let seq = 0;

/** 构造一条后端形状的产品事件（字段与 Go 的 contracts.Event 一致）。 */
function ev(type: string, payload: unknown, opts: { runId?: string; seq?: number } = {}): ProductEvent {
  seq = opts.seq ?? seq + 1;
  return {
    schemaVersion: 1,
    eventId: `e${seq}`,
    workspaceId: "ws",
    sessionId: "sess-1",
    runId: opts.runId ?? "run-1",
    seq,
    type,
    occurredAt: "2026-11-06T00:00:00Z",
    payload,
  };
}

/** 用真实事件序列算出界面状态。 */
function stateFrom(events: ProductEvent[], firstText = "看看这个仓库"): UiState {
  let s = reduce(initialState, { kind: "user_message", text: firstText });
  s = reduce(s, { kind: "run_started", runId: "run-1", state: "running" });
  for (const e of events) s = reduce(s, { kind: "event", ev: e });
  return s;
}

const SESSION = {
  id: "sess-1",
  workspaceId: "ws-a",
  title: "验收会话",
  pinned: false,
  archived: false,
  kind: "chat",
  createdAt: "2026-11-06T00:00:00Z",
  updatedAt: "2026-11-06T00:00:00Z",
  revision: 3,
};

function page(rows: any[] = [SESSION]) {
  return { groups: [{ workspaceId: "ws-a", sessions: rows }], hasMore: false };
}

// ---------------------------------------------------------------- 壳层 fake

function shellValue(ui: UiState, overrides: Partial<ShellValue> = {}): ShellValue {
  const noop = () => undefined;
  return {
    backend: {
      status: null,
      settings: { piPath: "", nodePath: "", provider: "p", baseUrl: "", model: "m", apiKey: "", modelRef: "", workspace: "/w", shellAllowed: false },
      workspaces: [],
      currentWorkspace: null,
      profiles: [],
      models: [],
      modelRevision: 0,
      modelOptions: { apis: [], inputKinds: [], thinkingLevels: [] },
      currentModel: null,
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
    settings: { piPath: "", nodePath: "", provider: "p", baseUrl: "", model: "m", apiKey: "", modelRef: "", workspace: "/w", shellAllowed: false },
    workspaces: [],
    currentWorkspace: null,
    profiles: [],
    models: [],
    modelOptions: { apis: [], inputKinds: [], thinkingLevels: [] },
    modelRevision: 0,
    currentModel: null,
    skills: [],
    skillRevision: 0,
    skillsPending: false,
    skillsActiveRun: false,
    extensions: [],
    extensionRevision: 0,
    ui,
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

function renderPage(ui: UiState, overrides: Partial<ShellValue> = {}, browser?: FakeBrowserApprovals) {
  const value = shellValue(ui, overrides);
  // D2c：页头由 ChatPage portal 进壳层顶栏的插槽，所以页面必须与壳层一起渲染。
  // 单独渲染 ChatPage 量不到“整个路由只有一个 header”，那正是本任务的验收点。
  return render(
    <ShellProvider value={value}>
      <Layout
        status={null}
        current="chat"
        onNavigate={() => undefined}
        search={<input aria-label={t("search.title")} />}
        history={<div data-shell-history-fixture="" />}
        footer={<div data-shell-footer-fixture="" />}
        right={<div data-shell-right-fixture="" />}
        rightOpen={false}
        onToggleRight={() => undefined}
      >
        <ChatPage
          onNavigate={() => undefined}
          route={routeFor("sess-1")}
          browserApprovals={browser?.backend}
          browserApprovalPollMs={browser?.pollMs ?? 0}
          readiness={READINESS_BACKEND}
        />
      </Layout>
    </ShellProvider>
  );
}

/**
 * 浏览器审批的替身：pending() 返回可变的后端事实，resolve() 只记录调用。
 *
 * 故意不在这里自动移除条目：移除必须来自 pending() 的变化（后端事实），
 * 那正是本文件要守的纪律。
 */
interface FakeBrowserApprovals {
  backend: BrowserApprovalBackend;
  pollMs: number;
  pending: BrowserApprovalItem[];
  resolved: { approvalId: string; choice: string }[];
  failResolve: CallError | null;
}

function fakeBrowserApprovals(
  initial: BrowserApprovalItem[] = [],
  pollMs = 5
): FakeBrowserApprovals {
  const fake: FakeBrowserApprovals = {
    pending: initial,
    pollMs,
    resolved: [],
    failResolve: null,
    backend: {
      pending: async () => fake.pending,
      resolve: async (approvalId, choice) => {
        if (fake.failResolve) return fake.failResolve;
        fake.resolved.push({ approvalId, choice });
        return null;
      },
    },
  };
  return fake;
}

function browserApprovalFixture(overrides: Partial<BrowserApprovalItem> = {}): BrowserApprovalItem {
  return {
    approvalId: "bapr-1",
    sessionId: "sess-runtime-1",
    runId: "run-1",
    toolName: "browser_act",
    tabId: "tab-1",
    kind: "click",
    origin: "http://127.0.0.1:45123",
    url: "http://127.0.0.1:45123/form",
    navigationId: "nav-7",
    arguments: {
      tabId: "tab-1",
      kind: "click",
      snapshotId: "snap-1",
      ref: "e3",
      origin: "http://127.0.0.1:45123",
      url: "http://127.0.0.1:45123/form",
      navigationId: "nav-7",
    },
    expiresAtUnix: Math.floor(Date.now() / 1000) + 60,
    ...overrides,
  };
}

/**
 * 发送前提的替身（UX1）。
 *
 * 这个文件守的是事件顺序、审批与附件，不是 Pi 探测。探测结果固定为“Pi 可用”，
 * 其余前提（工作区/模型）照旧由 shell 的事实决定：不在这里伪造一个“全都就绪”。
 */
const READY_PI_CANDIDATE = {
  installation: {
    id: "managed:1.5.0",
    executable: "D:/pi/pi.exe",
    origin: "managed",
    architecture: "x86_64",
    runnable: true,
    version: "1.5.0",
  },
  origin: "managed",
  distribution: "standalone",
  executableHash: "a".repeat(64),
  launcherPath: "D:/pi/pi.exe",
  runtimeExecutable: "D:/pi/pi.exe",
  runtimeHash: "b".repeat(64),
  compatibility: "ready_verified",
  diagnostics: [],
};

const READINESS_BACKEND = {
  discoverPi: async () => ({
    state: "ready_verified",
    selected: READY_PI_CANDIDATE,
    candidates: [READY_PI_CANDIDATE],
    diagnostics: [],
  }),
};

/** 最小 RouteMatch：ChatPage 只读 id 参数。 */
function routeFor(id: string) {
  return {
    spec: { id: "chat", pattern: "/chat/:id", component: ChatPage, labelKey: "nav.chat", hintKey: "nav.hints.chat" },
    path: `/chat/${id}`,
    params: { id },
    query: new URLSearchParams(),
    fallback: false,
  } as unknown as Parameters<typeof ChatPage>[0]["route"];
}

beforeEach(() => {
  backend.list = async () => page();
  backend.rename = async () => ({ item: { ...SESSION, title: "新标题", revision: 4 } });
  backend.setPinned = async () => ({ item: { ...SESSION, pinned: true, revision: 4 } });
  backend.setArchived = async () => ({ item: { ...SESSION, archived: true, revision: 4 } });
  backend.remove = async () => ({});
  backend.resolveAttachments = async () => ({ sessionId: "sess-1", items: [], rejected: [] });
  backend.exportSession = async (_id: string, format: string) => ({
    sessionId: "sess-1",
    format,
    path: `/root/exports/sess-1.${format === "markdown" ? "md" : format}`,
    bytes: 2048,
    sha256: "b".repeat(64),
    records: 12,
    messages: 6,
    redactions: 2,
    writtenAt: "2026-11-06T00:00:00Z",
  });
  backend.runArtifacts = async () => ({ items: [] });
  backend.runDetails = async () => ({ details: emptyDetails() });
  backend.artifact = async () => ({ item: { id: "art-1", sha256: "a", verification: { status: "verified", checkedAt: "" } } });
});

export function emptyDetails() {
  return {
    runId: "run-1",
    usage: {
      availability: "unavailable",
      runs: 1,
      reportedRuns: 0,
      unreportedRuns: 1,
      tokens: { availability: "unavailable", reportedRuns: 0, unreportedRuns: 1 },
      cost: { availability: "unavailable", knownRuns: 0, unknownRuns: 1 },
    },
    artifacts: [],
    tools: [],
    approvals: [],
    compactions: [],
    diagnostics: [],
    errors: [],
    eventCount: 0,
    unavailable: [],
  };
}

// ---------------------------------------------------------------- 用例

describe("ChatPage 事件顺序", () => {
  it("按事件顺序渲染流式文本 → 工具卡 → 审批卡", async () => {
    const ui = stateFrom([
      ev("message.delta", { text: "我先跑一下测试" }),
      ev("tool.start", { toolCallId: "c1", toolName: "bash", args: { command: "go test ./..." } }),
      ev("approval.request", {
        approvalId: "apr-1",
        toolName: "bash",
        toolCallId: "c1",
        arguments: { command: "rm -rf build" },
        expiresAtUnix: 1_800_000_000,
      }),
    ]);
    renderPage(ui);

    await waitFor(() => expect(screen.getByText("验收会话")).toBeInTheDocument());

    const text = document.querySelector("[data-message-text]");
    const tool = document.querySelector("[data-tool-card]");
    const approval = document.querySelector("[data-approval-card]");
    expect(text).not.toBeNull();
    expect(tool).not.toBeNull();
    expect(approval).not.toBeNull();

    // DOM 顺序 = 事件顺序（Node.DOCUMENT_POSITION_FOLLOWING = 4）。
    expect(text!.compareDocumentPosition(tool!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(tool!.compareDocumentPosition(approval!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(within(approval as HTMLElement).getByText("bash")).toBeInTheDocument();
  });

  it("失败的一轮不显示“已完成”，并给出错误码与下一步", async () => {
    const ui = stateFrom([
      ev("message.delta", { text: "输出了一段" }),
      ev("run.error", { code: "PROVIDER_ERROR", message: "配额用尽" }),
      ev("run.state", { state: "failed" }),
    ]);
    renderPage(ui);

    await waitFor(() => expect(screen.getByText("验收会话")).toBeInTheDocument());
    expect(screen.queryByText(t("status.completed"))).toBeNull();
    expect(screen.getByText(t("status.failed"))).toBeInTheDocument();
    expect(document.querySelector("[data-settled='failed']")).not.toBeNull();
    expect(document.querySelector("[data-run-error]")).not.toBeNull();
    const banner = document.querySelector("[data-run-error]") as HTMLElement;
    // 横幅里同时有错误码与后端给的原文：两者都要可见，不能只剩一句“失败”。
    expect(banner.textContent).toContain("PROVIDER_ERROR");
    expect(banner.textContent).toContain("配额用尽");
    expect(screen.getByText(t("chat.error.hintProvider"))).toBeInTheDocument();
  });

  it("点击审批只提交，卡片留在原处并标成已提交", async () => {
    const user = userEvent.setup();
    const onDecide = vi.fn();
    const ui = stateFrom([
      ev("approval.request", {
        approvalId: "apr-1",
        toolName: "bash",
        toolCallId: "c1",
        arguments: { command: "ls" },
        expiresAtUnix: 1_800_000_000,
      }),
    ]);
    renderPage(ui, { onDecide });

    await waitFor(() => expect(screen.getByText("验收会话")).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: t("chat.approval.once") }));

    expect(onDecide).toHaveBeenCalledTimes(1);
    expect(onDecide.mock.calls[0][1]).toBe("once");
    // 关键：组件自己不移除——卡片必须等后端 approval.resolved。
    expect(document.querySelector("[data-approval-card='apr-1']")).not.toBeNull();
  });

  it("已提交的审批仍留在原地、按钮禁用，但明确写着“等后端结论”", async () => {
    const pending = stateFrom([
      ev("approval.request", {
        approvalId: "apr-1",
        toolName: "bash",
        toolCallId: "c1",
        arguments: { command: "ls" },
        expiresAtUnix: 1_800_000_000,
      }),
    ]);
    // 点击之后界面拿到的就是这份状态：只标“已提交”，没有移除。
    const submitted = reduce(pending, { kind: "approval_submitted", approvalId: "apr-1" });
    renderPage(submitted);

    await waitFor(() => expect(document.querySelector("[data-approval-card='apr-1']")).not.toBeNull());
    const card = document.querySelector("[data-approval-card='apr-1']") as HTMLElement;
    expect(card.getAttribute("data-submitted")).toBe("true");
    expect(card.textContent).toContain(t("chat.approval.submitted"));
    expect(within(card).getByRole("button", { name: t("chat.approval.once") })).toBeDisabled();
    expect(within(card).getByRole("button", { name: t("chat.approval.deny") })).toBeDisabled();
  });

  it("后端已收口（移除）之后卡片才消失", async () => {
    const pending = stateFrom([
      ev("approval.request", {
        approvalId: "apr-1",
        toolName: "bash",
        toolCallId: "c1",
        arguments: {},
        expiresAtUnix: 1,
      }),
    ]);
    const { rerender } = renderPage(pending);
    await waitFor(() => expect(document.querySelector("[data-approval-card='apr-1']")).not.toBeNull());

    const resolved = reduce(pending, {
      kind: "event",
      ev: ev("approval.resolved", { approvalId: "apr-1", choice: "deny", reason: "run_settled" }),
    });
    rerender(
      <ShellProvider value={shellValue(resolved)}>
        <ChatPage onNavigate={() => undefined} route={routeFor("sess-1")} readiness={READINESS_BACKEND} />
      </ShellProvider>
    );
    await waitFor(() => expect(document.querySelector("[data-approval-card='apr-1']")).toBeNull());
  });

  it("澄清卡只来自 clarification.requested，回答不产生任何审批", async () => {
    const user = userEvent.setup();
    const onAnswerClarification = vi.fn();
    const ui = stateFrom([
      ev("message.delta", { text: "要用 staging 还是 prod？" }),
      ev("clarification.requested", {
        requestId: "ui-1",
        kind: "confirm",
        question: "确定要用 staging 吗？",
        options: [],
      }),
    ]);
    renderPage(ui, { onAnswerClarification });

    await waitFor(() => expect(screen.getByText("验收会话")).toBeInTheDocument());
    expect(document.querySelector("[data-clarification-card='ui-1']")).not.toBeNull();
    // 澄清不是审批：页面上不该有审批卡，也不该有“允许一次”的按钮。
    expect(document.querySelector("[data-approval-card]")).toBeNull();
    expect(screen.queryByRole("button", { name: t("chat.approval.once") })).toBeNull();

    await user.click(screen.getByRole("button", { name: t("chat.clarification.confirmYes") }));
    expect(onAnswerClarification).toHaveBeenCalledWith("ui-1", { confirmed: true });
  });
});

describe("ChatPage 会话归属", () => {
  it("另一个会话的运行不在本页显示它的审批卡", async () => {
    const ui = stateFrom([
      ev("approval.request", {
        approvalId: "apr-1",
        toolName: "bash",
        toolCallId: "c1",
        arguments: {},
        expiresAtUnix: 1_800_000_000,
      }),
    ]);
    // 归属是另一个会话：模拟“那一页正在跑、我切到这一页”。
    const other = { ...ui, activeSessionId: "sess-other" };
    renderPage(other);
    await waitFor(() => expect(screen.getByText("验收会话")).toBeInTheDocument());
    expect(document.querySelector("[data-other-session-run]")).not.toBeNull();
    expect(document.querySelector("[data-approval-card]")).toBeNull();
  });
});

describe("ChatPage 附件与导出", () => {
  it("附件逐条显示后端拒绝原因，而不是全部失败", async () => {
    const user = userEvent.setup();
    backend.resolveAttachments = async () => ({
      sessionId: "sess-1",
      items: [
        {
          id: "att-1",
          displayName: "a.md",
          size: 128,
          sha256: "c".repeat(64),
          mediaType: "text/markdown",
          status: "ready",
          expiresAt: "2026-11-07T00:00:00Z",
        },
      ],
      rejected: [
        {
          path: "/etc/shadow",
          displayName: "shadow",
          code: "UNSUPPORTED",
          message: "目标不在允许的范围内",
        },
      ],
    });
    renderPage(stateFrom([]));

    await waitFor(() => expect(screen.getByText("验收会话")).toBeInTheDocument());
    // 附件入口在「+」打开的抽屉里（原型的 attachment-drawer）。
    await user.click(screen.getByRole("button", { name: t("chat.composer.attachmentsMenu") }));
    const input = screen.getByLabelText(t("chat.composer.pathPlaceholder"));
    await user.type(input, "/etc/shadow");
    await user.click(screen.getByRole("button", { name: t("chat.composer.addFile") }));

    await waitFor(() => expect(document.querySelector("[data-attachment-accepted='att-1']")).not.toBeNull());
    const rejected = document.querySelector("[data-attachment-rejected='UNSUPPORTED']");
    expect(rejected).not.toBeNull();
    expect(within(rejected as HTMLElement).getByText(/UNSUPPORTED/)).toBeInTheDocument();
    // 被接收的那一条也还在（一个坏文件不能把整批变成失败）。
    expect(screen.getByText("a.md")).toBeInTheDocument();
  });

  it("导出调用后端并显示写入路径与指纹", async () => {
    const user = userEvent.setup();
    const exportSpy = vi.fn(async (_id: string, format: string) => ({
      sessionId: "sess-1",
      format,
      path: `/root/exports/sess-1.${format}`,
      bytes: 2048,
      sha256: "b".repeat(64),
      records: 12,
      messages: 6,
      redactions: 2,
      writtenAt: "2026-11-06T00:00:00Z",
    }));
    backend.exportSession = exportSpy;
    renderPage(stateFrom([]));

    await waitFor(() => expect(screen.getByText("验收会话")).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: t("chat.export.label") }));
    await user.click(screen.getByRole("menuitem", { name: `${t("chat.export.label")} · ${t("chat.export.markdown")}` }));

    await waitFor(() => expect(exportSpy).toHaveBeenCalledWith("sess-1", "markdown", ""));
    await waitFor(() => expect(document.querySelector("[data-export-path]")).not.toBeNull());
    expect(document.querySelector("[data-export-path]")?.textContent).toBe("/root/exports/sess-1.markdown");
    expect(screen.getByText(/2\.0 KB/)).toBeInTheDocument();
  });

  it("revision 过期时露出 CONFIG_CONFLICT 并给出重新读取", async () => {
    const user = userEvent.setup();
    const conflict: CallError = { code: "CONFIG_CONFLICT", message: "stale", retryable: false };
    backend.rename = async () => ({ error: conflict });
    renderPage(stateFrom([]));

    await waitFor(() => expect(screen.getByText("验收会话")).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: t("chat.head.more") }));
    await user.click(screen.getByRole("menuitem", { name: t("chat.sessionActions.rename") }));
    const input = screen.getByLabelText(t("chat.sessionActions.renamePlaceholder"));
    await user.clear(input);
    await user.type(input, "新的标题");
    await user.click(screen.getByRole("button", { name: t("chat.sessionActions.save") }));

    await waitFor(() => expect(document.querySelector("[data-session-action-error]")).not.toBeNull());
    expect(screen.getByText("CONFIG_CONFLICT")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("chat.sessionActions.reload") })).toBeInTheDocument();
  });
});

// ---------------------------------------------------------------- 右栏浏览器面板（W01）
//
// 面板的后端事实用 features/browser/testFixtures 的 fixture（形状 = Go 侧
// BrowserStateResult 的镜像）。这里守的是**接入**：聊天页真的把面板挂进了浏览器
// 分区、状态真的来自 BrowserState()、没有宿主时如实显示、抽屉开关不重挂载、
// 审批/澄清卡真的登记了遮挡。

/** 真实视口的替代：useNarrowLayout 读的就是 window.matchMedia。 */
function stubMatchMedia(narrow: boolean) {
  const listeners = new Set<() => void>();
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    writable: true,
    value: (query: string) => ({
      matches: narrow,
      media: query,
      onchange: null,
      addEventListener: (_: string, cb: () => void) => listeners.add(cb),
      removeEventListener: (_: string, cb: () => void) => listeners.delete(cb),
      addListener: () => undefined,
      removeListener: () => undefined,
      dispatchEvent: () => false,
    }),
  });
}

describe("ChatPage 右栏浏览器面板", () => {
  const originalMatchMedia = window.matchMedia;

  afterEach(() => {
    vi.restoreAllMocks();
    Object.defineProperty(window, "matchMedia", {
      configurable: true,
      writable: true,
      value: originalMatchMedia,
    });
  });

  it("浏览器分区挂的是真实面板，tabs 与控制态来自 BrowserState 绑定", async () => {
    const stateSpy = vi.spyOn(browserApi, "state").mockResolvedValue(browserStateFixture());
    const slotSpy = vi.spyOn(browserApi, "updateSlot").mockResolvedValue(null);

    renderPage(stateFrom([]));

    // 挂载路径：聊天页 → 上下文面板的浏览器分区 → BrowserPanel。
    await waitFor(() =>
      expect(document.querySelector("[data-context-section='browser'] [data-browser-panel]")).not.toBeNull()
    );
    // 旧的“没有浏览器状态接口”占位不再出现在聊天页上。
    expect(document.querySelector("[data-browser-section='unavailable']")).toBeNull();
    expect(stateSpy).toHaveBeenCalled();

    // tab 列表来自后端事实，不由 React 自己造。
    const tab = document.querySelector("[data-browser-tab='tab-1']") as HTMLElement;
    expect(tab).not.toBeNull();
    expect(tab.textContent).toContain("https://example.test/form");

    // 控制态也来自同一个快照（human = 用户已接管，可恢复）。
    expect(document.querySelector("[data-browser-control='human']")).not.toBeNull();
    expect(screen.getByRole("button", { name: t("browser.control.resume") })).toBeEnabled();

    // 槽位宣称的可用性 = 后端确实挂上了原生宿主。
    const slot = document.querySelector("[data-browser-slot]") as HTMLElement;
    expect(slot.getAttribute("data-browser-slot-state")).toBe("available");
    // 面板挂载后会把这块矩形报给后端（原生视图要摆到这里）。
    await waitFor(() => expect(slotSpy).toHaveBeenCalled());
  });

  it("没有原生宿主时如实显示 unavailable 与原因，不画假标签页", async () => {
    vi.spyOn(browserApi, "state").mockResolvedValue(
      browserStateFixture({
        available: false,
        tabs: [],
        activeTab: "",
        visible: false,
        requestedVisible: false,
        unsupported: [
          {
            capability: "surface",
            reason: "没有挂载原生宿主：右侧不显示网页（不是加载失败，也不是空页面）",
          },
        ],
      })
    );
    vi.spyOn(browserApi, "updateSlot").mockResolvedValue(null);

    renderPage(stateFrom([]));

    const slot = await waitFor(() => {
      const node = document.querySelector("[data-browser-slot]") as HTMLElement;
      expect(node).not.toBeNull();
      return node;
    });
    expect(slot.getAttribute("data-browser-slot-state")).toBe("unavailable");
    // 原因由后端给出：这是“没有宿主”，不是“加载失败”，也不是“没有数据”。
    const reason = document.querySelector("[data-browser-unavailable-reason='surface']");
    expect(reason?.textContent).toContain("没有挂载原生宿主");
    expect(document.querySelector("[data-browser-tabs='empty']")).not.toBeNull();
  });

  it("窄窗抽屉开关只改布局：面板与原生槽位节点不重建", async () => {
    stubMatchMedia(true);
    vi.spyOn(browserApi, "state").mockResolvedValue(browserStateFixture());
    vi.spyOn(browserApi, "updateSlot").mockResolvedValue(null);

    const user = userEvent.setup();
    renderPage(stateFrom([]));

    await waitFor(() => expect(document.querySelector("[data-browser-slot]")).not.toBeNull());
    // 进入窄窗时面板默认收起：收起是隐藏，不是卸载。
    const panel = document.querySelector("[data-context-panel]") as HTMLElement;
    await waitFor(() => expect(panel.getAttribute("data-mode")).toBe("drawer"));
    expect(panel.getAttribute("data-open")).toBe("false");

    const slotNode = document.querySelector("[data-browser-slot]");
    const panelNode = document.querySelector("[data-browser-panel]");

    await user.click(screen.getByRole("button", { name: t("chat.context.openPanel") }));
    await waitFor(() => expect(panel.getAttribute("data-open")).toBe("true"));

    // 同一个 DOM 节点：原生子视图的宿主没有被重建，tab 列表也还在。
    expect(document.querySelector("[data-browser-slot]")).toBe(slotNode);
    expect(document.querySelector("[data-browser-panel]")).toBe(panelNode);
    expect(document.querySelector("[data-browser-tab='tab-1']")).not.toBeNull();

    // 再收起一次：仍然是隐藏。
    await user.click(screen.getAllByRole("button", { name: t("chat.context.closePanel") })[0]);
    await waitFor(() => expect(panel.getAttribute("data-open")).toBe("false"));
    expect(document.querySelector("[data-browser-slot]")).toBe(slotNode);
  });

  it("审批卡与澄清卡挂载时登记遮挡层，消失后释放", async () => {
    vi.spyOn(browserApi, "state").mockResolvedValue(browserStateFixture());
    vi.spyOn(browserApi, "updateSlot").mockResolvedValue(null);

    const layers: Array<{ source: string; open: boolean }> = [];
    const occlusion = {
      setOverlay: async (source: string, open: boolean) => {
        layers.push({ source, open });
        return null;
      },
    };
    const tree = (ui: UiState) => (
      <BrowserOcclusionProvider api={occlusion}>
        <ShellProvider value={shellValue(ui)}>
          <ChatPage onNavigate={() => undefined} route={routeFor("sess-1")} readiness={READINESS_BACKEND} />
        </ShellProvider>
      </BrowserOcclusionProvider>
    );

    const pending = stateFrom([
      ev("approval.request", {
        approvalId: "apr-1",
        toolName: "bash",
        toolCallId: "c1",
        arguments: {},
        expiresAtUnix: 1_800_000_000,
      }),
      ev("clarification.requested", {
        requestId: "ui-1",
        kind: "confirm",
        question: "确定要用 staging 吗？",
        options: [],
      }),
    ]);
    const { rerender } = render(tree(pending));

    // 两张卡都是盖在原生网页之上的应用浮层：各自按名字登记。
    await waitFor(() => {
      expect(layers).toContainEqual({ source: "approval-card", open: true });
      expect(layers).toContainEqual({ source: "clarification-card", open: true });
    });

    const afterApproval = reduce(pending, {
      kind: "event",
      ev: ev("approval.resolved", { approvalId: "apr-1", choice: "deny", reason: "run_settled" }),
    });
    const resolved = reduce(afterApproval, {
      kind: "event",
      ev: ev("clarification.resolved", { requestId: "ui-1", answered: true }),
    });
    rerender(tree(resolved));

    await waitFor(() => {
      expect(layers).toContainEqual({ source: "approval-card", open: false });
      expect(layers).toContainEqual({ source: "clarification-card", open: false });
    });
    // 卡片消失后遮挡必须解除：否则原生网页会一直看不见。
    expect(document.querySelector("[data-approval-card]")).toBeNull();
    expect(document.querySelector("[data-clarification-card]")).toBeNull();
  });
});



// ---------------------------------------------------------------- 浏览器审批（M1）

describe("浏览器写动作审批（M1）", () => {
  it("在审批流里渲染工具名、目标 origin、动作摘要、范围与到期时间", async () => {
    const browser = fakeBrowserApprovals([browserApprovalFixture()]);
    renderPage(stateFrom([]), {}, browser);

    await waitFor(() =>
      expect(document.querySelector("[data-browser-approval-card='bapr-1']")).not.toBeNull()
    );
    const card = document.querySelector("[data-browser-approval-card='bapr-1']") as HTMLElement;

    expect(within(card).getByText("browser_act")).toBeInTheDocument();
    // 目标站点：用户批的是"这个 origin"的这次动作。
    const origin = card.querySelector("[data-browser-approval-origin]") as HTMLElement;
    expect(origin.getAttribute("data-browser-approval-origin")).toBe("http://127.0.0.1:45123");
    // 动作摘要：kind/ref + 文档身份（navigationId 变了，旧审批不再成立）。
    const summary = card.querySelector("[data-browser-approval-summary]") as HTMLElement;
    expect(summary.textContent).toContain("click");
    expect(summary.textContent).toContain("ref=e3");
    expect(summary.textContent).toContain("nav=nav-7");
    // 范围与到期时间都是用户判断"批多久、到什么时候"所需的事实。
    const scope = card.querySelector("[data-browser-approval-scope]") as HTMLElement;
    expect(scope.textContent).toBe(t("chat.browserApproval.scopeHint"));
    expect(card.querySelector("[data-browser-approval-expires]")).not.toBeNull();
    // 三个结论按钮都在，且文案来自 catalog。
    expect(within(card).getByRole("button", { name: t("chat.approval.once") })).toBeEnabled();
    expect(within(card).getByRole("button", { name: t("chat.approval.session") })).toBeEnabled();
    expect(within(card).getByRole("button", { name: t("chat.approval.deny") })).toBeEnabled();
  });

  it("点击只提交结论：后端仍列出这条时卡片留在原地并标已提交", async () => {
    const user = userEvent.setup();
    // pollMs=0：只做挂载时的第一次读取，之后没有新的后端事实。
    //
    // "提交不得乐观移除"这条语义由 browserApproval.test.ts 的归约器用例确定地守住：
    // 提交后紧跟着一次重读（decide() 里的 refresh）会把"乐观移除 + 重读"重新拉回
    // 同样的可见状态，所以组件测试在这里只能断言可见事实（卡片在、标已提交、无结论行）。
    const browser = fakeBrowserApprovals([browserApprovalFixture()], 0);
    renderPage(stateFrom([]), {}, browser);

    await waitFor(() =>
      expect(document.querySelector("[data-browser-approval-card='bapr-1']")).not.toBeNull()
    );
    await user.click(screen.getByRole("button", { name: t("chat.approval.once") }));

    // 结论确实提交给后端了（once）。
    await waitFor(() => expect(browser.resolved).toEqual([{ approvalId: "bapr-1", choice: "once" }]));
    // 关键：后端没有任何新事实 → 卡片必须留在原地，只标"已提交"。
    const card = document.querySelector("[data-browser-approval-card='bapr-1']") as HTMLElement;
    expect(card).not.toBeNull();
    expect(card.getAttribute("data-submitted")).toBe("true");
    expect(card.textContent).toContain(t("chat.approval.submitted"));
    expect(within(card).getByRole("button", { name: t("chat.approval.once") })).toBeDisabled();
    // 也还没有任何"结论"行：结论只能来自后端事实变化。
    expect(document.querySelector("[data-browser-approval-note]")).toBeNull();
    expect(browser.resolved).toHaveLength(1);
  });

  it("后端不再列出这条之后才移除，并留下已批准的原因", async () => {
    const user = userEvent.setup();
    const browser = fakeBrowserApprovals([browserApprovalFixture()], 5);
    renderPage(stateFrom([]), {}, browser);

    await waitFor(() =>
      expect(document.querySelector("[data-browser-approval-card='bapr-1']")).not.toBeNull()
    );
    await user.click(screen.getByRole("button", { name: t("chat.approval.once") }));
    await waitFor(() => expect(browser.resolved).toHaveLength(1));

    // 后端事实变化（不再待审批）之后才移除，并留下原因。
    browser.pending = [];
    await waitFor(() =>
      expect(document.querySelector("[data-browser-approval-card='bapr-1']")).toBeNull()
    );
    const note = document.querySelector("[data-browser-approval-note='approved']") as HTMLElement;
    expect(note).not.toBeNull();
    expect(note.textContent).toContain("browser_act");
    expect(note.textContent).toContain("http://127.0.0.1:45123");
  });

  it("拒绝之后卡片消失并写明没有派发任何输入", async () => {
    const user = userEvent.setup();
    const browser = fakeBrowserApprovals([browserApprovalFixture()]);
    renderPage(stateFrom([]), {}, browser);

    await waitFor(() =>
      expect(document.querySelector("[data-browser-approval-card='bapr-1']")).not.toBeNull()
    );
    await user.click(screen.getByRole("button", { name: t("chat.approval.deny") }));
    await waitFor(() => expect(browser.resolved).toEqual([{ approvalId: "bapr-1", choice: "deny" }]));

    // 后端已不再列这条：卡片消失，并且必须说明"没有派发任何输入"。
    browser.pending = [];
    await waitFor(() =>
      expect(document.querySelector("[data-browser-approval-card='bapr-1']")).toBeNull()
    );
    const note = document.querySelector("[data-browser-approval-note='denied']") as HTMLElement;
    expect(note).not.toBeNull();
    expect(note.textContent).toContain(t("chat.browserApproval.noteDenied", {
      tool: "browser_act",
      origin: "http://127.0.0.1:45123",
    }));
  });

  it("过期消失时给出原因，而不是让卡片默默消失", async () => {
    const expired = browserApprovalFixture({
      expiresAtUnix: Math.floor(Date.now() / 1000) - 1,
    });
    const browser = fakeBrowserApprovals([expired]);
    renderPage(stateFrom([]), {}, browser);

    await waitFor(() =>
      expect(document.querySelector("[data-browser-approval-card='bapr-1']")).not.toBeNull()
    );
    // 后端把过期条目清掉（PendingApprovals 会顺手清理）。
    browser.pending = [];
    await waitFor(() =>
      expect(document.querySelector("[data-browser-approval-note='expired']")).not.toBeNull()
    );
    const note = document.querySelector("[data-browser-approval-note='expired']") as HTMLElement;
    expect(note.textContent).toContain(t("chat.browserApproval.noteExpired", {
      tool: "browser_act",
      origin: "http://127.0.0.1:45123",
    }));
    // 过期不是"已批准"：不能出现批准结论。
    expect(document.querySelector("[data-browser-approval-note='approved']")).toBeNull();
  });

  it("提交失败时把原因留在卡片上，卡片不消失", async () => {
    const user = userEvent.setup();
    const browser = fakeBrowserApprovals([browserApprovalFixture()]);
    browser.failResolve = { code: "CONFIG_MISSING", message: "浏览器授权服务不可用：审批无法答复", retryable: false };
    renderPage(stateFrom([]), {}, browser);

    await waitFor(() =>
      expect(document.querySelector("[data-browser-approval-card='bapr-1']")).not.toBeNull()
    );
    await user.click(screen.getByRole("button", { name: t("chat.approval.once") }));

    await waitFor(() =>
      expect(document.querySelector("[data-browser-approval-error='CONFIG_MISSING']")).not.toBeNull()
    );
    // 失败之后卡片仍然在（可以重试），并且标成"未提交"。
    const card = document.querySelector("[data-browser-approval-card='bapr-1']") as HTMLElement;
    expect(card.getAttribute("data-submitted")).toBe("false");
    expect(within(card).getByRole("button", { name: t("chat.approval.once") })).toBeEnabled();
  });

  it("产品装配不传 prop 时也轮询真实绑定：答复走 ResolveBrowserApproval", async () => {
    // 用户实测的“每次都等 60 秒后被拒绝”就出在这里：如果页面必须靠上层传 prop
    // 才会轮询，而 App.tsx 不传，待审批卡片永远不会出现。
    // 这条测试**故意不传** browserApprovals，证明 ChatPage 自己就用默认绑定。
    const user = userEvent.setup();
    const pendingSpy = vi
      .spyOn(browserApprovalApi, "pending")
      .mockResolvedValue([browserApprovalFixture()]);
    const resolveSpy = vi.spyOn(browserApprovalApi, "resolve").mockResolvedValue(undefined);

    render(
      <ShellProvider value={shellValue(stateFrom([]))}>
        <ChatPage
          onNavigate={() => undefined}
          route={routeFor("sess-1")}
          // 不传 browserApprovals
          browserApprovalPollMs={0}
          readiness={READINESS_BACKEND}
        />
      </ShellProvider>
    );

    await waitFor(() =>
      expect(document.querySelector("[data-browser-approval-card='bapr-1']")).not.toBeNull()
    );
    expect(pendingSpy).toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: t("chat.approval.once") }));
    await waitFor(() => expect(resolveSpy).toHaveBeenCalledWith("bapr-1", "once"));
    // 答复之后立刻重读一次（不做乐观移除）。
    expect(pendingSpy.mock.calls.length).toBeGreaterThanOrEqual(2);

    resolveSpy.mockRestore();
    pendingSpy.mockRestore();
  });

  it("一次只允许一个读取在飞：轮询周期内的多次渲染不会重复请求", async () => {
    let calls = 0;
    let release: () => void = () => undefined;
    const backend: BrowserApprovalBackend = {
      pending: () => {
        calls += 1;
        return new Promise((resolve) => {
          release = () => resolve([browserApprovalFixture()]);
        });
      },
      resolve: async () => undefined,
    };

    const ui = stateFrom([]);
    const view = (marker: number) => (
      <ShellProvider value={shellValue(ui, { input: String(marker) })}>
        <ChatPage
          onNavigate={() => undefined}
          route={routeFor("sess-1")}
          browserApprovals={backend}
          browserApprovalPollMs={5}
          readiness={READINESS_BACKEND}
        />
      </ShellProvider>
    );

    const { rerender } = render(view(0));
    await waitFor(() => expect(calls).toBe(1));

    // 读取还挂着的时候反复渲染 + 等两个轮询周期：不许再发第二个请求。
    rerender(view(1));
    rerender(view(2));
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 60));
    });
    expect(calls).toBe(1);

    release();
    await waitFor(() =>
      expect(document.querySelector("[data-browser-approval-card='bapr-1']")).not.toBeNull()
    );
    // 读取落地之后轮询继续（不是"一次就停"）。
    await waitFor(() => expect(calls).toBeGreaterThanOrEqual(2));
  });

  it("别的会话的 run 在本页时不显示浏览器审批", async () => {
    const browser = fakeBrowserApprovals([browserApprovalFixture()]);
    const ui = stateFrom([]);
    // activeSessionId 指向另一个会话：本页不能替它批准任何东西。
    renderPage({ ...ui, activeSessionId: "sess-elsewhere" }, {}, browser);

    await waitFor(() => expect(screen.getByText("验收会话")).toBeInTheDocument());
    expect(document.querySelector("[data-browser-approval-card]")).toBeNull();
    expect(browser.resolved).toHaveLength(0);
  });
});

// ---------------------------------------------------------------- 骨架与几何契约（D2b）
//
// 这一组守的是**结构**：原型 chat.html 的容器链、页头动作、输入区解剖、右栏抽屉。
// 像素数字（760 / 400 / 26 / 22 / 9px 13px）在 jsdom 里量不到（不解析外部 CSS），
// 那部分由 `chat-diff.mjs` 在构建产物上实测并出 chat-diff.json；这里守的是
// 「盒子存在、层级正确、可访问名字对、顺序对」——几何错了会先在这里断。

describe("ChatPage 骨架（D2b）", () => {
  const originalMatchMedia = window.matchMedia;

  afterEach(() => {
    Object.defineProperty(window, "matchMedia", {
      configurable: true,
      writable: true,
      value: originalMatchMedia,
    });
  });

  it("容器链：页头在壳层顶栏插槽里 → layout(rail=open) → thread-col → thread → thread-inner → composer", async () => {
    renderPage(stateFrom([ev("message.delta", { text: "先跑一下测试" })]));
    await waitFor(() => expect(screen.getByText("验收会话")).toBeInTheDocument());

    const page = document.querySelector("[data-chat-page]") as HTMLElement;
    expect(page).not.toBeNull();

    // D2c：页头不再是页面里的一条带子，而是壳层 52px 顶栏插槽里的内容。
    const topbar = document.querySelector('[data-shell-region="topbar"]') as HTMLElement;
    const head = topbar.querySelector("[data-chat-head]") as HTMLElement;
    const layout = page.querySelector("[data-chat-layout]") as HTMLElement;
    expect(topbar.querySelector("[data-topbar-slot]")).not.toBeNull();
    expect(head).not.toBeNull();
    expect(layout).not.toBeNull();
    // 页头在布局之前（原型的 chat-head-main 在 .chat-layout 之上）。
    expect(head.compareDocumentPosition(layout) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    // 宽窗默认开栏：列宽由 data-rail 决定（1fr 400px）。
    expect(layout.getAttribute("data-rail")).toBe("open");

    const threadCol = layout.querySelector("[data-chat-thread-col]") as HTMLElement;
    expect(threadCol).not.toBeNull();
    const thread = threadCol.querySelector("[data-chat-thread]") as HTMLElement;
    const composer = threadCol.querySelector("[data-composer]") as HTMLElement;
    expect(thread).not.toBeNull();
    expect(composer).not.toBeNull();
    // 输入区在线程之后（同一个列里，滚动区只在线程上）。
    expect(thread.compareDocumentPosition(composer) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

    const inner = thread.querySelector("[data-thread-inner]") as HTMLElement;
    expect(inner).not.toBeNull();
    // 26px 的消息节奏来自 .thread-inner 的 flex gap：消息必须是它的直接孩子，
    // 不能套一层带自己间距的 wrapper（那会把节奏吃掉）。
    const list = inner.querySelector("[data-message-list]") as HTMLElement;
    expect(list).not.toBeNull();
    expect(list.parentElement).toBe(inner);
    expect(list.className).toContain("achat-msg-list");
  });

  it("消息节奏：用户是右侧气泡、助手块带头像/名字/模型，工具卡挂在助手块里", async () => {
    const ui = stateFrom([
      ev("message.delta", { text: "我先跑一下测试" }),
      ev("tool.start", { toolCallId: "c1", toolName: "bash", args: { command: "go test ./..." } }),
    ]);
    renderPage(ui, {
      currentModel: { provider: "deepseek", model: "deepseek-v4.1-flash" } as any,
    });
    await waitFor(() => expect(screen.getByText("验收会话")).toBeInTheDocument());

    const user = document.querySelector('[data-role="user"]') as HTMLElement;
    expect(user).not.toBeNull();
    expect(user.className).toContain("achat-msg");
    const bubble = user.querySelector("[data-message-text]") as HTMLElement;
    expect(bubble).not.toBeNull();
    expect(bubble.className).toContain("achat-bubble");
    // 用户消息不画头像/工具卡
    expect(user.querySelector("[data-tool-card]")).toBeNull();

    const assistant = document.querySelector('[data-role="assistant"]') as HTMLElement;
    expect(assistant).not.toBeNull();
    expect(assistant.querySelector(".achat-agent-badge")).not.toBeNull();
    expect(assistant.querySelector(".achat-agent-name")?.textContent).toBe(t("chat.role.assistant"));
    // 模型来自启动快照（真实事实），不是编的标签
    expect(assistant.querySelector(".achat-agent-model")?.textContent).toBe(
      "deepseek / deepseek-v4.1-flash"
    );
    expect(assistant.querySelector("[data-tool-card]")).not.toBeNull();
    // 正文是 prose 而不是气泡（助手块整宽）
    const prose = assistant.querySelector("[data-message-text]") as HTMLElement;
    expect(prose.className).toContain("achat-prose");
  });

  it("页头动作：待办徽标显示真实计数，计数为 0 时不出现", async () => {
    // 没有待办：两个徽标都不该出现（不做“0 项待授权”的噪音）。
    renderPage(stateFrom([]));
    await waitFor(() => expect(screen.getByText("验收会话")).toBeInTheDocument());
    expect(document.querySelector("[data-chat-clarification-badge]")).toBeNull();
    expect(document.querySelector("[data-chat-approval-badge]")).toBeNull();
    expect(document.querySelector("[data-chat-head] [data-session-title]")?.textContent).toBe("验收会话");
  });

  it("页头动作：有待澄清/待授权时给出计数，点一下跳到对应卡片", async () => {
    const user = userEvent.setup();
    const ui = stateFrom([
      ev("approval.request", {
        approvalId: "apr-1",
        toolName: "bash",
        toolCallId: "c1",
        arguments: { command: "rm -rf build" },
        expiresAtUnix: 1_800_000_000,
      }),
      ev("clarification.requested", {
        requestId: "ui-1",
        method: "select",
        question: "用哪个环境？",
        options: ["staging", "prod"],
      }),
    ]);
    renderPage(ui);
    await waitFor(() => expect(screen.getByText("验收会话")).toBeInTheDocument());

    // 审批与澄清都来自后端事件，徽标的数字就是它们的条数。
    const approvalBadge = document.querySelector("[data-chat-approval-badge]") as HTMLElement;
    const clarificationBadge = document.querySelector("[data-chat-clarification-badge]") as HTMLElement;
    expect(approvalBadge.textContent).toBe(t("chat.head.pendingApprovals", { count: 1 }));
    expect(clarificationBadge.textContent).toBe(t("chat.head.pendingClarifications", { count: 1 }));

    // 点击是真实动作：把对应卡片滚进视野并标记（不是装饰性徽标）。
    await user.click(clarificationBadge);
    expect(
      document.querySelector("[data-clarification-card='ui-1']")?.getAttribute("data-located")
    ).toBe("true");
    await user.click(approvalBadge);
    expect(document.querySelector("[data-approval-card='apr-1']")?.getAttribute("data-located")).toBe(
      "true"
    );
  });

  it("输入区解剖与焦点顺序：项目行 → 文本域 → + → 模型/思考/权限 → 停止/发送 → 脚注", async () => {
    renderPage(stateFrom([]), {
      currentWorkspace: {
        id: "ws-a",
        name: "my-agent-go-ui",
        canonicalPath: "/work/my-agent-go-ui",
        trusted: true,
        createdAt: "2026-11-06T00:00:00Z",
      },
    });
    await waitFor(() => expect(screen.getByText("验收会话")).toBeInTheDocument());

    const composer = document.querySelector("[data-composer]") as HTMLElement;
    // 项目行在输入框之前（原型 composer-project-row 在 form 之上）
    const project = composer.querySelector("[data-composer-project]") as HTMLElement;
    const textarea = composer.querySelector("#composer-input") as HTMLTextAreaElement;
    const bar = composer.querySelector(".achat-composer-bar") as HTMLElement;
    const foot = composer.querySelector(".achat-composer-foot") as HTMLElement;
    expect(project).not.toBeNull();
    expect(project.textContent).toContain("my-agent-go-ui");
    expect(textarea).not.toBeNull();
    expect(bar).not.toBeNull();
    expect(foot).not.toBeNull();
    expect(project.compareDocumentPosition(textarea) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(textarea.compareDocumentPosition(bar) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(bar.compareDocumentPosition(foot) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

    // 焦点顺序 = 视觉顺序：文本域 → 「+」 → 停止 → 发送（模型/思考/权限是事实展示，不可聚焦）
    const focusables = Array.from(
      composer.querySelectorAll<HTMLElement>("textarea, button, input, select, [tabindex]")
    ).filter((el) => el.tagName !== "BUTTON" || !(el as HTMLButtonElement).disabled);
    const order = focusables.map((el) => el.id || el.getAttribute("aria-label") || el.tagName);
    expect(order.indexOf("composer-input")).toBe(0);
    expect(order[1]).toBe(t("chat.composer.attachmentsMenu"));

    // bar 里的顺序（含禁用控件）：+ → 模型 → 思考级别 → 权限摘要 → 停止 → 发送
    const barChildren = Array.from(bar.children);
    expect(barChildren[0].getAttribute("data-composer-plus")).toBe("");
    expect(barChildren[1].querySelector("[data-composer-model]")).not.toBeNull();
    expect(barChildren[2].querySelector("[data-composer-thinking]")).not.toBeNull();
    expect(barChildren[3].getAttribute("data-composer-permission")).toBe("");
    const actions = barChildren[barChildren.length - 1];
    expect(actions.className).toContain("achat-composer-actions");
    const actionButtons = Array.from(actions.querySelectorAll("button")).map((b) =>
      b.getAttribute("aria-label") ?? b.textContent
    );
    expect(actionButtons[0]).toBe(t("chat.composer.stop"));
    expect(actionButtons[actionButtons.length - 1]).toBe(t("chat.composer.send"));

    // 每条控制都有可访问名字（不是只有图标）
    for (const name of [
      t("chat.composer.attachmentsMenu"),
      t("chat.composer.stop"),
      t("chat.composer.send"),
    ]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument();
    }
    // 权限摘要是后端事实；读不到就写“未知”，不编一个“允许”
    expect(document.querySelector("[data-permission-summary]")?.textContent).toContain(
      t("chat.composer.permission")
    );
    expect(document.querySelector("[data-composer-thinking]")?.textContent).toBe(
      t("chat.composer.thinkingUnknown")
    );
  });

  it("发送禁用态与原因：前提未满足时输入框与发送都禁用，并写明为什么", async () => {
    renderPage(stateFrom([]));
    await waitFor(() => expect(document.querySelector("[data-composer-blocked]")).not.toBeNull());
    const textarea = screen.getByLabelText(t("chat.composer.placeholder")) as HTMLTextAreaElement;
    expect(textarea.disabled).toBe(true);
    const send = screen.getByRole("button", { name: t("chat.composer.send") }) as HTMLButtonElement;
    expect(send.disabled).toBe(true);
  });

  it("右栏：宽窗是第二列，四个 tab 都在 DOM 里，切换只改可见性", async () => {
    const user = userEvent.setup();
    renderPage(stateFrom([]));
    await waitFor(() => expect(screen.getByText("验收会话")).toBeInTheDocument());

    const rail = document.querySelector("[data-context-panel]") as HTMLElement;
    expect(rail).not.toBeNull();
    expect(rail.getAttribute("data-mode")).toBe("aside");
    const tablist = rail.querySelector('[role="tablist"]') as HTMLElement;
    expect(tablist).not.toBeNull();
    const tabs = Array.from(tablist.querySelectorAll('[role="tab"]')) as HTMLElement[];
    expect(tabs.map((el) => el.getAttribute("data-rail-tab"))).toEqual([
      "browser",
      "artifacts",
      "citations",
      "execution",
    ]);
    // 四个分区始终在 DOM 里（切换是可见性，不是条件渲染——否则原生浏览器视图会被销毁）
    const paneOf = (id: string) =>
      document.querySelector(`[data-context-section='${id}']`) as HTMLElement;
    for (const id of ["browser", "artifacts", "citations", "execution"]) {
      expect(paneOf(id)).not.toBeNull();
    }
    expect(paneOf("browser").getAttribute("data-active")).toBe("true");
    expect(tabs[0].getAttribute("aria-selected")).toBe("true");

    await user.click(tabs[1]);
    expect(paneOf("browser").getAttribute("data-active")).toBe("false");
    expect(paneOf("artifacts").getAttribute("data-active")).toBe("true");
    expect(tabs[1].getAttribute("aria-selected")).toBe("true");
    // 切 tab 之后浏览器分区仍然在（节点没有被重建/摘掉）
    expect(paneOf("browser")).not.toBeNull();
  });

  it("窄窗：右栏变成抽屉，默认收起；展开后 data-open=true 并出现遮罩", async () => {
    const user = userEvent.setup();
    stubMatchMedia(true);
    renderPage(stateFrom([]));
    await waitFor(() => expect(screen.getByText("验收会话")).toBeInTheDocument());

    const rail = document.querySelector("[data-context-panel]") as HTMLElement;
    await waitFor(() => expect(rail.getAttribute("data-mode")).toBe("drawer"));
    expect(rail.getAttribute("data-open")).toBe("false");
    // 抽屉只在窄窗出现抓手
    expect(rail.querySelector(".achat-rail-handle")).not.toBeNull();
    expect(document.querySelector("[data-rail-scrim]")?.getAttribute("data-open")).toBe("false");

    await user.click(screen.getByRole("button", { name: t("chat.context.openPanel") }));
    await waitFor(() => expect(rail.getAttribute("data-open")).toBe("true"));
    expect(document.querySelector("[data-rail-scrim]")?.getAttribute("data-open")).toBe("true");
    // 遮罩是一层覆盖层，不接管控制权：点它等于收起
    await user.click(document.querySelector("[data-rail-scrim]") as HTMLElement);
    await waitFor(() => expect(rail.getAttribute("data-open")).toBe("false"));
  });
});

// ---------------------------------------------------------------- 页头在壳层顶栏里（D2c）
//
// 缺陷：原型 chat.html 只有一个 52px 的页头（`.topbar` 里的 chat-head-main +
// chat-head-actions）；我们却在页面里又画了一条 `.achat-head`，于是整页比原型低
// 52px、看起来是两个头叠着（chat-diff 实测 thread.top=104，原型 52）。
//
// 这一组守两件事，且都是"删掉第二带之后才成立的"：
//   1. chat 路由的文档里只有一个 <header>，它就是壳层顶栏，页头内容在它里面；
//   2. 旧的页头带类名与页内 <header> 都不再存在（页头是搬走了，不是复制了）。
// 像素（顶栏 52、thread.top 52）由 `chat-diff.mjs` 在构建产物上实测并出
// `chat-head-diff.json`；jsdom 不算布局，所以这里只认令牌与结构。
describe("对话页页头（D2c）", () => {
  it("chat 路由只有一个 header：就是壳层 52px 顶栏，页头内容在它里面", async () => {
    renderPage(stateFrom([ev("message.delta", { text: "先跑一下测试" })]));
    await waitFor(() => expect(screen.getByText("验收会话")).toBeInTheDocument());

    const headers = document.querySelectorAll("header");
    expect(headers).toHaveLength(1);

    const header = headers[0] as HTMLElement;
    // 就是壳层顶栏本身，不是页面里另画的一条。
    expect(header).toBe(document.querySelector('[data-shell-region="topbar"]'));
    expect(header.className).toContain("anvil-shell-topbar");
    // 占用标记：壳层已让出自己的 crumb/搜索/状态区（一条带子只装一套页头）。
    expect(header.getAttribute("data-topbar-claimed")).toBe("true");

    const head = header.querySelector("[data-chat-head]") as HTMLElement;
    expect(head).not.toBeNull();
    expect(head.querySelector("[data-session-title]")?.textContent).toBe("验收会话");
    // 页头没有被简化：语言选择、导出、右栏开关、更多都还在这个头里。
    expect(head.querySelector(`select[aria-label="${t("locale.label")}"]`)).not.toBeNull();
    expect(within(head).getByRole("button", { name: t("chat.head.more") })).toBeInTheDocument();
    expect(within(head).getByRole("button", { name: t("chat.context.closePanel") })).toBeInTheDocument();

    // 页面里没有第二个头：布局直接跟着顶栏（几何由 chat-head-diff.json 量）。
    expect(document.querySelector("[data-chat-page] header")).toBeNull();

    // 52px 的判据在源码层：顶栏高度消费令牌，令牌就是原型值。
    const { indexCss } = readContract();
    expect(indexCss).toContain("--anvil-topbar-h: 52px;");
    expect(indexCss).toContain("height: var(--anvil-topbar-h);");
    // 占用标记同时挂在壳层上：它驱动“第一行钉在 52px”的网格行定义
    // （否则 auto 行会平分剩余空间，页面就不紧贴顶栏）。
    expect(document.querySelector(".anvil-shell")?.getAttribute("data-topbar-claimed")).toBe("true");
  });

  it("旧的页头带不再存在：.achat-head 与页内 header 都没了，页头钩子只出现一次", async () => {
    renderPage(stateFrom([]));
    await waitFor(() => expect(screen.getByText("验收会话")).toBeInTheDocument());

    // 旧的 52px 带子（min-height:52px + border-bottom）已删除。
    expect(document.querySelector(".achat-head")).toBeNull();
    // 页头是搬进顶栏，不是复制：钩子只有一个。
    expect(document.querySelectorAll("[data-chat-head]")).toHaveLength(1);
    expect(
      document.querySelector('[data-shell-region="topbar"] [data-chat-head]')
    ).not.toBeNull();
    // 页面区域里也没有任何 header（空态/加载/错误态同样不画页内页头）。
    expect(document.querySelectorAll("[data-shell-region='page'] header")).toHaveLength(0);

    // 提示条（重命名/导出结果/错误）仍在页面里：它本来就不是页头，也不再叫 head。
    expect(document.querySelector(".achat-notice")).toBeNull(); // 没有动作时不该出现
    expect(document.querySelector("[data-chat-head-notice]")).toBeNull();
  });
});
