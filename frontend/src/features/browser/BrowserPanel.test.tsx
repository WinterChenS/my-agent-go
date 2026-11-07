// 右栏浏览面板（B07 / UI03）测试。
//
// 守的是四件事：
//
//  1. **停止按钮永远在原生网页区域之外**。原生视图是盖在 slot 那块矩形上的子窗口；
//     只要停止按钮是 slot 的兄弟节点、位于工具条里，它就不可能被盖住。
//  2. **tab 状态由后端驱动，不由 React 重建**。切换 tab 只调用后端、重新读状态；
//     slot 节点身份不变、挂载计数仍是 1、几何不重复上报（否则原生视图会被重摆）。
//  3. **读不到就说读不到**。没有原生宿主时显示具体原因，不留空白面板。
//  4. **控制状态可见**：agent/human/pausing 三档各有明确动作与原因，失败不静默。

import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { t } from "../../i18n";
import type { BrowserBackendApi, BrowserSlotGeometry, BrowserStateView } from "./api";
import { BrowserPanel } from "./BrowserPanel";

function view(over: Partial<BrowserStateView> = {}): BrowserStateView {
  return {
    available: true,
    slot: {
      slotId: "browser-slot",
      revision: 0,
      left: 640,
      top: 96,
      width: 480,
      height: 600,
      visible: true,
      zoom: 1,
    },
    hasSlot: false,
    chromeReservedTop: 96,
    activeTab: "tab-1",
    tabs: [
      {
        tabId: "tab-1",
        url: "https://example.test/",
        active: true,
        navigationId: "nav-1",
        controlGeneration: 1,
        mode: "agent",
        queued: 0,
        inFlight: 0,
      },
    ],
    control: {
      tabId: "tab-1",
      mode: "agent",
      generation: 1,
      navigationId: "nav-1",
      queued: 0,
      inFlight: 0,
      canTakeOver: true,
      canResume: false,
    },
    visible: true,
    requestedVisible: true,
    overlays: [],
    history: ["https://example.test/"],
    capabilities: {
      platform: "windows",
      engine: "WebView2",
      navigate: true,
      observe: true,
      act: true,
      screenshot: true,
      trustedInput: false,
      crossOriginFrames: false,
      closedShadowRoots: false,
      downloads: false,
      popups: false,
      unsupported: [],
    },
    unsupported: [],
    counters: {
      slotPresented: 1,
      slotDropped: 0,
      slotDeduped: 0,
      visibilityChanges: 1,
      overlayTransitions: 0,
      tabSelectDeduped: 0,
      tabsClosed: 0,
    },
    occlusion: [],
    ...over,
  };
}

interface FakeApi {
  api: BrowserBackendApi;
  calls: Array<{ name: string; args: unknown[] }>;
  set(next: BrowserStateView): void;
  failNext(name: string, error: { code: string; message: string; retryable: boolean }): void;
}

function fakeApi(initial: BrowserStateView): FakeApi {
  let current = initial;
  const calls: Array<{ name: string; args: unknown[] }> = [];
  const failures = new Map<string, { code: string; message: string; retryable: boolean }>();
  const record = (name: string, args: unknown[]) => {
    calls.push({ name, args });
    const failure = failures.get(name);
    if (failure) {
      failures.delete(name);
      throw failure;
    }
  };
  const api: BrowserBackendApi = {
    state: async () => current,
    updateSlot: async (g: BrowserSlotGeometry) => {
      record("updateSlot", [g]);
      return null;
    },
    selectTab: async (id: string) => {
      record("selectTab", [id]);
      return null;
    },
    closeTab: async (id: string) => {
      record("closeTab", [id]);
      return null;
    },
    navigate: async (id: string, url: string) => {
      record("navigate", [id, url]);
      return null;
    },
    back: async (id: string) => {
      record("back", [id]);
      return null;
    },
    reload: async (id: string) => {
      record("reload", [id]);
      return null;
    },
    takeOver: async (id: string) => {
      record("takeOver", [id]);
      return { tabId: id, control: current.control };
    },
    resume: async (id: string) => {
      record("resume", [id]);
      return { tabId: id, control: current.control };
    },
    setOverlay: async (source: string, open: boolean) => {
      record("setOverlay", [source, open]);
      return null;
    },
  };
  return {
    api,
    calls,
    set: (next) => {
      current = next;
    },
    failNext: (name, error) => {
      failures.set(name, error);
    },
  };
}

function tabButton(tabId: string): HTMLElement {
  const node = document.querySelector(`[data-browser-tab="${tabId}"]`);
  if (!node) throw new Error(`找不到 tab 按钮 ${tabId}（tab 列表没有来自后端状态？）`);
  return node as HTMLElement;
}

describe("BrowserPanel", () => {
  it("停止按钮在原生网页区域之外，点击真的调用停止", async () => {
    const fake = fakeApi(view());
    const onStop = vi.fn();
    render(<BrowserPanel api={fake.api} canStop onStop={onStop} pollIntervalMs={20} />);
    await waitFor(() => expect(document.querySelector("[data-browser-stop]")).not.toBeNull());

    const stop = document.querySelector("[data-browser-stop]") as HTMLElement;
    const slot = document.querySelector("[data-browser-slot]") as HTMLElement;
    expect(slot).not.toBeNull();
    expect(slot.contains(stop)).toBe(false);

    // 工具条（含停止按钮）的高度就是后端给出的保留区：原生视图从它下面开始。
    const chrome = document.querySelector("[data-browser-chrome]") as HTMLElement;
    expect(chrome.contains(stop)).toBe(true);
    expect(chrome.style.height).toBe(`${view().chromeReservedTop}px`);

    await userEvent.click(stop);
    expect(onStop).toHaveBeenCalledTimes(1);
  });

  it("tab 由后端状态驱动：切换 tab 不重建 slot、不重复上报几何", async () => {
    const secondTab = {
      tabId: "tab-2",
      url: "https://example.test/second",
      active: false,
      navigationId: "nav-2",
      controlGeneration: 1,
      mode: "agent",
      queued: 0,
      inFlight: 0,
    };
    const fake = fakeApi(view({ tabs: [view().tabs[0], secondTab] }));
    const geometry: BrowserSlotGeometry[] = [];
    const onSlotMount = vi.fn();
    render(
      <BrowserPanel
        api={fake.api}
        onGeometry={(g) => geometry.push(g)}
        onSlotMount={onSlotMount}
        pollIntervalMs={20}
      />
    );
    await waitFor(() => expect(document.querySelector("[data-browser-tab='tab-2']")).not.toBeNull());
    await waitFor(() => expect(geometry.length).toBeGreaterThan(0));

    const slotBefore = document.querySelector("[data-browser-slot]") as HTMLElement;
    const geometryBefore = geometry.length;

    // 后端状态推进：tab-2 变成当前 tab（前端不自己改 active）。
    fake.set(
      view({
        activeTab: "tab-2",
        tabs: [{ ...view().tabs[0], active: false }, { ...secondTab, active: true }],
      })
    );
    await userEvent.click(tabButton("tab-2"));
    await waitFor(() => expect(fake.calls.some((c) => c.name === "selectTab")).toBe(true));
    await waitFor(() =>
      expect(tabButton("tab-2").getAttribute("aria-selected")).toBe("true")
    );

    // 前端不自己造 tab：只调后端 selectTab + 重读状态（没有任何"建 tab"的调用）。
    expect(fake.calls.filter((c) => c.name === "selectTab")).toHaveLength(1);
    expect(fake.calls.map((c) => c.name)).not.toContain("createTab");

    const slotAfter = document.querySelector("[data-browser-slot]") as HTMLElement;
    expect(slotAfter).toBe(slotBefore);
    expect(onSlotMount).toHaveBeenCalledTimes(1);
    // 几何没有变化，所以没有新的上报（否则原生视图会被重摆一次）。
    expect(geometry.length).toBe(geometryBefore);
    expect(geometry[0].visible).toBe(true);
  });

  it("没有原生宿主时显示具体原因，不留空白面板", async () => {
    const fake = fakeApi(
      view({
        available: false,
        hasSlot: false,
        visible: false,
        tabs: [],
        control: { ...view().control, tabId: "", mode: "agent", canTakeOver: false },
        unsupported: [
          { capability: "surface", reason: "没有挂载原生宿主：右侧不显示网页" },
        ],
      })
    );
    render(<BrowserPanel api={fake.api} pollIntervalMs={20} />);
    await waitFor(() =>
      expect(document.querySelector("[data-browser-slot-state='unavailable']")).not.toBeNull()
    );
    expect(screen.getByText("没有挂载原生宿主：右侧不显示网页")).toBeInTheDocument();
    expect(document.querySelector("[data-browser-slot]")).not.toBeNull();
  });

  it("控制状态可见：agent 能接管，human 能恢复，pausing 明确不可恢复", async () => {
    const fake = fakeApi(view());
    render(<BrowserPanel api={fake.api} pollIntervalMs={20} />);
    await waitFor(() =>
      expect(screen.getByRole("button", { name: t("browser.control.takeOver") })).toBeEnabled()
    );
    await userEvent.click(screen.getByRole("button", { name: t("browser.control.takeOver") }));
    expect(fake.calls.some((c) => c.name === "takeOver")).toBe(true);

    fake.set(
      view({
        control: {
          tabId: "tab-1",
          mode: "human",
          generation: 2,
          navigationId: "nav-1",
          queued: 0,
          inFlight: 0,
          canTakeOver: false,
          canResume: true,
        },
      })
    );
    await waitFor(() =>
      expect(screen.getByRole("button", { name: t("browser.control.resume") })).toBeEnabled()
    );
    await userEvent.click(screen.getByRole("button", { name: t("browser.control.resume") }));
    expect(fake.calls.some((c) => c.name === "resume")).toBe(true);

    fake.set(
      view({
        control: {
          tabId: "tab-1",
          mode: "pausing",
          generation: 3,
          navigationId: "nav-1",
          queued: 0,
          inFlight: 1,
          detail: "1 个已派发的动作尚未静止",
          canTakeOver: false,
          canResume: false,
        },
      })
    );
    await waitFor(() => expect(screen.getByText("1 个已派发的动作尚未静止")).toBeInTheDocument());
    expect(screen.getByRole("button", { name: t("browser.control.resume") })).toBeDisabled();
    // 隐藏面板不是暂停：pausing 显示的是"正在接管"，不是"已接管"。
    expect(screen.getByText(t("browser.control.pausing"))).toBeInTheDocument();
  });

  it("动作失败显示结构化提示（按 code 分支，不显示空白）", async () => {
    const fake = fakeApi(view());
    fake.failNext("selectTab", { code: "NEEDS_HUMAN", message: "x", retryable: false });
    render(<BrowserPanel api={fake.api} pollIntervalMs={20} />);
    await waitFor(() => expect(document.querySelector("[data-browser-tab='tab-1']")).not.toBeNull());
    await userEvent.click(tabButton("tab-1"));
    await waitFor(() => expect(document.querySelector("[data-browser-hint]")).not.toBeNull());
    expect(document.querySelector("[data-browser-hint]")?.getAttribute("data-browser-hint")).toBe(
      "NEEDS_HUMAN"
    );
  });

  it("地址栏回车导航，回退与刷新各走自己的接口", async () => {
    const fake = fakeApi(view({ history: ["https://example.test/", "https://example.test/form"] }));
    render(<BrowserPanel api={fake.api} pollIntervalMs={20} />);
    const address = await screen.findByLabelText(t("browser.address.label"));
    await userEvent.clear(address);
    await userEvent.type(address, "https://example.test/typed{enter}");
    await waitFor(() => expect(fake.calls.find((c) => c.name === "navigate")).toBeTruthy());
    const nav = fake.calls.find((c) => c.name === "navigate")!;
    expect(nav.args).toEqual(["tab-1", "https://example.test/typed"]);

    await userEvent.click(screen.getByRole("button", { name: t("browser.address.back") }));
    await userEvent.click(screen.getByRole("button", { name: t("browser.address.reload") }));
    expect(fake.calls.map((c) => c.name)).toEqual(
      expect.arrayContaining(["navigate", "back", "reload"])
    );
  });
});
