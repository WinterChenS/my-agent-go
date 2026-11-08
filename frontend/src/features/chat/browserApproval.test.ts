// 浏览器审批的归约器（M1）。
//
// 这里守的是**语义**，不涉及时序：一条待审批什么时候算"没了"、提交之后卡片能不能
// 立刻消失、读失败算不算"没有待审批"。组件测试没法稳定覆盖这些——一次轮询就能把
// "乐观移除"掩盖掉（卡片消失又会因为后端仍然列出而重新出现），所以直接断言归约结果。

import { describe, expect, it } from "vitest";
import {
  actionSummary,
  initialBrowserApprovalState,
  reduceBrowserApprovals,
  type BrowserApprovalItem,
} from "./browserApproval";

function item(overrides: Partial<BrowserApprovalItem> = {}): BrowserApprovalItem {
  return {
    approvalId: "bapr-1",
    sessionId: "sess-runtime-1",
    runId: "run-1",
    toolName: "browser_act",
    tabId: "tab-1",
    kind: "click",
    origin: "http://127.0.0.1:45123",
    url: "http://127.0.0.1:45123/form",
    navigationId: "nav-7",
    arguments: { tabId: "tab-1", kind: "click", snapshotId: "snap-1", ref: "e3" },
    expiresAtUnix: 1_800_000_000,
    ...overrides,
  };
}

const T0 = 1_700_000_000_000;

describe("浏览器审批归约器", () => {
  it("提交只标已提交：后端仍然列出这条时卡片不能消失", () => {
    let state = initialBrowserApprovalState();
    state = reduceBrowserApprovals(state, { type: "observed", seq: 1, nowMs: T0, items: [item()] });
    expect(state.items).toHaveLength(1);

    state = reduceBrowserApprovals(state, { type: "submitted", approvalId: "bapr-1", choice: "once" });
    // 关键断言：乐观移除会让这里变成 0。
    expect(state.items).toHaveLength(1);
    expect(state.submitted["bapr-1"]).toBe("once");
    expect(state.notes).toHaveLength(0);
  });

  it("后端不再列出后才移除，并留下『已批准』的原因", () => {
    let state = initialBrowserApprovalState();
    state = reduceBrowserApprovals(state, { type: "observed", seq: 1, nowMs: T0, items: [item()] });
    state = reduceBrowserApprovals(state, { type: "submitted", approvalId: "bapr-1", choice: "once" });
    state = reduceBrowserApprovals(state, { type: "observed", seq: 2, nowMs: T0, items: [] });

    expect(state.items).toHaveLength(0);
    expect(state.notes).toEqual([
      {
        approvalId: "bapr-1",
        kind: "approved",
        toolName: "browser_act",
        origin: "http://127.0.0.1:45123",
        choice: "once",
      },
    ]);
    // 已收口条目的提交标记/错误要清掉，否则同一 id 复用时状态会串。
    expect(state.submitted["bapr-1"]).toBeUndefined();
  });

  it("拒绝的原因与批准分开：deny 记成 denied", () => {
    let state = initialBrowserApprovalState();
    state = reduceBrowserApprovals(state, { type: "observed", seq: 1, nowMs: T0, items: [item()] });
    state = reduceBrowserApprovals(state, { type: "submitted", approvalId: "bapr-1", choice: "deny" });
    state = reduceBrowserApprovals(state, { type: "observed", seq: 2, nowMs: T0, items: [] });
    expect(state.notes[0]?.kind).toBe("denied");
  });

  it("没提交就消失：到期时间已过是过期，否则是撤销", () => {
    const expiredAt = Math.floor(T0 / 1000) - 1;
    let state = initialBrowserApprovalState();
    state = reduceBrowserApprovals(state, {
      type: "observed",
      seq: 1,
      nowMs: T0,
      items: [item({ approvalId: "bapr-exp", expiresAtUnix: expiredAt })],
    });
    state = reduceBrowserApprovals(state, { type: "observed", seq: 2, nowMs: T0, items: [] });
    expect(state.notes.map((n) => n.kind)).toEqual(["expired"]);

    state = reduceBrowserApprovals(state, {
      type: "observed",
      seq: 3,
      nowMs: T0,
      items: [item({ approvalId: "bapr-rev" })],
    });
    state = reduceBrowserApprovals(state, { type: "observed", seq: 4, nowMs: T0, items: [] });
    expect(state.notes.map((n) => n.kind)).toEqual(["expired", "revoked"]);
  });

  it("迟到的旧读取不能复活已移除的卡片", () => {
    let state = initialBrowserApprovalState();
    state = reduceBrowserApprovals(state, { type: "observed", seq: 1, nowMs: T0, items: [item()] });
    state = reduceBrowserApprovals(state, { type: "observed", seq: 3, nowMs: T0, items: [] });
    // seq=2 的读取是更早发出的（那时后端还列着），它晚到也不能把卡片加回来。
    state = reduceBrowserApprovals(state, { type: "observed", seq: 2, nowMs: T0, items: [item()] });
    expect(state.items).toHaveLength(0);
    expect(state.notes).toHaveLength(1);
  });

  it("读取失败不清空列表：不知道有没有待审批 ≠ 没有待审批", () => {
    let state = initialBrowserApprovalState();
    state = reduceBrowserApprovals(state, { type: "observed", seq: 1, nowMs: T0, items: [item()] });
    state = reduceBrowserApprovals(state, {
      type: "poll_failed",
      seq: 2,
      error: { code: "UNKNOWN", message: "binding missing", retryable: false },
    });
    expect(state.items).toHaveLength(1);
    expect(state.hint?.code).toBe("UNKNOWN");
    // 下一次成功读取清掉 hint。
    state = reduceBrowserApprovals(state, { type: "observed", seq: 3, nowMs: T0, items: [item()] });
    expect(state.hint).toBeNull();
  });

  it("提交失败撤销已提交标记并保留原因，卡片仍在", () => {
    let state = initialBrowserApprovalState();
    state = reduceBrowserApprovals(state, { type: "observed", seq: 1, nowMs: T0, items: [item()] });
    state = reduceBrowserApprovals(state, { type: "submitted", approvalId: "bapr-1", choice: "once" });
    state = reduceBrowserApprovals(state, {
      type: "resolve_failed",
      approvalId: "bapr-1",
      error: { code: "NOT_FOUND", message: "没有待审批的浏览器调用: bapr-1", retryable: false },
    });
    expect(state.items).toHaveLength(1);
    expect(state.errors["bapr-1"]?.code).toBe("NOT_FOUND");
    // 失败之后按钮要能再点：已提交标记必须撤掉。
    expect(state.submitted["bapr-1"]).toBeUndefined();
  });

  it("动作摘要只包含 kind/ref/站点/文档身份，不含 fill 的正文", () => {
    const summary = actionSummary(
      item({ arguments: { kind: "fill", ref: "e9", text: "s3cr3t" } })
    );
    expect(summary).toContain("fill");
    expect(summary).toContain("ref=e9");
    expect(summary).toContain("http://127.0.0.1:45123");
    expect(summary).toContain("nav=nav-7");
    // 首行摘要不进用户输入的正文（卡片下方仍显示完整参数）。
    expect(summary).not.toContain("s3cr3t");
  });
});
