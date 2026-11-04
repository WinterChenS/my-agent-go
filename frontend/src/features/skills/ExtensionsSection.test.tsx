// 扩展区块（RES04）：系统扩展不可关闭、第三方默认关闭且启用需要确认。
//
// 这三条都是"界面上看得出来"的约束。真正的拒绝在后端，但界面如果画了
// 一个能点的关闭按钮，用户点下去才被告知不行——那本身就是缺陷。

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ExtensionsSection } from "./ExtensionsSection";
import type { Extension } from "../../types";

function ext(over: Partial<Extension> = {}): Extension {
  return {
    id: "harness-policy",
    name: "策略扩展（执行前授权）",
    path: "C:\\app\\pi-extensions\\harness-policy.ts",
    system: true,
    executable: true,
    enabled: true,
    origin: "system",
    canDisable: false,
    note: "系统资源，不可关闭。",
    revision: 2,
    hash: "ff00",
    ...over,
  };
}

vi.mock("../../api", () => ({
  api: {
    setExtensionEnabled: vi.fn(async () => ({ item: ext(), revision: 3, error: null })),
  },
}));

describe("ExtensionsSection", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("系统策略扩展不提供关闭入口", () => {
    render(<ExtensionsSection extensions={[ext()]} revision={2} onChanged={() => {}} />);
    expect(screen.getByText("系统资源 · 不可关闭")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "禁用" })).not.toBeInTheDocument();
  });

  it("第三方扩展默认显示未启用，且说明启用不等于免审批", () => {
    render(
      <ExtensionsSection
        extensions={[
          ext({
            id: "hello",
            name: "hello",
            path: "C:\\app\\extensions\\hello.ts",
            system: false,
            enabled: false,
            origin: "local",
            canDisable: true,
            note: "第三方可执行扩展：启用即信任，但不会让它的工具调用免审批。",
          }),
        ]}
        revision={2}
        onChanged={() => {}}
      />
    );
    expect(screen.getByText("未启用")).toBeInTheDocument();
    expect(
      screen.getByText("第三方可执行扩展：启用即信任，但不会让它的工具调用免审批。")
    ).toBeInTheDocument();
  });

  it("启用第三方扩展需要二次确认，确认后才调用后端", async () => {
    const { api } = await import("../../api");
    render(
      <ExtensionsSection
        extensions={[
          ext({
            id: "hello",
            name: "hello",
            system: false,
            enabled: false,
            origin: "local",
            canDisable: true,
          }),
        ]}
        revision={2}
        onChanged={() => {}}
      />
    );
    await userEvent.click(screen.getByRole("button", { name: "启用" }));
    expect(api.setExtensionEnabled).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "确认启用" }));
    await waitFor(() => expect(api.setExtensionEnabled).toHaveBeenCalledWith("hello", 2, true));
  });

  it("没有扩展时说明空列表的含义，而不是让人以为功能不存在", () => {
    render(<ExtensionsSection extensions={[]} revision={0} onChanged={() => {}} />);
    expect(screen.getByText(/系统策略扩展未解析到/)).toBeInTheDocument();
  });
});
