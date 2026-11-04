// 工作台的状态与交互。
//
// 重点不在"消息能显示"，而在前置条件不可满足时界面要说清缺什么。
// UI01 要求"缺 Pi/凭证仍能打开设置，发起任务显示修复路径"，
// 工作区未信任是同一类问题：用户需要知道下一步动作，而不是只看到失败。

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { WorkbenchView } from "./WorkbenchView";
import { initialState, type UiState } from "../../runState";
import type { Workspace } from "../../types";

const trusted: Workspace = {
  id: "ws-1",
  name: "proj",
  canonicalPath: "D:\\work\\proj",
  trusted: true,
  createdAt: "2026-11-04T00:00:00Z",
};

const untrusted: Workspace = { ...trusted, trusted: false };

function renderView(overrides: Partial<Parameters<typeof WorkbenchView>[0]> = {}) {
  const handlers = {
    onInput: vi.fn(),
    onSend: vi.fn(),
    onStop: vi.fn(),
    onDecide: vi.fn(),
    onGoSettings: vi.fn(),
  };
  render(
    <WorkbenchView
      ui={initialState}
      input=""
      busy={false}
      workspace={trusted}
      {...handlers}
      {...overrides}
    />
  );
  return handlers;
}

function uiWith(patch: Partial<UiState>): UiState {
  return { ...initialState, ...patch };
}

describe("WorkbenchView 前置条件", () => {
  it("没有工作区时明确说缺什么，并给出去设置的入口", () => {
    const h = renderView({ workspace: null });
    expect(screen.getByText(/还没有选择工作区/)).toBeInTheDocument();

    screen.getByRole("button", { name: "去设置里处理" }).click();
    expect(h.onGoSettings).toHaveBeenCalledTimes(1);
  });

  it("工作区未信任时说明原因，而不是只说不能发", () => {
    renderView({ workspace: untrusted });
    expect(screen.getByText(/尚未信任/)).toBeInTheDocument();
    // 解释"信任意味着什么"，否则用户不敢点。
    expect(screen.getByText(/执行命令和改文件/)).toBeInTheDocument();
  });

  it("已信任且无消息时显示引导，不显示权限提示", () => {
    renderView();
    expect(screen.getByText(/说点什么/)).toBeInTheDocument();
    expect(screen.queryByText(/尚未信任/)).toBeNull();
  });
});

describe("WorkbenchView 交互", () => {
  it("Enter 发送，Shift+Enter 不发送", async () => {
    const user = userEvent.setup();
    const h = renderView({ input: "干活" });
    const box = screen.getByLabelText("输入指令");

    await user.click(box);
    await user.keyboard("{Shift>}{Enter}{/Shift}");
    expect(h.onSend).not.toHaveBeenCalled();

    await user.keyboard("{Enter}");
    expect(h.onSend).toHaveBeenCalledTimes(1);
  });

  it("空输入时发送按钮禁用", () => {
    renderView({ input: "   " });
    expect(screen.getByRole("button", { name: /发送/ })).toBeDisabled();
  });

  it("忙碌时把发送换成停止", async () => {
    const user = userEvent.setup();
    const h = renderView({ busy: true, input: "x" });
    expect(screen.queryByRole("button", { name: /发送/ })).toBeNull();
    await user.click(screen.getByRole("button", { name: /停止/ }));
    expect(h.onStop).toHaveBeenCalledTimes(1);
  });
});

describe("WorkbenchView 审批与工具", () => {
  it("审批卡带工具名与三档结论，拒绝会回传 deny", async () => {
    const user = userEvent.setup();
    const h = renderView({
      ui: uiWith({
        approvals: [
          {
            approvalId: "a1",
            toolName: "bash",
            toolCallId: "c1",
            args: '{"cmd":"ls"}',
            expiresAtUnix: 0,
          },
        ],
      }),
    });

    expect(screen.getByText(/需要授权：bash/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /拒绝/ }));
    expect(h.onDecide).toHaveBeenCalledWith(
      expect.objectContaining({ approvalId: "a1" }),
      "deny"
    );
  });

  it("工具卡按状态显示执行中/完成/失败", () => {
    renderView({
      ui: uiWith({
        messages: [
          {
            id: "m1",
            role: "assistant",
            text: "",
            streaming: false,
            tools: [
              { runId: "r1", toolCallId: "c1", toolName: "bash", args: "{}", running: true },
              { runId: "r1", toolCallId: "c2", toolName: "bash", args: "{}", running: false, isError: false },
              { runId: "r1", toolCallId: "c3", toolName: "bash", args: "{}", running: false, isError: true },
            ],
          },
        ],
      }),
    });
    expect(screen.getByText("执行中…")).toBeInTheDocument();
    expect(screen.getByText("完成")).toBeInTheDocument();
    expect(screen.getByText("失败")).toBeInTheDocument();
  });

  it("错误条原样显示后端文案", () => {
    renderView({
      ui: uiWith({ error: "UNAUTHORIZED: 工作区尚未信任" }),
    });
    expect(screen.getByText("UNAUTHORIZED: 工作区尚未信任")).toBeInTheDocument();
  });
});
