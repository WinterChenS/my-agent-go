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

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { t } from "../../i18n";
import { initialState, reduce, type UiState } from "../../runState";
import type { CallError, ProductEvent } from "../../types";
import { ShellProvider, type ShellValue } from "../../app/shell";

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

function renderPage(ui: UiState, overrides: Partial<ShellValue> = {}) {
  const value = shellValue(ui, overrides);
  return render(
    <ShellProvider value={value}>
      <ChatPage onNavigate={() => undefined} route={routeFor("sess-1")} />
    </ShellProvider>
  );
}

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
        <ChatPage onNavigate={() => undefined} route={routeFor("sess-1")} />
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
    await user.click(screen.getByRole("button", { name: `${t("chat.export.label")} · ${t("chat.export.markdown")}` }));

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
    await user.click(screen.getByRole("button", { name: t("chat.sessionActions.rename") }));
    const input = screen.getByLabelText(t("chat.sessionActions.renamePlaceholder"));
    await user.clear(input);
    await user.type(input, "新的标题");
    await user.click(screen.getByRole("button", { name: t("chat.sessionActions.save") }));

    await waitFor(() => expect(document.querySelector("[data-session-action-error]")).not.toBeNull());
    expect(screen.getByText("CONFIG_CONFLICT")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("chat.sessionActions.reload") })).toBeInTheDocument();
  });
});
