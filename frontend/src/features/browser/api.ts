// 右栏浏览器的绑定调用（B07）。
//
// 与其余 feature 的 api.ts 同一形态：FQN 集中定义，字段手写对齐 Go 侧 DTO
// （internal/app/browser_api.go），返回结构化的 CallError 而不是 reject。
//
// 边界：这里**没有**也不会出现原生句柄。React 只上报事件与几何
// （updateSlot）、读取状态（state），原生视图由 Go 侧摆到同一块矩形上。

import { Call } from "@wailsio/runtime";
import type { CallError } from "../../types";

const FQN = "my-agent-go/internal/app.Desktop";

/** 槽位几何（与 browser.SlotGeometry 对齐，单位是应用 WebView 内的 CSS px）。 */
export interface BrowserSlotGeometry {
  slotId: string;
  revision: number;
  left: number;
  top: number;
  width: number;
  height: number;
  visible: boolean;
  zoom: number;
}

export interface BrowserUnsupported {
  capability: string;
  reason: string;
}

/** 实测能力（不是按平台写死的假设）。 */
export interface BrowserCapabilities {
  platform: string;
  engine: string;
  navigate: boolean;
  observe: boolean;
  act: boolean;
  screenshot: boolean;
  trustedInput: boolean;
  crossOriginFrames: boolean;
  closedShadowRoots: boolean;
  downloads: boolean;
  popups: boolean;
  unsupported: BrowserUnsupported[];
}

export interface BrowserTabView {
  tabId: string;
  url: string;
  active: boolean;
  navigationId: string;
  controlGeneration: number;
  mode: string;
  queued: number;
  inFlight: number;
}

export interface BrowserControlView {
  tabId: string;
  mode: string;
  generation: number;
  navigationId: string;
  queued: number;
  inFlight: number;
  detail?: string;
  canTakeOver: boolean;
  canResume: boolean;
}

export interface BrowserCounters {
  slotPresented: number;
  slotDropped: number;
  slotDeduped: number;
  visibilityChanges: number;
  overlayTransitions: number;
  tabSelectDeduped: number;
  tabsClosed: number;
}

export interface BrowserOcclusionStep {
  seq: number;
  source: string;
  open: boolean;
  visible: boolean;
  at: string;
}

/** 面板需要的全部事实（Go 侧 BrowserStateResult 的镜像）。 */
export interface BrowserStateView {
  available: boolean;
  slot: BrowserSlotGeometry;
  hasSlot: boolean;
  chromeReservedTop: number;
  activeTab: string;
  tabs: BrowserTabView[];
  control: BrowserControlView;
  visible: boolean;
  requestedVisible: boolean;
  overlays: string[];
  history: string[];
  capabilities: BrowserCapabilities;
  unsupported: BrowserUnsupported[];
  counters: BrowserCounters;
  occlusion: BrowserOcclusionStep[];
  error?: CallError;
}

export interface BrowserControlResult {
  tabId: string;
  control: BrowserControlView;
  error?: CallError;
}

/**
 * 绑定层的形状。
 *
 * 抽成接口是为了让组件测试注入"只记录调用"的替身：测试不该去 mock Wails runtime，
 * 也不该验证我自己编的假状态。
 */
export interface BrowserBackendApi {
  state(): Promise<BrowserStateView>;
  updateSlot(slot: BrowserSlotGeometry): Promise<CallError | null>;
  selectTab(tabId: string): Promise<CallError | null>;
  closeTab(tabId: string): Promise<CallError | null>;
  navigate(tabId: string, url: string): Promise<CallError | null>;
  back(tabId: string): Promise<CallError | null>;
  reload(tabId: string): Promise<CallError | null>;
  takeOver(tabId: string): Promise<BrowserControlResult>;
  resume(tabId: string): Promise<BrowserControlResult>;
  setOverlay(source: string, open: boolean): Promise<CallError | null>;
}

export const browserApi: BrowserBackendApi = {
  state: () => Call.ByName(`${FQN}.BrowserState`),
  updateSlot: (slot) => Call.ByName(`${FQN}.UpdateBrowserSlot`, slot),
  selectTab: (tabId) => Call.ByName(`${FQN}.SelectBrowserTab`, tabId),
  closeTab: (tabId) => Call.ByName(`${FQN}.CloseBrowserTab`, tabId),
  navigate: (tabId, url) => Call.ByName(`${FQN}.NavigateBrowserTab`, tabId, url),
  back: (tabId) => Call.ByName(`${FQN}.BackBrowserTab`, tabId),
  reload: (tabId) => Call.ByName(`${FQN}.ReloadBrowserTab`, tabId),
  takeOver: (tabId) => Call.ByName(`${FQN}.TakeOverBrowserTab`, tabId),
  resume: (tabId) => Call.ByName(`${FQN}.ResumeBrowserTab`, tabId),
  setOverlay: (source, open) => Call.ByName(`${FQN}.SetBrowserOverlay`, source, open),
};
