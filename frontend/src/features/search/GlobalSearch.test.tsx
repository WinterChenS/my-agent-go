// 全局搜索测试（U01 / SRCH01 的前端一半）。
//
// 守三件事：
//   - 少于 3 个字符不发请求，直接说"太短"（后端也会拒，但没必要打一次往返）；
//   - 索引未建立显示 unavailable 与"重建索引"，绝不显示"0 条结果"；
//   - 页面/导航动作来自静态目录（不依赖后端索引），点击后按结果自带 route 导航。

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { t } from "../../i18n";

const fake = vi.hoisted(() => ({
  calls: [] as { query: string; kinds: string[]; limit: number }[],
  search: null as unknown as (q: string, kinds: string[], limit: number) => Promise<any>,
  rebuild: null as unknown as () => Promise<any>,
}));

vi.mock("./api", () => ({
  searchApi: {
    search: (q: string, kinds: string[], limit: number) => fake.search(q, kinds, limit),
    rebuildIndex: () => fake.rebuild(),
  },
}));

import { GlobalSearch } from "./GlobalSearch";

function result(overrides: Record<string, any> = {}) {
  return {
    query: "alpha",
    availability: "available",
    items: [
      {
        kind: "session",
        refId: "sess-1",
        workspaceId: "ws-a",
        route: "/chat/sess-1",
        title: "alpha 会话",
        subtitle: "",
        updatedAt: "2026-11-06T09:00:00Z",
        rank: 1,
      },
      {
        kind: "artifact",
        refId: "art-1",
        workspaceId: "ws-a",
        route: "/library?artifact=art-1",
        title: "alpha-report.md",
        subtitle: "/work/ws-a/alpha-report.md",
        updatedAt: "2026-11-06T09:01:00Z",
        rank: 2,
      },
    ],
    ...overrides,
  };
}

function renderSearch() {
  const onNavigate = vi.fn();
  const utils = render(<GlobalSearch onNavigate={onNavigate} />);
  return { ...utils, onNavigate };
}

async function typeQuery(user: ReturnType<typeof userEvent.setup>, text: string) {
  const input = screen.getByLabelText(t("search.title"));
  await user.clear(input);
  await user.type(input, text);
}

describe("GlobalSearch", () => {
  beforeEach(() => {
    fake.calls = [];
    fake.search = async (q, kinds, limit) => {
      fake.calls.push({ query: q, kinds, limit });
      return result({ query: q });
    };
    fake.rebuild = async () => ({
      availability: "available",
      builtAt: "2026-11-06T10:00:00Z",
      summary: { sessions: 1, agents: 1, skills: 1, artifacts: 1 },
    });
  });

  it("少于 3 个字符不调后端，直接提示太短", async () => {
    const user = userEvent.setup();
    renderSearch();
    await typeQuery(user, "ab");
    expect(fake.calls).toHaveLength(0);
    expect(screen.getByText(t("search.tooShort"))).toBeInTheDocument();
  });

  it("3 个字符起调用后端并渲染真实结果", async () => {
    const user = userEvent.setup();
    renderSearch();
    await typeQuery(user, "alpha");
    await waitFor(() => expect(fake.calls).toHaveLength(1));
    expect(fake.calls[0].query).toBe("alpha");
    expect(fake.calls[0].kinds).toEqual([]);
    expect(screen.getByText("alpha 会话")).toBeInTheDocument();
    expect(screen.getByText(t("search.kinds.artifact"))).toBeInTheDocument();
  });

  it("点击结果按结果自带的 route 导航（后端给的是稳定 id）", async () => {
    const user = userEvent.setup();
    const { onNavigate } = renderSearch();
    await typeQuery(user, "alpha");
    await waitFor(() => expect(screen.getByText("alpha 会话")).toBeInTheDocument());
    await user.click(screen.getByText("alpha 会话"));
    expect(onNavigate).toHaveBeenCalledWith("/chat/sess-1");
  });

  it("索引未建立显示 unavailable 与重建入口，而不是 0 条", async () => {
    fake.search = async (q, kinds, limit) => {
      fake.calls.push({ query: q, kinds, limit });
      return { query: q, availability: "unavailable", items: [] };
    };
    const user = userEvent.setup();
    renderSearch();
    await typeQuery(user, "alpha");
    await waitFor(() =>
      expect(screen.getByText(t("search.indexUnavailable"))).toBeInTheDocument()
    );
    expect(screen.getByText(t("status.unavailable"))).toBeInTheDocument();
    expect(screen.queryByText("0")).toBeNull();

    await user.click(screen.getByRole("button", { name: t("search.rebuild") }));
    await waitFor(() => expect(fake.calls.length).toBeGreaterThanOrEqual(2));
  });

  it("页面动作来自静态目录，即使索引没有命中也能跳到设置", async () => {
    fake.search = async (q, kinds, limit) => {
      fake.calls.push({ query: q, kinds, limit });
      return { query: q, availability: "available", items: [] };
    };
    const user = userEvent.setup();
    const { onNavigate } = renderSearch();
    // 3 个字符以上才会打后端；这里顺便验证静态页面目录不依赖后端索引。
    await typeQuery(user, "settings");
    await waitFor(() => expect(fake.calls).toHaveLength(1));

    const pages = screen.getByTestId("search-pages");
    await user.click(within(pages).getByRole("button", { name: new RegExp(t("nav.settings")) }));
    expect(onNavigate).toHaveBeenCalledWith("/settings");
  });

  it("后端拒绝（例如查询太短）时显示本地化错误码文案", async () => {
    fake.search = async (q, kinds, limit) => {
      fake.calls.push({ query: q, kinds, limit });
      return {
        query: q,
        availability: "unavailable",
        items: [],
        error: { code: "QUERY_TOO_SHORT", message: "too short", retryable: false },
      };
    };
    const user = userEvent.setup();
    renderSearch();
    await typeQuery(user, "alpha");
    await waitFor(() =>
      expect(screen.getByText(t("errors.QUERY_TOO_SHORT"))).toBeInTheDocument()
    );
  });
});
