// 侧栏底部渲染测试（D1）。
//
// 断言的是"用户能不能看出运行时的真实状态"：有版本就显示版本，没有就显示
// "未检测到 Pi"，工作区显示名称与路径。绝不允许出现设计稿里的示例数字
// （"本机运行中 · 4 个任务"）——那是原型的数据，不是产品的数据。

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RuntimeFooter } from "./RuntimeFooter";
import { t } from "../../i18n";
import type { Status, Workspace } from "../../types";

const workspace: Workspace = {
  id: "ws-1",
  name: "alpha",
  canonicalPath: "/work/alpha",
  trusted: true,
  createdAt: "2026-11-01T00:00:00Z",
};

function renderFooter(overrides: Partial<Parameters<typeof RuntimeFooter>[0]> = {}) {
  const onOpenRuntime = vi.fn();
  const onOpenWorkspace = vi.fn();
  const utils = render(
    <RuntimeFooter
      status={null}
      workspace={null}
      onOpenRuntime={onOpenRuntime}
      onOpenWorkspace={onOpenWorkspace}
      {...overrides}
    />
  );
  return { ...utils, onOpenRuntime, onOpenWorkspace };
}

describe("RuntimeFooter", () => {
  it("读到 Pi 版本时显示版本，并把运行时行做成可点入口", async () => {
    const user = userEvent.setup();
    const status = { piVersion: "1.5.2" } as unknown as Status;
    const { onOpenRuntime } = renderFooter({ status });
    expect(screen.getByText(t("shell.piAgent"))).toBeInTheDocument();
    expect(screen.getByText(new RegExp("1\\.5\\.2"))).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: new RegExp(t("shell.piAgent")) }));
    expect(onOpenRuntime).toHaveBeenCalledTimes(1);
  });

  it("读不到 Pi 时显示未检测到，而不是编一个状态", () => {
    renderFooter({ status: null });
    expect(screen.getByText(t("shell.piMissing"))).toBeInTheDocument();
    // 原型里的示例数字不许出现在真实界面里。
    expect(screen.queryByText(/\d+ 个任务/)).toBeNull();
  });

  it("当前工作区显示名称与路径，点击交给上层导航", async () => {
    const user = userEvent.setup();
    const { onOpenWorkspace } = renderFooter({ workspace, status: { piVersion: "1.5.2" } as unknown as Status });
    expect(screen.getByText("alpha")).toBeInTheDocument();
    expect(screen.getByText("/work/alpha")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /alpha/ }));
    expect(onOpenWorkspace).toHaveBeenCalledTimes(1);
  });

  it("没有工作区时显示本地化占位文案", () => {
    renderFooter({ workspace: null });
    expect(screen.getByText(t("shell.noWorkspace"))).toBeInTheDocument();
  });
});
