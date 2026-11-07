// 右栏 slot 的几何上报（B07）。
//
// React 只负责"这块矩形在哪"，不碰原生句柄；这个模块负责把"每帧可能来十几次的
// 尺寸变化"压成一条可用的消息：
//
//   - **每帧合并**：同一帧内的多次 report 只发一条（最后那个）；
//   - **去重**：几何与可见性都没变就不发（revision 也不前进），
//     否则原生视图会被反复重摆，拖动分栏时会抖；
//   - **单调 revision**：每次真的发出去都 +1。接收方据此丢弃迟到的旧消息。
//
// frame 调度器可注入：依赖真实 requestAnimationFrame 的测试会变成"等一会儿看看"，
// 那类断言在 CI 上最先烂。

export interface SlotRect {
  left: number;
  top: number;
  width: number;
  height: number;
}

/** 一条几何消息；字段与 Go 的 browser.SlotGeometry 一一对应。 */
export interface SlotMessage extends SlotRect {
  slotId: string;
  revision: number;
  visible: boolean;
  zoom: number;
}

export interface SlotReporterOptions {
  /** 真正把消息交出去（产品里是 Desktop.UpdateBrowserSlot）。 */
  emit: (message: SlotMessage) => void;
  /** 槽位 ID，默认 "browser-slot"（与 app.BrowserSlotID 一致）。 */
  slotId?: string;
  /** 原生视图的页面缩放因子，默认 1。 */
  zoom?: number;
  /** 每帧调度器，默认 requestAnimationFrame（缺失时退回 setTimeout）。 */
  frame?: (cb: () => void) => void;
}

export interface SlotReporter {
  /** 上报一次测量结果（可以每帧多次调用）。 */
  report(rect: SlotRect): void;
  /** 隐藏（沿用最近一次几何）；没有测量过时什么也不发。 */
  hide(): void;
  /** 恢复显示（沿用最近一次几何）。 */
  show(): void;
  /** 立刻发出待发的消息（卸载/测试用）。 */
  flush(): void;
  /** 已经发出的消息数（= 最后一次 revision）。 */
  readonly revision: number;
  /** 因为"完全没变"被吞掉的次数。 */
  readonly deduped: number;
}

/** 保留两位小数：亚像素抖动不该被当成几何变化。 */
function round2(value: number): number {
  return Math.round(value * 100) / 100;
}

function normalize(rect: SlotRect): SlotRect {
  return {
    left: round2(rect.left),
    top: round2(rect.top),
    width: round2(rect.width),
    height: round2(rect.height),
  };
}

function sameRect(a: SlotRect, b: SlotRect): boolean {
  return a.left === b.left && a.top === b.top && a.width === b.width && a.height === b.height;
}

function defaultFrame(cb: () => void): void {
  if (typeof requestAnimationFrame === "function") {
    requestAnimationFrame(() => cb());
    return;
  }
  setTimeout(cb, 0);
}

export function createSlotReporter(options: SlotReporterOptions): SlotReporter {
  const emit = options.emit;
  const frame = options.frame ?? defaultFrame;
  const slotId = options.slotId ?? "browser-slot";
  const zoom = options.zoom ?? 1;

  let pending: { rect: SlotRect; visible: boolean } | null = null;
  let scheduled = false;
  let revision = 0;
  let deduped = 0;
  let last: SlotMessage | null = null;
  let visible = true;

  const pump = () => {
    scheduled = false;
    const next = pending;
    pending = null;
    if (!next) return;
    if (last && last.visible === next.visible && sameRect(last, next.rect)) {
      deduped += 1;
      return;
    }
    revision += 1;
    const message: SlotMessage = {
      slotId,
      revision,
      visible: next.visible,
      zoom,
      ...normalize(next.rect),
    };
    last = message;
    emit(message);
  };

  const schedule = () => {
    if (scheduled) return;
    scheduled = true;
    frame(pump);
  };

  const reuseLast = (nextVisible: boolean) => {
    visible = nextVisible;
    if (!last) return;
    pending = {
      rect: { left: last.left, top: last.top, width: last.width, height: last.height },
      visible: nextVisible,
    };
    schedule();
  };

  return {
    report(rect: SlotRect) {
      pending = { rect, visible };
      schedule();
    },
    hide() {
      reuseLast(false);
    },
    show() {
      reuseLast(true);
    },
    flush() {
      if (pending) pump();
    },
    get revision() {
      return revision;
    },
    get deduped() {
      return deduped;
    },
  };
}
