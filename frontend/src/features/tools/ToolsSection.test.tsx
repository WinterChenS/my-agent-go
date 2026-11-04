// 统一工具目录的关键断言（C04 / RES04）。
//
// 重点：
//   - 来源分组可读（builtin / extension / mcp / harness），不是一张无名表；
//   - hidden / 已禁用 / 未知都不会显示成"可用"；
//   - 空 / 加载 / 失败三态可分辨。

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ToolsSection } from "./ToolsSection";
import type { ToolDescriptor, ToolListResult } from "../../types";

function tool(over: Partial<ToolDescriptor> = {}): ToolDescriptor {
  return {
    name: "read",
    source: "builtin",
    description: "读取文件",
    enabled: true,
    policy: "approval",
    capabilityState: "available",
    ...over,
  };
}

function toolsResult(items: ToolDescriptor[]): ToolListResult {
  return { items };
}

vi.mock("../../api", () => ({
  api: {
    tools: vi.fn(async () =>
      toolsResult([
        tool(),
        tool({
          name: "mcp__fixture__fixture_echo",
          source: "mcp",
          server: "fixture",
          policy: "approval",
        }),
        tool({
          name: "harness_probe",
          source: "harness",
          description: "自有探针",
        }),
      ])
    ),
  },
}));

describe("ToolsSection", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("加载中显示加载态", async () => {
    const { api } = await import("../../api");
    vi.mocked(api.tools).mockImplementationOnce(() => new Promise<ToolListResult>(() => {}));
    render(<ToolsSection />);
    expect(screen.getByRole("status")).toHaveTextContent(/读取工具目录/);
  });

  it("没有工具时说清原因", async () => {
    const { api } = await import("../../api");
    vi.mocked(api.tools).mockResolvedValueOnce(toolsResult([]));
    render(<ToolsSection />);
    expect(await screen.findByText("工具目录为空")).toBeInTheDocument();
    expect(screen.getByText(/MCP 服务的工具需要先诊断/)).toBeInTheDocument();
  });

  it("读取失败显示 code 与 message", async () => {
    const { api } = await import("../../api");
    vi.mocked(api.tools).mockResolvedValueOnce({
      items: [],
      error: { code: "CONFIG_MISSING", message: "Pi 未配置", retryable: false },
    });
    render(<ToolsSection />);
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("CONFIG_MISSING");
    expect(alert).toHaveTextContent("Pi 未配置");
  });

  it("按来源分组并显示策略口径", async () => {
    render(<ToolsSection />);
    expect(await screen.findByText("内置（Pi）")).toBeInTheDocument();
    expect(screen.getByText("外部 MCP")).toBeInTheDocument();
    expect(screen.getByText("自有 MCP")).toBeInTheDocument();
    expect(screen.getByText("mcp__fixture__fixture_echo")).toBeInTheDocument();
    expect(screen.getAllByText("逐次审批").length).toBeGreaterThan(0);
  });

  it("hidden 与已禁用的工具显示为不可用，不显示成可用", async () => {
    const { api } = await import("../../api");
    vi.mocked(api.tools).mockResolvedValueOnce(
      toolsResult([
        tool({
          name: "mcp__fixture__fixture_echo",
          source: "mcp",
          server: "fixture",
          enabled: false,
          capabilityState: "hidden",
        }),
        tool({
          name: "mcp__off__*",
          source: "mcp",
          server: "off",
          enabled: false,
          capabilityState: "disabled",
          description: "已禁用：不会出现在 mcp.json",
        }),
        tool({
          name: "demo-ext",
          source: "extension",
          enabled: true,
          capabilityState: "unknown",
        }),
      ])
    );
    render(<ToolsSection />);
    expect(await screen.findByText("已隐藏")).toBeInTheDocument();
    expect(screen.getByText("已禁用")).toBeInTheDocument();
    expect(screen.getByText("未知")).toBeInTheDocument();
    expect(screen.queryByText("可用")).not.toBeInTheDocument();
  });

  it("deferred 工具标为需 tool_search（不是直接可用）", async () => {
    const { api } = await import("../../api");
    vi.mocked(api.tools).mockResolvedValueOnce(
      toolsResult([
        tool({
          name: "tool_search",
          source: "builtin",
          capabilityState: "available",
        }),
        tool({
          name: "mcp__big__search",
          source: "mcp",
          server: "big",
          enabled: true,
          capabilityState: "deferred",
        }),
      ])
    );
    render(<ToolsSection />);
    expect(await screen.findByText("需 tool_search")).toBeInTheDocument();
    expect(screen.getByText("可用")).toBeInTheDocument();
  });

  it("刷新会重新读取工具目录", async () => {
    const { api } = await import("../../api");
    render(<ToolsSection />);
    await screen.findByText("mcp__fixture__fixture_echo");
    await userEvent.click(screen.getByRole("button", { name: /刷新/ }));
    await waitFor(() => expect(api.tools).toHaveBeenCalledTimes(2));
  });
});
