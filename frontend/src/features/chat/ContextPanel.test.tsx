// 右栏四分区测试（U02 / UI06）。
//
// 两件事必须一直被守住：
//
//  1. **读不到就是读不到。** 缺来源（没有浏览器状态接口、RunDetails 没有引用字段、
//     产物列表报错）只显示 unavailable + 原因，不显示 0 或空列表冒充“没有”。
//
//  2. **窄窗变抽屉不重建浏览器分区。** 真机上浏览器是原生子视图：宿主节点被重建
//     等于把页面、滚动位置和登录态一起丢掉。测试用挂载计数与节点身份断言这一点。

import { useEffect } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { t } from "../../i18n";

const backend = vi.hoisted(() => ({
  runArtifacts: null as unknown as (...a: any[]) => Promise<any>,
  runDetails: null as unknown as (...a: any[]) => Promise<any>,
  artifact: null as unknown as (...a: any[]) => Promise<any>,
}));

vi.mock("./api", () => ({
  chatApi: {
    startSessionRun: vi.fn(),
    abort: vi.fn(),
    resolveApproval: vi.fn(),
    answerClarification: vi.fn(),
    replay: vi.fn(),
    resolveAttachments: vi.fn(),
    exportSession: vi.fn(),
    runArtifacts: (...a: any[]) => backend.runArtifacts(...a),
    runDetails: (...a: any[]) => backend.runDetails(...a),
    artifact: (...a: any[]) => backend.artifact(...a),
  },
}));

import { ContextPanel } from "./ContextPanel";
import { BrowserPanel } from "../browser/BrowserPanel";
import { browserApiStub, browserStateFixture } from "../browser/testFixtures";

function details(overrides: Record<string, any> = {}) {
  return {
    runId: "run-1",
    record: {
      runId: "run-1",
      state: "completed",
      provider: "deepseek",
      model: "deepseek-v4.1-flash",
      configRevision: 2,
      lastSeq: 9,
      usage: { availability: "unavailable" },
    },
    usage: {
      availability: "unavailable",
      runs: 1,
      reportedRuns: 0,
      unreportedRuns: 1,
      tokens: { availability: "unavailable", reportedRuns: 0, unreportedRuns: 1 },
      cost: { availability: "unavailable", knownRuns: 0, unknownRuns: 1 },
    },
    artifacts: [],
    tools: [{ seq: 3, eventId: "e3", type: "tool.start", occurredAt: "2026-11-06T00:00:00Z", toolCallId: "c1", toolName: "bash" }],
    approvals: [],
    compactions: [],
    diagnostics: [],
    errors: [],
    eventCount: 9,
    unavailable: [{ section: "context", reason: "Pi 事件不含压缩明细" }],
    ...overrides,
  };
}

beforeEach(() => {
  backend.runArtifacts = async () => ({ items: [] });
  backend.runDetails = async () => ({ details: details() });
  backend.artifact = async () => ({ item: { id: "art-1", sha256: "a".repeat(64), verification: { status: "verified", checkedAt: "" } } });
});

describe("ContextPanel 四个分区", () => {
  it("没有 run 时四个分区都说明“先发一条消息”，不显示假数据", async () => {
    render(<ContextPanel runId="" open onToggle={() => undefined} narrow={false} />);
    for (const id of ["browser", "artifacts", "citations", "execution"]) {
      expect(document.querySelector(`[data-context-section='${id}']`)).not.toBeNull();
    }
    expect(screen.getAllByText(t("chat.context.noRun")).length).toBe(3);
    // 浏览器分区：后端没有接口，如实说不可用。
    expect(document.querySelector("[data-browser-section='unavailable']")).not.toBeNull();
  });

  it("缺来源显示 unavailable：浏览器接口缺失、引用字段缺失", async () => {
    render(<ContextPanel runId="run-1" open onToggle={() => undefined} narrow={false} />);
    await waitFor(() =>
      expect(document.querySelector("[data-execution-state]")?.textContent).toBe("completed")
    );
    expect(document.querySelector("[data-browser-section='unavailable']")).not.toBeNull();
    expect(screen.getByText(t("chat.context.browserMissing"))).toBeInTheDocument();
    expect(screen.getByText(t("chat.context.citationsMissing"))).toBeInTheDocument();
    // 后端明确说“这段答不出来”的原样显示。
    expect(document.querySelector("[data-execution-unavailable]")).not.toBeNull();
    expect(screen.getByText(/Pi 事件不含压缩明细/)).toBeInTheDocument();
  });

  it("用量缺失时显示不可用，不显示 0", async () => {
    render(<ContextPanel runId="run-1" open onToggle={() => undefined} narrow={false} />);
    await waitFor(() => expect(document.querySelector("[data-usage='unavailable']")).not.toBeNull());
    expect(screen.getByText(t("chat.context.usageUnavailable"))).toBeInTheDocument();
    expect(document.querySelector("[data-usage='reported']")).toBeNull();
  });

  it("产物显示磁盘路径与 hash，核对结果按后端状态渲染", async () => {
    const user = userEvent.setup();
    backend.runArtifacts = async () => ({
      items: [
        {
          id: "art-1",
          workspaceId: "ws-a",
          runId: "run-1",
          sessionId: "sess-1",
          displayName: "report.md",
          canonicalPath: "/work/alpha/report.md",
          sha256: "a".repeat(64),
          size: 2048,
          mediaType: "text/markdown",
          createdAt: "2026-11-06T00:00:00Z",
          updatedAt: "2026-11-06T00:00:00Z",
        },
      ],
    });
    backend.artifact = async () => ({
      item: {
        id: "art-1",
        sha256: "a".repeat(64),
        verification: { status: "drifted", observedSha256: "d".repeat(64), checkedAt: "2026-11-06T01:00:00Z" },
      },
    });
    const onLocateTurn = vi.fn();
    render(
      <ContextPanel runId="run-1" open onToggle={() => undefined} narrow={false} onLocateTurn={onLocateTurn} />
    );

    await waitFor(() => expect(document.querySelector("[data-artifact-id='art-1']")).not.toBeNull());
    expect(screen.getByText("/work/alpha/report.md")).toBeInTheDocument();
    expect(document.querySelector("[data-artifact-hash]")?.textContent).toBe("a".repeat(64));

    await user.click(screen.getByRole("button", { name: t("chat.context.verify") }));
    await waitFor(() => expect(document.querySelector("[data-artifact-observed]")).not.toBeNull());
    expect(document.querySelector("[data-artifact-observed]")?.textContent).toBe("d".repeat(64));
    expect(screen.getByText(t("chat.context.hashDrifted"))).toBeInTheDocument();

    // 产物能回到来源轮次。
    await user.click(screen.getByRole("button", { name: t("chat.context.locateTurn") }));
    expect(onLocateTurn).toHaveBeenCalledWith("run-1");
  });

  it("产物列表报错时显示错误码，不显示空列表", async () => {
    backend.runArtifacts = async () => ({
      items: [],
      error: { code: "CONFIG_MISSING", message: "索引未就绪", retryable: false },
    });
    render(<ContextPanel runId="run-1" open onToggle={() => undefined} narrow={false} />);
    await waitFor(() => expect(screen.getByText("CONFIG_MISSING")).toBeInTheDocument());
    expect(screen.queryByText(t("chat.context.noArtifacts"))).toBeNull();
  });
});

describe("窄窗抽屉不重建浏览器分区", () => {
  it("宽窗 → 窄窗只换布局，浏览器分区挂载次数保持 1 且节点不变", async () => {
    const mounts = { count: 0 };
    function Probe() {
      useEffect(() => {
        mounts.count += 1;
      }, []);
      return <div data-browser-probe="">native host</div>;
    }

    const { rerender } = render(
      <ContextPanel runId="run-1" open onToggle={() => undefined} narrow={false} browserSlot={<Probe />} />
    );
    expect(document.querySelector("[data-context-panel]")?.getAttribute("data-mode")).toBe("aside");
    expect(mounts.count).toBe(1);
    const node = document.querySelector("[data-browser-probe]");
    expect(node).not.toBeNull();

    rerender(
      <ContextPanel runId="run-1" open onToggle={() => undefined} narrow browserSlot={<Probe />} />
    );

    expect(document.querySelector("[data-context-panel]")?.getAttribute("data-mode")).toBe("drawer");
    // 同一个 DOM 节点：原生子视图的宿主没有被重建。
    expect(document.querySelector("[data-browser-probe]")).toBe(node);
    expect(mounts.count).toBe(1);
  });

  it("抽屉关闭时是隐藏而不是卸载（原生视图不能被销毁）", () => {
    const mounts = { count: 0 };
    function Probe() {
      useEffect(() => {
        mounts.count += 1;
      }, []);
      return <div data-browser-probe="">native host</div>;
    }
    const { rerender } = render(
      <ContextPanel runId="run-1" open onToggle={() => undefined} narrow browserSlot={<Probe />} />
    );
    rerender(
      <ContextPanel runId="run-1" open={false} onToggle={() => undefined} narrow browserSlot={<Probe />} />
    );
    expect(document.querySelector("[data-browser-probe]")).not.toBeNull();
    expect(document.querySelector("[data-context-panel]")?.getAttribute("data-open")).toBe("false");
    expect(mounts.count).toBe(1);
  });

  it("装的是真实浏览器面板时，抽屉开关同样不重挂载原生槽位", async () => {
    // 这一条用**产品里真的会挂上去的组件**（BrowserPanel），而不是探针：
    // 挂载计数来自 BrowserSlot 自己的 onMount，节点身份来自 data-browser-slot。
    const mounts = { count: 0 };
    const api = browserApiStub(browserStateFixture());
    const panel = (
      <BrowserPanel
        api={api}
        onSlotMount={() => {
          mounts.count += 1;
        }}
        pollIntervalMs={0}
      />
    );

    const { rerender } = render(
      <ContextPanel runId="run-1" open onToggle={() => undefined} narrow={false} browserSlot={panel} />
    );
    await waitFor(() => expect(document.querySelector("[data-browser-slot]")).not.toBeNull());
    expect(mounts.count).toBe(1);
    const slot = document.querySelector("[data-browser-slot]");
    const tabs = document.querySelector("[data-browser-tabs]");
    expect(tabs).not.toBeNull();

    rerender(<ContextPanel runId="run-1" open onToggle={() => undefined} narrow browserSlot={panel} />);
    expect(document.querySelector("[data-context-panel]")?.getAttribute("data-mode")).toBe("drawer");
    expect(document.querySelector("[data-browser-slot]")).toBe(slot);
    expect(document.querySelector("[data-browser-tabs]")).toBe(tabs);
    expect(mounts.count).toBe(1);

    rerender(
      <ContextPanel runId="run-1" open={false} onToggle={() => undefined} narrow browserSlot={panel} />
    );
    expect(document.querySelector("[data-browser-slot]")).toBe(slot);
    expect(mounts.count).toBe(1);
  });
});
