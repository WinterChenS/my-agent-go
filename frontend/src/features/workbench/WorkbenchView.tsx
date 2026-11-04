// 工作台：消息流 + 授权 + 输入框。
//
// 状态全部来自 runState 的纯归约，组件只做渲染与转发。
// 唯一在组件里判断的是"工作区是否就绪"——这不是运行状态，是前置条件，
// 拿不到它就发不出任务，所以要在输入之前就说清楚，而不是等报错。

import { useEffect, useRef, useState } from "react";
import {
  AlertTriangle,
  Check,
  ChevronDown,
  ChevronRight,
  CircleStop,
  Send,
  Terminal,
  X,
} from "lucide-react";
import type { UiState } from "../../runState";
import type { ApprovalChoice, ApprovalItem, ToolCard, Workspace } from "../../types";
import { NoPermissionState } from "../../components/StateViews";

export interface WorkbenchViewProps {
  ui: UiState;
  input: string;
  onInput: (v: string) => void;
  onSend: () => void;
  onStop: () => void;
  onDecide: (item: ApprovalItem, choice: ApprovalChoice) => void;
  busy: boolean;
  workspace: Workspace | null;
  onGoSettings: () => void;
}

export function WorkbenchView({
  ui,
  input,
  onInput,
  onSend,
  onStop,
  onDecide,
  busy,
  workspace,
  onGoSettings,
}: WorkbenchViewProps) {
  const { messages, approvals, error } = ui;
  const scrollRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [messages, approvals]);

  const blocked = !workspace || !workspace.trusted;

  return (
    <div className="flex h-full flex-col">
      <div ref={scrollRef} className="min-h-0 flex-1 overflow-y-auto px-4 py-5">
        <div className="mx-auto flex max-w-3xl flex-col gap-4">
          {blocked && (
            <NoPermissionState
              message={
                workspace
                  ? `工作区「${workspace.name}」尚未信任。信任表示允许 agent 在这个目录里执行命令和改文件。`
                  : "还没有选择工作区。先添加一个目录并信任它，agent 才知道在哪里干活。"
              }
              action={
                <button
                  onClick={onGoSettings}
                  className="rounded-md bg-amber-400 px-2.5 py-1 text-xs font-medium text-amber-950 hover:bg-amber-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-amber-300"
                >
                  去设置里处理
                </button>
              }
            />
          )}

          {messages.length === 0 && !blocked && (
            <div className="mt-14 text-center">
              <div className="mx-auto mb-3 flex h-10 w-10 items-center justify-center rounded-xl bg-white/5">
                <Terminal className="h-5 w-5 text-slate-400" aria-hidden />
              </div>
              <p className="text-sm text-slate-400">说点什么，让 agent 在工作目录里干活。</p>
              <p className="mt-1 text-xs text-slate-600">
                工具调用会在执行前请求授权；拒绝的话命令不会运行。
              </p>
            </div>
          )}

          {messages.map((m) => (
            <article key={m.id} className={m.role === "user" ? "flex justify-end" : "flex justify-start"}>
              {m.role === "user" ? (
                <div className="max-w-[85%] whitespace-pre-wrap rounded-xl rounded-br-sm bg-sky-500/15 px-3.5 py-2.5 text-sm text-sky-50">
                  {m.text}
                </div>
              ) : (
                <div className="w-full">
                  {m.text && (
                    <div className="whitespace-pre-wrap text-sm leading-relaxed text-slate-200">{m.text}</div>
                  )}
                  {m.tools.map((t) => (
                    <ToolView key={`${t.runId}:${t.toolCallId}`} card={t} />
                  ))}
                  {m.streaming && !m.text && m.tools.length === 0 && (
                    <div className="flex items-center gap-1.5 text-xs text-slate-500">
                      <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-sky-400" aria-hidden />
                      正在思考…
                    </div>
                  )}
                </div>
              )}
            </article>
          ))}

          {approvals.map((a) => (
            <section
              key={a.approvalId}
              className="rounded-xl border border-amber-400/30 bg-amber-400/5 p-3.5"
              aria-label={`需要授权：${a.toolName}`}
            >
              <div className="flex items-center gap-2 text-sm font-medium text-amber-200">
                <AlertTriangle className="h-4 w-4" aria-hidden />
                需要授权：{a.toolName}
              </div>
              <pre className="mt-2 max-h-40 overflow-auto rounded-md bg-black/30 p-2.5 text-[11px] leading-relaxed text-slate-300">
                {a.args}
              </pre>
              <div className="mt-3 flex flex-wrap gap-2">
                <button
                  onClick={() => onDecide(a, "once")}
                  className="flex items-center gap-1.5 rounded-md bg-emerald-500 px-3 py-1.5 text-xs font-medium text-emerald-950 hover:bg-emerald-400 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-300"
                >
                  <Check className="h-3.5 w-3.5" aria-hidden /> 允许一次
                </button>
                <button
                  onClick={() => onDecide(a, "session")}
                  className="rounded-md border border-white/15 px-3 py-1.5 text-xs text-slate-200 hover:bg-white/5 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-400"
                >
                  本会话允许
                </button>
                <button
                  onClick={() => onDecide(a, "deny")}
                  className="flex items-center gap-1.5 rounded-md border border-rose-400/40 px-3 py-1.5 text-xs text-rose-300 hover:bg-rose-500/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-rose-400"
                >
                  <X className="h-3.5 w-3.5" aria-hidden /> 拒绝
                </button>
              </div>
            </section>
          ))}

          {error && (
            <div className="rounded-lg border border-rose-400/30 bg-rose-500/5 px-3 py-2 text-xs text-rose-300">
              {error}
            </div>
          )}
        </div>
      </div>

      <div className="shrink-0 border-t border-white/10 px-4 py-2.5">
        <div className="mx-auto flex max-w-3xl items-end gap-2">
          <textarea
            value={input}
            onChange={(e) => onInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                onSend();
              }
            }}
            rows={2}
            aria-label="输入指令"
            placeholder="输入指令，Enter 发送，Shift+Enter 换行"
            className="max-h-40 min-h-[44px] flex-1 resize-none rounded-lg border border-white/10 bg-[#161b24] px-3 py-2.5 text-sm text-slate-100 outline-none placeholder:text-slate-600 focus:border-sky-500/50"
          />
          {busy ? (
            <button
              onClick={onStop}
              className="flex h-[44px] items-center gap-1.5 rounded-lg bg-rose-500/90 px-3.5 text-xs font-medium text-white hover:bg-rose-500 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-rose-300"
            >
              <CircleStop className="h-4 w-4" aria-hidden /> 停止
            </button>
          ) : (
            <button
              onClick={onSend}
              disabled={!input.trim()}
              className="flex h-[44px] items-center gap-1.5 rounded-lg bg-sky-500 px-3.5 text-xs font-medium text-white hover:bg-sky-400 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-300 disabled:opacity-40"
            >
              <Send className="h-4 w-4" aria-hidden /> 发送
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

function ToolView({ card }: { card: ToolCard }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="mt-2 overflow-hidden rounded-lg border border-white/10 bg-[#141922]">
      <button
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs text-slate-300 hover:bg-white/[0.03] focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-sky-400"
      >
        {open ? (
          <ChevronDown className="h-3.5 w-3.5" aria-hidden />
        ) : (
          <ChevronRight className="h-3.5 w-3.5" aria-hidden />
        )}
        <Terminal className="h-3.5 w-3.5 text-slate-500" aria-hidden />
        <span className="font-medium">{card.toolName}</span>
        {card.running ? (
          <span className="ml-auto text-[10px] text-sky-400">执行中…</span>
        ) : card.isError ? (
          <span className="ml-auto text-[10px] text-rose-400">失败</span>
        ) : (
          <span className="ml-auto text-[10px] text-emerald-400">完成</span>
        )}
      </button>
      {open && (
        <div className="border-t border-white/5 px-3 py-2">
          <div className="text-[10px] uppercase tracking-wide text-slate-600">参数</div>
          <pre className="mt-1 max-h-48 overflow-auto whitespace-pre-wrap text-[11px] text-slate-400">{card.args}</pre>
          {card.result !== undefined && (
            <>
              <div className="mt-2 text-[10px] uppercase tracking-wide text-slate-600">结果</div>
              <pre className="mt-1 max-h-64 overflow-auto whitespace-pre-wrap text-[11px] text-slate-300">
                {card.result || "(空)"}
              </pre>
            </>
          )}
        </div>
      )}
    </div>
  );
}
