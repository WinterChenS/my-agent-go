// 会话状态归约的单元测试。
//
// 这些用例只测一件事：**后端事件序列 → 界面上应该是什么**。
// 不渲染 React、不碰 DOM，所以失败时能直接指出是哪条映射错了，
// 而不是"截图看起来不对"。
//
// 断言的都是会真的出现在用户眼前的行为：气泡数量、流式标记、工具卡状态、
// 审批条内容。不测实现细节（比如内部数组的引用是否变化）。
import { describe, expect, it } from "vitest";
import { initialState, isTerminal, reduce, type UiState, type UiAction } from "./runState";
import type { ProductEvent } from "./types";

let seq = 0;

/** 构造一条产品事件；只写用例关心的字段，其余给固定值。 */
function ev(type: string, payload: unknown, opts: { runId?: string; seq?: number } = {}): ProductEvent {
  seq = opts.seq ?? seq + 1;
  return {
    schemaVersion: 1,
    eventId: `e${seq}`,
    workspaceId: "ws",
    sessionId: "s",
    runId: opts.runId ?? "run-1",
    seq,
    type,
    occurredAt: "2026-11-04T00:00:00Z",
    payload,
  };
}

/** 依次应用多个动作。 */
function applyAll(state: UiState, actions: UiAction[]): UiState {
  return actions.reduce(reduce, state);
}

/** 开始一轮对话（等价于用户按下发送）。 */
function started(): UiState {
  return reduce(initialState, { kind: "user_message", text: "你好" });
}

/**
 * 构造"发起工具调用 + 回填结果"这一对事件。
 *
 * runId 与 seq 都显式给出：本用例要的就是"不同 run 复用同一个 toolCallId"，
 * 依赖模块级自增会把这个前提掩盖成随机行为。
 */
function toolRound(runId: string, text: string, isError: boolean, at: number): UiAction[] {
  return [
    {
      kind: "event",
      ev: ev("tool.start", { toolCallId: "c1", toolName: "bash", args: {} }, { runId, seq: at }),
    },
    {
      kind: "event",
      ev: ev("tool.end", { toolCallId: "c1", text, isError, exitCode: isError ? null : 0 }, { runId, seq: at + 1 }),
    },
  ];
}

describe("消息流", () => {
  it("发送后先建用户气泡与一个占位助手气泡", () => {
    const s = started();
    expect(s.messages).toHaveLength(2);
    expect(s.messages[0]).toMatchObject({ role: "user", text: "你好", streaming: false });
    expect(s.messages[1]).toMatchObject({ role: "assistant", text: "", streaming: true });
  });

  it("连续增量追加到同一条助手气泡，不会另起一条", () => {
    let s = started();
    s = reduce(s, { kind: "event", ev: ev("message.delta", { text: "你" }) });
    s = reduce(s, { kind: "event", ev: ev("message.delta", { text: "好" }) });
    expect(s.messages).toHaveLength(2);
    expect(s.messages[1].text).toBe("你好");
  });

  it("没有占位气泡时（例如恢复会话）增量会新建一条助手气泡", () => {
    let s = reduce(initialState, { kind: "event", ev: ev("message.delta", { text: "第一段" }) });
    expect(s.messages).toHaveLength(1);
    expect(s.messages[0]).toMatchObject({ role: "assistant", text: "第一段", streaming: true });
  });

  it("重放的增量不会被处理第二次（同一 run 的 seq 单调）", () => {
    const delta = ev("message.delta", { text: "只出现一次" });
    let s = reduce(started(), { kind: "event", ev: delta });
    const once = s.messages[1].text;
    s = reduce(s, { kind: "event", ev: delta });
    expect(s.messages[1].text).toBe(once);
    expect(s.messages[1].text).toBe("只出现一次");
  });

  it("不同 run 的 seq 各自独立，不会把新 run 的事件当成重放", () => {
    let s = reduce(started(), { kind: "event", ev: ev("message.delta", { text: "A" }, { runId: "run-1", seq: 5 }) });
    // 新 run 从 seq=1 开始。若只按全局最大 seq 去重，这条会被当成重放丢掉。
    s = reduce(s, { kind: "event", ev: ev("message.delta", { text: "B" }, { runId: "run-2", seq: 1 }) });
    expect(s.messages[1].text).toBe("AB");
    expect(s.lastSeqByRun["run-1"]).toBe(5);
    expect(s.lastSeqByRun["run-2"]).toBe(1);
  });
});

describe("工具卡", () => {
  it("tool.start 挂到当前助手气泡下", () => {
    let s = started();
    s = reduce(s, {
      kind: "event",
      ev: ev("tool.start", { toolCallId: "c1", toolName: "bash", args: { command: "ls" } }),
    });
    expect(s.messages).toHaveLength(2);
    expect(s.messages[1].tools).toHaveLength(1);
    expect(s.messages[1].tools[0]).toMatchObject({ toolCallId: "c1", toolName: "bash", running: true });
    expect(s.messages[1].tools[0].args).toContain("\"command\": \"ls\"");
  });

  it("tool.end 按 toolCallId 精确回填，不误改另一个并行工具", () => {
    let s = started();
    s = applyAll(s, [
      { kind: "event", ev: ev("tool.start", { toolCallId: "c1", toolName: "bash", args: {} }) },
      { kind: "event", ev: ev("tool.start", { toolCallId: "c2", toolName: "write", args: {} }) },
      // c2 先结束：完成顺序与发起顺序无关。
      { kind: "event", ev: ev("tool.end", { toolCallId: "c2", text: "ok", isError: false, exitCode: 0 }) },
    ]);
    const [c1, c2] = s.messages[1].tools;
    expect(c1).toMatchObject({ toolCallId: "c1", running: true });
    expect(c2).toMatchObject({ toolCallId: "c2", running: false, result: "ok", isError: false });
  });

  it("工具报错时标记 isError，界面据此显示失败", () => {
    let s = started();
    s = applyAll(s, [
      { kind: "event", ev: ev("tool.start", { toolCallId: "c1", toolName: "bash", args: {} }) },
      { kind: "event", ev: ev("tool.end", { toolCallId: "c1", text: "未获授权", isError: true, exitCode: null }) },
    ]);
    expect(s.messages[1].tools[0]).toMatchObject({ running: false, isError: true, result: "未获授权" });
  });

  it("找不到对应 toolCallId 的 tool.end 不会崩溃，也不会误改其它卡", () => {
    let s = started();
    s = reduce(s, { kind: "event", ev: ev("tool.start", { toolCallId: "c1", toolName: "bash", args: {} }) });
    s = reduce(s, { kind: "event", ev: ev("tool.end", { toolCallId: "不存在", text: "x", isError: false, exitCode: 0 }) });
    expect(s.messages[1].tools[0]).toMatchObject({ toolCallId: "c1", running: true });
  });

  // 这条是从真实桌面操作里发现的：第一轮"拒绝"把卡片标成失败，
  // 第二轮"允许"之后，第一轮的卡片跟着变成了"完成"。
  // 原因是两次 run 复用了同一个 toolCallId（受控 provider 固定发 call_fixture_1，
  // 真实模型重试时也可能复用），而回填只按 id 匹配、没有按 run 隔离。
  it("两个 run 复用同一个 toolCallId 时不互相覆盖状态", () => {
    let s = applyAll(started(), toolRound("run-1", "未获授权", true, 10));
    s = reduce(s, { kind: "user_message", text: "再来一次" });
    s = applyAll(s, toolRound("run-2", "ok", false, 20));

    const cards = s.messages.flatMap((m) => m.tools);
    const first = cards.find((c) => c.runId === "run-1");
    const second = cards.find((c) => c.runId === "run-2");
    expect(first).toBeDefined();
    expect(second).toBeDefined();
    expect(first).toMatchObject({ toolCallId: "c1", isError: true, result: "未获授权", running: false });
    expect(second).toMatchObject({ toolCallId: "c1", isError: false, result: "ok", running: false });
  });
});

describe("审批", () => {
  const request = (id: string) =>
    ev("approval.request", {
      approvalId: id,
      toolName: "bash",
      toolCallId: "c1",
      arguments: { command: "rm -rf /" },
      expiresAtUnix: 1_800_000_000,
    });

  it("approval.request 进入审批条，arguments 以可读 JSON 展示", () => {
    const s = reduce(started(), { kind: "event", ev: request("apr-1") });
    expect(s.approvals).toHaveLength(1);
    expect(s.approvals[0]).toMatchObject({ approvalId: "apr-1", toolName: "bash" });
    expect(s.approvals[0].args).toContain("rm -rf /");
  });

  it("同一 approvalId 重复投递只保留一条（否则会出现点了必然失败的第二个按钮组）", () => {
    let s = reduce(started(), { kind: "event", ev: request("apr-1") });
    s = reduce(s, { kind: "event", ev: request("apr-1") });
    expect(s.approvals).toHaveLength(1);
  });

  it("approval.resolved 只移除对应的一条", () => {
    let s = applyAll(started(), [
      { kind: "event", ev: request("apr-1") },
      { kind: "event", ev: request("apr-2") },
    ]);
    s = reduce(s, { kind: "event", ev: ev("approval.resolved", { approvalId: "apr-1" }) });
    expect(s.approvals.map((a) => a.approvalId)).toEqual(["apr-2"]);
  });

  it("本地点击后立即移除（不必等后端回事件）", () => {
    let s = reduce(started(), { kind: "event", ev: request("apr-1") });
    s = reduce(s, { kind: "approval_resolved", approvalId: "apr-1" });
    expect(s.approvals).toHaveLength(0);
  });
});

describe("运行状态", () => {
  it("进入终态：停止流式并清空审批条", () => {
    let s = applyAll(started(), [
      { kind: "event", ev: ev("message.delta", { text: "输出" }) },
      {
        kind: "event",
        ev: ev("approval.request", {
          approvalId: "apr-1", toolName: "bash", toolCallId: "c1", arguments: {}, expiresAtUnix: 1,
        }),
      },
      { kind: "event", ev: ev("run.state", { state: "completed" }) },
    ]);
    expect(s.phase).toBe("completed");
    expect(s.approvals).toHaveLength(0);
    expect(s.messages.every((m) => !m.streaming)).toBe(true);
  });

  it("等待授权不是终态：审批条保留，流式标记不动", () => {
    let s = reduce(started(), { kind: "event", ev: ev("run.state", { state: "waiting_approval" }) });
    expect(s.phase).toBe("waiting_approval");
    expect(s.messages[1].streaming).toBe(true);
    expect(isTerminal("waiting_approval")).toBe(false);
  });

  it("stopping 不是终态（停止还在进行中，不能提前显示成已取消）", () => {
    expect(isTerminal("stopping")).toBe(false);
    expect(isTerminal("cancelled")).toBe(true);
  });

  it("启动失败：置为 failed 并关闭流式占位气泡", () => {
    const s = reduce(started(), { kind: "run_start_failed", message: "启动失败" });
    expect(s.phase).toBe("failed");
    expect(s.error).toBe("启动失败");
    expect(s.messages[1].streaming).toBe(false);
  });
});

describe("错误与诊断", () => {
  it("run.error 展示成 code: message", () => {
    const s = reduce(started(), { kind: "event", ev: ev("run.error", { code: "PI_CRASH", message: "退出码 1" }) });
    expect(s.error).toBe("PI_CRASH: 退出码 1");
  });

  it("diagnostic 带上 kind，便于区分协议噪音与真实失败", () => {
    const s = reduce(started(), { kind: "event", ev: ev("diagnostic", { kind: "unknown-record", message: "丢弃一行" }) });
    expect(s.error).toBe("诊断 unknown-record: 丢弃一行");
  });

  it("未识别的事件类型不改界面，但水位仍前进（避免重放时反复投递）", () => {
    const s = reduce(started(), { kind: "event", ev: ev("message.done", { text: "x" }, { seq: 9 }) });
    expect(s.messages).toHaveLength(2);
    expect(s.lastSeqByRun["run-1"]).toBe(9);
  });

  it("clear_error 清掉错误条", () => {
    let s = reduce(started(), { kind: "error", message: "出错了" });
    s = reduce(s, { kind: "clear_error" });
    expect(s.error).toBeNull();
  });
});
