// 会话 UI 的状态归约（纯函数）。
//
// 为什么把它从 App.tsx 里抽出来：这段逻辑是"后端事实 → 界面"的唯一映射，
// 也是出问题最贵的地方（少画一条审批、把失败显示成完成、事件重放导致消息翻倍）。
// 放在组件里既测不了，也没法单独说清它到底保证什么。
//
// 归约函数不碰 React、不碰 DOM、不读时钟、不自增模块级计数器——
// 同样的输入必然得到同样的输出，所以可以直接断言。
import type {
  ApprovalItem,
  ChatMessage,
  ProductEvent,
  RunState,
  SkillAutoDiscovery,
  ToolCard,
} from "./types";

export type Phase = RunState | "idle";

/**
 * 工具卡的结果三态 + 一个“还不能说”的状态。
 *
 * `unknown` 是这里唯一新增的语义，而且它是必须的：停止/中断之后，
 * 有些工具调用既没有回填结果，也不会再有结果。把它显示成“完成”是在编造成功，
 * 显示成“失败”同样是编造——真实的答案就是“不知道”。
 */
export type ToolOutcome = "pending" | "ok" | "error" | "unknown";

/** 界面里的工具卡：后端 DTO + 一个“结果是否已知”的判断。 */
export interface UiToolCard extends ToolCard {
  /**
   * 结果三态。可选是为了兼容 U01 页面测试里手写的 fixture；
   * 归约器建的卡总是带这个字段，读的时候用 toolOutcomeOf()。
   */
  outcome?: ToolOutcome;
}

/**
 * 一张工具卡的结果。
 *
 * 缺字段时只能从 running/isError 推出“还在跑”或“失败/完成”：
 * 那是 U01 手写 fixture 的语义，不是归约器的语义。
 */
export function toolOutcomeOf(card: UiToolCard): ToolOutcome {
  if (card.outcome) return card.outcome;
  if (card.running) return "pending";
  return card.isError ? "error" : "ok";
}

/** 界面里的消息。tools 带 outcome，其余与 ChatMessage 一致（可直接当 ChatMessage 用）。 */
export interface UiMessage extends Omit<ChatMessage, "tools"> {
  tools: UiToolCard[];
}

/**
 * 待审批项。
 *
 * `submitted` 只表示“用户点过，请求已提交”，**不是**结论。
 * 卡片必须留到后端 approval.resolved 到达（规格 §5.3/§11：乐观 UI 只能表示已提交）。
 */
export interface UiApproval extends ApprovalItem {
  /** 可选的原因同 UiToolCard.outcome：U01 的 fixture 不必重写。 */
  submitted?: boolean;
}

/** 后端对一条审批的最终结论。reason 只在后端给了理由时出现（例如 run 终止）。 */
export interface ApprovalResolution {
  approvalId: string;
  choice: string;
  reason?: string;
}

/** 内容澄清的种类（与 internal/run 的 dialogMethods 对齐）。 */
export type ClarificationKind = "select" | "confirm" | "input" | "editor";

/**
 * 一条待回答的内容澄清。
 *
 * 它只来自后端的 clarification.requested（C06f 把 Pi 的 extension_ui_request 映射过来），
 * 与工具审批是两条独立通道：回答它不产生 approval、session grant 或任何工具许可。
 */
export interface ClarificationItem {
  runId: string;
  requestId: string;
  kind: ClarificationKind | string;
  question: string;
  options: string[];
  /** 已提交答复，等 clarification.resolved 收口。 */
  submitted: boolean;
  /** 已提交的答复值（select/input/editor 为文本，confirm 为 true/false）。 */
  answeredValue?: string;
}

/** 后端对一条澄清的收口结论。 */
export interface ClarificationResolution {
  requestId: string;
  outcome: string;
  value?: string;
  confirmed?: boolean;
  reason?: string;
}

export interface UiState {
  phase: Phase;
  runId: string | null;
  messages: UiMessage[];
  approvals: UiApproval[];
  /**
   * 待回答的内容澄清。
   *
   * 可选：U01 已合入的页面测试里有手写的 UiState 字面量，不该因为 U02 新增一段
   * 状态就全部改一遍。归约器总是写这个字段；读的时候用 clarificationsOf()。
   */
  clarifications?: ClarificationItem[];
  /**
   * 这一轮的最终结论。**只**由终局 run.state 事件置位。
   *
   * 工具结果、message.done、点击停止都不是“收口”：把它们当成收口，
   * 界面上就会出现“看着完成了、其实还在跑”的假状态。
   */
  settled?: RunState | null;
  /** 已经请求过停止（后端已接受 abort）。此后不再派发新消息。 */
  stopRequested?: boolean;
  /** 最近一条审批的后端结论（含拒绝理由）。用于把“为什么没了”说清楚。 */
  lastApprovalResolution?: ApprovalResolution | null;
  /** 最近一条澄清的后端收口结论。 */
  lastClarificationResolution?: ClarificationResolution | null;
  error: string | null;
  /** 错误码，单独存：文案按 code 翻译，不解析 message。 */
  errorCode?: string | null;
  /**
   * 每个 run 已处理到的事件序号。
   *
   * 为什么要它：事件是可以被重放的（订阅方重连、以后要做的 Replay 面板），
   * 而 message.delta 不是幂等的——同一段增量处理两次，界面上就是重复的句子。
   * 后端已经保证 seq 在单个 run 内严格单调递增，前端只需按 run 记住水位即可。
   */
  lastSeqByRun: Record<string, number>;
  /**
   * 已处理过的事件 ID。
   *
   * 与 seq 水位互补：重放/重连时同一条事件可能带着不一样的 seq 再来一次，
   * 只比 seq 会让重复投递穿透成第二条消息。eventId 是后端给事件的稳定身份。
   */
  seenEventIds?: Record<string, true>;
  /** 消息 id 的自增源。放在状态里而不是模块变量，保证测试可重复。 */
  msgSeq: number;
  /**
   * 当前 run 实际使用的 provider/模型与配置 revision（MOD04）。
   *
   * 取自启动那一刻的快照，而不是"现在设置里是什么"——用户中途换了模型，
   * 这一轮用的仍然是启动时那个；两者混在一起就再也说不清哪一轮跑的是什么。
   */
  runMeta: RunMeta | null;
}

/** 待回答的澄清列表（缺字段 = 这一份状态里从来没有澄清）。 */
export function clarificationsOf(state: UiState): ClarificationItem[] {
  return state.clarifications ?? [];
}

/** 这一轮是否已经收口，以及收口成了什么。 */
export function settledOf(state: UiState): RunState | null {
  return state.settled ?? null;
}

export interface RunMeta {
  provider: string;
  model: string;
  configRevision: number;
  /**
   * 这一轮通过 --skill 显式加载的托管技能 ID（RES02）。
   * 空数组 = 确实没显式加载托管技能，不是“没有任何技能”：
   * Pi 自己发现的技能不在这个列表里，见 skillAutoDiscovery。
   */
  managedSkills: string[];
  /** Pi 侧技能自动发现的开关。缺失 = 没记录，界面必须显示“未知”。 */
  skillAutoDiscovery?: SkillAutoDiscovery;
}

export const initialState: UiState = {
  phase: "idle",
  runId: null,
  messages: [],
  approvals: [],
  clarifications: [],
  settled: null,
  stopRequested: false,
  lastApprovalResolution: null,
  lastClarificationResolution: null,
  error: null,
  errorCode: null,
  lastSeqByRun: {},
  seenEventIds: {},
  msgSeq: 0,
  runMeta: null,
};

export type UiAction =
  | { kind: "event"; ev: ProductEvent }
  | { kind: "user_message"; text: string }
  | { kind: "run_started"; runId: string; state: RunState; meta?: RunMeta }
  | { kind: "run_start_failed"; message: string }
  /** 审批结论已提交（后端调用成功），等 approval.resolved。 */
  | { kind: "approval_submitted"; approvalId: string }
  /** 停止已请求（后端已接受 abort）。 */
  | { kind: "stop_requested" }
  /** 澄清答复已提交，等 clarification.resolved。 */
  | { kind: "clarification_submitted"; requestId: string; value?: string }
  | { kind: "error"; message: string; code?: string }
  | { kind: "clear_error" };

/** 终态集合。进入终态后界面不再“正在输出”，审批条也必须清空。 */
export function isTerminal(state: RunState): boolean {
  return (
    state === "completed" ||
    state === "failed" ||
    state === "cancelled" ||
    state === "interrupted"
  );
}

/**
 * “Pi 本地自动发现”的中文表述。
 *
 * 产品拿不到 Pi 自动发现到的清单，只能报开关；未知就是未知，
 * 不能写成“未加载”——那是托管显式加载那一半的事实，两件事不能混。
 */
export function skillAutoDiscoveryLabel(value: SkillAutoDiscovery | undefined): string {
  switch (value) {
    case "enabled":
      return "已开启（清单未知）";
    case "disabled":
      return "已关闭";
    default:
      return "未知";
  }
}

/**
 * 托管显式加载清单的展示文案。
 *
 * 空数组只能说“未显式加载”，不能说“没有技能”：
 * 自动发现开着时 Pi 仍可能带着用户级技能在跑。
 */
export function managedSkillsLabel(ids: string[]): string {
  return ids.length > 0 ? ids.join("、") : "未显式加载";
}

function nextMessageId(s: UiState): { id: string; msgSeq: number } {
  const n = s.msgSeq + 1;
  return { id: `m${n}`, msgSeq: n };
}

/** 找到最后一条助手消息的下标；没有则返回 -1。 */
function lastAssistantIndex(messages: UiMessage[]): number {
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i].role === "assistant") return i;
  }
  return -1;
}

/** 找到最后一条仍在流式输出的助手消息下标；没有则返回 -1。 */
function lastStreamingAssistantIndex(messages: UiMessage[]): number {
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i];
    if (m.role === "assistant" && m.streaming) return i;
  }
  return -1;
}

function closeStreaming(messages: UiMessage[]): UiMessage[] {
  return messages.map((m) => (m.role === "assistant" ? { ...m, streaming: false } : m));
}

/** 把仍在执行的工具卡标成“结果未知”（停止/终止时的事实：不会再有结果了）。 */
function markRunningToolsUnknown(messages: UiMessage[]): UiMessage[] {
  return messages.map((m) => ({
    ...m,
    tools: m.tools.map((t) =>
      t.running ? { ...t, running: false, outcome: "unknown" as const } : t
    ),
  }));
}

function payloadOf<T>(ev: ProductEvent): T {
  return (ev.payload ?? {}) as T;
}

/** 把一条产品事件并入状态。未识别的事件类型原样返回，不猜、不吞。 */
function applyEvent(state: UiState, ev: ProductEvent): UiState {
  // 重放/重复投递的事件不能二次生效。
  // 两道闸：eventId（事件的稳定身份）与 seq 水位（单个 run 内严格单调）。
  // 两道都要：同一条事件重放时 seq 可能被改写，而水位挡不住跨 run 的 eventId 复用。
  const eventId = typeof ev.eventId === "string" ? ev.eventId : "";
  const seenIds = state.seenEventIds ?? {};
  if (eventId !== "" && seenIds[eventId]) return state;
  const seen = state.lastSeqByRun[ev.runId] ?? 0;
  if (ev.seq > 0 && ev.seq <= seen) return state;
  const base: UiState = {
    ...state,
    lastSeqByRun: { ...state.lastSeqByRun, [ev.runId]: ev.seq },
    seenEventIds: eventId === "" ? seenIds : { ...seenIds, [eventId]: true },
  };

  switch (ev.type) {
    case "run.state": {
      const p = payloadOf<{ state: RunState }>(ev);
      const terminal = isTerminal(p.state);
      return {
        ...base,
        phase: p.state,
        // settled 只在这里置位：工具结果、message.done、点击停止都不能把一轮说成收口。
        settled: terminal ? p.state : base.settled,
        // 终止时：未回填结果的工具只能算“未知”，不能算成功。
        messages: terminal
          ? markRunningToolsUnknown(closeStreaming(base.messages))
          : base.messages,
        // 这一轮已经结束，不会再有人回答的审批不能永远挂着。
        // 注意这不是“标记已批准”：卡片消失不等于授权成立，后端会自己发 approval.resolved。
        approvals: terminal ? [] : base.approvals,
      };
    }
    case "message.delta": {
      const p = payloadOf<{ text: string }>(ev);
      const text = p.text ?? "";
      const at = lastStreamingAssistantIndex(base.messages);
      if (at === -1) {
        const { id, msgSeq } = nextMessageId(base);
        return {
          ...base,
          msgSeq,
          messages: [
            ...base.messages,
            { id, role: "assistant", text, tools: [], streaming: true },
          ],
        };
      }
      const messages = [...base.messages];
      messages[at] = { ...messages[at], text: messages[at].text + text };
      return { ...base, messages };
    }
    case "tool.start": {
      const p = payloadOf<{ toolCallId: string; toolName: string; args: unknown }>(ev);
      const card: UiToolCard = {
        runId: ev.runId,
        toolCallId: p.toolCallId,
        toolName: p.toolName,
        args: JSON.stringify(p.args ?? {}, null, 2),
        running: true,
        outcome: "pending",
      };
      const at = lastAssistantIndex(base.messages);
      if (at === -1) {
        const { id, msgSeq } = nextMessageId(base);
        return {
          ...base,
          msgSeq,
          messages: [
            ...base.messages,
            { id, role: "assistant", text: "", tools: [card], streaming: true },
          ],
        };
      }
      const messages = [...base.messages];
      messages[at] = { ...messages[at], tools: [...messages[at].tools, card] };
      return { ...base, messages };
    }
    case "tool.end": {
      const p = payloadOf<{
        toolCallId: string;
        text: string;
        isError: boolean;
        exitCode: number | null;
      }>(ev);
      // 按 toolCallId **且** 同一个 run 精确回填：
      //   - 不能用"最后一张卡"代替：并行的多个工具调用完成顺序与发起顺序无关；
      //   - 不能只按 toolCallId：id 只在单个 run 内唯一，跨 run 复用时会改错卡
      //     （已实测：上一轮标"失败"的卡片被下一轮的"完成"覆盖）。
      return {
        ...base,
        messages: base.messages.map((m) => ({
          ...m,
          tools: m.tools.map((t) =>
            t.toolCallId === p.toolCallId && t.runId === ev.runId
              ? {
                  ...t,
                  result: p.text,
                  isError: p.isError,
                  running: false,
                  // 真实的工具结果可以推翻“未知”（停止时先标未知，随后回填）。
                  outcome: p.isError ? ("error" as const) : ("ok" as const),
                }
              : t
          ),
        })),
      };
    }
    case "approval.request": {
      const p = payloadOf<{
        approvalId: string;
        toolName: string;
        toolCallId: string;
        arguments: unknown;
        expiresAtUnix: number;
      }>(ev);
      // 同一 approvalId 重复投递只保留一条：否则界面上会出现两个按钮组，
      // 其中一个点了必然失败（后端只认一次）。
      if (base.approvals.some((a) => a.approvalId === p.approvalId)) return base;
      return {
        ...base,
        approvals: [
          ...base.approvals,
          {
            approvalId: p.approvalId,
            toolName: p.toolName,
            toolCallId: p.toolCallId,
            args: JSON.stringify(p.arguments ?? {}, null, 2),
            expiresAtUnix: p.expiresAtUnix,
            submitted: false,
          },
        ],
      };
    }
    case "approval.resolved": {
      // 审批**唯一**的推进方式。choice=deny 时后端会带上 reason（例如 run 已终止），
      // 把它存下来，界面上才能说清“为什么这条没了”，而不是默默消失。
      const p = payloadOf<{ approvalId: string; choice?: string; reason?: string }>(ev);
      const resolution: ApprovalResolution = {
        approvalId: p.approvalId,
        choice: p.choice ?? "",
      };
      if (p.reason) resolution.reason = p.reason;
      return {
        ...base,
        approvals: base.approvals.filter((a) => a.approvalId !== p.approvalId),
        lastApprovalResolution: resolution,
      };
    }
    case "clarification.requested": {
      // 只认后端事件：assistant 文本里出现问句不能猜成“模型在问用户”。
      const p = payloadOf<{
        requestId: string;
        kind: string;
        question: string;
        options?: string[];
      }>(ev);
      if (!p.requestId) return base;
      if ((base.clarifications ?? []).some((c) => c.requestId === p.requestId)) return base;
      return {
        ...base,
        clarifications: [
          ...(base.clarifications ?? []),
          {
            runId: ev.runId,
            requestId: p.requestId,
            kind: p.kind ?? "input",
            question: p.question ?? "",
            options: p.options ?? [],
            submitted: false,
          },
        ],
      };
    }
    case "clarification.resolved": {
      const p = payloadOf<{
        requestId: string;
        outcome: string;
        value?: string;
        confirmed?: boolean;
        reason?: string;
      }>(ev);
      const resolution: ClarificationResolution = {
        requestId: p.requestId,
        outcome: p.outcome ?? "",
      };
      if (p.value !== undefined) resolution.value = p.value;
      if (p.confirmed !== undefined) resolution.confirmed = p.confirmed;
      if (p.reason !== undefined) resolution.reason = p.reason;
      return {
        ...base,
        clarifications: (base.clarifications ?? []).filter((c) => c.requestId !== p.requestId),
        lastClarificationResolution: resolution,
      };
    }
    case "run.error": {
      const p = payloadOf<{ code: string; message: string }>(ev);
      return { ...base, errorCode: p.code, error: `${p.code}: ${p.message}` };
    }
    case "diagnostic": {
      const p = payloadOf<{ kind: string; message: string }>(ev);
      return { ...base, error: `诊断 ${p.kind}: ${p.message}` };
    }
    default:
      return base;
  }
}

export function reduce(state: UiState, action: UiAction): UiState {
  switch (action.kind) {
    case "event":
      return applyEvent(state, action.ev);
    case "user_message": {
      // 停止已请求、这一轮还没收口：不接受新消息。
      // 否则就会出现“上一轮还在停、下一轮已经开跑”的双 run 界面，而用户以为只发了一条。
      if (state.stopRequested && !isTerminal(state.phase as RunState)) return state;
      // 用户消息与占位助手气泡同时建立：后续 delta 会往这条占位里追加，
      // 不会因为"还没开流"而另起一条，界面上不会出现两条助手消息。
      const u = nextMessageId(state);
      const a = { id: `m${u.msgSeq + 1}`, msgSeq: u.msgSeq + 1 };
      return {
        ...state,
        msgSeq: a.msgSeq,
        error: null,
        errorCode: null,
        stopRequested: false,
        settled: null,
        messages: [
          ...state.messages,
          { id: u.id, role: "user", text: action.text, tools: [], streaming: false },
          { id: a.id, role: "assistant", text: "", tools: [], streaming: true },
        ],
      };
    }
    case "run_started":
      return {
        ...state,
        runId: action.runId,
        phase: action.state,
        runMeta: action.meta ?? state.runMeta,
      };
    case "run_start_failed":
      return {
        ...state,
        error: action.message,
        phase: "failed",
        messages: closeStreaming(state.messages),
      };
    case "approval_submitted":
      // 只把“请求已提交”标上去。移除只能来自 approval.resolved。
      return {
        ...state,
        approvals: state.approvals.map((a) =>
          a.approvalId === action.approvalId ? { ...a, submitted: true } : a
        ),
      };
    case "stop_requested":
      // 停止已由后端接受：界面进入“停止中”，还没回填结果的工具从此只能算“未知”。
      return {
        ...state,
        phase: "stopping",
        stopRequested: true,
        messages: markRunningToolsUnknown(state.messages),
      };
    case "clarification_submitted":
      // 回答一个内容问题不等于授权任何一个工具：这里只动澄清列表。
      return {
        ...state,
        clarifications: (state.clarifications ?? []).map((c) =>
          c.requestId === action.requestId
            ? { ...c, submitted: true, answeredValue: action.value }
            : c
        ),
      };
    case "error":
      return { ...state, error: action.message, errorCode: action.code ?? state.errorCode };
    case "clear_error":
      return { ...state, error: null, errorCode: null };
    default:
      return state;
  }
}
