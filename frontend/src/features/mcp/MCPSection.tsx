// MCP 服务管理（C04）。
//
// 这一块要防的偏差是"看着像连上了"：
//   - 列表里显示的是**配置**，连接状态只有诊断能回答；没诊断就写"未检查"；
//   - 需要 OAuth 的服务显示"需鉴权 / V1 未支持"，绝不显示已连接；
//   - 禁用后状态里带 pending：配置已变，运行中的旧 run 仍用旧 mcp.json，
//     下一次安全点（ensureRuntime 重建）才生效。
//
// 诊断按钮会启动一个短命的 Pi 进程（`pi mcp list --json`）：连接由 Pi 拥有，
// Go 侧没有第二个 MCP client。这句话也展示在界面上，避免用户以为代价是零。

import { useCallback, useEffect, useMemo, useState } from "react";
import { Plug, RefreshCw, ShieldQuestion } from "lucide-react";
import { api } from "../../api";
import { toCallError } from "../../app/useBackend";
import type {
  CallError,
  MCPDefinition,
  MCPExposure,
  MCPListResult,
  MCPStatus,
  MCPTransport,
} from "../../types";
import { EmptyState, ErrorState, LoadingState } from "../../components/StateViews";

interface Draft {
  id: string;
  transport: MCPTransport;
  command: string;
  args: string;
  workingDir: string;
  url: string;
  exposure: MCPExposure;
  envRefs: string;
  headerRefs: string;
}

function emptyDraft(): Draft {
  return {
    id: "",
    transport: "stdio",
    command: "",
    args: "",
    workingDir: "",
    url: "",
    exposure: "direct",
    envRefs: "",
    headerRefs: "",
  };
}

function draftFrom(def: MCPDefinition): Draft {
  return {
    id: def.id,
    transport: def.transport,
    command: def.command ?? "",
    args: (def.args ?? []).join("\n"),
    workingDir: def.workingDir ?? "",
    url: def.url ?? "",
    exposure: def.exposure,
    envRefs: pairsToText(def.envRefs),
    headerRefs: pairsToText(def.headerRefs),
  };
}

function pairsToText(map?: Record<string, string> | null): string {
  if (!map) return "";
  return Object.keys(map)
    .sort()
    .map((k) => `${k}=${map[k]}`)
    .join("\n");
}

/** 把 `NAME=ref` 行解析成对象；格式错误直接抛给调用方，不静默丢弃。 */
function textToPairs(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const rawLine of text.split("\n")) {
    const line = rawLine.trim();
    if (!line) continue;
    const idx = line.indexOf("=");
    if (idx <= 0 || idx === line.length - 1) {
      throw new Error(`引用行必须写成 NAME=ref：${line}`);
    }
    out[line.slice(0, idx).trim()] = line.slice(idx + 1).trim();
  }
  return out;
}

const STATE_LABEL: Record<string, string> = {
  connected: "已连接",
  failed: "连接失败",
  timeout: "连接超时",
  auth_required: "需要鉴权",
  disabled: "已禁用",
  unavailable: "配置不完整",
};

const STATE_TONE: Record<string, "emerald" | "slate" | "rose" | "amber"> = {
  connected: "emerald",
  failed: "rose",
  timeout: "amber",
  auth_required: "amber",
  disabled: "slate",
  unavailable: "rose",
};

export function MCPSection() {
  const [data, setData] = useState<MCPListResult | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<CallError | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [confirming, setConfirming] = useState<string | null>(null);
  const [statuses, setStatuses] = useState<Record<string, MCPStatus>>({});
  const [credRef, setCredRef] = useState("");
  const [credSecret, setCredSecret] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await api.mcpServers();
      if (res.error) {
        setError(res.error);
        setData(null);
      } else {
        setData(res);
      }
    } catch (e) {
      setError(toCallError(e));
      setData(null);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const items = data?.items ?? [];
  const revision = data?.revision ?? 0;

  const pendingNote = useMemo(() => {
    if (!data?.pending) return null;
    return data.activeRun
      ? "MCP 改动已保存。当前还有 run 在跑，它仍使用旧的 mcp.json；结束后的下一次运行生效。"
      : "MCP 改动已保存，下一次运行生效（重建运行时后会移除已禁用的服务）。";
  }, [data]);

  async function toggle(def: MCPDefinition) {
    setError(null);
    setNotice(null);
    setBusy(def.id);
    try {
      const res = await api.setMCPEnabled(def.id, revision, !def.enabled);
      if (res.error) {
        setError(res.error);
        return;
      }
      await load();
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusy(null);
    }
  }

  async function changeExposure(def: MCPDefinition, exposure: MCPExposure) {
    setError(null);
    setNotice(null);
    setBusy(def.id);
    try {
      const res = await api.saveMCP(revision, { ...def, exposure });
      if (res.error) {
        setError(res.error);
        return;
      }
      setNotice(`已更新 ${def.id} 的暴露策略为 ${exposure}。`);
      await load();
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusy(null);
    }
  }

  async function remove(id: string) {
    setError(null);
    setBusy(id);
    try {
      const res = await api.deleteMCP(revision, id);
      if (res.error) {
        setError(res.error);
        return;
      }
      setConfirming(null);
      await load();
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusy(null);
    }
  }

  async function save() {
    if (!draft) return;
    setError(null);
    setNotice(null);
    if (!draft.id.trim()) {
      setError({ code: "UNSUPPORTED", message: "请填写服务 ID（会成为 mcp.json 的键名）", retryable: false });
      return;
    }
    let envRefs: Record<string, string>;
    let headerRefs: Record<string, string>;
    try {
      envRefs = textToPairs(draft.envRefs);
      headerRefs = textToPairs(draft.headerRefs);
    } catch (e) {
      setError(toCallError(e));
      return;
    }
    // 编辑时保留原有的启用状态：改参数/暴露策略不应顺手把禁用的服务打开。
    const existing = items.find((it) => it.id === draft.id.trim());
    const def: MCPDefinition = {
      id: draft.id.trim(),
      transport: draft.transport,
      command: draft.transport === "stdio" ? draft.command.trim() : "",
      args:
        draft.transport === "stdio"
          ? draft.args
              .split("\n")
              .map((v) => v.trim())
              .filter((v) => v.length > 0)
          : null,
      workingDir: draft.transport === "stdio" ? draft.workingDir.trim() : "",
      url: draft.transport === "http" ? draft.url.trim() : "",
      envRefs,
      headerRefs,
      enabled: existing ? existing.enabled : true,
      exposure: draft.exposure,
    };
    setBusy("save");
    try {
      const res = await api.saveMCP(editingId ? revision : 0, def);
      if (res.error) {
        setError(res.error);
        return;
      }
      setDraft(null);
      setEditingId(null);
      setNotice(`已保存 ${def.id}。保存不会执行 command，也不会连接 url。`);
      await load();
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusy(null);
    }
  }

  async function diagnose(id: string) {
    setError(null);
    setBusy("diagnose-" + id);
    try {
      const res = await api.diagnoseMCP(id);
      if (res.error) {
        setError(res.error);
        return;
      }
      setStatuses((prev) => ({ ...prev, [id]: res.status }));
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusy(null);
    }
  }

  async function diagnoseAll() {
    setError(null);
    setBusy("diagnose-all");
    try {
      const res = await api.diagnoseAllMCP();
      if (res.error) {
        setError(res.error);
        return;
      }
      const next: Record<string, MCPStatus> = {};
      for (const st of res.items ?? []) next[st.id] = st;
      setStatuses(next);
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusy(null);
    }
  }

  async function saveCredential() {
    setError(null);
    setNotice(null);
    if (!credRef.trim() || !credSecret.trim()) {
      setNotice("请填写凭证引用名与值；值只写入 OS 凭证库，不会回显。");
      return;
    }
    setBusy("credential");
    try {
      const res = await api.saveMCPCredential(credRef.trim(), credSecret);
      if (res.error) {
        setError(res.error);
        return;
      }
      setCredSecret("");
      setNotice(`已保存凭证 ${res.ref}（值不会回显，也不会写进 mcp.json）。`);
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <section>
      <header className="mb-2 flex items-start justify-between gap-2">
        <div>
          <h2 className="text-sm font-medium text-[color:var(--anvil-foreground)]">MCP 服务</h2>
          <p className="mt-0.5 text-[11px] text-[color:var(--anvil-muted)]">
            外部 MCP 由 Pi 连接（stdio / Streamable HTTP）。配置里只保存 command 与参数
            或 url，以及凭证引用；禁用会把服务从隔离 agentDir 的 mcp.json 里移除，
            下一次运行生效。
          </p>
        </div>
        <div className="flex shrink-0 gap-1">
          <button
            onClick={() => {
              setDraft(emptyDraft());
              setEditingId(null);
            }}
            className="rounded border border-[color:var(--anvil-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
          >
            新建
          </button>
          <button
            onClick={() => void diagnoseAll()}
            disabled={busy === "diagnose-all" || items.length === 0 || !data?.diagnoseReady}
            className="flex items-center gap-1 rounded border border-[color:var(--anvil-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
          >
            <RefreshCw className="h-3 w-3" aria-hidden /> 诊断全部
          </button>
        </div>
      </header>

      {pendingNote && (
        <p
          role="status"
          className="mb-2 rounded border border-[color:var(--anvil-warning-border)] bg-[color:var(--anvil-warning-surface)] px-2 py-1.5 text-[11px] text-[color:var(--anvil-warning)]"
        >
          {pendingNote}
        </p>
      )}
      {data && !data.diagnoseReady && !loading && (
        <p className="mb-2 rounded border border-[color:var(--anvil-warning-border)] bg-[color:var(--anvil-warning-surface)] px-2 py-1.5 text-[11px] text-[color:var(--anvil-warning)]">
          未配置可用的 Pi，诊断不可用；状态不会显示成"已连接"。
        </p>
      )}

      {error && (
        <div className="mb-2">
          <ErrorState error={error} onRetry={() => void load()} title="MCP 操作失败" />
        </div>
      )}
      {notice && !error && (
        <p role="status" className="mb-2 text-[11px] text-[color:var(--anvil-muted)]">
          {notice}
        </p>
      )}

      <div className="mb-3 rounded-lg border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] p-2">
        <div className="mb-1 flex items-center gap-1 text-[10px] text-[color:var(--anvil-muted)]">
          <ShieldQuestion className="h-3 w-3" aria-hidden /> 凭证只存 OS 凭证库；配置里只写 ref
        </div>
        <div className="flex flex-wrap gap-1.5">
          <input
            aria-label="凭证引用名"
            value={credRef}
            onChange={(e) => setCredRef(e.target.value)}
            placeholder="mcp-docs-token"
            className="min-w-0 flex-1 rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] placeholder:text-[color:var(--anvil-muted)]"
          />
          <input
            aria-label="凭证值"
            type="password"
            value={credSecret}
            onChange={(e) => setCredSecret(e.target.value)}
            placeholder="令牌原文（如 Bearer xyz）"
            className="min-w-0 flex-1 rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] placeholder:text-[color:var(--anvil-muted)]"
          />
          <button
            onClick={() => void saveCredential()}
            disabled={busy === "credential"}
            className="rounded border border-[color:var(--anvil-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-50"
          >
            保存凭证
          </button>
        </div>
      </div>

      {draft && (
        <MCPEditor
          draft={draft}
          editingId={editingId}
          busy={busy === "save"}
          onChange={setDraft}
          onSave={() => void save()}
          onCancel={() => {
            setDraft(null);
            setEditingId(null);
          }}
        />
      )}

      {loading && items.length === 0 ? (
        <LoadingState label="读取 MCP 配置…" />
      ) : items.length === 0 ? (
        !error && (
          <EmptyState
            title="还没有 MCP 服务"
            hint="stdio 需要可执行文件与参数分开填写；HTTP 需要 Streamable HTTP 端点（通常是 /mcp）。密钥请先保存为凭证，再在配置里引用。"
            icon={<Plug className="h-4 w-4" aria-hidden />}
          />
        )
      ) : (
        <ul className="space-y-1">
          {items.map((def) => {
            const st = statuses[def.id];
            const running = (data?.runtimeServers ?? []).includes(def.id);
            return (
              <li key={def.id} className="rounded-md border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-2.5 py-2">
                <div className="flex items-start gap-2">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <span className="truncate text-[12px] text-[color:var(--anvil-foreground)]">{def.id}</span>
                      <Badge tone="slate">{def.transport}</Badge>
                      <Badge tone={def.enabled ? "emerald" : "slate"}>
                        {def.enabled ? "已启用" : "已禁用"}
                      </Badge>
                      <Badge tone="slate">{`暴露 ${def.exposure}`}</Badge>
                      {data?.pending && def.enabled && !running && (
                        <Badge tone="amber">下次运行生效</Badge>
                      )}
                      {data?.pending && !def.enabled && running && (
                        <Badge tone="amber">运行中仍在使用</Badge>
                      )}
                      {st && (
                        <Badge tone={STATE_TONE[st.state] ?? "slate"}>
                          {STATE_LABEL[st.state] ?? st.state}
                        </Badge>
                      )}
                      {!st && <Badge tone="slate">未检查</Badge>}
                    </div>
                    <div className="truncate text-[10px] text-[color:var(--anvil-muted)]">
                      {def.transport === "stdio"
                        ? [def.command, ...(def.args ?? [])].join(" ")
                        : def.url || "（缺少 url）"}
                    </div>
                    {st && (
                      <div className="mt-0.5 text-[10px] text-[color:var(--anvil-muted)]">
                        {st.state === "connected"
                          ? `工具 ${st.tools?.length ?? 0} 个${st.tools && st.tools.length > 0 ? "：" + st.tools.map((t) => t.name).join(", ") : ""}`
                          : st.error?.message || st.detail || ""}
                      </div>
                    )}
                    {st?.state === "auth_required" && (
                      <div className="mt-0.5 text-[10px] text-[color:var(--anvil-warning)]">
                        V1 未支持 MCP OAuth 登录；请把凭证保存为 ref 并通过 headerRefs 引用，
                        或在外部客户端完成授权。这里不会显示成已连接。
                      </div>
                    )}
                    {st && !st.oauthSupported && st.state === "connected" && (
                      <div className="mt-0.5 text-[10px] text-[color:var(--anvil-muted)]">
                        连接由 Pi 管理；诊断结果不含 inputSchema。
                      </div>
                    )}
                  </div>
                  <div className="flex shrink-0 flex-wrap items-center justify-end gap-1">
                    <select
                      aria-label={`暴露策略 ${def.id}`}
                      value={def.exposure}
                      onChange={(e) => void changeExposure(def, e.target.value as MCPExposure)}
                      disabled={busy === def.id}
                      className="rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-1 py-0.5 text-[11px] text-[color:var(--anvil-foreground)]"
                    >
                      <option value="direct">direct</option>
                      <option value="deferred">deferred</option>
                      <option value="hidden">hidden</option>
                    </select>
                    <button
                      onClick={() => void diagnose(def.id)}
                      disabled={busy === "diagnose-" + def.id || !data?.diagnoseReady}
                      className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-40"
                    >
                      诊断
                    </button>
                    <button
                      onClick={() => {
                        setDraft(draftFrom(def));
                        setEditingId(def.id);
                      }}
                      className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)]"
                    >
                      编辑
                    </button>
                    <button
                      onClick={() => void toggle(def)}
                      disabled={busy === def.id}
                      className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-40"
                    >
                      {def.enabled ? "禁用" : "启用"}
                    </button>
                    {confirming === def.id ? (
                      <>
                        <button
                          onClick={() => void remove(def.id)}
                          disabled={busy === def.id}
                          className="rounded border border-[color:var(--anvil-danger-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-danger)] hover:bg-[color:var(--anvil-danger-surface)] disabled:opacity-40"
                        >
                          确认删除
                        </button>
                        <button
                          onClick={() => setConfirming(null)}
                          className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)]"
                        >
                          取消
                        </button>
                      </>
                    ) : (
                      <button
                        onClick={() => setConfirming(def.id)}
                        className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)]"
                      >
                        删除
                      </button>
                    )}
                  </div>
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

function MCPEditor({
  draft,
  editingId,
  busy,
  onChange,
  onSave,
  onCancel,
}: {
  draft: Draft;
  editingId: string | null;
  busy: boolean;
  onChange: (d: Draft) => void;
  onSave: () => void;
  onCancel: () => void;
}) {
  const input =
    "min-w-0 flex-1 rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] placeholder:text-[color:var(--anvil-muted)]";
  return (
    <div className="mb-3 rounded-lg border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] p-2">
      <div className="mb-1 text-[11px] text-[color:var(--anvil-muted)]">
        {editingId ? `编辑 ${editingId}` : "新建 MCP 服务"}
      </div>
      <div className="grid grid-cols-2 gap-1.5">
        <label className="flex flex-col gap-0.5 text-[10px] text-[color:var(--anvil-muted)]">
          服务 ID
          <input
            aria-label="MCP 服务 ID"
            value={draft.id}
            onChange={(e) => onChange({ ...draft, id: e.target.value })}
            disabled={editingId !== null}
            className={input}
          />
        </label>
        <label className="flex flex-col gap-0.5 text-[10px] text-[color:var(--anvil-muted)]">
          传输方式
          <select
            aria-label="传输方式"
            value={draft.transport}
            onChange={(e) => onChange({ ...draft, transport: e.target.value as MCPTransport })}
            className={input}
          >
            <option value="stdio">stdio</option>
            <option value="http">http（Streamable HTTP）</option>
          </select>
        </label>
        {draft.transport === "stdio" ? (
          <>
            <label className="flex flex-col gap-0.5 text-[10px] text-[color:var(--anvil-muted)]">
              可执行文件（不含参数）
              <input
                aria-label="命令"
                value={draft.command}
                onChange={(e) => onChange({ ...draft, command: e.target.value })}
                placeholder="npx"
                className={input}
              />
            </label>
            <label className="flex flex-col gap-0.5 text-[10px] text-[color:var(--anvil-muted)]">
              工作目录（可选，绝对路径）
              <input
                aria-label="工作目录"
                value={draft.workingDir}
                onChange={(e) => onChange({ ...draft, workingDir: e.target.value })}
                className={input}
              />
            </label>
            <label className="col-span-2 flex flex-col gap-0.5 text-[10px] text-[color:var(--anvil-muted)]">
              参数（每行一个；不要写 shell 命令）
              <textarea
                aria-label="参数"
                value={draft.args}
                onChange={(e) => onChange({ ...draft, args: e.target.value })}
                rows={3}
                className={input}
              />
            </label>
          </>
        ) : (
          <label className="col-span-2 flex flex-col gap-0.5 text-[10px] text-[color:var(--anvil-muted)]">
            URL（Streamable HTTP）
            <input
              aria-label="URL"
              value={draft.url}
              onChange={(e) => onChange({ ...draft, url: e.target.value })}
              placeholder="https://example.test/mcp"
              className={input}
            />
          </label>
        )}
        <label className="flex flex-col gap-0.5 text-[10px] text-[color:var(--anvil-muted)]">
          暴露策略
          <select
            aria-label="暴露策略"
            value={draft.exposure}
            onChange={(e) => onChange({ ...draft, exposure: e.target.value as MCPExposure })}
            className={input}
          >
            <option value="direct">direct（直接声明给模型）</option>
            <option value="deferred">deferred（经 tool_search）</option>
            <option value="hidden">hidden（不可达）</option>
          </select>
        </label>
        <div className="col-span-1" />
        <label className="flex flex-col gap-0.5 text-[10px] text-[color:var(--anvil-muted)]">
          环境变量引用（NAME=ref）
          <textarea
            aria-label="环境变量引用"
            value={draft.envRefs}
            onChange={(e) => onChange({ ...draft, envRefs: e.target.value })}
            rows={2}
            className={input}
          />
        </label>
        <label className="flex flex-col gap-0.5 text-[10px] text-[color:var(--anvil-muted)]">
          请求头引用（Name=ref）
          <textarea
            aria-label="请求头引用"
            value={draft.headerRefs}
            onChange={(e) => onChange({ ...draft, headerRefs: e.target.value })}
            rows={2}
            className={input}
          />
        </label>
      </div>
      <div className="mt-1.5 flex items-center gap-1.5">
        <button
          onClick={onSave}
          disabled={busy}
          className="rounded bg-[color:var(--anvil-accent)] px-2.5 py-1 text-[11px] font-medium text-[color:var(--anvil-background)] hover:bg-[color:var(--anvil-accent)] disabled:opacity-50"
        >
          保存 MCP
        </button>
        <button
          onClick={onCancel}
          className="rounded border border-[color:var(--anvil-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)]"
        >
          取消
        </button>
        <span className="text-[10px] text-[color:var(--anvil-muted)]">
          保存只做校验与写配置：不执行 command，不连接 url。
        </span>
      </div>
    </div>
  );
}

function Badge({ tone, children }: { tone: "emerald" | "slate" | "rose" | "amber"; children: string }) {
  const cls = {
    emerald: "bg-[color:var(--anvil-success-surface)] text-[color:var(--anvil-success)]",
    slate: "bg-[color:var(--anvil-surface)] text-[color:var(--anvil-muted)]",
    rose: "bg-[color:var(--anvil-danger-surface)] text-[color:var(--anvil-danger)]",
    amber: "bg-[color:var(--anvil-warning-surface)] text-[color:var(--anvil-warning)]",
  }[tone];
  return <span className={`shrink-0 rounded px-1 py-0.5 text-[9px] ${cls}`}>{children}</span>;
}
