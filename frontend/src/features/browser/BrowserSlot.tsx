// 原生网页的槽位（B07）。
//
// 这个 div 只是**位置**：真实网页是 Go 侧把 WebView2 子窗口摆在这块矩形上。
// 所以这里绝不渲染 iframe、截图或任何"看起来像网页"的东西——那会把
// "右侧真的是同一主窗口里的原生视图"（R06）偷偷换掉。
//
// 两个可观察属性给测试与诊断用：
//   data-browser-slot-state=available|unavailable
//   data-slot-visible / data-slot-revision（后端报告的事实，不是本地状态）

import { useEffect, useRef, type RefObject } from "react";
import { t } from "../../i18n";
import type { BrowserStateView } from "./api";

export interface BrowserSlotProps {
  slotRef: RefObject<HTMLDivElement>;
  state: BrowserStateView | null;
  /** 挂载一次回调（挂载计数断言用；也是"没有重建"的唯一可信证据）。 */
  onMount?: () => void;
}

export function BrowserSlot({ slotRef, state, onMount }: BrowserSlotProps) {
  const mounted = useRef(false);
  useEffect(() => {
    if (mounted.current) return;
    mounted.current = true;
    onMount?.();
  }, [onMount]);

  const available = state?.available ?? false;
  const occluded = (state?.overlays.length ?? 0) > 0;
  const missingHost = state?.unsupported.find((item) => item.capability === "surface") ?? null;
  const otherGaps = (state?.unsupported ?? []).filter((item) => item.capability !== "surface");

  return (
    <div
      ref={slotRef}
      data-browser-slot=""
      data-browser-slot-state={available ? "available" : "unavailable"}
      data-slot-visible={String(state?.visible ?? false)}
      data-slot-revision={state?.slot.revision ?? 0}
      className="absolute inset-0 flex flex-col items-start gap-1 overflow-hidden rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] p-2 text-[10px] text-[color:var(--anvil-muted)]"
    >
      {available ? (
        <>
          {occluded ? (
            <p data-browser-slot-note="occluded">{t("browser.slot.occluded")}</p>
          ) : (
            <p data-browser-slot-note="native">{t("browser.slot.native")}</p>
          )}
          {otherGaps.length > 0 ? (
            <ul data-browser-unsupported="" className="space-y-0.5">
              {otherGaps.map((gap) => (
                <li key={gap.capability} className="text-[color:var(--anvil-warning)]">
                  {gap.capability}
                  {": "}
                  {gap.reason}
                </li>
              ))}
            </ul>
          ) : null}
        </>
      ) : (
        <>
          <p className="text-[color:var(--anvil-warning)]">{t("status.unavailable")}</p>
          {missingHost ? (
            <p data-browser-unavailable-reason={missingHost.capability}>{missingHost.reason}</p>
          ) : (
            <p data-browser-unavailable-reason="unknown">{t("browser.slot.noHost")}</p>
          )}
          {otherGaps.length > 0 ? (
            <ul className="space-y-0.5">
              {otherGaps.map((gap) => (
                <li key={gap.capability}>
                  {gap.capability}
                  {": "}
                  {gap.reason}
                </li>
              ))}
            </ul>
          ) : null}
        </>
      )}
    </div>
  );
}
