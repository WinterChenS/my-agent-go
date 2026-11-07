// 输入区测试（U02 / UI06）。
//
// 这里守的是“附件以后端为准”：
//   - 用户输入的是路径字符串，它只是候选定位串，必须经 ResolveAttachments 复核；
//   - 通过复核的附件才以能力 ID 的形式进 run；
//   - 被拒的每个文件都要带着后端给的原因出现在界面上，不能被折叠成“全部失败”，
//     也不能被静默丢掉。

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { t } from "../../i18n";

const backend = vi.hoisted(() => ({
  resolveAttachments: null as unknown as (...a: any[]) => Promise<any>,
  resolveCalls: [] as { sessionId: string; requests: { path: string; displayName: string }[] }[],
}));

vi.mock("./api", () => ({
  chatApi: {
    startSessionRun: vi.fn(),
    abort: vi.fn(),
    resolveApproval: vi.fn(),
    answerClarification: vi.fn(),
    replay: vi.fn(),
    resolveAttachments: (sessionId: string, requests: any[]) => {
      backend.resolveCalls.push({ sessionId, requests });
      return backend.resolveAttachments(sessionId, requests);
    },
    exportSession: vi.fn(),
    runArtifacts: vi.fn(),
    runDetails: vi.fn(),
    artifact: vi.fn(),
  },
}));

import { Composer } from "./Composer";

function renderComposer(overrides: Partial<Parameters<typeof Composer>[0]> = {}) {
  const onSend = vi.fn();
  const onStop = vi.fn();
  const onChange = vi.fn();
  const props: Parameters<typeof Composer>[0] = {
    sessionId: "sess-1",
    value: "看看这个仓库",
    onChange,
    onSend,
    onStop,
    stopRequested: false,
    busy: false,
    modelLabel: "deepseek / deepseek-v4.1-flash",
    profile: null,
    skills: [],
    permission: { filesystem: null, network: null, shell: false },
    ...overrides,
  };
  const utils = render(<Composer {...props} />);
  return { ...utils, onSend, onStop, onChange };
}

const ACCEPTED = {
  id: "att-1",
  displayName: "a.md",
  size: 2048,
  sha256: "c".repeat(64),
  mediaType: "text/markdown",
  status: "ready",
  expiresAt: "2026-11-07T00:00:00Z",
};

beforeEach(() => {
  backend.resolveCalls = [];
  backend.resolveAttachments = async () => ({ sessionId: "sess-1", items: [], rejected: [] });
});

describe("Composer 附件", () => {
  it("选择文件后调用 ResolveAttachments，并逐条显示被拒原因", async () => {
    const user = userEvent.setup();
    backend.resolveAttachments = async () => ({
      sessionId: "sess-1",
      items: [ACCEPTED],
      rejected: [
        {
          path: "/work/locked.bin",
          displayName: "locked.bin",
          code: "UNSUPPORTED",
          message: "这个类型不允许作为附件",
        },
      ],
    });
    renderComposer();

    await user.type(screen.getByLabelText(t("chat.composer.pathPlaceholder")), "/work/locked.bin");
    await user.click(screen.getByRole("button", { name: t("chat.composer.addFile") }));

    await waitFor(() => expect(backend.resolveCalls).toHaveLength(1));
    expect(backend.resolveCalls[0].sessionId).toBe("sess-1");
    expect(backend.resolveCalls[0].requests).toEqual([
      { path: "/work/locked.bin", displayName: "locked.bin" },
    ]);

    await waitFor(() => expect(document.querySelector("[data-attachment-accepted='att-1']")).not.toBeNull());
    const rejected = document.querySelector("[data-attachment-rejected='UNSUPPORTED']");
    expect(rejected).not.toBeNull();
    expect(within(rejected as HTMLElement).getByText(/不允许作为附件/)).toBeInTheDocument();
  });

  it("发送只带后端复核过的附件 ID，不带路径字符串", async () => {
    const user = userEvent.setup();
    backend.resolveAttachments = async () => ({ sessionId: "sess-1", items: [ACCEPTED], rejected: [] });
    const { onSend } = renderComposer();

    await user.type(screen.getByLabelText(t("chat.composer.pathPlaceholder")), "/work/a.md");
    await user.click(screen.getByRole("button", { name: t("chat.composer.addFile") }));
    await waitFor(() => expect(document.querySelector("[data-attachment-accepted='att-1']")).not.toBeNull());

    await user.click(screen.getByRole("button", { name: t("chat.composer.send") }));
    expect(onSend).toHaveBeenCalledWith("看看这个仓库", ["att-1"]);
  });

  it("解析失败时显示错误码，而不是把文件当成已接收", async () => {
    const user = userEvent.setup();
    backend.resolveAttachments = async () => ({
      sessionId: "sess-1",
      items: [],
      rejected: [],
      error: { code: "CONFIG_MISSING", message: "附件存储不可用", retryable: false },
    });
    renderComposer();

    await user.type(screen.getByLabelText(t("chat.composer.pathPlaceholder")), "/work/a.md");
    await user.click(screen.getByRole("button", { name: t("chat.composer.addFile") }));

    await waitFor(() =>
      expect(screen.getByText(t("chat.composer.resolveFailed", { code: "CONFIG_MISSING" }))).toBeInTheDocument()
    );
    expect(document.querySelector("[data-attachment-accepted]")).toBeNull();
  });
});

describe("Composer 运行事实", () => {
  it("显示后端给的模型与权限摘要；未知字段写未知", () => {
    renderComposer();
    expect(document.querySelector("[data-composer-model]")?.textContent).toBe(
      "deepseek / deepseek-v4.1-flash"
    );
    // 思考级别后端没给：只能显示未知，不能编一个“默认级别”。
    expect(document.querySelector("[data-composer-thinking]")?.textContent).toBe(
      t("chat.composer.thinkingUnknown")
    );
    expect(screen.getByText(new RegExp(t("chat.composer.permissionOff")))).toBeInTheDocument();
  });

  it("停止已请求时不再接受新消息，停止按钮也不可点", async () => {
    const user = userEvent.setup();
    const { onSend, onStop } = renderComposer({ stopRequested: true, busy: true });
    expect(screen.getByRole("button", { name: t("chat.composer.send") })).toBeDisabled();
    expect(screen.getByLabelText(t("chat.composer.placeholder"))).toBeDisabled();
    expect(document.querySelector("[data-composer-readonly]")).not.toBeNull();
    // 停止按钮变成“正在停止…”并禁用：同一件事不能提供第二条提交路径。
    const stopButton = screen.getByRole("button", { name: t("chat.composer.stopping") });
    expect(stopButton).toBeDisabled();
    await user.click(stopButton);
    expect(onStop).not.toHaveBeenCalled();
    expect(onSend).not.toHaveBeenCalled();
  });

  it("运行中停止按钮可用，并只转发一次停止请求", async () => {
    const user = userEvent.setup();
    const { onStop } = renderComposer({ busy: true });
    await user.click(screen.getByRole("button", { name: t("chat.composer.stop") }));
    expect(onStop).toHaveBeenCalledTimes(1);
  });
});
