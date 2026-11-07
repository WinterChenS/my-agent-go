// 遮挡协调器（B07 / UI04）。
//
// 原生网页是盖在右侧 slot 上的子窗口：它**不会**被 DOM 的 z-index 挡住。
// 所以任何应用浮层（设置 dialog、命令面板、审批框、确认框）在打开前必须让
// 原生视图让位，关闭后恢复。这件事只能有一个入口——每个浮层各自去调后端，
// 迟早有一处忘记，然后那一个面板就被网页盖住。
//
// 纪律：
//   - 每个浮层用**有名字**的来源登记（空名字直接拒绝：否则"还有哪层开着"无从判断）；
//   - 同一来源可被多个实例持有，引用计数归零才恢复；
//   - 协调器只调 setOverlay。它**不**接管控制权、不停止 run：
//     **隐藏面板 ≠ 暂停 agent**。把"藏起来"做成"停下来"会静默终止用户正在跑的 run。
//
// 已经接入的来源（每个都只是在组件里一行 `useBrowserOcclusionLayer(名字, open)`）：
//   - `approval-card`：工具审批卡（features/chat/ApprovalCard.tsx）；
//   - `clarification-card`：内容澄清卡（features/chat/ClarificationCard.tsx）。
//
// 以后新增的 dialog / 命令面板 / 确认框照同一方式接入，**不要**自己去调
// setOverlay，也不要在打开时顺手 stop run：
//
//   useBrowserOcclusionLayer("settings-dialog", open);
//
// 漏接的后果是可以预期的：那一层弹出时会被原生网页盖住。

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  type ReactNode,
} from "react";
import { browserApi } from "../features/browser/api";

/** 协调器需要的最小能力面（测试注入替身用）。 */
export interface BrowserOcclusionApi {
  setOverlay(source: string, open: boolean): Promise<unknown>;
}

type SetLayer = (source: string, open: boolean) => void;

const OcclusionContext = createContext<SetLayer | null>(null);

export interface BrowserOcclusionProviderProps {
  api?: BrowserOcclusionApi;
  children: ReactNode;
}

/**
 * 遮挡协调器。
 *
 * 只维护"哪些来源开着"这一份事实；隐藏/恢复的判定与队列都在后端
 * （app.BrowserBridge），前端不复制一份，避免两处口径分叉。
 */
export function BrowserOcclusionProvider({ api = browserApi, children }: BrowserOcclusionProviderProps) {
  const counts = useRef(new Map<string, number>());

  const setLayer = useCallback<SetLayer>(
    (source, open) => {
      const name = source.trim();
      if (!name) {
        console.warn("browser occlusion: empty source ignored");
        return;
      }
      const map = counts.current;
      const current = map.get(name) ?? 0;
      if (open) {
        map.set(name, current + 1);
        if (current === 0) {
          void api.setOverlay(name, true).catch((err: unknown) => {
            console.warn("browser occlusion: open failed", err);
          });
        }
        return;
      }
      if (current <= 1) {
        map.delete(name);
        if (current === 1) {
          void api.setOverlay(name, false).catch((err: unknown) => {
            console.warn("browser occlusion: close failed", err);
          });
        }
        return;
      }
      map.set(name, current - 1);
    },
    [api]
  );

  return <OcclusionContext.Provider value={setLayer}>{children}</OcclusionContext.Provider>;
}

/**
 * 在组件挂载（且 open=true）期间持有一层浮层的登记。
 *
 * 用法：dialog/palette/审批框组件里一行
 *   useBrowserOcclusionLayer("approval-dialog", open);
 */
export function useBrowserOcclusionLayer(source: string, open: boolean): void {
  const setLayer = useContext(OcclusionContext);
  useEffect(() => {
    if (!setLayer || !open) return;
    setLayer(source, true);
    return () => setLayer(source, false);
  }, [setLayer, source, open]);
}

/** 把任意浮层包一层：打开时自动登记遮挡，关闭/卸载时恢复。 */
export function BrowserOcclusionLayer({
  source,
  open,
  children,
}: {
  source: string;
  open: boolean;
  children: ReactNode;
}) {
  useBrowserOcclusionLayer(source, open);
  return <>{children}</>;
}
