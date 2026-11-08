// MCP 页面（U04 / MCP02 / MCP03 / MCP05、CONN07 的外壳部分）：MCP 服务 + 工具目录。
//
// 这一块要防的偏差是"看着像连上了"：
//   - 列表里显示的是**配置**，连接状态只有诊断能回答；没诊断就写"未检查"；
//   - 需要 OAuth 的服务显示"需鉴权 / V1 未支持"，绝不显示已连接；
//   - 禁用后状态里带 pending：配置已变，运行中的旧 run 仍用旧 mcp.json，
//     下一次安全点（ensureRuntime 重建）才生效。
//
// 诊断按钮会启动一个短命的 Pi 进程（`pi mcp list --json`）：连接由 Pi 拥有，
// Go 侧没有第二个 MCP client。这句话也展示在界面上，避免用户以为代价是零。
//
// 文案全部来自 catalog（`mcp.*` / `tools.*`），颜色全部来自语义 token。

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
import { t } from "../../i18n";
import { ToolsSection } from "../tools/ToolsSection";

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
      // 这里只把原始行抛给调用方：文案在 catalog 里（`mcp.errors.refLine`），
      // 不把中文措辞写进解析函数。
      throw new Error(line);
    }
    out[line.slice(0, idx).trim()] = line.slice(idx + 1).trim();
  }
  return out;
}

/** 暴露策略是 API 关键字，不是文案：界面上按原值显示，不翻译、不手抄第二份。 */
const EXPOSURES: readonly MCPExposure[] = ["direct", "deferred", "hidden"];

/** 传输方式同理：stdio / http 是协议名。 */
const TRANSPORTS: ReadonlyArray<{ value: MCPTransport; labelKey: string }> = [
  { value: "stdio", labelKey: "mcp.editor.transportStdioOption" },
  { value: "http", labelKey: "mcp.editor.transportHttpOption" },
];

/** 诊断状态的文案 key；未知状态原样显示，不猜。 */
const STATE_KEY: Record<string, string> = {
  connected: "status.connected",
  failed: "mcp.state.failed",
  timeout: "mcp.state.timeout",
  auth_required: "mcp.state.authRequired",
  disabled: "mcp.state.disabled",
  unavailable: "mcp.state.incomplete",
};

const STATE_TONE: Record<string, "success" | "neutral" | "danger" | "warning"> = {
  connected: "success",
  failed: "danger",
  timeout: "warning",
  auth_required: "warning",
  disabled: "neutral",
  unavailable: "danger",
};

export function McpPage() {
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
    return data.activeRun ? t("mcp.pending.activeRun") : t("mcp.pending.saved");
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
      setNotice(t("mcp.notice.exposure", { id: def.id, exposure }));
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
      setError({ code: "UNSUPPORTED", message: t("mcp.errors.idRequired"), retryable: false });
      return;
    }
    let envRefs: Record<string, string>;
    let headerRefs: Record<string, string>;
    try {
      envRefs = textToPairs(draft.envRefs);
      headerRefs = textToPairs(draft.headerRefs);
    } catch (e) {
      // 引用行格式错误是前端解析失败：message 用 catalog 文案（`{line}` 带原行）。
      const line = e instanceof Error ? e.message : "";
      setError({
        code: "UNSUPPORTED",
        message: t("mcp.errors.refLine", { line }),
        retryable: false,
      });
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
      setNotice(t("mcp.notice.saved", { id: def.id }));
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
      setNotice(t("mcp.credential.required"));
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
      setNotice(t("mcp.credential.saved", { ref: res.ref }));
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <>
      <section>
        <header className="mb-2 flex items-start justify-between gap-2">
          <div>
            <h2 className="text-sm font-medium text-[color:var(--anvil-foreground)]">
              {t("mcp.title")}
            </h2>
            <p className="mt-0.5 text-[11px] text-[color:var(--anvil-muted)]">{t("mcp.subtitle")}</p>
          </div>
          <div className="flex shrink-0 gap-1">
            <button
              onClick={() => {
                setDraft(emptyDraft());
                setEditingId(null);
              }}
              className="rounded border border-[color:var(--anvil-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface-raised)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
            >
              {t("mcp.actions.create")}
            </button>
            <button
              onClick={() => void diagnoseAll()}
              disabled={busy === "diagnose-all" || items.length === 0 || !data?.diagnoseReady}
              className="flex items-center gap-1 rounded border border-[color:var(--anvil-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface-raised)] disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
            >
              <RefreshCw className="h-3 w-3" aria-hidden /> {t("mcp.actions.diagnoseAll")}
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
            {t("mcp.diagnoseNotReady")}
          </p>
        )}

        {error && (
          <div className="mb-2">
            <ErrorState error={error} onRetry={() => void load()} title={t("mcp.errorTitle")} />
          </div>
        )}
        {notice && !error && (
          <p role="status" className="mb-2 text-[11px] text-[color:var(--anvil-muted)]">
            {notice}
          </p>
        )}

        <div className="mb-3 rounded-lg border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] p-2">
          <div className="mb-1 flex items-center gap-1 text-[10px] text-[color:var(--anvil-muted)]">
            <ShieldQuestion className="h-3 w-3" aria-hidden /> {t("mcp.credential.hint")}
          </div>
          <div className="flex flex-wrap gap-1.5">
            <input
              aria-label={t("mcp.credential.refAria")}
              value={credRef}
              onChange={(e) => setCredRef(e.target.value)}
              placeholder="mcp-docs-token"
              className="min-w-0 flex-1 rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] placeholder:text-[color:var(--anvil-muted)]"
            />
            <input
              aria-label={t("mcp.credential.valueAria")}
              type="password"
              value={credSecret}
              onChange={(e) => setCredSecret(e.target.value)}
              placeholder={t("mcp.credential.valuePlaceholder")}
              className="min-w-0 flex-1 rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] placeholder:text-[color:var(--anvil-muted)]"
            />
            <button
              onClick={() => void saveCredential()}
              disabled={busy === "credential"}
              className="rounded border border-[color:var(--anvil-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface-raised)] disabled:opacity-50"
            >
              {t("mcp.credential.save")}
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
          <LoadingState label={t("mcp.loading")} />
        ) : items.length === 0 ? (
          !error && (
            <EmptyState
              title={t("mcp.empty.title")}
              hint={t("mcp.empty.hint")}
              icon={<Plug className="h-4 w-4" aria-hidden />}
            />
          )
        ) : (
          <ul className="space-y-1">
            {items.map((def) => {
              const st = statuses[def.id];
              const running = (data?.runtimeServers ?? []).includes(def.id);
              return (
                <li
                  key={def.id}
                  className="rounded-md border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-2.5 py-2"
                >
                  <div className="flex items-start gap-2">
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-1.5">
                        <span className="truncate text-[12px] text-[color:var(--anvil-foreground)]">
                          {def.id}
                        </span>
                        <Badge tone="neutral">{def.transport}</Badge>
                        <Badge tone={def.enabled ? "success" : "neutral"}>
                          {def.enabled ? t("mcp.state.enabled") : t("mcp.state.disabled")}
                        </Badge>
                        <Badge tone="neutral">
                          {t("mcp.labels.exposure", { value: def.exposure })}
                        </Badge>
                        {data?.pending && def.enabled && !running && (
                          <Badge tone="warning">{t("mcp.badges.pendingNextRun")}</Badge>
                        )}
                        {data?.pending && !def.enabled && running && (
                          <Badge tone="warning">{t("mcp.badges.runningInUse")}</Badge>
                        )}
                        {st && (
                          <Badge tone={STATE_TONE[st.state] ?? "neutral"}>
                            {STATE_KEY[st.state] ? t(STATE_KEY[st.state]) : st.state}
                          </Badge>
                        )}
                        {!st && <Badge tone="neutral">{t("mcp.state.unchecked")}</Badge>}
                      </div>
                      <div className="truncate text-[10px] text-[color:var(--anvil-muted)]">
                        {def.transport === "stdio"
                          ? [def.command, ...(def.args ?? [])].join(" ")
                          : def.url || t("mcp.labels.missingUrl")}
                      </div>
                      {st && (
                        <div className="mt-0.5 text-[10px] text-[color:var(--anvil-muted)]">
                          {st.state === "connected"
                            ? st.tools && st.tools.length > 0
                              ? t("mcp.labels.toolsWithNames", {
                                  count: st.tools.length,
                                  names: st.tools.map((tool) => tool.name).join(", "),
                                })
                              : t("mcp.labels.tools", { count: st.tools?.length ?? 0 })
                            : st.error?.message || st.detail || ""}
                        </div>
                      )}
                      {st?.state === "auth_required" && (
                        <div className="mt-0.5 text-[10px] text-[color:var(--anvil-warning)]">
                          {t("mcp.authUnsupported")}
                        </div>
                      )}
                      {st && !st.oauthSupported && st.state === "connected" && (
                        <div className="mt-0.5 text-[10px] text-[color:var(--anvil-muted)]">
                          {t("mcp.toolsNote")}
                        </div>
                      )}
                    </div>
                    <div className="flex shrink-0 flex-wrap items-center justify-end gap-1">
                      <select
                        aria-label={t("mcp.exposureAria", { id: def.id })}
                        value={def.exposure}
                        onChange={(e) => void changeExposure(def, e.target.value as MCPExposure)}
                        disabled={busy === def.id}
                        className="rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-1 py-0.5 text-[11px] text-[color:var(--anvil-foreground)]"
                      >
                        {EXPOSURES.map((value) => (
                          <option key={value} value={value}>
                            {value}
                          </option>
                        ))}
                      </select>
                      <button
                        onClick={() => void diagnose(def.id)}
                        disabled={busy === "diagnose-" + def.id || !data?.diagnoseReady}
                        className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface-raised)] disabled:opacity-40"
                      >
                        {t("mcp.actions.diagnose")}
                      </button>
                      <button
                        onClick={() => {
                          setDraft(draftFrom(def));
                          setEditingId(def.id);
                        }}
                        className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface-raised)]"
                      >
                        {t("mcp.actions.edit")}
                      </button>
                      <button
                        onClick={() => void toggle(def)}
                        disabled={busy === def.id}
                        className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface-raised)] disabled:opacity-40"
                      >
                        {def.enabled ? t("mcp.actions.disable") : t("mcp.actions.enable")}
                      </button>
                      {confirming === def.id ? (
                        <>
                          <button
                            onClick={() => void remove(def.id)}
                            disabled={busy === def.id}
                            className="rounded border border-[color:var(--anvil-danger-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-danger)] hover:bg-[color:var(--anvil-danger-surface)] disabled:opacity-40"
                          >
                            {t("mcp.actions.deleteConfirm")}
                          </button>
                          <button
                            onClick={() => setConfirming(null)}
                            className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface-raised)]"
                          >
                            {t("common.cancel")}
                          </button>
                        </>
                      ) : (
                        <button
                          onClick={() => setConfirming(def.id)}
                          className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface-raised)]"
                        >
                          {t("mcp.actions.delete")}
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

      <ToolsSection />
    </>
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
      <div className="mb-1 text-[11px] text-[color:var(--anvil-foreground)]">
        {editingId ? t("mcp.editor.titleEdit", { id: editingId }) : t("mcp.editor.titleCreate")}
      </div>
      <div className="grid grid-cols-2 gap-1.5">
        <label className="flex flex-col gap-0.5 text-[10px] text-[color:var(--anvil-muted)]">
          {t("mcp.editor.idLabel")}
          <input
            aria-label={t("mcp.editor.idAria")}
            value={draft.id}
            onChange={(e) => onChange({ ...draft, id: e.target.value })}
            disabled={editingId !== null}
            className={input}
          />
        </label>
        <label className="flex flex-col gap-0.5 text-[10px] text-[color:var(--anvil-muted)]">
          {t("mcp.editor.transportLabel")}
          <select
            aria-label={t("mcp.editor.transportAria")}
            value={draft.transport}
            onChange={(e) => onChange({ ...draft, transport: e.target.value as MCPTransport })}
            className={input}
          >
            {TRANSPORTS.map((item) => (
              <option key={item.value} value={item.value}>
                {t(item.labelKey)}
              </option>
            ))}
          </select>
        </label>
        {draft.transport === "stdio" ? (
          <>
            <label className="flex flex-col gap-0.5 text-[10px] text-[color:var(--anvil-muted)]">
              {t("mcp.editor.commandLabel")}
              <input
                aria-label={t("mcp.editor.commandAria")}
                value={draft.command}
                onChange={(e) => onChange({ ...draft, command: e.target.value })}
                placeholder="npx"
                className={input}
              />
            </label>
            <label className="flex flex-col gap-0.5 text-[10px] text-[color:var(--anvil-muted)]">
              {t("mcp.editor.workingDirLabel")}
              <input
                aria-label={t("mcp.editor.workingDirAria")}
                value={draft.workingDir}
                onChange={(e) => onChange({ ...draft, workingDir: e.target.value })}
                className={input}
              />
            </label>
            <label className="col-span-2 flex flex-col gap-0.5 text-[10px] text-[color:var(--anvil-muted)]">
              {t("mcp.editor.argsLabel")}
              <textarea
                aria-label={t("mcp.editor.argsAria")}
                value={draft.args}
                onChange={(e) => onChange({ ...draft, args: e.target.value })}
                rows={3}
                className={input}
              />
            </label>
          </>
        ) : (
          <label className="col-span-2 flex flex-col gap-0.5 text-[10px] text-[color:var(--anvil-muted)]">
            {t("mcp.editor.urlLabel")}
            <input
              aria-label={t("mcp.editor.urlAria")}
              value={draft.url}
              onChange={(e) => onChange({ ...draft, url: e.target.value })}
              placeholder={t("mcp.editor.urlPlaceholder")}
              className={input}
            />
          </label>
        )}
        <label className="flex flex-col gap-0.5 text-[10px] text-[color:var(--anvil-muted)]">
          {t("mcp.editor.exposureLabel")}
          <select
            aria-label={t("mcp.editor.exposureAria")}
            value={draft.exposure}
            onChange={(e) => onChange({ ...draft, exposure: e.target.value as MCPExposure })}
            className={input}
          >
            <option value="direct">{t("mcp.editor.exposureDirectOption")}</option>
            <option value="deferred">{t("mcp.editor.exposureDeferredOption")}</option>
            <option value="hidden">{t("mcp.editor.exposureHiddenOption")}</option>          </select>
        </label>
        <div className="col-span-1" />
        <label className="flex flex-col gap-0.5 text-[10px] text-[color:var(--anvil-muted)]">
          {t("mcp.editor.envRefsLabel")}
          <textarea
            aria-label={t("mcp.editor.envRefsAria")}
            value={draft.envRefs}
            onChange={(e) => onChange({ ...draft, envRefs: e.target.value })}
            rows={2}
            className={input}
          />
        </label>
        <label className="flex flex-col gap-0.5 text-[10px] text-[color:var(--anvil-muted)]">
          {t("mcp.editor.headerRefsLabel")}
          <textarea
            aria-label={t("mcp.editor.headerRefsAria")}
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
          className="rounded bg-[color:var(--anvil-accent)] px-2.5 py-1 text-[11px] font-medium text-[color:var(--anvil-background)] hover:opacity-90 disabled:opacity-50"
        >
          {t("mcp.editor.save")}
        </button>
        <button
          onClick={onCancel}
          className="rounded border border-[color:var(--anvil-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface-raised)]"
        >
          {t("common.cancel")}
        </button>
        <span className="text-[10px] text-[color:var(--anvil-muted)]">{t("mcp.editor.hint")}</span>
      </div>
    </div>
  );
}

function Badge({
  tone,
  children,
}: {
  tone: "success" | "warning" | "danger" | "neutral";
  children: string;
}) {
  const cls = {
    success: "bg-[color:var(--anvil-success-surface)] text-[color:var(--anvil-success)]",
    warning: "bg-[color:var(--anvil-warning-surface)] text-[color:var(--anvil-warning)]",
    danger: "bg-[color:var(--anvil-danger-surface)] text-[color:var(--anvil-danger)]",
    neutral: "bg-[color:var(--anvil-surface-raised)] text-[color:var(--anvil-muted)]",
  }[tone];
  return <span className={`shrink-0 rounded px-1 py-0.5 text-[9px] ${cls}`}>{children}</span>;
}
