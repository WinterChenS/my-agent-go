// MCP 区块的关键断言（C04 / MCP02 / MCP03 / MCP05）。
//
// 重点不是"能不能渲染"，而是几件容易骗人的事：
//   - 没诊断就写"未检查"，不能默认成已连接；
//   - 鉴权失败显示"需要鉴权 + V1 未支持 OAuth"，不能显示 connected；
//   - 禁用会调用后端（带上 revision），pending 时明说旧 run 仍在使用；
//   - 空 / 加载 / 失败三态可分辨。

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MCPSection } from "./MCPSection";
import type { MCPDefinition, MCPListResult, MCPStatus } from "../../types";

function def(over: Partial<MCPDefinition> = {}): MCPDefinition {
  return {
    id: "fixture-ext",
    transport: "stdio",
    command: "npx",
    args: ["-y", "fixture-mcp"],
    enabled: true,
    exposure: "direct",
    ...over,
  };
}

function listResult(over: Partial<MCPListResult> = {}): MCPListResult {
  return {
    items: [def()],
    revision: 3,
    runtimeServers: ["fixture-ext"],
    pending: false,
    activeRun: false,
    diagnoseReady: true,
    ...over,
  };
}

function status(over: Partial<MCPStatus> = {}): MCPStatus {
  return {
    id: "fixture-ext",
    configured: true,
    transport: "stdio",
    enabled: true,
    exposure: "direct",
    state: "connected",
    tools: [{ name: "fixture_echo", exposure: "direct", enabled: true, capabilityState: "available" }],
    source: "pi mcp list --json",
    oauthSupported: false,
    checkedAt: "2026-11-04T00:00:00Z",
    ...over,
  };
}

vi.mock("../../api", () => ({
  api: {
    mcpServers: vi.fn(async () => listResult()),
    saveMCP: vi.fn(async () => ({ revision: 4, pending: false })),
    deleteMCP: vi.fn(async () => ({ revision: 4, pending: false })),
    setMCPEnabled: vi.fn(async () => ({ revision: 4, pending: true })),
    saveMCPCredential: vi.fn(async () => ({ ref: "mcp-ref" })),
    diagnoseMCP: vi.fn(async () => ({ status: status() })),
    diagnoseAllMCP: vi.fn(async () => ({ items: [status()] })),
  },
}));

describe("MCPSection", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("加载中显示加载态，而不是空列表", async () => {
    const { api } = await import("../../api");
    vi.mocked(api.mcpServers).mockImplementationOnce(
      () => new Promise<MCPListResult>(() => {})
    );
    render(<MCPSection />);
    expect(screen.getByRole("status")).toHaveTextContent(/读取 MCP 配置/);
  });

  it("没有服务时说清下一步", async () => {
    const { api } = await import("../../api");
    vi.mocked(api.mcpServers).mockResolvedValueOnce(
      listResult({ items: [], runtimeServers: [] })
    );
    render(<MCPSection />);
    expect(await screen.findByText("还没有 MCP 服务")).toBeInTheDocument();
  });

  it("读取失败同时显示 code 与 message", async () => {
    const { api } = await import("../../api");
    vi.mocked(api.mcpServers).mockResolvedValueOnce({
      items: [],
      revision: 0,
      runtimeServers: [],
      pending: false,
      activeRun: false,
      diagnoseReady: false,
      error: { code: "CONFIG_MISSING", message: "配置目录不可读", retryable: false },
    });
    render(<MCPSection />);
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("CONFIG_MISSING");
    expect(alert).toHaveTextContent("配置目录不可读");
  });

  it("未诊断时显示未检查，不冒充已连接", async () => {
    render(<MCPSection />);
    expect(await screen.findByText("未检查")).toBeInTheDocument();
    expect(screen.queryByText("已连接")).not.toBeInTheDocument();
  });

  it("诊断成功后显示工具数；鉴权失败必须显示需要鉴权而不是已连接", async () => {
    const { api } = await import("../../api");
    render(<MCPSection />);
    await userEvent.click(await screen.findByRole("button", { name: "诊断" }));
    await waitFor(() => expect(api.diagnoseMCP).toHaveBeenCalledWith("fixture-ext"));
    expect(await screen.findByText("已连接")).toBeInTheDocument();
    expect(screen.getByText(/fixture_echo/)).toBeInTheDocument();

    vi.mocked(api.diagnoseMCP).mockResolvedValueOnce({
      status: status({
        state: "auth_required",
        tools: [],
        error: {
          code: "UNAUTHORIZED",
          message: "服务需要鉴权。V1 不支持 MCP OAuth 登录",
          retryable: false,
        },
      }),
    });
    await userEvent.click(screen.getByRole("button", { name: "诊断" }));
    expect(await screen.findByText("需要鉴权")).toBeInTheDocument();
    expect(screen.getByText(/V1 未支持 MCP OAuth/)).toBeInTheDocument();
    expect(screen.queryByText("已连接")).not.toBeInTheDocument();
  });

  it("禁用会调用后端并带上当前 revision", async () => {
    const { api } = await import("../../api");
    render(<MCPSection />);
    await userEvent.click(await screen.findByRole("button", { name: "禁用" }));
    await waitFor(() => expect(api.setMCPEnabled).toHaveBeenCalledWith("fixture-ext", 3, false));
  });

  it("禁用后重新读取后端状态，列表与 pending 一致", async () => {
    const { api } = await import("../../api");
    vi.mocked(api.mcpServers)
      .mockResolvedValueOnce(listResult())
      .mockResolvedValueOnce(
        listResult({ items: [def({ enabled: false })], runtimeServers: [], pending: true })
      );
    render(<MCPSection />);
    await userEvent.click(await screen.findByRole("button", { name: "禁用" }));
    await waitFor(() => expect(api.setMCPEnabled).toHaveBeenCalledWith("fixture-ext", 3, false));
    // 第二次读取到的是后端的新状态（已禁用 + pending），不是前端自己猜的。
    expect(await screen.findByText("已禁用")).toBeInTheDocument();
    expect(await screen.findByText(/MCP 改动已保存/)).toBeInTheDocument();
  });

  it("pending 时明说旧 run 仍在使用", async () => {
    const { api } = await import("../../api");
    vi.mocked(api.mcpServers).mockResolvedValueOnce(
      listResult({ items: [def({ enabled: false })], pending: true, activeRun: true })
    );
    render(<MCPSection />);
    expect(await screen.findByText(/当前还有 run 在跑/)).toBeInTheDocument();
    expect(screen.getByText("运行中仍在使用")).toBeInTheDocument();
  });

  it("凭证输入后调用后端，值不回显", async () => {
    const { api } = await import("../../api");
    render(<MCPSection />);
    await screen.findByText("fixture-ext");
    await userEvent.type(screen.getByLabelText("凭证引用名"), "mcp-docs-token");
    await userEvent.type(screen.getByLabelText("凭证值"), "Bearer topsecret");
    await userEvent.click(screen.getByRole("button", { name: "保存凭证" }));
    await waitFor(() =>
      expect(api.saveMCPCredential).toHaveBeenCalledWith("mcp-docs-token", "Bearer topsecret")
    );
    expect(screen.queryByDisplayValue("Bearer topsecret")).not.toBeInTheDocument();
  });

  it("空 secret 只提示，不发请求", async () => {
    const { api } = await import("../../api");
    render(<MCPSection />);
    await screen.findByText("fixture-ext");
    await userEvent.type(screen.getByLabelText("凭证引用名"), "mcp-docs-token");
    await userEvent.click(screen.getByRole("button", { name: "保存凭证" }));
    await waitFor(() => expect(screen.getByText(/请填写凭证引用名与值/)).toBeInTheDocument());
    expect(api.saveMCPCredential).not.toHaveBeenCalled();
  });
});
