// 右栏浏览面板（B07 / UI03、UI04）。
//
// 布局是一份契约，不是样式偏好：
//
//   ┌───────────────────────────────┐
//   │ 工具条（地址栏 / 控制状态 / 停止） │  ← data-browser-chrome，高 = chromeReservedTop
//   │ tab 条                         │
//   ├───────────────────────────────┤
//   │ 原生网页区域（data-browser-slot）│  ← 原生子窗口只盖这一块
//   └───────────────────────────────┘
//
// 工具条高度由**后端**给出（state.chromeReservedTop，与 Go 的
// BrowserChromeReservedTop 同一个常量）。后端会拒绝落进这块保留区的几何，
// 所以"停止按钮被网页盖住"不可能只靠样式碰巧不发生。
//
// slot 节点只创建一次：切 tab / 换面板 / 遮挡都不重建它（挂载计数断言在用例里）。

import { formatError, t } from "../../i18n";
import type { BrowserBackendApi, BrowserSlotGeometry } from "./api";
import { BrowserSlot } from "./BrowserSlot";
import { BrowserTabs } from "./BrowserTabs";
import { BrowserToolbar } from "./BrowserToolbar";
import { useBrowserState } from "./useBrowserState";

export interface BrowserPanelProps {
  api?: BrowserBackendApi;
  /** 当前是否有可停止的 run（停止按钮只在需要时出现，位置始终在原生区域之外）。 */
  canStop?: boolean;
  onStop?: () => void;
  /** 测试注入的几何通道；产品里走 UpdateBrowserSlot。 */
  onGeometry?: (slot: BrowserSlotGeometry) => void;
  /** 槽位挂载一次的回调（挂载计数断言）。 */
  onSlotMount?: () => void;
  /**
   * 状态轮询间隔（毫秒）。生产用默认 2s；测试用更短的间隔观察"后端状态驱动"，
   * 而不是靠等真实时间（慢且不稳）。
   */
  pollIntervalMs?: number;
}

const FALLBACK_CHROME_HEIGHT = 96;

export function BrowserPanel({
  api,
  canStop = false,
  onStop,
  onGeometry,
  onSlotMount,
  pollIntervalMs,
}: BrowserPanelProps) {
  const { state, hint, actions, slotRef } = useBrowserState({ api, onGeometry, pollIntervalMs });

  const chromeHeight = state?.chromeReservedTop ?? FALLBACK_CHROME_HEIGHT;
  const tabs = state?.tabs ?? [];
  const activeTab = tabs.find((tab) => tab.tabId === state?.activeTab) ?? null;
  const control = state?.control ?? {
    tabId: "",
    mode: "agent",
    generation: 0,
    navigationId: "",
    queued: 0,
    inFlight: 0,
    canTakeOver: false,
    canResume: false,
  };

  return (
    <section data-browser-panel="" className="flex h-full min-h-0 flex-col">
      <div
        data-browser-chrome=""
        style={{ height: `${chromeHeight}px` }}
        className="shrink-0 overflow-hidden border-b border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-1.5 py-1"
      >
        <BrowserToolbar
          tab={activeTab}
          control={control}
          history={state?.history ?? []}
          canStop={canStop}
          onStop={onStop}
          onNavigate={actions.navigate}
          onBack={actions.back}
          onReload={actions.reload}
          onTakeOver={actions.takeOver}
          onResume={actions.resume}
        />
        <BrowserTabs tabs={tabs} onSelect={actions.selectTab} onClose={actions.closeTab} />
      </div>

      {hint ? (
        <p
          data-browser-hint={hint.code}
          className="shrink-0 px-2 py-1 text-[10px] text-[color:var(--anvil-danger)]"
        >
          {formatError(hint)}
        </p>
      ) : null}

      {state?.overlays && state.overlays.length > 0 ? (
        <p
          data-browser-occlusion={state.overlays.join(",")}
          className="shrink-0 px-2 py-1 text-[10px] text-[color:var(--anvil-muted)]"
        >
          {t("browser.occlusion.hidden")}
        </p>
      ) : null}

      <div className="relative min-h-0 flex-1">
        <BrowserSlot slotRef={slotRef} state={state} onMount={onSlotMount} />
      </div>
    </section>
  );
}
