// 记忆页的关键断言（C05 / MEM01/MEM02/MEM05）。
//
// 重点不是"能渲染列表"，而是几件容易骗人的事：
//   - 短查询显示"至少 3 个字符"，不是空结果；
//   - candidate 有"未注入"标识，不能看起来和已生效一样；
//   - 删除后列表立即消失，且界面不宣称"已经发给模型的历史被撤回"；
//   - 空 / 加载 / 失败三态可分辨，失败带错误码。

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryView } from "./MemoryView";
import type { MemoryItem, MemoryListResult, MemoryScopeState } from "../../types";

function item(over: Partial<MemoryItem> = {}): MemoryItem {
  return {
    id: "mem-1",
    scope: "workspace",
    workspaceId: "ws-a",
    kind: "convention",
    content: "数据库连接池上限为二十",
    sourceSessionId: "sess-1",
    status: "active",
    createdAt: "2026-11-04T10:00:00Z",
    updatedAt: "2026-11-04T10:00:00Z",
    ...over,
  };
}

function scopes(over: Partial<MemoryScopeState>[] = []): MemoryScopeState[] {
  const base: MemoryScopeState[] = [
    { scope: "user", workspaceId: "", enabled: true },
    { scope: "workspace", workspaceId: "ws-a", enabled: true },
  ];
  return base.map((s, i) => ({ ...s, ...(over[i] ?? {}) }));
}

function listResult(over: Partial<MemoryListResult> = {}): MemoryListResult {
  return {
    items: [
      item(),
      item({
        id: "mem-cand",
        status: "candidate",
        content: "数据库连接池候选：上限为九十九",
      }),
    ],
    scopes: scopes(),
    ...over,
  };
}

vi.mock("../../api", () => ({
  api: {
    memories: vi.fn(async () => listResult()),
    saveMemory: vi.fn(async () => ({ item: item({ id: "mem-new" }) })),
    confirmMemory: vi.fn(async () => ({
      item: item({ id: "mem-cand", status: "active" }),
    })),
    deleteMemory: vi.fn(async () => ({
      item: item({ id: "mem-1", status: "deleted" }),
    })),
    searchMemories: vi.fn(async () => ({ items: [item()] })),
    exportMemories: vi.fn(async () => ({ json: '{"items":[]}', itemCount: 0 })),
    setMemoryScopeEnabled: vi.fn(async (scope: string, enabled: boolean) => ({
      state: { scope, workspaceId: scope === "workspace" ? "ws-a" : "", enabled },
    })),
  },
}));

describe("MemoryView", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("加载中显示加载态，而不是空列表", async () => {
    const { api } = await import("../../api");
    vi.mocked(api.memories).mockImplementationOnce(
      () => new Promise<MemoryListResult>(() => {})
    );
    render(<MemoryView />);
    expect(screen.getByRole("status")).toHaveTextContent(/读取记忆/);
  });

  it("少于 3 个字符的查询显示提示，且不调用后端检索", async () => {
    const { api } = await import("../../api");
    render(<MemoryView />);
    await waitFor(() => expect(screen.getByText("数据库连接池上限为二十")).toBeInTheDocument());

    await userEvent.type(screen.getByLabelText("搜索记忆"), "数据");
    await userEvent.click(screen.getByRole("button", { name: "搜索" }));

    expect(screen.getByRole("alert")).toHaveTextContent("请输入至少 3 个字符");
    expect(api.searchMemories).not.toHaveBeenCalled();
  });

  it("候选条目显示“未注入”，与已生效条目可区分", async () => {
    render(<MemoryView />);
    await waitFor(() =>
      expect(screen.getByText("数据库连接池候选：上限为九十九")).toBeInTheDocument()
    );

    const candidateRow = screen
      .getByText("数据库连接池候选：上限为九十九")
      .closest("li") as HTMLElement;
    expect(within(candidateRow).getByText("候选 · 未注入")).toBeInTheDocument();
    expect(within(candidateRow).queryByText("已生效")).not.toBeInTheDocument();

    const activeRow = screen
      .getByText("数据库连接池上限为二十")
      .closest("li") as HTMLElement;
    expect(within(activeRow).getByText("已生效")).toBeInTheDocument();
    expect(within(activeRow).queryByText("候选 · 未注入")).not.toBeInTheDocument();
  });

  it("删除后条目从列表消失，并说明历史消息无法撤回", async () => {
    const { api } = await import("../../api");
    render(<MemoryView />);
    await waitFor(() => expect(screen.getByText("数据库连接池上限为二十")).toBeInTheDocument());

    await userEvent.click(screen.getByLabelText("删除记忆 mem-1"));
    await userEvent.click(screen.getByRole("button", { name: "确认删除" }));

    await waitFor(() =>
      expect(screen.queryByText("数据库连接池上限为二十")).not.toBeInTheDocument()
    );
    expect(api.deleteMemory).toHaveBeenCalledWith("mem-1");
    expect(screen.getByRole("status")).toHaveTextContent(/历史消息无法撤回/);
  });

  it("后端返回 QUERY_TOO_SHORT 时同样显示短查询提示，而不是空结果", async () => {
    const { api } = await import("../../api");
    vi.mocked(api.searchMemories).mockResolvedValueOnce({
      items: [],
      error: { code: "QUERY_TOO_SHORT", message: "检索关键词至少 3 个字符", retryable: false },
    });
    render(<MemoryView />);
    await waitFor(() => expect(screen.getByText("数据库连接池上限为二十")).toBeInTheDocument());

    await userEvent.type(screen.getByLabelText("搜索记忆"), "数据库");
    await userEvent.click(screen.getByRole("button", { name: "搜索" }));

    expect(screen.getByRole("alert")).toHaveTextContent("请输入至少 3 个字符");
    expect(screen.queryByText(/检索命中/)).not.toBeInTheDocument();
  });

  it("列表加载失败显示可识别的错误码与重试入口", async () => {
    const { api } = await import("../../api");
    vi.mocked(api.memories).mockResolvedValueOnce({
      items: [],
      scopes: [],
      error: { code: "INTERNAL_ERROR", message: "数据库被占用", retryable: true },
    });
    render(<MemoryView />);
    await waitFor(() => expect(screen.getByRole("alert")).toBeInTheDocument());
    expect(screen.getByRole("alert")).toHaveTextContent("INTERNAL_ERROR");
    expect(screen.getByRole("button", { name: "重试" })).toBeInTheDocument();
  });

  it("按 scope 关闭注入会调用后端，并保留条目", async () => {
    const { api } = await import("../../api");
    render(<MemoryView />);
    await waitFor(() => expect(screen.getByText("数据库连接池上限为二十")).toBeInTheDocument());

    const scopeSection = screen.getByLabelText("注入范围");
    const buttons = within(scopeSection).getAllByRole("button", { name: "关闭注入" });
    // 第一个是 user，第二个是 workspace。
    await userEvent.click(buttons[1]);

    expect(api.setMemoryScopeEnabled).toHaveBeenCalledWith("workspace", false);
    await waitFor(() =>
      expect(within(scopeSection).getByRole("button", { name: "开启注入" })).toBeInTheDocument()
    );
    expect(screen.getByText("数据库连接池上限为二十")).toBeInTheDocument();
  });

  it("导出显示本地 JSON，不上传", async () => {
    const { api } = await import("../../api");
    vi.mocked(api.exportMemories).mockResolvedValueOnce({
      json: '{\n  "schemaVersion": 1\n}',
      itemCount: 1,
    });
    render(<MemoryView />);
    await waitFor(() => expect(screen.getByText("数据库连接池上限为二十")).toBeInTheDocument());

    await userEvent.click(screen.getByRole("button", { name: "导出" }));
    await waitFor(() =>
      expect(screen.getByLabelText("记忆导出 JSON")).toBeInTheDocument()
    );
    expect(screen.getByRole("status")).toHaveTextContent(/不会上传/);
  });
});
