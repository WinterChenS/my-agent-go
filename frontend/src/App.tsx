import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AlertTriangle,
  Check,
  ChevronDown,
  ChevronRight,
  CircleStop,
  Send,
  Settings as SettingsIcon,
  ShieldCheck,
  Terminal,
  X,
} from "lucide-react";
import { api, onRunEvent } from "./api";
import type {
  ApprovalChoice,
  ApprovalItem,
  ChatMessage,
  ProductEvent,
  RunState,
  Settings,
  Status,
  ToolCard,
} from "./types";

type Phase = RunState | "idle";

const PHASE_LABEL: Record<string, string> = {
  idle: "空闲",
  queued: "排队中",
  starting: "启动中",
  running: "运行中",
  waiting_approval: "等待授权",
  stopping: "停止中",
  completed: "已完成",
  failed: "失败",
  cancelled: "已取消",
  interrupted: "已中断",
};

const PHASE_COLOR: Record<string, string> = {
  idle: "bg-slate-500",
  queued: "bg-amber-400",
  starting: "bg-sky-400",
  running: "bg-emerald-400",
  waiting_approval: "bg-amber-400",
  stopping: "bg-orange-400",
  completed: "bg-emerald-400",
  failed: "bg-rose-400",
  cancelled: "bg-slate-400",
  interrupted: "bg-rose-400",
};

let seq = 0;
const nextId = () => `m${++seq}`;

function payloadOf<T>(ev: ProductEvent): T {
  return (ev.payload ?? {}) as T;
}

export default function App() {
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [input, setInput] = useState("");
  const [phase, setPhase] = useState<Phase>("idle");
  const [runId, setRunId] = useState<string | null>(null);
  const [approvals, setApprovals] = useState<ApprovalItem[]>([]);
  const [status, setStatus] = useState<Status | null>(null);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [showSettings, setShowSettings] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const activeRun = useRef<string | null>(null);
  const scrollRef = useRef<HTMLDivElement | null>(null);

  const busy = phase === "starting" || phase === "running" || phase === "waiting_approval" || phase === "stopping";

  useEffect(() => {
    api
      .status()
      .then(setStatus)
      .catch((e) => setError(String(e)));
    api
      .settings()
      .then(setSettings)
      .catch(() => undefined);
  }, []);

  // 事件订阅。所有 UI 变更都由事件驱动，而不是由调用返回值猜测——
  // 这样"后端已经失败但前端以为在跑"的假状态不会出现。
  useEffect(() => {
    const off = onRunEvent((ev) => {
      switch (ev.type) {
        case "run.state": {
          const p = payloadOf<{ state: RunState }>(ev);
          setPhase(p.state);
          if (p.state === "completed" || p.state === "failed" || p.state === "cancelled" || p.state === "interrupted") {
            setMessages((prev) =>
              prev.map((m) => (m.role === "assistant" ? { ...m, streaming: false } : m))
            );
            setApprovals([]);
          }
          break;
        }
        case "message.delta": {
          const p = payloadOf<{ text: string }>(ev);
          setMessages((prev) => {
            const idx = [...prev].reverse().findIndex((m) => m.role === "assistant" && m.streaming);
            if (idx === -1) {
              return [...prev, { id: nextId(), role: "assistant", text: p.text, tools: [], streaming: true }];
            }
            const at = prev.length - 1 - idx;
            const copy = [...prev];
            copy[at] = { ...copy[at], text: copy[at].text + p.text };
            return copy;
          });
          break;
        }
        case "tool.start": {
          const p = payloadOf<{ toolCallId: string; toolName: string; args: unknown }>(ev);
          const card: ToolCard = {
            toolCallId: p.toolCallId,
            toolName: p.toolName,
            args: JSON.stringify(p.args ?? {}, null, 2),
            running: true,
          };
          setMessages((prev) => {
            const idx = [...prev].reverse().findIndex((m) => m.role === "assistant");
            if (idx === -1) {
              return [...prev, { id: nextId(), role: "assistant", text: "", tools: [card], streaming: true }];
            }
            const at = prev.length - 1 - idx;
            const copy = [...prev];
            copy[at] = { ...copy[at], tools: [...copy[at].tools, card] };
            return copy;
          });
          break;
        }
        case "tool.end": {
          const p = payloadOf<{ toolCallId: string; text: string; isError: boolean; exitCode: number | null }>(ev);
          setMessages((prev) =>
            prev.map((m) => ({
              ...m,
              tools: m.tools.map((t) =>
                t.toolCallId === p.toolCallId
                  ? { ...t, result: p.text, isError: p.isError, running: false }
                  : t
              ),
            }))
          );
          break;
        }
        case "approval.request": {
          const p = payloadOf<{
            approvalId: string;
            toolName: string;
            toolCallId: string;
            arguments: unknown;
            expiresAtUnix: number;
          }>(ev);
          setApprovals((prev) => [
            ...prev,
            {
              approvalId: p.approvalId,
              toolName: p.toolName,
              toolCallId: p.toolCallId,
              args: JSON.stringify(p.arguments ?? {}, null, 2),
              expiresAtUnix: p.expiresAtUnix,
            },
          ]);
          break;
        }
        case "approval.resolved": {
          const p = payloadOf<{ approvalId: string }>(ev);
          setApprovals((prev) => prev.filter((a) => a.approvalId !== p.approvalId));
          break;
        }
        case "run.error": {
          const p = payloadOf<{ code: string; message: string }>(ev);
          setError(`${p.code}: ${p.message}`);
          break;
        }
        case "diagnostic": {
          const p = payloadOf<{ kind: string; message: string }>(ev);
          setError(`诊断 ${p.kind}: ${p.message}`);
          break;
        }
        default:
          break;
      }
    });
    return off;
  }, []);

  useEffect(() => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [messages, approvals]);

  const send = useCallback(async () => {
    const text = input.trim();
    if (!text || busy) return;
    setError(null);
    setInput("");
    setApprovals([]);
    setMessages((prev) => [
      ...prev,
      { id: nextId(), role: "user", text, tools: [], streaming: false },
      { id: nextId(), role: "assistant", text: "", tools: [], streaming: true },
    ]);
    try {
      const snap = await api.start(text);
      activeRun.current = snap.runId;
      setRunId(snap.runId);
      setPhase(snap.state);
    } catch (e) {
      setError(String(e));
      setPhase("failed");
      setMessages((prev) =>
        prev.map((m) => (m.role === "assistant" ? { ...m, streaming: false } : m))
      );
    }
  }, [input, busy]);

  const stop = useCallback(async () => {
    if (!runId) return;
    try {
      await api.abort(runId);
    } catch (e) {
      setError(String(e));
    }
  }, [runId]);

  const decide = useCallback(
    async (item: ApprovalItem, choice: ApprovalChoice) => {
      if (!runId) return;
      try {
        await api.resolveApproval(runId, item.approvalId, choice);
        setApprovals((prev) => prev.filter((a) => a.approvalId !== item.approvalId));
      } catch (e) {
        setError(String(e));
      }
    },
    [runId]
  );

  const saveSettings = useCallback(async () => {
    if (!settings) return;
    setSaving(true);
    try {
      await api.saveSettings(settings);
      const st = await api.status();
      setStatus(st);
      setShowSettings(false);
      setError(null);
    } catch (e) {
      setError(String(e));
    } finally {
      setSaving(false);
    }
  }, [settings]);

  const isolation = useMemo(() => {
    const flags = status?.isolation?.split(" ").filter(Boolean) ?? [];
    return flags;
  }, [status]);

  return (
    <div className="flex h-screen flex-col bg-[#0f1218] text-slate-200">
      <header className="flex h-12 shrink-0 items-center justify-between border-b border-white/10 px-4">
        <div className="flex items-center gap-2.5">
          <div className="flex h-6 w-6 items-center justify-center rounded-md bg-sky-500/15">
            <ShieldCheck className="h-4 w-4 text-sky-400" />
          </div>
          <span className="text-sm font-semibold tracking-wide">MyAgent</span>
          <span className="rounded border border-white/10 px-1.5 py-0.5 text-[10px] text-slate-400">
            {status?.platform ?? "—"}
          </span>
        </div>
        <div className="flex items-center gap-3 text-xs text-slate-400">
          <span className="flex items-center gap-1.5">
            <span className={`h-1.5 w-1.5 rounded-full ${PHASE_COLOR[phase] ?? "bg-slate-500"}`} />
            {PHASE_LABEL[phase] ?? phase}
          </span>
          <span className="hidden md:inline">
            {status?.provider ?? "—"} / {status?.model ?? "—"}
          </span>
          <button
            onClick={() => setShowSettings((v) => !v)}
            className="rounded p-1.5 hover:bg-white/5"
            title="设置"
          >
            <SettingsIcon className="h-4 w-4" />
          </button>
        </div>
      </header>

      {showSettings && settings && (
        <section className="shrink-0 border-b border-white/10 bg-[#131822] px-4 py-3">
          <div className="mx-auto grid max-w-4xl grid-cols-1 gap-2 md:grid-cols-3">
            <Field label="Pi 路径" value={settings.piPath} onChange={(v) => setSettings({ ...settings, piPath: v })} />
            <Field label="Node 路径" value={settings.nodePath} onChange={(v) => setSettings({ ...settings, nodePath: v })} />
            <Field label="Provider" value={settings.provider} onChange={(v) => setSettings({ ...settings, provider: v })} />
            <Field label="Base URL" value={settings.baseUrl} onChange={(v) => setSettings({ ...settings, baseUrl: v })} />
            <Field label="Model" value={settings.model} onChange={(v) => setSettings({ ...settings, model: v })} />
            <Field label="API Key" value={settings.apiKey} onChange={(v) => setSettings({ ...settings, apiKey: v })} />
            <Field label="工作目录" value={settings.workspace} onChange={(v) => setSettings({ ...settings, workspace: v })} />
            <label className="flex items-center gap-2 self-end text-xs text-slate-400">
              <input
                type="checkbox"
                checked={settings.shellAllowed}
                onChange={(e) => setSettings({ ...settings, shellAllowed: e.target.checked })}
                className="h-3.5 w-3.5 accent-sky-500"
              />
              允许 shell 工具
            </label>
            <button
              onClick={saveSettings}
              disabled={saving}
              className="self-end rounded-md bg-sky-500 px-3 py-2 text-xs font-medium text-white hover:bg-sky-400 disabled:opacity-50"
            >
              {saving ? "保存中…" : "保存设置"}
            </button>
          </div>
          <div className="mx-auto mt-2 max-w-4xl text-[11px] text-slate-500">
            隔离参数：{isolation.join(" ")}
          </div>
        </section>
      )}

      <main ref={scrollRef} className="min-h-0 flex-1 overflow-y-auto px-4 py-6">
        <div className="mx-auto flex max-w-3xl flex-col gap-4">
          {messages.length === 0 && (
            <div className="mt-16 text-center">
              <div className="mx-auto mb-3 flex h-10 w-10 items-center justify-center rounded-xl bg-white/5">
                <Terminal className="h-5 w-5 text-slate-400" />
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
                    <ToolView key={t.toolCallId} card={t} />
                  ))}
                  {m.streaming && !m.text && m.tools.length === 0 && (
                    <div className="flex items-center gap-1.5 text-xs text-slate-500">
                      <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-sky-400" />
                      正在思考…
                    </div>
                  )}
                </div>
              )}
            </article>
          ))}

          {approvals.map((a) => (
            <section key={a.approvalId} className="rounded-xl border border-amber-400/30 bg-amber-400/5 p-3.5">
              <div className="flex items-center gap-2 text-sm font-medium text-amber-200">
                <AlertTriangle className="h-4 w-4" />
                需要授权：{a.toolName}
              </div>
              <pre className="mt-2 max-h-40 overflow-auto rounded-md bg-black/30 p-2.5 text-[11px] leading-relaxed text-slate-300">
                {a.args}
              </pre>
              <div className="mt-3 flex flex-wrap gap-2">
                <button
                  onClick={() => decide(a, "once")}
                  className="flex items-center gap-1.5 rounded-md bg-emerald-500 px-3 py-1.5 text-xs font-medium text-emerald-950 hover:bg-emerald-400"
                >
                  <Check className="h-3.5 w-3.5" /> 允许一次
                </button>
                <button
                  onClick={() => decide(a, "session")}
                  className="rounded-md border border-white/15 px-3 py-1.5 text-xs text-slate-200 hover:bg-white/5"
                >
                  本会话允许
                </button>
                <button
                  onClick={() => decide(a, "deny")}
                  className="flex items-center gap-1.5 rounded-md border border-rose-400/40 px-3 py-1.5 text-xs text-rose-300 hover:bg-rose-500/10"
                >
                  <X className="h-3.5 w-3.5" /> 拒绝
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
      </main>

      <footer className="shrink-0 border-t border-white/10 px-4 py-3">
        <div className="mx-auto flex max-w-3xl items-end gap-2">
          <textarea
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                void send();
              }
            }}
            rows={2}
            placeholder="输入指令，Enter 发送，Shift+Enter 换行"
            className="max-h-40 min-h-[46px] flex-1 resize-none rounded-lg border border-white/10 bg-[#161b24] px-3 py-2.5 text-sm text-slate-100 outline-none placeholder:text-slate-600 focus:border-sky-500/50"
          />
          {busy ? (
            <button
              onClick={stop}
              className="flex h-[46px] items-center gap-1.5 rounded-lg bg-rose-500/90 px-3.5 text-xs font-medium text-white hover:bg-rose-500"
            >
              <CircleStop className="h-4 w-4" /> 停止
            </button>
          ) : (
            <button
              onClick={send}
              disabled={!input.trim()}
              className="flex h-[46px] items-center gap-1.5 rounded-lg bg-sky-500 px-3.5 text-xs font-medium text-white hover:bg-sky-400 disabled:opacity-40"
            >
              <Send className="h-4 w-4" /> 发送
            </button>
          )}
        </div>
        <div className="mx-auto mt-1.5 flex max-w-3xl items-center justify-between text-[10px] text-slate-600">
          <span>agentDir: {status?.agentDir ?? "—"}</span>
          <span>{runId ? `run ${runId}` : "无活跃 run"}</span>
        </div>
      </footer>
    </div>
  );
}

function Field({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-[11px] text-slate-500">{label}</span>
      <input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="rounded-md border border-white/10 bg-[#0f1218] px-2 py-1.5 text-xs text-slate-200 outline-none focus:border-sky-500/50"
      />
    </label>
  );
}

function ToolView({ card }: { card: ToolCard }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="mt-2 overflow-hidden rounded-lg border border-white/10 bg-[#141922]">
      <button
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs text-slate-300 hover:bg-white/[0.03]"
      >
        {open ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
        <Terminal className="h-3.5 w-3.5 text-slate-500" />
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
              <pre className="mt-1 max-h-64 overflow-auto whitespace-pre-wrap text-[11px] text-slate-300">{card.result || "(空)"}</pre>
            </>
          )}
        </div>
      )}
    </div>
  );
}
