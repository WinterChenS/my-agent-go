// 设置页测试。
//
// UI01 里最容易漏的一条是"缺 Pi 仍能打开设置"。所以这里断言的重点是：
// 即使 status 为 null（Pi 从未启动成功），表单依然完整可编辑、可保存。

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SettingsView } from "./SettingsView";
import type { Settings, Status, Workspace } from "../../types";

const baseSettings: Settings = {
  piPath: "D:\\pi\\cli.js",
  nodePath: "D:\\node\\node.exe",
  provider: "fixture",
  baseUrl: "http://127.0.0.1:1/v1",
  model: "fixture-model",
  apiKey: "",
  modelRef: "",
  workspace: "D:\\work\\proj",
  shellAllowed: true,
};

const workspace: Workspace = {
  id: "ws-1",
  name: "proj",
  canonicalPath: "D:\\work\\proj",
  trusted: false,
  createdAt: "2026-11-04T00:00:00Z",
};

function renderSettings(overrides: Partial<Parameters<typeof SettingsView>[0]> = {}) {
  // 参数类型从实现推断，mock.calls 才能拿到具体的参数元组。
  const handlers = {
    onSave: vi.fn(async (_s: Settings) => {}),
    onTrust: vi.fn(async (_id: string, _trusted: boolean) => {}),
  };
  render(
    <SettingsView
      settings={baseSettings}
      status={null}
      workspaces={[workspace]}
      saving={false}
      error={null}
      {...handlers}
      {...overrides}
    />
  );
  return handlers;
}

describe("SettingsView", () => {
  it("status 为 null（Pi 从未就绪）时表单依然可用", () => {
    renderSettings();
    expect(screen.getByDisplayValue("D:\\pi\\cli.js")).toBeInTheDocument();
    expect(screen.getByDisplayValue("fixture-model")).toBeInTheDocument();
    // 诊断区没有数据时显示占位，而不是空白导致看起来像卡住。
    expect(screen.getByText("平台")).toBeInTheDocument();
    // 四项诊断都缺数据，占位符会有多个。
    expect(screen.getAllByText("—").length).toBeGreaterThan(0);
  });

  it("编辑后保存会把改动传出去", async () => {
    const user = userEvent.setup();
    const h = renderSettings();
    const model = screen.getByDisplayValue("fixture-model");
    await user.clear(model);
    await user.type(model, "m-new");
    await user.click(screen.getByRole("button", { name: /保存设置/ }));

    expect(h.onSave).toHaveBeenCalledTimes(1);
    expect(h.onSave.mock.calls[0][0]).toMatchObject({ model: "m-new" });
  });

  it("未信任的工作区显示状态并可一键信任", async () => {
    const user = userEvent.setup();
    const h = renderSettings();
    expect(screen.getByText("未信任")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "信任" }));
    expect(h.onTrust).toHaveBeenCalledWith("ws-1", true);
  });

  it("已信任时按钮语义反过来", async () => {
    const user = userEvent.setup();
    const h = renderSettings({ workspaces: [{ ...workspace, trusted: true }] });
    expect(screen.getByText("已信任")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "取消信任" }));
    expect(h.onTrust).toHaveBeenCalledWith("ws-1", false);
  });

  it("没有任何工作区时给出下一步，而不是空白列表", () => {
    renderSettings({ workspaces: [] });
    expect(screen.getByText(/还没有登记任何工作区/)).toBeInTheDocument();
  });

  it("保存失败时显示 code 与原因", () => {
    renderSettings({
      error: { code: "INTERNAL_ERROR", message: "写入 settings.json 失败", retryable: false },
    });
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("INTERNAL_ERROR");
    expect(alert).toHaveTextContent("写入 settings.json 失败");
  });

  it("隔离参数按实际传入的那一份展示", () => {
    const status: Status = {
      appRoot: "C:\\app",
      agentDir: "C:\\app\\agents\\ws-1",
      piPath: "pi.js",
      piVersion: "1.5.0",
      provider: "fixture",
      model: "m",
      modelRef: "",
      workspace: "D:\\work\\proj",
      isolation: "-na -nc -e policy.ts",
      policyActive: true,
      platform: "windows/amd64",
      runtimeProvider: "fixture",
      runtimeModel: "m",
      runtimePending: false,
    };
    renderSettings({ status });
    expect(screen.getByText("-na -nc -e policy.ts")).toBeInTheDocument();
    // agentDir 要能核对，否则排查"会话写哪儿了"只能靠猜。
    expect(screen.getByText("C:\\app\\agents\\ws-1")).toBeInTheDocument();
  });
});
