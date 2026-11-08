// 浏览器写动作的审批通道（M1）。
//
// 为什么这条通道**不**走 run 的 approval 事件流：
//   - 浏览器审批的权威是 internal/app 的 browserAuthorization（策略服务 + 审批队列），
//     它只服务 browser_act，不进 Pi 的 run 事件；后端也没有对应的产品事件。
//   - 所以这里只能读后端事实（PendingBrowserApprovals）而不是"等一条事件"。
//
// 三条纪律（每条都有测试）：
//  1. **点击 ≠ 已批准**：decide() 只把结论交给 ResolveBrowserApproval；卡片继续留在
//     原位并标成"已提交"，直到**后端的读取结果里不再有这条**才移除。
//     任何"点了就移除"的乐观 UI 都会在"后端其实拒绝了/根本没收到"时显示成批准过。
//  2. 移除必须带原因：未提交就消失 → 过期（到期时间已过）或撤销；提交后消失 →
//     已批准/已拒绝。界面上留一行说明，不用"卡片默默消失"表达结论。
//  3. 读失败不说成"没有待审批"：保留上一次已知列表，另给 hint（按 code 显示）。
//     把读失败画成空列表，等于替用户回答"没有要批的东西"。
//
// 轮询而不是订阅：M1 不给后端加事件（那要动 internal/run 与事件契约）。
// 代价是"结论到达界面"最多晚一个周期；这是可接受的，因为真正决定放不放行的
// 是后端（Recheck），界面只是显示事实。

import { useCallback, useEffect, useReducer, useRef } from "react";
import { Call } from "@wailsio/runtime";
import { toCallError } from "../../app/useBackend";
import type { CallError } from "../../types";

const FQN = "my-agent-go/internal/app.Desktop";

/** 与 policy.ApprovalChoice 的浏览器侧取值一致（T1 的 ResolveBrowserApproval）。 */
export type BrowserApprovalChoice = "once" | "session_scope" | "deny";

/**
 * 授权指纹里展示需要的字段（只读视图）。
 *
 * Go 侧 BrowserApprovalView.Arguments 是 json.RawMessage，跨到 JS 就是一个对象；
 * 字段与 harnessmcp.browserCallArguments 对齐。**这里不重算指纹**：
 * 判定用的指纹在后端，前端只做摘要展示。
 */
export interface BrowserApprovalArguments {
  tabId?: string;
  kind?: string;
  snapshotId?: string;
  ref?: string;
  key?: string;
  text?: string;
  origin?: string;
  url?: string;
  navigationId?: string;
}

/** 一条待人工答复的浏览器写动作（Go 侧 BrowserApprovalView 的镜像）。 */
export interface BrowserApprovalItem {
  approvalId: string;
  sessionId: string;
  runId: string;
  toolName: string;
  tabId: string;
  kind: string;
  /** 用户批的是"这个站点"的这次动作：origin 与 navigationId 来自授权指纹。 */
  origin: string;
  url: string;
  navigationId: string;
  arguments?: BrowserApprovalArguments | null;
  expiresAtUnix: number;
}

/** 绑定通道的形状（测试注入替身，不 mock Wails runtime）。 */
export interface BrowserApprovalBackend {
  pending(): Promise<BrowserApprovalItem[] | null | undefined>;
  /** 返回结构化错误（null = 已答复）。绑定层不再用裸 error，见 Go 侧注释。 */
  resolve(approvalId: string, choice: BrowserApprovalChoice): Promise<CallError | null | undefined>;
}

export const browserApprovalApi: BrowserApprovalBackend = {
  pending: () => Call.ByName(`${FQN}.PendingBrowserApprovals`),
  resolve: (approvalId, choice) =>
    Call.ByName(`${FQN}.ResolveBrowserApproval`, approvalId, choice),
};

/** 一条"卡片已经消失"的原因。 */
export type BrowserApprovalNoteKind = "approved" | "denied" | "expired" | "revoked";

export interface BrowserApprovalNote {
  approvalId: string;
  kind: BrowserApprovalNoteKind;
  toolName: string;
  origin: string;
  choice?: BrowserApprovalChoice;
}

/**
 * 归约器状态。
 *
 * 导出是为了能像 runState 一样被**直接**断言："提交之后 items 不变"这类语义
 * 靠组件测试去卡时序是不稳的（一次轮询就能把乐观移除掩盖掉）。
 */
export interface BrowserApprovalState {
  /** 最近一次后端读取结果（事实，不是本地推断）。 */
  items: BrowserApprovalItem[];
  /** 已提交的结论（只表示"请求已提交"，不是放行）。 */
  submitted: Record<string, BrowserApprovalChoice>;
  /** 提交失败的原因（显示在卡片上，卡片不因此消失）。 */
  errors: Record<string, CallError>;
  /** 最近几条"为什么没了"。 */
  notes: BrowserApprovalNote[];
  /** 最近一次接受的读取序号：迟到的旧结果不许复活已移除的卡片。 */
  seq: number;
  /** 最近一次读取失败（读失败 ≠ 没有待审批）。 */
  hint: CallError | null;
}

export type BrowserApprovalAction =
  | { type: "observed"; seq: number; nowMs: number; items: BrowserApprovalItem[] }
  | { type: "poll_failed"; seq: number; error: CallError }
  | { type: "submitted"; approvalId: string; choice: BrowserApprovalChoice }
  | { type: "resolve_failed"; approvalId: string; error: CallError };

const NOTE_LIMIT = 4;

export function initialBrowserApprovalState(): BrowserApprovalState {
  return { items: [], submitted: {}, errors: {}, notes: [], seq: 0, hint: null };
}

function noteFor(
  item: BrowserApprovalItem,
  choice: BrowserApprovalChoice | undefined,
  nowMs: number
): BrowserApprovalNote {
  if (choice) {
    const kind: BrowserApprovalNoteKind = choice === "deny" ? "denied" : "approved";
    return { approvalId: item.approvalId, kind, toolName: item.toolName, origin: item.origin, choice };
  }
  // 没提交就消失：到期时间已过就是过期，否则是撤销（接管/会话关闭/后端清理）。
  const expired = item.expiresAtUnix > 0 && nowMs / 1000 >= item.expiresAtUnix;
  return {
    approvalId: item.approvalId,
    kind: expired ? "expired" : "revoked",
    toolName: item.toolName,
    origin: item.origin,
  };
}

export function reduceBrowserApprovals(
  state: BrowserApprovalState,
  action: BrowserApprovalAction
): BrowserApprovalState {
  switch (action.type) {
    case "observed": {
      // 迟到的旧读取：丢弃。否则一张已经被判定移除的卡片会被旧结果复活。
      if (action.seq <= state.seq) return state;
      const present = new Set(action.items.map((item) => item.approvalId));
      const notes = [...state.notes];
      const submitted = { ...state.submitted };
      const errors = { ...state.errors };
      for (const prev of state.items) {
        if (present.has(prev.approvalId)) continue;
        notes.push(noteFor(prev, submitted[prev.approvalId], action.nowMs));
        delete submitted[prev.approvalId];
        delete errors[prev.approvalId];
      }
      return {
        ...state,
        seq: action.seq,
        items: action.items,
        submitted,
        errors,
        notes: notes.slice(-NOTE_LIMIT),
        hint: null,
      };
    }
    case "poll_failed":
      if (action.seq <= state.seq) return state;
      // 读失败**不**清空列表：我们不知道现在还有没有待审批。
      return { ...state, seq: action.seq, hint: action.error };
    case "submitted": {
      const errors = { ...state.errors };
      delete errors[action.approvalId];
      return {
        ...state,
        submitted: { ...state.submitted, [action.approvalId]: action.choice },
        errors,
      };
    }
    case "resolve_failed": {
      // 提交失败：撤掉"已提交"标记（用户要能重试），原因留在卡片上。
      const submitted = { ...state.submitted };
      delete submitted[action.approvalId];
      return {
        ...state,
        submitted,
        errors: { ...state.errors, [action.approvalId]: action.error },
      };
    }
    default:
      return state;
  }
}

export interface UseBrowserApprovalsOptions {
  api?: BrowserApprovalBackend;
  /** false 时不轮询（例如这一页不属于当前 run）。 */
  enabled?: boolean;
  /**
   * 轮询间隔（毫秒）。<=0 时只做挂载时的**一次**读取（测试用），
   * 之后靠 refresh 手动驱动。
   */
  pollIntervalMs?: number;
}

export interface BrowserApprovalHandle {
  items: BrowserApprovalItem[];
  submitted: Record<string, BrowserApprovalChoice>;
  errors: Record<string, CallError>;
  notes: BrowserApprovalNote[];
  hint: CallError | null;
  decide(item: BrowserApprovalItem, choice: BrowserApprovalChoice): void;
  refresh(): void;
}

const DEFAULT_POLL_MS = 1000;

function normalizeItems(list: BrowserApprovalItem[] | null | undefined): BrowserApprovalItem[] {
  if (!Array.isArray(list)) return [];
  return list.filter((item) => typeof item?.approvalId === "string" && item.approvalId !== "");
}

export function useBrowserApprovals(options: UseBrowserApprovalsOptions = {}): BrowserApprovalHandle {
  const api = options.api ?? browserApprovalApi;
  const enabled = options.enabled ?? true;
  const pollMs = options.pollIntervalMs ?? DEFAULT_POLL_MS;
  const [state, dispatch] = useReducer(reduceBrowserApprovals, initialBrowserApprovalState());

  // 一次只允许一个读取在飞：两个读取乱序返回会让卡片提前消失或复活。
  const inFlight = useRef(false);
  const seq = useRef(0);

  const refresh = useCallback(() => {
    if (inFlight.current) return;
    inFlight.current = true;
    const next = ++seq.current;
    const nowMs = Date.now();
    void api.pending().then(
      (list) => {
        inFlight.current = false;
        dispatch({ type: "observed", seq: next, nowMs, items: normalizeItems(list) });
      },
      (err: unknown) => {
        inFlight.current = false;
        dispatch({ type: "poll_failed", seq: next, error: toCallError(err) });
      }
    );
  }, [api]);

  useEffect(() => {
    if (!enabled) return;
    // 首次读取总是做：pollMs<=0 只是"不要定时轮询"（测试手动/单次驱动），
    // 不是"连当前状态都不要看"。
    refresh();
    if (pollMs <= 0) return;

    let timer: ReturnType<typeof setInterval> | null = null;
    const stop = () => {
      if (timer !== null) {
        clearInterval(timer);
        timer = null;
      }
    };
    const start = () => {
      if (timer === null) timer = setInterval(refresh, pollMs);
    };

    // 页面在后台时暂停轮询：隐藏的窗口里没人会看卡片，每秒一次跨进程调用只是
    // 白烧 CPU。回到前台时立刻补一次，不用等满一个周期才知道结论。
    // 挂载时的那一次读取不受影响：这一页有没有待审批，总得先知道。
    const doc = typeof document === "undefined" ? null : document;
    const onVisibility = () => {
      if (!doc) return;
      if (doc.hidden) {
        stop();
        return;
      }
      refresh();
      start();
    };
    if (!doc || !doc.hidden) start();
    doc?.addEventListener("visibilitychange", onVisibility);

    return () => {
      stop();
      doc?.removeEventListener("visibilitychange", onVisibility);
    };
  }, [enabled, pollMs, refresh]);

  const decide = useCallback(
    (item: BrowserApprovalItem, choice: BrowserApprovalChoice) => {
      // 与工具审批卡同一口径：点击后**立刻**标"已提交"（这只表示请求已发出），
      // 卡片本身不移动。先标再发请求，也让"提交后排队的读取结果"能认出结论来源。
      dispatch({ type: "submitted", approvalId: item.approvalId, choice });
      void api.resolve(item.approvalId, choice).then(
        (failure) => {
          if (failure) {
            // 结构化失败：可能是"已过期/已撤销/参数变了/根本没这条"。
            dispatch({ type: "resolve_failed", approvalId: item.approvalId, error: failure });
          }
          // 移除仍然只能来自后端读取结果（见 observed 分支）。
          refresh();
        },
        (err: unknown) => {
          // 绑定调用本身失败（Wails runtime 层）：同样保留原因并重读。
          dispatch({ type: "resolve_failed", approvalId: item.approvalId, error: toCallError(err) });
          refresh();
        }
      );
    },
    [api, refresh]
  );

  return {
    items: state.items,
    submitted: state.submitted,
    errors: state.errors,
    notes: state.notes,
    hint: state.hint,
    decide,
    refresh,
  };
}

/**
 * 动作摘要：给用户看"这一批到底要做什么"，不重算指纹。
 *
 * 刻意**不含** fill 的文本内容：摘要出现在卡片首行，容易进截图/日志；
 * 完整参数在卡片下方的参数块里（用户展开就能看到原文）。
 */
export function actionSummary(item: BrowserApprovalItem): string {
  const args = item.arguments ?? {};
  const kind = args.kind || item.kind;
  const target = args.ref ? `ref=${args.ref}` : args.key ? `key=${args.key}` : "";
  const parts = [kind, target, item.origin, item.navigationId ? `nav=${item.navigationId}` : ""];
  return parts.filter((part) => part !== "").join(" · ");
}
