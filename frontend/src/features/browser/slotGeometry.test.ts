// 右栏 slot 几何上报（B07）的纯逻辑测试。
//
// 三个不变量：
//   1. 同一帧内多次 report 只发一条（拖动分栏时每帧一次，不是每个事件一次）；
//   2. 几何与可见性都没变的重复上报不发送（去重），revision 不前进；
//   3. 隐藏/恢复必须发送（可见性变化是真实变化），且沿用最近一次几何。
//
// 这里用可控的 frame 调度器，不依赖 requestAnimationFrame 的真实时序：
// 依赖真实 rAF 会让断言变成"等一会儿看看"，那类测试在 CI 上最先烂。

import { describe, expect, it } from "vitest";
import { createSlotReporter, type SlotMessage } from "./slotGeometry";

function manualFrame() {
  const queue: Array<() => void> = [];
  return {
    frame: (cb: () => void) => {
      queue.push(cb);
    },
    flush: () => {
      for (const cb of queue.splice(0)) cb();
    },
    pending: () => queue.length,
  };
}

function collector() {
  const sent: SlotMessage[] = [];
  return { sent, emit: (g: SlotMessage) => sent.push(g) };
}

const baseRect = { left: 640, top: 96, width: 480, height: 600 };

describe("createSlotReporter", () => {
  it("同一帧内多次上报只发一条，revision 只前进一次", () => {
    const scheduler = manualFrame();
    const sink = collector();
    const reporter = createSlotReporter({ emit: sink.emit, frame: scheduler.frame });

    reporter.report({ ...baseRect, width: 400 });
    reporter.report({ ...baseRect, width: 440 });
    reporter.report({ ...baseRect, width: 480 });
    expect(sink.sent).toHaveLength(0);
    expect(scheduler.pending()).toBe(1);

    scheduler.flush();
    expect(sink.sent).toHaveLength(1);
    expect(sink.sent[0].width).toBe(480);
    expect(sink.sent[0].revision).toBe(1);
    expect(sink.sent[0].visible).toBe(true);
    expect(reporter.revision).toBe(1);
  });

  it("相同几何与可见性的重复上报不重复发送，revision 不前进", () => {
    const scheduler = manualFrame();
    const sink = collector();
    const reporter = createSlotReporter({ emit: sink.emit, frame: scheduler.frame });

    reporter.report(baseRect);
    scheduler.flush();
    reporter.report(baseRect);
    scheduler.flush();

    expect(sink.sent).toHaveLength(1);
    expect(reporter.revision).toBe(1);
    expect(reporter.deduped).toBe(1);
  });

  it("几何变化时 revision 前进", () => {
    const scheduler = manualFrame();
    const sink = collector();
    const reporter = createSlotReporter({ emit: sink.emit, frame: scheduler.frame });

    reporter.report(baseRect);
    scheduler.flush();
    reporter.report({ ...baseRect, height: 640 });
    scheduler.flush();

    expect(sink.sent.map((m) => m.revision)).toEqual([1, 2]);
    expect(sink.sent[1].height).toBe(640);
  });

  it("隐藏时沿用最近一次几何并发送 visible=false；恢复再发一次 true", () => {
    const scheduler = manualFrame();
    const sink = collector();
    const reporter = createSlotReporter({ emit: sink.emit, frame: scheduler.frame });

    reporter.report(baseRect);
    scheduler.flush();
    reporter.hide();
    scheduler.flush();
    reporter.show();
    scheduler.flush();

    expect(sink.sent).toHaveLength(3);
    expect(sink.sent.map((m) => m.visible)).toEqual([true, false, true]);
    expect(sink.sent[1].width).toBe(baseRect.width);
    // 可见性变化也算一次新 revision：迟到的旧消息不能把它拽回去。
    expect(sink.sent.map((m) => m.revision)).toEqual([1, 2, 3]);
  });

  it("还没有任何测量时隐藏不发送（没有几何可隐藏）", () => {
    const scheduler = manualFrame();
    const sink = collector();
    const reporter = createSlotReporter({ emit: sink.emit, frame: scheduler.frame });

    reporter.hide();
    scheduler.flush();
    expect(sink.sent).toHaveLength(0);
  });

  it("卸载前的 flush 会把待发的几何带走（不丢最后一帧）", () => {
    const scheduler = manualFrame();
    const sink = collector();
    const reporter = createSlotReporter({ emit: sink.emit, frame: scheduler.frame });

    reporter.report(baseRect);
    reporter.flush();
    expect(sink.sent).toHaveLength(1);
    // 已经发完，再 flush 不重复发送。
    reporter.flush();
    expect(sink.sent).toHaveLength(1);
  });
});
