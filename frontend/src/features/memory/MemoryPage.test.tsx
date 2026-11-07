// 记忆页面测试（U05 / UI09）。
//
// 三条不能含糊的语义：
//   - empty ≠ unavailable：库打不开要显示故障与原因码，不能显示"还没有记忆"；
//   - disabled ≠ deleted：关闭某个 scope 的注入后条目仍在，并标"未注入"；
//   - 短查询不是空结果：少于 3 个字符要提示，且不调用后端检索。

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { t } from "../../i18n";

const fake = vi.hoisted(() => ({
  list: null as unknown as () => Promise<any>,
  save: null as unknown as (...a: any[]) => Promise<any>,
  confirm: null as unknown as (...a: any[]) => Promise<any>,
  remove: null as unknown as (...a: any[]) => Promise<any>,
  search: null as unknown as (...a: any[]) => Promise<any>,
  exportJson: null as unknown as () => Promise<any>,
  setScopeEnabled: null as unknown as (...a: any[]) => Promise<any>,
}));

vi.mock("./api", () => ({ memoryApi: fake }));

import { MemoryPage } from "./MemoryPage";

function memory(overrides: Record<string, any> = {}) {
  return {
    id: "mem-1",
    scope: "workspace",
    workspaceId: "ws-1",
    kind: "convention",
    content: "数据库连接池上限为二十",
    sourceSessionId: "sess-1",
    status: "active",
    createdAt: "2026-11-01T00:00:00Z",
    updatedAt: "2026-11-01T00:00:00Z",
    ...overrides,
  };
}

function listResult(overrides: Record<string, any> = {}) {
  return {
    items: [memory()],
    scopes: [
      { scope: "user", workspaceId: "", enabled: true },
      { scope: "workspace", workspaceId: "ws-1", enabled: true },
    ],
    availability: { available: true },
    ...overrides,
  };
}

beforeEach(() => {
  fake.list = vi.fn(async () => listResult());
  fake.save = vi.fn(async () => ({ item: memory() }));
  fake.confirm = vi.fn(async () => ({ item: memory({ status: "active" }) }));
  fake.remove = vi.fn(async () => ({ item: memory({ status: "deleted" }) }));
  fake.search = vi.fn(async () => ({ items: [memory()] }));
  fake.exportJson = vi.fn(async () => ({ json: '{\n  "items": []\n}', itemCount: 0 }));
  fake.setScopeEnabled = vi.fn(async (scope: string, enabled: boolean) => ({
    state: { scope, workspaceId: scope === "workspace" ? "ws-1" : "", enabled },
  }));
});

describe("MemoryPage", () => {
  it("loads real entries and shows scope/kind/status/source", async () => {
    render(<MemoryPage />);
    const row = await screen.findByTestId("memory-mem-1");
    expect(row).toHaveTextContent("数据库连接池上限为二十");
    expect(row).toHaveTextContent(t("memory.scope.workspace"));
    expect(row).toHaveTextContent(t("memory.kind.convention"));
    expect(row).toHaveTextContent(t("memory.status.active"));
    expect(row).toHaveTextContent("sess-1");
  });

  it("shows an empty state when there are no entries", async () => {
    fake.list = vi.fn(async () => listResult({ items: [] }));
    render(<MemoryPage />);
    expect(await screen.findByText(t("memory.emptyTitle"))).toBeInTheDocument();
    expect(screen.queryByText(t("memory.noResults"))).toBeNull();
  });

  it("separates unavailable from empty", async () => {
    fake.list = vi.fn(async () => ({
      items: [],
      scopes: [],
      availability: { available: false, reasonCode: "INTERNAL_ERROR", detail: "库打不开" },
    }));
    render(<MemoryPage />);
    expect(await screen.findByText(t("memory.unavailableHint", { code: "INTERNAL_ERROR" }))).toBeInTheDocument();
    expect(screen.queryByText(t("memory.emptyTitle"))).toBeNull();
  });

  it("blocks on a real error (no workspace) instead of showing empty", async () => {
    fake.list = vi.fn(async () => ({
      items: [],
      scopes: [],
      availability: { available: true },
      error: { code: "CONFIG_MISSING", message: "尚未选择工作区", retryable: false },
    }));
    render(<MemoryPage />);
    expect(await screen.findByText(t("errors.CONFIG_MISSING"))).toBeInTheDocument();
    expect(screen.queryByText(t("memory.emptyTitle"))).toBeNull();
  });

  it("disabling a scope keeps the entries and marks them not injecting", async () => {
    render(<MemoryPage />);
    await screen.findByTestId("memory-mem-1");
    // 明确点 workspace 那一行的开关。
    const workspaceRow = screen.getByTestId("scope-state-workspace").closest("li") as HTMLElement;
    await userEvent.click(within(workspaceRow).getByRole("button"));
    await waitFor(() => expect(fake.setScopeEnabled).toHaveBeenCalledWith("workspace", false));
    expect(await screen.findByTestId("memory-mem-1")).toBeInTheDocument();
    expect(await screen.findByTestId("memory-not-injecting-mem-1")).toBeInTheDocument();
    expect(screen.getByTestId("scope-state-workspace")).toHaveTextContent(t("memory.scopeToggle.disabled"));
  });

  it("deletes only after confirmation and reloads the list", async () => {
    const remove = vi.fn(async () => ({ item: memory({ status: "deleted" }) }));
    fake.remove = remove;
    const list = fake.list as unknown as ReturnType<typeof vi.fn>;
    render(<MemoryPage />);
    const row = await screen.findByTestId("memory-mem-1");
    await userEvent.click(within(row).getByRole("button", { name: t("memory.delete") }));
    // 未确认前不调用后端。
    expect(remove).not.toHaveBeenCalled();
    await userEvent.click(within(row).getByRole("button", { name: t("memory.deleteConfirm") }));
    await waitFor(() => expect(remove).toHaveBeenCalledWith("mem-1"));
    await waitFor(() => expect(list.mock.calls.length).toBeGreaterThanOrEqual(2));
  });

  it("confirms a candidate before it becomes active", async () => {
    fake.list = vi.fn(async () => listResult({ items: [memory({ id: "mem-c", status: "candidate" })] }));
    render(<MemoryPage />);
    const row = await screen.findByTestId("memory-mem-c");
    expect(row).toHaveTextContent(t("memory.candidateHint"));
    await userEvent.click(within(row).getByRole("button", { name: t("memory.confirm") }));
    await waitFor(() => expect(fake.confirm).toHaveBeenCalledWith("mem-c"));
  });

  it("treats a short query as too short and does not call the backend", async () => {
    render(<MemoryPage />);
    await screen.findByTestId("memory-mem-1");
    await userEvent.type(screen.getByLabelText(t("memory.searchLabel")), "数");
    await userEvent.click(screen.getByRole("button", { name: t("memory.search") }));
    expect(await screen.findByText(t("memory.tooShort", { count: 3 }))).toBeInTheDocument();
    expect(fake.search).not.toHaveBeenCalled();
  });

  it("searches through the backend and can clear the results", async () => {
    render(<MemoryPage />);
    await screen.findByTestId("memory-mem-1");
    await userEvent.type(screen.getByLabelText(t("memory.searchLabel")), "连接池");
    await userEvent.click(screen.getByRole("button", { name: t("memory.search") }));
    await waitFor(() => expect(fake.search).toHaveBeenCalledWith("连接池"));
    await userEvent.click(screen.getByRole("button", { name: t("memory.clearSearch") }));
    expect(screen.queryByRole("button", { name: t("memory.clearSearch") })).toBeNull();
  });

  it("exports readable JSON locally", async () => {
    render(<MemoryPage />);
    await screen.findByTestId("memory-mem-1");
    await userEvent.click(screen.getByRole("button", { name: t("memory.export") }));
    const pre = await screen.findByTestId("memory-export");
    expect(pre.textContent).toContain('"items"');
    expect(pre.textContent).toContain("\n");
    expect(fake.exportJson).toHaveBeenCalled();
  });

  it("filters entries by scope without touching the backend", async () => {
    fake.list = vi.fn(async () =>
      listResult({
        items: [
          memory({ id: "mem-w", scope: "workspace" }),
          memory({ id: "mem-u", scope: "user", workspaceId: "" }),
        ],
      })
    );
    render(<MemoryPage />);
    await screen.findByTestId("memory-mem-w");
    await userEvent.click(screen.getByRole("button", { name: t("memory.scope.user") }));
    expect(screen.queryByTestId("memory-mem-w")).toBeNull();
    expect(screen.getByTestId("memory-mem-u")).toBeInTheDocument();
  });

  it("saves a new memory and reloads", async () => {
    const save = vi.fn(async () => ({ item: memory({ id: "mem-new" }) }));
    fake.save = save;
    render(<MemoryPage />);
    await screen.findByTestId("memory-mem-1");
    await userEvent.type(screen.getByLabelText(t("memory.new.contentLabel")), "新的一条约定");
    await userEvent.click(screen.getByRole("button", { name: t("memory.new.save") }));
    await waitFor(() => expect(save).toHaveBeenCalledWith("新的一条约定", "convention", "workspace"));
    expect(await screen.findByText(t("memory.notice.saved"))).toBeInTheDocument();
  });
});
