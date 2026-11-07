// 会话状态归约的单元测试。
//
// 这些用例只测一件事：**后端事件序列 → 界面上应该是什么**。
// 不渲染 React、不碰 DOM，所以失败时能直接指出是哪条映射错了，
// 而不是"截图看起来不对"。
//
// 断言的都是会真的出现在用户眼前的行为：气泡数量、流式标记、工具卡状态、
// 审批条内容。不测实现细节（比如内部数组的引用是否变化）。
import { describe, expect, it } from "vitest";
import {
  clarificationsOf,
  initialState,
  isTerminal,
  managedSkillsLabel,
  reduce,
  skillAutoDiscoveryLabel,
  type UiState,
  type UiAction,
} from "./runState";
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

  // 这条替换了旧的“本地点击后立即移除（不必等后端回事件）”。
  // 旧用例断言的是规格 §5.3 明确禁止的行为：前端在按钮点击后自行标记成功。
  // 现在的纪律是：点击只表示“请求已提交”，卡片留在原处，直到 approval.resolved 到达。
  it("does not mark approval accepted until backend event arrives", () => {
    let s = reduce(started(), { kind: "event", ev: request("apr-1") });
    s = reduce(s, { kind: "approval_submitted", approvalId: "apr-1" });

    // 点过之后卡片仍然在，只是标成“已提交”。
    expect(s.approvals).toHaveLength(1);
    expect(s.approvals[0]).toMatchObject({ approvalId: "apr-1", submitted: true });
    // 也不能顺手记成“已经批准”。
    expect(s.lastApprovalResolution).toBeNull();

    s = reduce(s, {
      kind: "event",
      ev: ev("approval.resolved", { approvalId: "apr-1", choice: "once" }),
    });
    expect(s.approvals).toHaveLength(0);
    expect(s.lastApprovalResolution).toEqual({ approvalId: "apr-1", choice: "once" });
  });

  it("后端答复拒绝时把那条连同原因一起移除", () => {
    let s = reduce(started(), { kind: "event", ev: request("apr-1") });
    s = reduce(s, {
      kind: "event",
      ev: ev("approval.resolved", {
        approvalId: "apr-1",
        choice: "deny",
        reason: "这一轮已经结束，不会再有人回答",
      }),
    });
    expect(s.approvals).toHaveLength(0);
    expect(s.lastApprovalResolution).toEqual({
      approvalId: "apr-1",
      choice: "deny",
      reason: "这一轮已经结束，不会再有人回答",
    });
  });
});

describe("澄清（不是审批）", () => {
  const question = (id: string, kind = "confirm") =>
    ev("clarification.requested", {
      requestId: id,
      kind,
      question: "要用 staging 还是 prod？",
      options: [],
    });

  it("澄清只来自 clarification.requested，assistant 文本里的问句不算", () => {
    // 普通文本里出现问号，不能被猜成“模型在问用户”。
    let s = reduce(started(), {
      kind: "event",
      ev: ev("message.delta", { text: "要用 staging 还是 prod？" }),
    });
    expect(clarificationsOf(s)).toHaveLength(0);

    s = reduce(s, { kind: "event", ev: question("ui-1") });
    expect(clarificationsOf(s)).toHaveLength(1);
    expect(clarificationsOf(s)[0]).toMatchObject({
      requestId: "ui-1",
      kind: "confirm",
      submitted: false,
    });
  });

  it("回答澄清不产生任何审批或授权，收口只由 clarification.resolved 决定", () => {
    let s = reduce(started(), { kind: "event", ev: question("ui-1", "select") });
    s = reduce(s, { kind: "clarification_submitted", requestId: "ui-1", value: "staging" });

    // 关键断言：回答一个内容问题，审批条数量必须还是 0。
    expect(s.approvals).toHaveLength(0);
    expect(s.lastApprovalResolution).toBeNull();
    // 卡片也还在（等后端收口），只是标成已提交。
    expect(clarificationsOf(s)).toHaveLength(1);
    expect(clarificationsOf(s)[0]).toMatchObject({ submitted: true, answeredValue: "staging" });

    s = reduce(s, {
      kind: "event",
      ev: ev("clarification.resolved", { requestId: "ui-1", outcome: "value", value: "staging" }),
    });
    expect(clarificationsOf(s)).toHaveLength(0);
    expect(s.approvals).toHaveLength(0);
    expect(s.lastClarificationResolution).toEqual({
      requestId: "ui-1",
      outcome: "value",
      value: "staging",
    });
  });
});

describe("会话归属", () => {
  it("运行属于哪个会话由发送时的会话与事件共同确定", () => {
    let s = reduce(initialState, { kind: "user_message", text: "你好", sessionId: "sess-a" });
    expect(s.activeSessionId).toBe("sess-a");

    s = reduce(s, { kind: "run_started", runId: "run-9", state: "running", sessionId: "sess-a" });
    // 占位气泡在发送时就存在，runId 由 run_started 回填：右栏产物靠它定位来源轮次。
    expect(s.messages[1].runId).toBe("run-9");
    expect(s.messages[0].runId).toBe("run-9");

    // 上一轮事件的重放不能把页面挪到别的会话。
    s = reduce(s, {
      kind: "event",
      ev: ev("message.delta", { text: "旧事件" }, { runId: "run-0", seq: 1 }),
    });
    expect(s.activeSessionId).toBe("sess-a");
  });
});

describe("终局与停止", () => {
  const running = (): UiState =>
    applyAll(started(), [{ kind: "run_started", runId: "run-1", state: "running" }]);

  it("settled 只由终局 run.state 事件决定（工具结果不足以收口）", () => {
    let s = applyAll(running(), [
      { kind: "event", ev: ev("tool.start", { toolCallId: "c1", toolName: "bash", args: {} }) },
      {
        kind: "event",
        ev: ev("tool.end", { toolCallId: "c1", text: "ok", isError: false, exitCode: 0 }),
      },
    ]);
    // 工具跑完了，这一轮还没结束：界面不能说“已完成”。
    expect(s.settled).toBeNull();
    expect(s.phase).toBe("running");

    s = reduce(s, { kind: "event", ev: ev("run.state", { state: "completed" }) });
    expect(s.settled).toBe("completed");
  });

  it("失败的一轮以 failed 收口，不冒充完成", () => {
    const s = applyAll(running(), [
      { kind: "event", ev: ev("run.error", { code: "PROVIDER_ERROR", message: "配额用尽" }) },
      { kind: "event", ev: ev("run.state", { state: "failed" }) },
    ]);
    expect(s.settled).toBe("failed");
    expect(s.settled).not.toBe("completed");
    expect(s.error).toBe("PROVIDER_ERROR: 配额用尽");
    expect(s.errorCode).toBe("PROVIDER_ERROR");
    expect(s.messages.every((m) => !m.streaming)).toBe(true);
  });

  it("停止后不再派发新消息，未回填的工具结果标为未知而不是成功", () => {
    let s = applyAll(running(), [
      { kind: "event", ev: ev("tool.start", { toolCallId: "c1", toolName: "bash", args: {} }) },
    ]);
    s = reduce(s, { kind: "stop_requested" });

    expect(s.phase).toBe("stopping");
    expect(s.stopRequested).toBe(true);
    // 停止时这个工具还没有结果：只能显示“结果未知”。
    expect(s.messages[1].tools[0]).toMatchObject({ running: false, outcome: "unknown" });
    expect(s.messages[1].tools[0].outcome).not.toBe("ok");
    expect(s.messages[1].tools[0].result).toBeUndefined();

    const blocked = reduce(s, { kind: "user_message", text: "再问一句" });
    expect(blocked.messages).toHaveLength(s.messages.length);
    expect(blocked.msgSeq).toBe(s.msgSeq);
  });

  it("停止后仍然回填的工具结果以真实结果为准（未知可以被事实推翻）", () => {
    let s = applyAll(running(), [
      { kind: "event", ev: ev("tool.start", { toolCallId: "c1", toolName: "bash", args: {} }) },
    ]);
    s = reduce(s, { kind: "stop_requested" });
    s = reduce(s, {
      kind: "event",
      ev: ev("tool.end", { toolCallId: "c1", text: "已经写完", isError: false, exitCode: 0 }),
    });
    expect(s.messages[1].tools[0]).toMatchObject({
      running: false,
      outcome: "ok",
      result: "已经写完",
    });
  });

  it("重放同一 eventId 的事件不会重复一条消息（即使 seq 被改写）", () => {
    const delta = ev("message.delta", { text: "只出现一次" }, { seq: 7 });
    let s = reduce(started(), { kind: "event", ev: delta });
    s = reduce(s, { kind: "event", ev: { ...delta } });
    // 同一 eventId、更大 seq：仍然视为同一条事件。
    s = reduce(s, { kind: "event", ev: { ...delta, seq: 9 } });
    expect(s.messages).toHaveLength(2);
    expect(s.messages[1].text).toBe("只出现一次");
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

// MOD04：这一轮用的是哪个 provider/模型、哪个配置 revision，必须来自启动时的
// 快照。写成"当前设置里的模型"就会在用户中途换模型之后撒谎——那一轮用的
// 仍然是启动时那个。
// C03：技能同理，而且必须分两段——托管显式加载的清单 + 自动发现开关。
describe("run_started 记录实际使用的模型", () => {
  it("保存 provider/模型/配置 revision", () => {
    const s = reduce(started(), {
      kind: "run_started",
      runId: "run-meta",
      state: "running",
      meta: {
        provider: "deepseek",
        model: "deepseek-v4.1-flash",
        configRevision: 4,
        managedSkills: ["demo-skill"],
        skillAutoDiscovery: "enabled",
      },
    });
    expect(s.runMeta).toEqual({
      provider: "deepseek",
      model: "deepseek-v4.1-flash",
      configRevision: 4,
      managedSkills: ["demo-skill"],
      skillAutoDiscovery: "enabled",
    });
  });

  it("没有 meta 时不清空上一轮记录，也不编造", () => {
    const meta = {
      provider: "p",
      model: "m",
      configRevision: 3,
      managedSkills: [],
      skillAutoDiscovery: "enabled" as const,
    };
    const first = reduce(started(), {
      kind: "run_started",
      runId: "run-a",
      state: "running",
      meta,
    });
    const second = reduce(first, { kind: "run_started", runId: "run-b", state: "running" });
    expect(second.runMeta).toEqual(meta);
    expect(initialState.runMeta).toBeNull();
  });
});

// C03：技能的两段事实必须能分开读。托管空数组不是"没有技能"，
// 自动发现状态缺失也不是"已关闭"。
describe("技能事实的展示文案", () => {
  it("托管为空只能写「未显式加载」，不能写「没有技能」", () => {
    expect(managedSkillsLabel([])).toBe("未显式加载");
    expect(managedSkillsLabel(["demo-skill"])).toBe("demo-skill");
    expect(managedSkillsLabel([])).not.toContain("没有技能");
  });

  it("自动发现三态：开着=清单未知，关着=已关闭，没记录=未知", () => {
    expect(skillAutoDiscoveryLabel("enabled")).toBe("已开启（清单未知）");
    expect(skillAutoDiscoveryLabel("disabled")).toBe("已关闭");
    expect(skillAutoDiscoveryLabel(undefined)).toBe("未知");
  });
});
