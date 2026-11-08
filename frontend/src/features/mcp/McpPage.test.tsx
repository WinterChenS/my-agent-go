// MCP 页面（U04 / MCP02 / MCP03 / MCP05）的关键断言。
//
// 重点不是"能不能渲染"，而是几件容易骗人的事：
//   - 没诊断就写"未检查"，不能默认成已连接；
//   - 鉴权失败显示"需要鉴权 + V1 未支持 OAuth"，不能显示 connected；
//   - 禁用会调用后端（带上 revision），pending 时明说旧 run 仍在使用；
//   - 空 / 加载 / 失败三态可分辨。
//
// 断言用 `t()` 取 catalog 文案：文案在两种语言之间搬动时用例仍然成立，
// 而"en-US 下漏出中文"由 scripts/check-ui-contract.mjs 与 I18N04 负责。

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { McpPage } from "./McpPage";
import { t } from "../../i18n";
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
    // 工具目录是这一页的一部分：没有工具也必须是"空"，不能是假数据。
    tools: vi.fn(async () => ({ items: [] })),
  },
}));

describe("McpPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("加载中显示加载态，而不是空列表", async () => {
    const { api } = await import("../../api");
    vi.mocked(api.mcpServers).mockImplementationOnce(() => new Promise<MCPListResult>(() => {}));
    render(<McpPage />);
    // 工具目录也会自己发一次请求（同样是加载态），所以只断言这条文案出现。
    expect(await screen.findByText(t("mcp.loading"))).toBeInTheDocument();
    expect(screen.queryByText(t("mcp.empty.title"))).not.toBeInTheDocument();
  });

  it("没有服务时说清下一步", async () => {
    const { api } = await import("../../api");
    vi.mocked(api.mcpServers).mockResolvedValueOnce(listResult({ items: [], runtimeServers: [] }));
    render(<McpPage />);
    expect(await screen.findByText(t("mcp.empty.title"))).toBeInTheDocument();
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
    render(<McpPage />);
    const alert = (await screen.findByRole("alert")) as HTMLElement;
    expect(alert).toHaveTextContent("CONFIG_MISSING");
    expect(alert).toHaveTextContent("配置目录不可读");
  });

  it("未诊断时显示未检查，不冒充已连接", async () => {
    render(<McpPage />);
    expect(await screen.findByText(t("mcp.state.unchecked"))).toBeInTheDocument();
    expect(screen.queryByText(t("status.connected"))).not.toBeInTheDocument();
  });

  it("诊断成功后显示工具数；鉴权失败必须显示需要鉴权而不是已连接", async () => {
    const { api } = await import("../../api");
    render(<McpPage />);
    await userEvent.click(await screen.findByRole("button", { name: t("mcp.actions.diagnose") }));
    await waitFor(() => expect(api.diagnoseMCP).toHaveBeenCalledWith("fixture-ext"));
    expect(await screen.findByText(t("status.connected"))).toBeInTheDocument();
    expect(screen.getByText(/fixture_echo/)).toBeInTheDocument();

    vi.mocked(api.diagnoseMCP).mockResolvedValueOnce({
      status: status({
        state: "auth_required",
        tools: [],
        error: {
          code: "MCP_AUTH_REQUIRED",
          message: "服务需要鉴权。V1 不支持 MCP OAuth 登录",
          retryable: false,
        },
      }),
    });
    await userEvent.click(screen.getByRole("button", { name: t("mcp.actions.diagnose") }));
    expect(await screen.findByText(t("mcp.state.authRequired"))).toBeInTheDocument();
    expect(screen.getByText(t("mcp.authUnsupported"))).toBeInTheDocument();
    expect(screen.queryByText(t("status.connected"))).not.toBeInTheDocument();
  });

  it("禁用会调用后端并带上当前 revision", async () => {
    const { api } = await import("../../api");
    render(<McpPage />);
    await userEvent.click(await screen.findByRole("button", { name: t("mcp.actions.disable") }));
    await waitFor(() => expect(api.setMCPEnabled).toHaveBeenCalledWith("fixture-ext", 3, false));
  });

  it("禁用后重新读取后端状态，列表与 pending 一致", async () => {
    const { api } = await import("../../api");
    vi.mocked(api.mcpServers)
      .mockResolvedValueOnce(listResult())
      .mockResolvedValueOnce(
        listResult({ items: [def({ enabled: false })], runtimeServers: [], pending: true })
      );
    render(<McpPage />);
    await userEvent.click(await screen.findByRole("button", { name: t("mcp.actions.disable") }));
    await waitFor(() => expect(api.setMCPEnabled).toHaveBeenCalledWith("fixture-ext", 3, false));
    // 第二次读取到的是后端的新状态（已禁用 + pending），不是前端自己猜的。
    expect(await screen.findByText(t("mcp.state.disabled"))).toBeInTheDocument();
    expect(await screen.findByText(t("mcp.pending.saved"))).toBeInTheDocument();
  });

  it("pending 时明说旧 run 仍在使用", async () => {
    const { api } = await import("../../api");
    vi.mocked(api.mcpServers).mockResolvedValueOnce(
      listResult({ items: [def({ enabled: false })], pending: true, activeRun: true })
    );
    render(<McpPage />);
    expect(await screen.findByText(t("mcp.pending.activeRun"))).toBeInTheDocument();
    expect(screen.getByText(t("mcp.badges.runningInUse"))).toBeInTheDocument();
  });

  it("凭证输入后调用后端，值不回显", async () => {
    const { api } = await import("../../api");
    render(<McpPage />);
    await screen.findByText("fixture-ext");
    await userEvent.type(screen.getByLabelText(t("mcp.credential.refAria")), "mcp-docs-token");
    await userEvent.type(screen.getByLabelText(t("mcp.credential.valueAria")), "Bearer topsecret");
    await userEvent.click(screen.getByRole("button", { name: t("mcp.credential.save") }));
    await waitFor(() =>
      expect(api.saveMCPCredential).toHaveBeenCalledWith("mcp-docs-token", "Bearer topsecret")
    );
    expect(screen.queryByDisplayValue("Bearer topsecret")).not.toBeInTheDocument();
  });

  it("空 secret 只提示，不发请求", async () => {
    const { api } = await import("../../api");
    render(<McpPage />);
    await screen.findByText("fixture-ext");
    await userEvent.type(screen.getByLabelText(t("mcp.credential.refAria")), "mcp-docs-token");
    await userEvent.click(screen.getByRole("button", { name: t("mcp.credential.save") }));
    await waitFor(() =>
      expect(screen.getByText(t("mcp.credential.required"))).toBeInTheDocument()
    );
    expect(api.saveMCPCredential).not.toHaveBeenCalled();
  });
});
