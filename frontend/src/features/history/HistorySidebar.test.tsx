// 历史侧栏测试（U01 / UI05）。
//
// 这里守的是"后端说了算"：改名/置顶/归档/删除之后界面显示的是**重新读取**的结果，
// 不是本地乐观更新的结果。为此 fake 后端会在服务端做一次归一化（去空白），
// 并且 CAS 冲突时拒绝写入——如果组件偷偷乐观更新，第一条断言就会红。
//
// fake 的排序与 C06 目录一致（pinned DESC, updatedAt DESC, id DESC），
// 但"真实 SQLite 上确实是这个顺序"由 Go 侧测试证明（internal/app/workbench_api_test.go）。

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { CallError, Workspace } from "../../types";
import { t } from "../../i18n";

interface Row {
  id: string;
  workspaceId: string;
  title: string;
  pinned: boolean;
  archived: boolean;
  kind: string;
  createdAt: string;
  updatedAt: string;
  revision: number;
}

const backend = vi.hoisted(() => {
  const state = {
    rows: [] as Row[],
    listCalls: 0,
    rename: null as unknown as (...a: any[]) => Promise<any>,
    setPinned: null as unknown as (...a: any[]) => Promise<any>,
    setArchived: null as unknown as (...a: any[]) => Promise<any>,
    remove: null as unknown as (...a: any[]) => Promise<any>,
    list: null as unknown as (...a: any[]) => Promise<any>,
    /** 测试里把一页压小，用来验证游标翻页（组件本身一直按真实页大小请求）。 */
    pageCap: null as number | null,
  };
  return state;
});

vi.mock("./api", () => ({
  HISTORY_PAGE_SIZE: 20,
  historyApi: {
    list: (q: any) => backend.list(q),
    rename: (id: string, rev: number, title: string) => backend.rename(id, rev, title),
    setPinned: (id: string, rev: number, pinned: boolean) => backend.setPinned(id, rev, pinned),
    setArchived: (id: string, rev: number, archived: boolean) => backend.setArchived(id, rev, archived),
    remove: (id: string, rev: number) => backend.remove(id, rev),
  },
}));

import { HistorySidebar } from "./HistorySidebar";

const WS_A = "ws-a";
const WS_B = "ws-b";

const workspaces: Workspace[] = [
  { id: WS_A, name: "alpha", canonicalPath: "/work/alpha", trusted: true, createdAt: "2026-11-01T00:00:00Z" },
  { id: WS_B, name: "beta", canonicalPath: "/work/beta", trusted: true, createdAt: "2026-11-01T00:00:00Z" },
];

let clock = 0;

function row(id: string, workspaceId: string, title: string, overrides: Partial<Row> = {}): Row {
  clock += 1;
  const ts = new Date(Date.UTC(2026, 10, 6, 10, 0, clock)).toISOString();
  return {
    id,
    workspaceId,
    title,
    pinned: false,
    archived: false,
    kind: "chat",
    createdAt: ts,
    updatedAt: ts,
    revision: 1,
    ...overrides,
  };
}

function conflict(): CallError {
  return { code: "CONFIG_CONFLICT", message: "stale", retryable: false };
}

function sortRows(rows: Row[]): Row[] {
  return [...rows].sort((a, b) => {
    if (a.pinned !== b.pinned) return a.pinned ? -1 : 1;
    if (a.updatedAt !== b.updatedAt) return a.updatedAt < b.updatedAt ? 1 : -1;
    return a.id < b.id ? 1 : -1;
  });
}

function installBackend() {
  backend.rows = [];
  backend.listCalls = 0;
  backend.pageCap = null;
  clock = 0;

  backend.list = async (q: any) => {
    backend.listCalls += 1;
    let rows = backend.rows.filter((r) => !r.archived);
    if (q.workspaceId) rows = rows.filter((r) => r.workspaceId === q.workspaceId);
    rows = sortRows(rows);
    const limit = backend.pageCap ?? q.limit ?? 20;
    let start = 0;
    if (q.cursor) {
      const idx = rows.findIndex((r) => r.id === q.cursor);
      start = idx < 0 ? 0 : idx + 1;
    }
    const page = rows.slice(start, start + limit);
    const hasMore = rows.length > start + limit;
    const groups: { workspaceId: string; sessions: Row[] }[] = [];
    for (const r of page) {
      const g = groups.find((x) => x.workspaceId === r.workspaceId);
      if (g) g.sessions.push({ ...r });
      else groups.push({ workspaceId: r.workspaceId, sessions: [{ ...r }] });
    }
    return {
      groups,
      hasMore,
      nextCursor: hasMore ? page[page.length - 1]?.id : undefined,
    };
  };

  backend.rename = async (id: string, rev: number, title: string) => {
    const r = backend.rows.find((x) => x.id === id);
    if (!r) return { error: { code: "NOT_FOUND", message: "gone", retryable: false } };
    if (r.revision !== rev) return { error: conflict() };
    // 服务端归一化：这就是"以 Go API 返回为准"能被观察到的原因。
    r.title = title.trim();
    r.revision += 1;
    r.updatedAt = new Date(Date.UTC(2026, 10, 6, 11, 0, clock++)).toISOString();
    return { item: { ...r } };
  };

  backend.setPinned = async (id: string, rev: number, pinned: boolean) => {
    const r = backend.rows.find((x) => x.id === id);
    if (!r) return { error: { code: "NOT_FOUND", message: "gone", retryable: false } };
    if (r.revision !== rev) return { error: conflict() };
    r.pinned = pinned;
    r.revision += 1;
    r.updatedAt = new Date(Date.UTC(2026, 10, 6, 12, 0, clock++)).toISOString();
    return { item: { ...r } };
  };

  backend.setArchived = async (id: string, rev: number, archived: boolean) => {
    const r = backend.rows.find((x) => x.id === id);
    if (!r) return { error: { code: "NOT_FOUND", message: "gone", retryable: false } };
    if (r.revision !== rev) return { error: conflict() };
    r.archived = archived;
    r.revision += 1;
    return { item: { ...r } };
  };

  backend.remove = async (id: string, rev: number) => {
    const r = backend.rows.find((x) => x.id === id);
    if (!r) return { error: { code: "NOT_FOUND", message: "gone", retryable: false } };
    if (r.revision !== rev) return { error: conflict() };
    backend.rows = backend.rows.filter((x) => x.id !== id);
    return {};
  };
}

function renderSidebar(overrides: Partial<Parameters<typeof HistorySidebar>[0]> = {}) {
  const onOpenSession = vi.fn();
  const onNewSession = vi.fn();
  const onFilterChange = vi.fn();
  const utils = render(
    <HistorySidebar
      workspaces={workspaces}
      workspaceFilter=""
      onFilterChange={onFilterChange}
      activeSessionId={null}
      onOpenSession={onOpenSession}
      onNewSession={onNewSession}
      {...overrides}
    />
  );
  return { ...utils, onOpenSession, onNewSession, onFilterChange };
}

/** 等待列表加载完成（loading 文案消失）。 */
async function settled() {
  await waitFor(() => {
    expect(screen.queryByText(t("shell.loadingHistory"))).toBeNull();
  });
}

function rowOrder(): string[] {
  return screen.getAllByTestId("history-session-title").map((el) => el.textContent ?? "");
}

describe("HistorySidebar", () => {
  beforeEach(() => {
    installBackend();
  });

  it("按项目分组显示会话，未知项目单列为普通会话", async () => {
    backend.rows = [
      row("s1", WS_A, "alpha 一"),
      row("s2", WS_B, "beta 一"),
      row("s3", "ws-legacy", "旧项目会话"),
      row("s4", WS_A, ""),
    ];
    renderSidebar();
    await settled();

    const groups = screen.getAllByTestId("history-group");
    expect(groups).toHaveLength(3);
    expect(within(groups[0]).getByText("alpha")).toBeInTheDocument();
    expect(within(groups[0]).getByText("alpha 一")).toBeInTheDocument();
    // 没有标题的会话显示本地化占位文案，而不是空白行。
    expect(within(groups[0]).getByText(t("shell.untitledSession"))).toBeInTheDocument();
    expect(screen.getByText(t("shell.ordinarySessions"))).toBeInTheDocument();
    expect(screen.getByText("旧项目会话")).toBeInTheDocument();
  });

  it("分页用游标继续读取，而不是把第一页重读一遍", async () => {
    const user = userEvent.setup();
    backend.pageCap = 2;
    backend.rows = [row("s1", WS_A, "一"), row("s2", WS_A, "二"), row("s3", WS_A, "三")];
    renderSidebar();
    await settled();
    expect(rowOrder().length).toBe(2);

    await user.click(screen.getByRole("button", { name: t("shell.loadMore") }));
    await waitFor(() => expect(rowOrder().length).toBe(3));
    expect(backend.listCalls).toBeGreaterThanOrEqual(2);
    expect(screen.queryByRole("button", { name: t("shell.loadMore") })).toBeNull();
  });

  it("改名后以重新读取的结果为准（服务端归一化会覆盖输入）", async () => {
    const user = userEvent.setup();
    backend.rows = [row("s1", WS_A, "旧标题")];
    renderSidebar();
    await settled();

    await user.click(screen.getByRole("button", { name: `${t("shell.rename")} 旧标题` }));
    const input = screen.getByLabelText(t("shell.renameInput"));
    await user.clear(input);
    await user.type(input, "  新标题  ");
    await user.click(screen.getByRole("button", { name: t("common.save") }));

    await waitFor(() => expect(screen.getByText("新标题")).toBeInTheDocument());
    // 乐观更新会显示 "  新标题  " 或本地值；这里显示的必须是服务端存下的形态。
    expect(screen.queryByText("  新标题  ")).toBeNull();
    expect(screen.getByTestId("history-session-title")).toHaveTextContent("新标题");
  });

  it("版本过期时显示 CONFIG_CONFLICT，并重新读取列表", async () => {
    const user = userEvent.setup();
    backend.rows = [row("s1", WS_A, "标题", { revision: 5 })];
    renderSidebar();
    await settled();
    const callsBefore = backend.listCalls;

    // 组件拿到的 revision=5 与"库里已变成 6"冲突：模拟别处先改过。
    backend.rows[0].revision = 6;
    await user.click(screen.getByRole("button", { name: `${t("shell.rename")} 标题` }));
    const input = screen.getByLabelText(t("shell.renameInput"));
    await user.clear(input);
    await user.type(input, "改不动");
    await user.click(screen.getByRole("button", { name: t("common.save") }));

    await waitFor(() =>
      expect(screen.getByText(t("errors.CONFIG_CONFLICT"))).toBeInTheDocument()
    );
    expect(backend.listCalls).toBeGreaterThan(callsBefore);
    // 失败后标题保持服务端版本，不能留下用户这次输入。
    expect(screen.queryByText("改不动")).toBeNull();
  });

  it("置顶后顺序来自重新读取（置顶在前）", async () => {
    const user = userEvent.setup();
    backend.rows = [row("s1", WS_A, "甲"), row("s2", WS_A, "乙")];
    renderSidebar();
    await settled();
    expect(rowOrder()).toEqual(["乙", "甲"]);

    await user.click(screen.getByRole("button", { name: `${t("shell.pin")} 甲` }));
    await waitFor(() => expect(rowOrder()).toEqual(["甲", "乙"]));
    expect(backend.rows.find((r) => r.id === "s1")?.pinned).toBe(true);
  });

  it("归档后该会话从默认列表消失", async () => {
    const user = userEvent.setup();
    backend.rows = [row("s1", WS_A, "甲"), row("s2", WS_A, "乙")];
    renderSidebar();
    await settled();

    await user.click(screen.getByRole("button", { name: `${t("shell.archive")} 甲` }));
    await waitFor(() => expect(screen.queryByText("甲")).toBeNull());
    expect(screen.getByText("乙")).toBeInTheDocument();
  });

  it("删除必须二次确认；取消不调用 API", async () => {
    const user = userEvent.setup();
    backend.rows = [row("s1", WS_A, "甲")];
    renderSidebar();
    await settled();

    await user.click(screen.getByRole("button", { name: `${t("shell.delete")} 甲` }));
    expect(screen.getByText(t("shell.confirmDelete"))).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: t("common.cancel") }));
    expect(screen.getByText("甲")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: `${t("shell.delete")} 甲` }));
    await user.click(screen.getByRole("button", { name: t("shell.confirm") }));
    await waitFor(() => expect(screen.queryByText("甲")).toBeNull());
    expect(backend.rows).toHaveLength(0);
  });

  it("工作区筛选交给上层，并按新筛选重新读取", async () => {
    const user = userEvent.setup();
    backend.rows = [row("s1", WS_A, "甲"), row("s2", WS_B, "乙")];
    const { onFilterChange, rerender } = renderSidebar();
    await settled();
    expect(screen.getByText("甲")).toBeInTheDocument();
    expect(screen.getByText("乙")).toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText(t("shell.filterLabel")), WS_B);
    expect(onFilterChange).toHaveBeenCalledWith(WS_B);

    rerender(
      <HistorySidebar
        workspaces={workspaces}
        workspaceFilter={WS_B}
        onFilterChange={onFilterChange}
        activeSessionId={null}
        onOpenSession={() => {}}
        onNewSession={() => {}}
      />
    );
    await settled();
    expect(screen.queryByText("甲")).toBeNull();
    expect(screen.getByText("乙")).toBeInTheDocument();
  });

  it("重启（卸载后重新挂载）显示同样的顺序", async () => {
    backend.rows = [
      row("s1", WS_A, "甲"),
      row("s2", WS_A, "乙", { pinned: true }),
      row("s3", WS_A, "丙"),
    ];
    const first = renderSidebar();
    await settled();
    const before = rowOrder();
    first.unmount();

    renderSidebar();
    await settled();
    expect(rowOrder()).toEqual(before);
  });

  it("打开会话与新对话都是显式回调，不自己导航", async () => {
    const user = userEvent.setup();
    backend.rows = [row("s1", WS_A, "甲")];
    const { onOpenSession, onNewSession } = renderSidebar();
    await settled();

    await user.click(screen.getByTestId("history-session-title"));
    expect(onOpenSession).toHaveBeenCalledWith("s1");
    await user.click(screen.getByRole("button", { name: t("shell.newSession") }));
    expect(onNewSession).toHaveBeenCalled();
  });
});
