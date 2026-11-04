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
  ToolCard,
} from "./types";

export type Phase = RunState | "idle";

export interface UiState {
  phase: Phase;
  runId: string | null;
  messages: ChatMessage[];
  approvals: ApprovalItem[];
  error: string | null;
  /**
   * 每个 run 已处理到的事件序号。
   *
   * 为什么要它：事件是可以被重放的（订阅方重连、以后要做的 Replay 面板），
   * 而 message.delta 不是幂等的——同一段增量处理两次，界面上就是重复的句子。
   * 后端已经保证 seq 在单个 run 内严格单调递增，前端只需按 run 记住水位即可。
   */
  lastSeqByRun: Record<string, number>;
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

export interface RunMeta {
  provider: string;
  model: string;
  configRevision: number;
}

export const initialState: UiState = {
  phase: "idle",
  runId: null,
  messages: [],
  approvals: [],
  error: null,
  lastSeqByRun: {},
  msgSeq: 0,
  runMeta: null,
};

export type UiAction =
  | { kind: "event"; ev: ProductEvent }
  | { kind: "user_message"; text: string }
  | { kind: "run_started"; runId: string; state: RunState; meta?: RunMeta }
  | { kind: "run_start_failed"; message: string }
  | { kind: "approval_resolved"; approvalId: string }
  | { kind: "error"; message: string }
  | { kind: "clear_error" };

/** 终态集合。进入终态后界面不再"正在输出"，审批条也必须清空。 */
export function isTerminal(state: RunState): boolean {
  return (
    state === "completed" ||
    state === "failed" ||
    state === "cancelled" ||
    state === "interrupted"
  );
}

function nextMessageId(s: UiState): { id: string; msgSeq: number } {
  const n = s.msgSeq + 1;
  return { id: `m${n}`, msgSeq: n };
}

/** 找到最后一条助手消息的下标；没有则返回 -1。 */
function lastAssistantIndex(messages: ChatMessage[]): number {
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i].role === "assistant") return i;
  }
  return -1;
}

/** 找到最后一条仍在流式输出的助手消息下标；没有则返回 -1。 */
function lastStreamingAssistantIndex(messages: ChatMessage[]): number {
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i];
    if (m.role === "assistant" && m.streaming) return i;
  }
  return -1;
}

function closeStreaming(messages: ChatMessage[]): ChatMessage[] {
  return messages.map((m) => (m.role === "assistant" ? { ...m, streaming: false } : m));
}

function payloadOf<T>(ev: ProductEvent): T {
  return (ev.payload ?? {}) as T;
}

/** 把一条产品事件并入状态。未识别的事件类型原样返回，不猜、不吞。 */
function applyEvent(state: UiState, ev: ProductEvent): UiState {
  // 重放/重复投递的增量事件不能二次生效。
  const seen = state.lastSeqByRun[ev.runId] ?? 0;
  if (ev.seq > 0 && ev.seq <= seen) return state;
  const base: UiState = {
    ...state,
    lastSeqByRun: { ...state.lastSeqByRun, [ev.runId]: ev.seq },
  };

  switch (ev.type) {
    case "run.state": {
      const p = payloadOf<{ state: RunState }>(ev);
      const terminal = isTerminal(p.state);
      return {
        ...base,
        phase: p.state,
        messages: terminal ? closeStreaming(base.messages) : base.messages,
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
      const card: ToolCard = {
        runId: ev.runId,
        toolCallId: p.toolCallId,
        toolName: p.toolName,
        args: JSON.stringify(p.args ?? {}, null, 2),
        running: true,
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
              ? { ...t, result: p.text, isError: p.isError, running: false }
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
          },
        ],
      };
    }
    case "approval.resolved": {
      const p = payloadOf<{ approvalId: string }>(ev);
      return { ...base, approvals: base.approvals.filter((a) => a.approvalId !== p.approvalId) };
    }
    case "run.error": {
      const p = payloadOf<{ code: string; message: string }>(ev);
      return { ...base, error: `${p.code}: ${p.message}` };
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
      // 用户消息与占位助手气泡同时建立：后续 delta 会往这条占位里追加，
      // 不会因为"还没开流"而另起一条，界面上不会出现两条助手消息。
      const u = nextMessageId(state);
      const a = { id: `m${u.msgSeq + 1}`, msgSeq: u.msgSeq + 1 };
      return {
        ...state,
        msgSeq: a.msgSeq,
        error: null,
        approvals: [],
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
    case "approval_resolved":
      return {
        ...state,
        approvals: state.approvals.filter((a) => a.approvalId !== action.approvalId),
      };
    case "error":
      return { ...state, error: action.message };
    case "clear_error":
      return { ...state, error: null };
    default:
      return state;
  }
}
