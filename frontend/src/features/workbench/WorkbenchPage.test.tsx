// 工作台测试（U01 / UI05）。
//
// 核心断言：缺来源显示 unavailable，而不是 0。0 会被读成"确实没有"，
// 而"读不出来"需要用户去修——两者在界面上必须是不同的话。
// 断言渲染出来的文字，不是只断言 DTO。

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import { t } from "../../i18n";

const fake = vi.hoisted(() => ({
  summary: null as unknown as () => Promise<any>,
}));

vi.mock("./api", () => ({
  workbenchApi: {
    summary: () => fake.summary(),
  },
}));

import { WorkbenchPage } from "./WorkbenchPage";

type Section = {
  availability: "available" | "unavailable";
  reasonCode?: string;
  count?: number;
};

function section(overrides: Partial<Section> = {}): Section {
  return { availability: "available", count: 0, ...overrides };
}

function summary(overrides: Record<string, any> = {}) {
  return {
    generatedAt: "2026-11-06T10:00:00Z",
    currentRuns: section({ count: 1 }),
    pendingApprovals: section({ count: 1 }),
    recentSessions: section({ count: 1 }),
    recentArtifacts: section({ count: 1 }),
    schedules: section({ count: 1 }),
    health: section({ count: 1 }),
    runs: [
      {
        runId: "run-1",
        sessionId: "sess-1",
        sessionTitle: "alpha 会话",
        state: "running",
        updatedAt: "2026-11-06T09:59:00Z",
      },
    ],
    approvals: [
      {
        runId: "run-1",
        sessionId: "sess-1",
        approvalId: "ap-1",
        toolName: "shell",
        requestedAt: "2026-11-06T09:58:00Z",
      },
    ],
    sessions: [
      {
        sessionId: "sess-1",
        workspaceId: "ws-a",
        title: "alpha 会话",
        pinned: false,
        updatedAt: "2026-11-06T09:57:00Z",
      },
    ],
    artifacts: [
      {
        artifactId: "art-1",
        workspaceId: "ws-a",
        displayName: "report.md",
        size: 42,
        updatedAt: "2026-11-06T09:56:00Z",
      },
    ],
    schedule: [
      {
        scheduleId: "sch-1",
        name: "每日整理",
        enabled: true,
        nextScheduledAt: "2026-11-07T01:00:00Z",
        consecutiveFailures: 0,
      },
    ],
    healthInfo: {
      activeRuns: 1,
      completed24h: 3,
      failed24h: 1,
      lastRunId: "run-1",
      lastState: "running",
      lastErrorCode: "",
    },
    ...overrides,
  };
}

function card(title: string): HTMLElement {
  const heading = screen.getByRole("heading", { name: title });
  const el = heading.closest("[data-workbench-card]");
  if (!(el instanceof HTMLElement)) throw new Error(`找不到卡片: ${title}`);
  return el;
}

describe("WorkbenchPage", () => {
  beforeEach(() => {
    fake.summary = async () => summary();
  });

  it("先显示加载态，再显示六段真实数据", async () => {
    let release: (v: any) => void = () => {};
    fake.summary = () => new Promise((resolve) => (release = resolve));
    render(<WorkbenchPage onNavigate={() => {}} />);
    expect(screen.getByText(t("state.loading"))).toBeInTheDocument();

    release(summary());
    await waitFor(() => expect(screen.queryByText(t("state.loading"))).toBeNull());
    for (const key of [
      "workbench.currentRuns",
      "workbench.pendingApprovals",
      "workbench.recentSessions",
      "workbench.recentArtifacts",
      "workbench.schedules",
      "workbench.health",
    ]) {
      expect(screen.getByRole("heading", { name: t(key) })).toBeInTheDocument();
    }
    expect(within(card(t("workbench.currentRuns"))).getByText("alpha 会话")).toBeInTheDocument();
    expect(within(card(t("workbench.pendingApprovals"))).getByText("shell")).toBeInTheDocument();
    expect(within(card(t("workbench.recentArtifacts"))).getByText("report.md")).toBeInTheDocument();
    expect(within(card(t("workbench.schedules"))).getByText("每日整理")).toBeInTheDocument();
  });

  it("来源读不出来时显示 unavailable 与原因，且不出现 0", async () => {
    fake.summary = async () =>
      summary({
        schedules: { availability: "unavailable", reasonCode: "CONFIG_MISSING" },
      });
    render(<WorkbenchPage onNavigate={() => {}} />);
    await waitFor(() => expect(screen.queryByText(t("state.loading"))).toBeNull());

    const schedules = card(t("workbench.schedules"));
    // 渲染出来的是"不可用"，不是数字 0。
    expect(within(schedules).getByText(t("status.unavailable"))).toBeInTheDocument();
    expect(within(schedules).getByText(t("workbench.unavailable"))).toBeInTheDocument();
    expect(within(schedules).queryByText("0")).toBeNull();
    expect(within(schedules).getByText("CONFIG_MISSING")).toBeInTheDocument();

    // 其它段仍然是可用并显示各自的计数，不被一段故障连坐。
    expect(within(card(t("workbench.recentSessions"))).queryByText(t("status.unavailable"))).toBeNull();
  });

  it("没有数据时显示空态文案，不显示伪造的统计", async () => {
    fake.summary = async () =>
      summary({
        currentRuns: section({ count: 0 }),
        pendingApprovals: section({ count: 0 }),
        recentSessions: section({ count: 0 }),
        recentArtifacts: section({ count: 0 }),
        schedules: section({ count: 0 }),
        runs: [],
        approvals: [],
        sessions: [],
        artifacts: [],
        schedule: [],
        healthInfo: {
          activeRuns: 0,
          completed24h: 0,
          failed24h: 0,
          lastRunId: "",
          lastState: "",
          lastErrorCode: "",
        },
      });
    render(<WorkbenchPage onNavigate={() => {}} />);
    await waitFor(() => expect(screen.queryByText(t("state.loading"))).toBeNull());

    // 每个空段都要说"暂无数据"，而不是留一片空白让人以为还在加载。
    expect(screen.getAllByText(t("workbench.empty")).length).toBeGreaterThanOrEqual(4);
    expect(within(card(t("workbench.health"))).getByText(t("workbench.lastRunNone"))).toBeInTheDocument();
  });

  it("整体读取失败时显示结构化错误（带 code）与重试", async () => {
    fake.summary = async () => ({
      ...summary({}),
      error: { code: "INTERNAL_ERROR", message: "boom", retryable: true },
    });
    render(<WorkbenchPage onNavigate={() => {}} />);
    await waitFor(() => expect(screen.getByRole("alert")).toBeInTheDocument());
    expect(screen.getByRole("alert")).toHaveTextContent("INTERNAL_ERROR");
    expect(screen.getByRole("button", { name: t("common.retry") })).toBeInTheDocument();
  });

  it("运行健康显示真实计数与最近一次结论", async () => {
    render(<WorkbenchPage onNavigate={() => {}} />);
    await waitFor(() => expect(screen.queryByText(t("state.loading"))).toBeNull());
    const health = card(t("workbench.health"));
    expect(
      within(health).getByText(t("workbench.completed24h", { count: 3 }))
    ).toBeInTheDocument();
    expect(within(health).getByText(t("workbench.failed24h", { count: 1 }))).toBeInTheDocument();
    expect(within(health).getByText(t("status.running"))).toBeInTheDocument();
  });

  it("会话与产物条目点击后导航到稳定路由", async () => {
    const routes: string[] = [];
    render(<WorkbenchPage onNavigate={(r) => routes.push(r)} />);
    await waitFor(() => expect(screen.queryByText(t("state.loading"))).toBeNull());

    within(card(t("workbench.recentSessions"))).getByRole("button", { name: /alpha 会话/ }).click();
    within(card(t("workbench.recentArtifacts"))).getByRole("button", { name: /report\.md/ }).click();
    expect(routes).toEqual(["/chat/sess-1", "/library?artifact=art-1"]);
  });
});
