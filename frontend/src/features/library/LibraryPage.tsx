// 资源库页面（U04 / UI08、RES05）。
//
// 这一页只做"浏览 + 显式动作"：
//   - Skill：真实托管目录里的条目，来源/路径/hash/依赖/启用范围都来自 Go；
//     新建、链接导入、启停、编辑、删除、隔离试运行都是真实调用；
//   - Agent：Profile 的只读浏览（编辑在 Agent 页，那里有完整的产品化编辑器）；
//   - MCP：配置条目 + 真实诊断状态。只有协议握手和工具列表都成功才显示 connected。
//
// 拒绝原因逐项展示：链接检查的每条规则、技能不可用的原因、诊断失败的错误码，
// 都不折叠成一句"操作失败"。

import { useCallback, useEffect, useState } from "react";
import { AsyncState } from "../../components/states/AsyncState";
import { formatBytes, formatError, t } from "../../i18n";
import type { CallError, MCPDefinition, Profile, SkillResource } from "../../types";
import {
  libraryApi,
  type SkillDryRunResult,
  type SkillLinkInspection,
} from "./api";
import { SkillImportDialog } from "./SkillImportDialog";

type Tab = "skills" | "agents" | "mcp";

export interface LibraryPageProps {
  workspaceId?: string;
  onNavigate?: (route: string) => void;
}

export function LibraryPage({ workspaceId = "", onNavigate }: LibraryPageProps) {
  const [tab, setTab] = useState<Tab>("skills");
  return (
    <div className="mx-auto max-w-4xl space-y-4 px-4 py-5">
      <header className="space-y-1">
        <h1 className="text-sm font-medium text-[color:var(--anvil-foreground)]">{t("library.title")}</h1>
        <p className="text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">{t("library.subtitle")}</p>
      </header>
      <div role="tablist" aria-label={t("library.title")} className="flex gap-1 border-b border-[color:var(--anvil-border)] pb-1">
        {(["skills", "agents", "mcp"] as Tab[]).map((id) => (
          <button
            key={id}
            type="button"
            role="tab"
            aria-selected={tab === id}
            onClick={() => setTab(id)}
            className={`rounded-md px-2.5 py-1 text-[11px] ${
              tab === id
                ? "bg-[color:var(--anvil-surface)] text-[color:var(--anvil-foreground)]"
                : "text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)]"
            }`}
          >
            {t(`library.tabs.${id}`)}
          </button>
        ))}
      </div>
      {tab === "skills" ? <SkillsTab workspaceId={workspaceId} /> : null}
      {tab === "agents" ? <AgentsTab onNavigate={onNavigate} /> : null}
      {tab === "mcp" ? <MCPTab /> : null}
    </div>
  );
}

// ---------------------------------------------------------------- Skill 分区

function SkillsTab({ workspaceId }: { workspaceId: string }) {
  const [items, setItems] = useState<SkillResource[]>([]);
  const [revision, setRevision] = useState(0);
  const [pending, setPending] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<CallError | null>(null);
  const [dialog, setDialog] = useState<"link" | "create" | null>(null);
  const [actionError, setActionError] = useState<CallError | null>(null);
  const [editing, setEditing] = useState<{ id: string; content: string } | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null);
  const [dryRunFor, setDryRunFor] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await libraryApi.skills();
      if (res.error) {
        setError(res.error);
        setItems([]);
      } else {
        setItems(res.items ?? []);
        setRevision(res.revision);
        setPending(res.pending);
      }
    } catch (e) {
      setError(asCallError(e));
      setItems([]);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function toggle(item: SkillResource) {
    setActionError(null);
    try {
      const res = await libraryApi.setSkillEnabled(item.id, revision, !item.enabled);
      if (res.error) {
        setActionError(res.error);
        return;
      }
      await load();
    } catch (e) {
      setActionError(asCallError(e));
    }
  }

  async function remove(id: string) {
    setActionError(null);
    setConfirmDelete(null);
    try {
      const res = await libraryApi.deleteSkill(revision, id);
      if (res.error) {
        setActionError(res.error);
        return;
      }
      await load();
    } catch (e) {
      setActionError(asCallError(e));
    }
  }

  async function openEditor(id: string) {
    setActionError(null);
    try {
      const res = await libraryApi.readSkill(id);
      if (res.error) {
        setActionError(res.error);
        return;
      }
      setEditing({ id, content: res.content });
    } catch (e) {
      setActionError(asCallError(e));
    }
  }

  async function saveEditor() {
    if (!editing) return;
    setActionError(null);
    try {
      const res = await libraryApi.saveSkill(editing.id, revision, editing.content);
      if (res.error) {
        setActionError(res.error);
        return;
      }
      setEditing(null);
      await load();
    } catch (e) {
      setActionError(asCallError(e));
    }
  }

  const phase = loading ? "loading" : error ? "error" : items.length === 0 ? "empty" : "success";

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          onClick={() => setDialog("link")}
          className="rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)]"
        >
          {t("library.actions.importLink")}
        </button>
        <button
          type="button"
          onClick={() => setDialog("create")}
          className="rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)]"
        >
          {t("library.actions.createLocal")}
        </button>
        <button
          type="button"
          onClick={() => void load()}
          className="rounded-md px-2.5 py-1 text-[11px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)]"
        >
          {t("library.actions.reload")}
        </button>
        {pending ? (
          <span className="text-[10px] text-[color:var(--anvil-warning)]">{t("library.labels.pendingNextRun")}</span>
        ) : null}
      </div>

      {dialog ? (
        <SkillImportDialog
          workspaceId={workspaceId}
          mode={dialog}
          onImported={() => void load()}
          onClose={() => setDialog(null)}
        />
      ) : null}

      {actionError ? (
        <p role="alert" className="rounded-md border border-[color:var(--anvil-danger-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-danger)]">
          {formatError(actionError)}
        </p>
      ) : null}

      <AsyncState
        phase={phase}
        error={error}
        onRetry={phase === "error" ? () => void load() : undefined}
        emptyTitle={t("library.empty.skills")}
        emptyHint={t("library.empty.hint")}
      >
        <ul className="space-y-2">
          {items.map((item) => (
            <li key={item.id}>
              <article
                data-testid={`skill-${item.id}`}
                className="space-y-1.5 rounded-lg border border-[color:var(--anvil-border)] p-2.5"
              >
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-[12px] font-medium text-[color:var(--anvil-foreground)]">
                    {item.name || item.id}
                  </span>
                  <Chip
                    label={item.enabled ? t("library.labels.enabled") : t("library.labels.disabled")}
                    tone={item.enabled ? "success" : "neutral"}
                  />
                  <span className="text-[10px] text-[color:var(--anvil-muted)]">
                    {t(`library.scope.${item.scope}`)}
                  </span>
                  <span className="text-[10px] text-[color:var(--anvil-muted)]">
                    {t(`library.origin.${item.origin}`)}
                  </span>
                  {!item.available ? (
                    <span className="text-[10px] text-[color:var(--anvil-danger)]">
                      {t("library.labels.unavailable")}
                    </span>
                  ) : null}
                </div>
                <p className="text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">{item.description}</p>
                <dl className="grid grid-cols-2 gap-x-3 gap-y-0.5 text-[10px]">
                  <dt className="text-[color:var(--anvil-muted)]">{t("library.labels.path")}</dt>
                  <dd className="truncate font-mono text-[color:var(--anvil-foreground)]" title={item.path}>
                    {item.path}
                  </dd>
                  <dt className="text-[color:var(--anvil-muted)]">{t("library.labels.hash")}</dt>
                  <dd className="font-mono text-[color:var(--anvil-foreground)]">{item.hash || t("common.unknown")}</dd>
                  <dt className="text-[color:var(--anvil-muted)]">{t("library.labels.size")}</dt>
                  <dd className="text-[color:var(--anvil-foreground)]">{formatBytes(item.bytes ?? 0)}</dd>
                  <dt className="text-[color:var(--anvil-muted)]">{t("library.labels.dependencies")}</dt>
                  <dd className="text-[color:var(--anvil-foreground)]">
                    {(item.dependencies ?? []).length > 0
                      ? (item.dependencies ?? []).join(", ")
                      : t("common.notAvailable")}
                  </dd>
                </dl>
                {!item.available && item.unavailableReason ? (
                  <p className="text-[10px] text-[color:var(--anvil-danger)]">
                    {t("library.labels.unavailableReason", { reason: item.unavailableReason })}
                  </p>
                ) : null}
                {(item.missingDependencies ?? []).length > 0 ? (
                  <p className="text-[10px] text-[color:var(--anvil-danger)]">
                    {t("library.labels.missingDependencies", {
                      commands: (item.missingDependencies ?? []).join(", "),
                    })}
                  </p>
                ) : null}
                <div className="flex flex-wrap gap-2 pt-0.5">
                  <button
                    type="button"
                    onClick={() => void toggle(item)}
                    className="rounded-md border border-[color:var(--anvil-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)]"
                  >
                    {item.enabled ? t("library.actions.disable") : t("library.actions.enable")}
                  </button>
                  <button
                    type="button"
                    onClick={() => void openEditor(item.id)}
                    className="rounded-md border border-[color:var(--anvil-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)]"
                  >
                    {t("library.actions.edit")}
                  </button>
                  <button
                    type="button"
                    onClick={() => setDryRunFor(dryRunFor === item.id ? null : item.id)}
                    className="rounded-md border border-[color:var(--anvil-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)]"
                  >
                    {t("library.actions.dryRun")}
                  </button>
                  {confirmDelete === item.id ? (
                    <>
                      <button
                        type="button"
                        onClick={() => void remove(item.id)}
                        className="rounded-md border border-[color:var(--anvil-danger-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-danger)]"
                      >
                        {t("library.actions.deleteConfirm")}
                      </button>
                      <button
                        type="button"
                        onClick={() => setConfirmDelete(null)}
                        className="rounded-md px-2 py-0.5 text-[10px] text-[color:var(--anvil-muted)]"
                      >
                        {t("common.cancel")}
                      </button>
                    </>
                  ) : (
                    <button
                      type="button"
                      onClick={() => setConfirmDelete(item.id)}
                      className="rounded-md px-2 py-0.5 text-[10px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)]"
                    >
                      {t("library.actions.delete")}
                    </button>
                  )}
                </div>
                {editing?.id === item.id ? (
                  <div className="space-y-1.5">
                    <textarea
                      value={editing.content}
                      onChange={(e) => setEditing({ id: editing.id, content: e.target.value })}
                      aria-label={t("library.actions.edit")}
                      rows={10}
                      className="w-full rounded-md border border-[color:var(--anvil-border)] bg-transparent p-2 font-mono text-[11px] text-[color:var(--anvil-foreground)]"
                    />
                    <div className="flex gap-2">
                      <button
                        type="button"
                        onClick={() => void saveEditor()}
                        className="rounded-md border border-[color:var(--anvil-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-foreground)]"
                      >
                        {t("library.actions.saveEdit")}
                      </button>
                      <button
                        type="button"
                        onClick={() => setEditing(null)}
                        className="rounded-md px-2 py-0.5 text-[10px] text-[color:var(--anvil-muted)]"
                      >
                        {t("library.actions.cancelEdit")}
                      </button>
                    </div>
                  </div>
                ) : null}
                {dryRunFor === item.id ? <DryRunPanel itemId={item.id} /> : null}
              </article>
            </li>
          ))}
        </ul>
      </AsyncState>
    </div>
  );
}

// ---------------------------------------------------------------- 试运行面板

/** 试运行必须先讲清费用与数据流向，再让用户点开始。 */
function DryRunPanel({ itemId }: { itemId: string }) {
  const [prompt, setPrompt] = useState("");
  const [running, setRunning] = useState(false);
  const [result, setResult] = useState<SkillDryRunResult | null>(null);
  const [error, setError] = useState<CallError | null>(null);

  async function start() {
    setError(null);
    setResult(null);
    setRunning(true);
    try {
      const res = await libraryApi.dryRunSkill(itemId, prompt.trim());
      setResult(res);
      if (res.error) {
        setError(res.error);
      }
    } catch (e) {
      setError(asCallError(e));
    } finally {
      setRunning(false);
    }
  }

  return (
    <div data-testid="dry-run-panel" className="space-y-2 rounded-md border border-[color:var(--anvil-border)] p-2">
      <p className="text-[11px] font-medium text-[color:var(--anvil-foreground)]">{t("library.dryrun.title")}</p>
      <p className="text-[10px] leading-relaxed text-[color:var(--anvil-warning)]">{t("library.dryrun.warningBody")}</p>
      <label className="block text-[10px] text-[color:var(--anvil-muted)]">
        {t("library.dryrun.promptLabel")}
        <textarea
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
          aria-label={t("library.dryrun.promptLabel")}
          placeholder={t("library.dryrun.promptPlaceholder")}
          rows={2}
          className="mt-1 w-full rounded-md border border-[color:var(--anvil-border)] bg-transparent p-2 text-[11px] text-[color:var(--anvil-foreground)]"
        />
      </label>
      <button
        type="button"
        onClick={() => void start()}
        disabled={running || prompt.trim() === ""}
        className="rounded-md border border-[color:var(--anvil-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-foreground)] disabled:opacity-50"
      >
        {running ? t("library.dryrun.running") : t("library.dryrun.start")}
      </button>
      {error ? (
        <p role="alert" className="text-[10px] text-[color:var(--anvil-danger)]">
          {formatError(error)}
        </p>
      ) : null}
      {result ? (
        <div className="space-y-1 text-[10px]">
          <p className="text-[color:var(--anvil-foreground)]">
            {t("library.dryrun.stateLabel")}: {t(`library.dryrun.state.${result.state}`)}
          </p>
          <p className="text-[color:var(--anvil-foreground)]">
            {t("library.dryrun.policyLabel")}: {result.policy.filesystem} /{" "}
            {t("library.dryrun.networkLabel")}={String(result.policy.network)} /{" "}
            {t("library.dryrun.shellLabel")}={String(result.policy.shell)}
          </p>
          <p className="text-[color:var(--anvil-muted)]">
            {t("library.dryrun.tempCleaned")}: {String(result.tempCleaned)} ·{" "}
            {t("library.dryrun.skillUnchanged")}: {String(result.skillUnchanged)} ·{" "}
            {t("library.dryrun.userStateUnchanged")}: {String(result.userStateUnchanged)}
          </p>
          {(result.denials ?? []).length > 0 ? (
            <div>
              <p className="font-medium text-[color:var(--anvil-warning)]">{t("library.dryrun.denials")}</p>
              <ul className="list-disc pl-4">
                {(result.denials ?? []).map((d, i) => (
                  <li key={`${d.toolName}-${i}`}>
                    {d.toolName} — {t(`library.reject.${d.rule.toLowerCase()}`)}
                  </li>
                ))}
              </ul>
            </div>
          ) : (
            <p className="text-[color:var(--anvil-muted)]">{t("library.dryrun.noDenials")}</p>
          )}
          {(result.limitations ?? []).length > 0 ? (
            <div>
              <p className="font-medium text-[color:var(--anvil-muted)]">{t("library.dryrun.limitations")}</p>
              <ul className="list-disc pl-4">
                {(result.limitations ?? []).map((l) => (
                  <li key={l}>{t(`library.limit.${l}`)}</li>
                ))}
              </ul>
            </div>
          ) : null}
          {result.output ? (
            <pre className="max-h-40 overflow-auto whitespace-pre-wrap rounded bg-[color:var(--anvil-surface)] p-1.5 text-[10px] text-[color:var(--anvil-foreground)]">
              {result.output}
            </pre>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------- Agent 分区

function AgentsTab({ onNavigate }: { onNavigate?: (route: string) => void }) {
  const [items, setItems] = useState<Profile[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<CallError | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await libraryApi.profiles();
      if (res.error) {
        setError(res.error);
        setItems([]);
      } else {
        setItems(res.items ?? []);
      }
    } catch (e) {
      setError(asCallError(e));
      setItems([]);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const phase = loading ? "loading" : error ? "error" : items.length === 0 ? "empty" : "success";
  return (
    <AsyncState
      phase={phase}
      error={error}
      onRetry={phase === "error" ? () => void load() : undefined}
      emptyTitle={t("library.empty.agents")}
      emptyHint={t("library.empty.hint")}
    >
      <ul className="space-y-2">
        {items.map((p) => (
          <li
            key={p.id}
            className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-[color:var(--anvil-border)] p-2.5"
          >
            <div className="min-w-0">
              <p className="text-[12px] font-medium text-[color:var(--anvil-foreground)]">{p.name || p.id}</p>
              <p className="text-[10px] text-[color:var(--anvil-muted)]">
                {t("library.agents.model")}: {p.modelRef || t("common.notAvailable")} ·{" "}
                {t("library.agents.skills")}: {(p.skillIDs ?? []).length} ·{" "}
                {t("library.agents.mcp")}: {(p.mcpIDs ?? []).length}
              </p>
            </div>
            {onNavigate ? (
              <button
                type="button"
                onClick={() => onNavigate(`/agents?agent=${encodeURIComponent(p.id)}`)}
                className="rounded-md border border-[color:var(--anvil-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)]"
              >
                {t("library.actions.openAgent")}
              </button>
            ) : null}
          </li>
        ))}
      </ul>
    </AsyncState>
  );
}

// ---------------------------------------------------------------- MCP 分区

interface McpRow {
  definition: MCPDefinition;
  /** 未诊断是 "unknown"；只有握手与工具列表都成功才是 connected。 */
  state: string;
  error?: CallError;
  detail?: string;
  tools: number;
}

function MCPTab() {
  const [rows, setRows] = useState<McpRow[]>([]);
  const [revision, setRevision] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<CallError | null>(null);
  const [actionError, setActionError] = useState<CallError | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await libraryApi.mcpServers();
      if (res.error) {
        setError(res.error);
        setRows([]);
      } else {
        setRows(
          (res.items ?? []).map((d) => ({
            definition: d,
            // 没诊断过就是未知：不能因为"配置存在"就显示已连接。
            state: d.enabled ? "unknown" : "disabled",
            tools: 0,
          }))
        );
        setRevision(res.revision);
      }
    } catch (e) {
      setError(asCallError(e));
      setRows([]);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function diagnose(id: string) {
    setActionError(null);
    setBusyId(id);
    try {
      const res = await libraryApi.diagnoseMCP(id);
      if (res.error) {
        setActionError(res.error);
        return;
      }
      setRows((prev) =>
        prev.map((row) =>
          row.definition.id === id
            ? {
                ...row,
                state: res.status.state,
                error: res.status.error,
                detail: res.status.detail,
                tools: (res.status.tools ?? []).length,
              }
            : row
        )
      );
    } catch (e) {
      setActionError(asCallError(e));
    } finally {
      setBusyId(null);
    }
  }

  async function toggle(def: MCPDefinition) {
    setActionError(null);
    try {
      const res = await libraryApi.setMCPEnabled(def.id, revision, !def.enabled);
      if (res.error) {
        setActionError(res.error);
        return;
      }
      await load();
    } catch (e) {
      setActionError(asCallError(e));
    }
  }

  async function remove(id: string) {
    setActionError(null);
    setConfirmDelete(null);
    try {
      const res = await libraryApi.deleteMCP(revision, id);
      if (res.error) {
        setActionError(res.error);
        return;
      }
      await load();
    } catch (e) {
      setActionError(asCallError(e));
    }
  }

  const phase = loading ? "loading" : error ? "error" : rows.length === 0 ? "empty" : "success";
  return (
    <div className="space-y-3">
      {actionError ? (
        <p role="alert" className="rounded-md border border-[color:var(--anvil-danger-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-danger)]">
          {formatError(actionError)}
        </p>
      ) : null}
      <AsyncState
        phase={phase}
        error={error}
        onRetry={phase === "error" ? () => void load() : undefined}
        emptyTitle={t("library.empty.mcp")}
        emptyHint={t("library.empty.hint")}
      >
        <ul className="space-y-2">
          {rows.map(({ definition, state, error: diagError, detail, tools }) => (
            <li key={definition.id}>
              <article
                data-testid={`mcp-${definition.id}`}
                className="space-y-1.5 rounded-lg border border-[color:var(--anvil-border)] p-2.5"
              >
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-[12px] font-medium text-[color:var(--anvil-foreground)]">
                    {definition.id}
                  </span>
                  <Chip
                    label={t(`library.mcpState.${state}`)}
                    tone={state === "connected" ? "success" : state === "unknown" ? "neutral" : "danger"}
                  />
                  <span className="text-[10px] text-[color:var(--anvil-muted)]">
                    {t("library.labels.transport")}: {definition.transport}
                  </span>
                  <span className="text-[10px] text-[color:var(--anvil-muted)]">
                    {t("library.labels.exposure")}: {definition.exposure}
                  </span>
                  <span className="text-[10px] text-[color:var(--anvil-muted)]">
                    {t("library.mcp.tools")}: {tools}
                  </span>
                </div>
                {state === "unknown" ? (
                  <p className="text-[10px] text-[color:var(--anvil-muted)]">{t("library.mcp.unknown")}</p>
                ) : null}
                {state === "connected" ? (
                  <p className="text-[10px] text-[color:var(--anvil-success)]">{t("library.mcp.connected")}</p>
                ) : null}
                {diagError || detail ? (
                  <div className="space-y-0.5">
                    {diagError ? (
                      <p className="text-[10px] text-[color:var(--anvil-danger)]">
                        {formatError(diagError)}
                        {diagError.code ? ` (${diagError.code})` : ""}
                      </p>
                    ) : null}
                    {detail ? (
                      <p className="font-mono text-[10px] text-[color:var(--anvil-muted)]">{detail}</p>
                    ) : null}
                    <p className="text-[10px] text-[color:var(--anvil-muted)]">{t("library.mcp.repairHint")}</p>
                  </div>
                ) : null}
                <div className="flex flex-wrap gap-2 pt-0.5">
                  <button
                    type="button"
                    onClick={() => void diagnose(definition.id)}
                    disabled={busyId === definition.id}
                    className="rounded-md border border-[color:var(--anvil-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-foreground)] disabled:opacity-50"
                  >
                    {busyId === definition.id ? t("library.actions.diagnosing") : t("library.actions.diagnose")}
                  </button>
                  <button
                    type="button"
                    onClick={() => void toggle(definition)}
                    className="rounded-md border border-[color:var(--anvil-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-foreground)]"
                  >
                    {definition.enabled ? t("library.actions.disable") : t("library.actions.enable")}
                  </button>
                  {confirmDelete === definition.id ? (
                    <>
                      <button
                        type="button"
                        onClick={() => void remove(definition.id)}
                        className="rounded-md border border-[color:var(--anvil-danger-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-danger)]"
                      >
                        {t("library.actions.deleteConfirm")}
                      </button>
                      <button
                        type="button"
                        onClick={() => setConfirmDelete(null)}
                        className="rounded-md px-2 py-0.5 text-[10px] text-[color:var(--anvil-muted)]"
                      >
                        {t("common.cancel")}
                      </button>
                    </>
                  ) : (
                    <button
                      type="button"
                      onClick={() => setConfirmDelete(definition.id)}
                      className="rounded-md px-2 py-0.5 text-[10px] text-[color:var(--anvil-muted)]"
                    >
                      {t("library.actions.delete")}
                    </button>
                  )}
                </div>
              </article>
            </li>
          ))}
        </ul>
      </AsyncState>
    </div>
  );
}

/** 状态胶囊：只显示来自后端的状态词，不从点击推断。 */
function Chip({ label, tone }: { label: string; tone: "neutral" | "success" | "danger" }) {
  const cls =
    tone === "success"
      ? "border-[color:var(--anvil-success-border)] text-[color:var(--anvil-success)]"
      : tone === "danger"
        ? "border-[color:var(--anvil-danger-border)] text-[color:var(--anvil-danger)]"
        : "border-[color:var(--anvil-border)] text-[color:var(--anvil-muted)]";
  return <span className={`rounded border px-1.5 py-0.5 text-[10px] ${cls}`}>{label}</span>;
}

function asCallError(e: unknown): CallError {
  if (typeof e === "object" && e !== null && "code" in e) {
    return e as CallError;
  }
  return { code: "INTERNAL_ERROR", message: String(e), retryable: false };
}

export type { SkillLinkInspection };
