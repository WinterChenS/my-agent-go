// 观测页测试（U06 / UI11）。
//
// 这一页的全部价值是"不编造"：
//   - 缺 usage 或价格显示 unavailable，不显示 0（RUN06）；
//   - run 链路只来自后端事件记录，缺事件就写"答不出来"；
//   - 导出只展示后端返回的路径/hash/脱敏计数；
//   - 没有列 run 的绑定时，明确写成 unavailable 并说清原因。

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { formatNumber, t } from "../../i18n";
import { resolveRoute } from "../../app/navigation";

const fake = vi.hoisted(() => ({
  usageSummary: null as unknown as (...a: any[]) => Promise<any>,
  runDetails: null as unknown as (...a: any[]) => Promise<any>,
  exportDiagnostics: null as unknown as (...a: any[]) => Promise<any>,
}));

vi.mock("./api", () => ({ observabilityApi: fake }));

import { ObservabilityPage } from "./ObservabilityPage";

function summary(overrides: Record<string, any> = {}) {
  return {
    summary: {
      availability: "unavailable",
      range: {},
      runs: 3,
      reportedRuns: 0,
      unreportedRuns: 3,
      tokens: { availability: "unavailable", reportedRuns: 0, unreportedRuns: 3 },
      cost: { availability: "unavailable", knownRuns: 0, unknownRuns: 0 },
      note: "3 个 run 有记录，但都没有可用的用量报告",
      ...overrides,
    },
  };
}

function runDetails(overrides: Record<string, any> = {}) {
  return {
    details: {
      runId: "run-1",
      record: {
        runId: "run-1",
        sessionId: "sess-1",
        state: "failed",
        piVersion: "1.5.0",
        provider: "openai",
        model: "gpt-5",
        configRevision: 4,
        lastSeq: 9,
        startedAt: "2026-11-05T01:00:00Z",
        updatedAt: "2026-11-05T01:05:00Z",
        usage: { availability: "unavailable" },
        error: { code: "PROVIDER_UNREACHABLE", message: "连不上", retryable: true },
      },
      usage: summary().summary,
      artifacts: [],
      tools: [
        { seq: 2, eventId: "e2", type: "tool.start", occurredAt: "2026-11-05T01:01:00Z", toolCallId: "c1", toolName: "read" },
        { seq: 3, eventId: "e3", type: "tool.end", occurredAt: "2026-11-05T01:02:00Z", toolCallId: "c1", toolName: "read", isError: true },
      ],
      approvals: [
        { seq: 4, eventId: "e4", type: "approval.request", occurredAt: "2026-11-05T01:03:00Z", approvalId: "a1", toolName: "shell" },
        { seq: 5, eventId: "e5", type: "approval.resolved", occurredAt: "2026-11-05T01:03:30Z", approvalId: "a1", toolName: "shell", choice: "deny" },
      ],
      compactions: [],
      diagnostics: [],
      errors: [
        { seq: 9, eventId: "e9", type: "run.error", occurredAt: "2026-11-05T01:05:00Z", kind: "PROVIDER_UNREACHABLE", message: "连不上" },
      ],
      eventCount: 9,
      lastEventId: "e9",
      unavailable: [
        { section: "context", reason: "本版本不重建 run 级上下文来源明细" },
      ],
      ...overrides,
    },
  };
}

beforeEach(() => {
  fake.usageSummary = vi.fn(async () => summary());
  fake.runDetails = vi.fn(async () => runDetails());
  fake.exportDiagnostics = vi.fn(async () => ({
    path: "D:/anvil/diagnostics/diag-1.json",
    bytes: 2048,
    sha256: "f".repeat(64),
    redactions: 3,
    runs: 1,
    events: 9,
    artifacts: 0,
    schemaVersion: 1,
    generatedAt: "2026-11-05T02:00:00Z",
  }));
});

function renderPage(path = "/observability") {
  const onNavigate = vi.fn();
  const view = render(<ObservabilityPage onNavigate={onNavigate} route={resolveRoute(path)} />);
  return { onNavigate, view };
}

describe("观测页 · usage", () => {
  it("缺 usage 时显示 unavailable，不显示伪造的 0", async () => {
    renderPage();
    const section = await screen.findByTestId("observability-usage");

    expect(await within(section).findByTestId("usage-unavailable")).toHaveTextContent(
      t("observability.usage.unavailable")
    );
    expect(within(section).queryByTestId("usage-tokens")).toBeNull();
    expect(within(section).queryByTestId("usage-cost")).toBeNull();
    expect(within(section).getByTestId("usage-runs")).toHaveTextContent("3");
    expect(within(section).getByTestId("usage-note")).toHaveTextContent("3 个 run");
  });

  it("有 tokens 没有价格时，tokens 照报、费用 unavailable", async () => {
    fake.usageSummary = vi.fn(async () => ({
      summary: {
        availability: "reported",
        range: {},
        runs: 2,
        reportedRuns: 2,
        unreportedRuns: 0,
        tokens: {
          availability: "reported",
          inputTokens: 1200,
          outputTokens: 300,
          totalTokens: 1500,
          reportedRuns: 2,
          unreportedRuns: 0,
        },
        cost: { availability: "unavailable", knownRuns: 0, unknownRuns: 2 },
        note: "2 个 run 有 tokens 但没有价格，合计费用不可知",
      },
    }));
    renderPage();
    const section = await screen.findByTestId("observability-usage");

    expect(await within(section).findByTestId("usage-tokens-input")).toHaveTextContent(
      formatNumber(1200)
    );
    expect(within(section).getByTestId("usage-tokens-total")).toHaveTextContent(formatNumber(1500));
    expect(within(section).getByTestId("usage-cost")).toHaveTextContent(
      t("observability.usage.unavailable")
    );
    expect(within(section).getByTestId("usage-cost")).not.toHaveTextContent("0");
  });

  it("费用可用时显示金额、来源与估算标记", async () => {
    fake.usageSummary = vi.fn(async () => ({
      summary: {
        availability: "reported",
        range: {},
        runs: 1,
        reportedRuns: 1,
        unreportedRuns: 0,
        tokens: {
          availability: "reported",
          inputTokens: 10,
          outputTokens: 20,
          totalTokens: 30,
          reportedRuns: 1,
          unreportedRuns: 0,
        },
        cost: { availability: "reported", costUsd: 0.42, priceSource: "catalog", estimated: true, knownRuns: 1, unknownRuns: 0 },
      },
    }));
    renderPage();
    const section = await screen.findByTestId("usage-cost");
    expect(await within(section).findByTestId("usage-cost-value")).toHaveTextContent("0.42");
    expect(within(section).getByTestId("usage-cost-source")).toHaveTextContent("catalog");
    expect(within(section).getByTestId("usage-cost-estimated")).toBeInTheDocument();
  });

  it("usage 读取失败显示错误码与重试", async () => {
    fake.usageSummary = vi
      .fn()
      .mockResolvedValueOnce({ summary: summary().summary, error: { code: "INTERNAL_ERROR", message: "库打不开", retryable: true } })
      .mockResolvedValueOnce(summary());
    renderPage();
    const section = await screen.findByTestId("observability-usage");
    expect(await within(section).findByText(t("errors.INTERNAL_ERROR"))).toBeInTheDocument();
    await userEvent.click(within(section).getByRole("button", { name: t("common.retry") }));
    expect(await within(section).findByTestId("usage-unavailable")).toBeInTheDocument();
    expect(fake.usageSummary).toHaveBeenCalledTimes(2);
  });

  it("把时间范围原样传给后端（空串表示不限）", async () => {
    renderPage();
    const section = await screen.findByTestId("observability-usage");
    const from = await within(section).findByLabelText(t("observability.usage.from"));
    await userEvent.type(from, "2026-11-01T00:00:00Z");
    await userEvent.click(within(section).getByRole("button", { name: t("observability.usage.apply") }));

    await waitFor(() =>
      expect(fake.usageSummary).toHaveBeenLastCalledWith({
        from: "2026-11-01T00:00:00Z",
        to: "",
        workspaceId: "",
        sessionId: "",
        runIds: [],
      })
    );
  });
});

describe("观测页 · run 链路", () => {
  it("按 run id 载入真实工具/审批/错误事实，并按 seq 组成链路", async () => {
    renderPage("/observability?run=run-1");
    const section = await screen.findByTestId("observability-run");

    await waitFor(() => expect(fake.runDetails).toHaveBeenCalledWith("run-1"));
    const rows = await within(section).findAllByTestId(/^chain-row-/);
    // 链路按 seq 升序，不按到达顺序/类型分组。
    expect(rows.map((r) => r.getAttribute("data-seq"))).toEqual(["2", "3", "4", "5", "9"]);
    expect(within(section).getByTestId("chain-row-9")).toHaveTextContent("PROVIDER_UNREACHABLE");
    expect(within(section).getByTestId("run-event-count")).toHaveTextContent("9");
  });

  it("缺失的段落显示 unavailable（不是空数组冒充没发生）", async () => {
    renderPage("/observability?run=run-1");
    const section = await screen.findByTestId("observability-run");
    const unavailable = await within(section).findByTestId("run-unavailable");
    expect(unavailable).toHaveTextContent("context");
    expect(unavailable).toHaveTextContent("本版本不重建 run 级上下文来源明细");
  });

  it("run 不存在时显示 NOT_FOUND，不渲染链路", async () => {
    fake.runDetails = vi.fn(async () => ({
      details: { runId: "run-x", usage: summary().summary, unavailable: [] },
      error: { code: "NOT_FOUND", message: "没有这个 run", retryable: false },
    }));
    renderPage("/observability?run=run-x");
    const section = await screen.findByTestId("observability-run");
    expect(await within(section).findByText(t("errors.NOT_FOUND"))).toBeInTheDocument();
    expect(within(section).queryByTestId("run-chain")).toBeNull();
  });

  it("没有 run id 时给出空态，并说明缺「列 run」绑定", async () => {
    renderPage();
    const section = await screen.findByTestId("observability-run");
    expect(await within(section).findByText(t("observability.run.emptyTitle"))).toBeInTheDocument();
    expect(within(section).getByTestId("run-list-unavailable")).toHaveTextContent(
      t("observability.run.listUnavailableReason")
    );
    expect(fake.runDetails).not.toHaveBeenCalled();
  });

  it("链路条目可以深链回来源会话", async () => {
    const { onNavigate } = renderPage("/observability?run=run-1");
    const section = await screen.findByTestId("observability-run");
    await userEvent.click(
      await within(section).findByRole("button", { name: t("observability.run.openSession") })
    );
    expect(onNavigate).toHaveBeenCalledWith("/chat/sess-1");
  });

  it("事件很多时有上界，可手动展开（不一次渲染全部）", async () => {
    const tools = Array.from({ length: 60 }, (_, i) => ({
      seq: i + 1,
      eventId: `e${i + 1}`,
      type: "tool.start",
      occurredAt: "2026-11-05T01:01:00Z",
      toolCallId: `c${i}`,
      toolName: "read",
    }));
    fake.runDetails = vi.fn(async () => runDetails({ tools, approvals: [], errors: [] }));
    renderPage("/observability?run=run-1");
    const section = await screen.findByTestId("observability-run");

    expect(await within(section).findAllByTestId(/^chain-row-/)).toHaveLength(50);
    await userEvent.click(within(section).getByRole("button", { name: t("common.showMore") }));
    await waitFor(() =>
      expect(within(section).getAllByTestId(/^chain-row-/)).toHaveLength(60)
    );
  });
});

describe("观测页 · 诊断导出", () => {
  it("导出走真实绑定，并展示路径/hash/脱敏计数", async () => {
    renderPage("/observability?run=run-1");
    const section = await screen.findByTestId("observability-export");

    await userEvent.click(await within(section).findByRole("button", { name: t("observability.export.action") }));
    await waitFor(() =>
      expect(fake.exportDiagnostics).toHaveBeenCalledWith({
        runIds: ["run-1"],
        sessionId: "",
        workspaceId: "",
        from: "",
        to: "",
        target: "",
      })
    );

    const result = await within(section).findByTestId("export-result");
    expect(within(result).getByTestId("export-path")).toHaveTextContent(
      "D:/anvil/diagnostics/diag-1.json"
    );
    expect(within(result).getByTestId("export-sha256")).toHaveTextContent("f".repeat(64));
    expect(within(result).getByTestId("export-redactions")).toHaveTextContent("3");
    expect(within(result).getByTestId("export-schema")).toHaveTextContent("1");
  });

  it("导出失败显示错误码，不显示成功路径", async () => {
    fake.exportDiagnostics = vi.fn(async () => ({
      path: "",
      bytes: 0,
      sha256: "",
      redactions: 0,
      runs: 0,
      events: 0,
      artifacts: 0,
      schemaVersion: 0,
      generatedAt: "",
      error: { code: "CONFIG_MISSING", message: "诊断根目录不可用", retryable: false },
    }));
    renderPage("/observability?run=run-1");
    const section = await screen.findByTestId("observability-export");
    await userEvent.click(await within(section).findByRole("button", { name: t("observability.export.action") }));

    expect(await within(section).findByText(t("errors.CONFIG_MISSING"))).toBeInTheDocument();
    expect(within(section).queryByTestId("export-result")).toBeNull();
  });
});
