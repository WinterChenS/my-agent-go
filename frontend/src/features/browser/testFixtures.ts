// 浏览器面板的测试 fixture。
//
// 只被 `*.test.tsx` import，产品代码不引用（也不会进打包产物）。
//
// 为什么字段给全：`BrowserStateView` 是 Go 侧 `BrowserStateResult` 的镜像，缺字段
// 就该编译不过——用 `as` 造一个"够用的半截对象"会让"绑定多了一个字段/少了一个字段"
// 这类漂移在测试里悄悄通过。
//
// 默认值刻意是"已经挂上原生宿主、一个 tab、用户已接管"：这样用例断言的是
// "面板按后端事实渲染"，而不是"恰好渲染出了某个默认兜底文案"。

import type { BrowserBackendApi, BrowserStateView } from "./api";

export function browserStateFixture(over: Partial<BrowserStateView> = {}): BrowserStateView {
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
        url: "https://example.test/form",
        active: true,
        navigationId: "nav-1",
        controlGeneration: 2,
        mode: "human",
        queued: 0,
        inFlight: 0,
      },
    ],
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
    visible: true,
    requestedVisible: true,
    overlays: [],
    history: ["https://example.test/form"],
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

/**
 * 一个"只记录调用"的绑定替身。
 *
 * 状态由 fixture 给；动作用例要断言时看 calls，不靠"点了之后界面看起来变了"。
 */
export function browserApiStub(
  state: BrowserStateView
): BrowserBackendApi & { calls: Array<{ name: string; args: unknown[] }> } {
  const calls: Array<{ name: string; args: unknown[] }> = [];
  const record = (name: string, args: unknown[] = []) => {
    calls.push({ name, args });
    return null;
  };
  const control = { tabId: state.activeTab, control: state.control };
  return {
    calls,
    state: async () => state,
    updateSlot: async () => record("updateSlot"),
    selectTab: async () => record("selectTab"),
    closeTab: async () => record("closeTab"),
    navigate: async () => record("navigate"),
    back: async () => record("back"),
    reload: async () => record("reload"),
    takeOver: async () => {
      calls.push({ name: "takeOver", args: [] });
      return control;
    },
    resume: async () => {
      calls.push({ name: "resume", args: [] });
      return control;
    },
    setOverlay: async () => record("setOverlay"),
  };
}
