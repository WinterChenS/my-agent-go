// 单 Agent/Profile 的产品化编辑器（U03 / UI07）。
//
// 它只做三件事：把后端的现场读出来、让用户改草稿、把草稿按 revision 写回去。
// 所有"事实"都来自后端：上限、来源、缺失引用、保存后的 revision。
//
// 几条硬规则（都在测试里可执行）：
//   - 保存带 expectedRevision；CONFIG_CONFLICT 时展示磁盘差异，并明说"什么都没写"；
//   - 超过上限的权限选项直接禁用；即使绕过界面，后端也会结构化拒绝并给出原因；
//   - 删除先确认，按 revision 删；失败时编辑区留在原地；
//   - 领域模型不承载的字段（说明 / system prompt / 思考级别 / 工具白名单）
//     显示 unavailable，而不是画一个"看起来能保存"的输入框。
//
// 文案全部来自 catalog（`agents.*`），颜色来自语义 token。

import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { AlertTriangle, Copy, Loader2, Save, Trash2 } from "lucide-react";
import { AsyncState } from "../../components/states/AsyncState";
import { StatusBadge } from "../../components/status/StatusBadge";
import { formatError, hasKey, t } from "../../i18n";
import type { CallError, FilesystemScope, Profile, ProfileConflictResult } from "../../types";
import {
  agentsApi,
  type AgentCeiling,
  type AgentEditorState,
  type AgentResources,
} from "./api";
import { FILESYSTEM_VALUES, filesystemAtMost } from "./permissions";

const EMPTY_PROFILE: Profile = {
  id: "",
  name: "",
  modelRef: "",
  skillIDs: null,
  mcpIDs: null,
  toolPolicy: { filesystem: "none", network: false, shell: false },
  memoryPolicy: { enabled: false, maxEntries: 0 },
  revision: 0,
};

const UNAVAILABLE_FIELDS = ["description", "systemPrompt", "reasoningLevel", "toolIDs"] as const;

export interface AgentEditorProps {
  /** null 表示新建。 */
  profileId: string | null;
  workspaceId: string;
  onSaved?: (p: Profile) => void;
  onDeleted?: (id: string) => void;
  onDuplicated?: (p: Profile) => void;
}

type Phase = "loading" | "ready" | "error";

export function AgentEditor({
  profileId,
  workspaceId,
  onSaved,
  onDeleted,
  onDuplicated,
}: AgentEditorProps) {
  const [phase, setPhase] = useState<Phase>("loading");
  const [loadError, setLoadError] = useState<CallError | null>(null);
  const [backendState, setBackendState] = useState<AgentEditorState | null>(null);
  const [resources, setResources] = useState<AgentResources>({
    models: [],
    skills: [],
    mcp: [],
    tools: [],
    memoryScopes: [],
  });

  const [draft, setDraft] = useState<Profile>(EMPTY_PROFILE);
  const [newId, setNewId] = useState("");
  const [baseRevision, setBaseRevision] = useState(0);
  const [saving, setSaving] = useState(false);
  const [savedRevision, setSavedRevision] = useState<number | null>(null);
  const [actionError, setActionError] = useState<CallError | null>(null);
  const [conflict, setConflict] = useState<ProfileConflictResult | null>(null);
  const [renameTo, setRenameTo] = useState("");
  const [duplicateId, setDuplicateId] = useState("");
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const [deleteError, setDeleteError] = useState<CallError | null>(null);

  const applyState = useCallback((next: AgentEditorState) => {
    setBackendState(next);
    setDraft({ ...EMPTY_PROFILE, ...next.profile });
    setBaseRevision(next.profile.revision);
    setRenameTo(next.profile.name);
    setSavedRevision(null);
  }, []);

  const load = useCallback(async () => {
    setPhase("loading");
    setLoadError(null);
    setConflict(null);
    setActionError(null);
    try {
      const [res, loaded] = await Promise.all([
        agentsApi.resources(),
        profileId
          ? agentsApi.state(profileId, workspaceId)
          : agentsApi.ceiling(workspaceId).then(
              (c): AgentEditorState => ({
                profile: { ...EMPTY_PROFILE },
                ceiling: c.ceiling,
                sources: [],
                missing: [],
                warnings: [],
                unavailableFields: c.unavailableFields,
                error: c.error,
              })
            ),
      ]);
      setResources(res);
      if (loaded.error) {
        setLoadError(loaded.error);
        setPhase("error");
        return;
      }
      const next: AgentEditorState = {
        profile: loaded.profile,
        ceiling: loaded.ceiling,
        sources: loaded.sources ?? [],
        missing: loaded.missing,
        warnings: loaded.warnings,
        unavailableFields: loaded.unavailableFields?.length ? loaded.unavailableFields : [...UNAVAILABLE_FIELDS],
      };
      applyState(next);
      setPhase("ready");
    } catch (e) {
      setLoadError(e as CallError);
      setPhase("error");
    }
  }, [applyState, profileId, workspaceId]);

  useEffect(() => {
    void load();
  }, [load]);

  const ceiling: AgentCeiling = backendState?.ceiling ?? {
    filesystem: "none",
    network: false,
    shell: false,
    sources: {},
    available: false,
  };

  const patch = (next: Partial<Profile>) => setDraft((d) => ({ ...d, ...next }));

  const targetId = (profileId ?? newId).trim();

  const handleSave = async () => {
    if (!targetId) {
      setActionError({ code: "INVALID_INPUT", message: "", retryable: false, params: { field: "id" } });
      return;
    }
    setSaving(true);
    setActionError(null);
    setConflict(null);
    setSavedRevision(null);
    try {
      const res = await agentsApi.save(targetId, workspaceId, baseRevision, { ...draft, id: targetId });
      if (res.error) {
        if (res.error.code === "CONFIG_CONFLICT") {
          // 冲突必须拿磁盘现状来对比，不能拿自己保存前的快照充数。
          const snapshot = await agentsApi.conflict(targetId, { ...draft, id: targetId });
          if (snapshot.error) setActionError(snapshot.error);
          else setConflict(snapshot);
          return;
        }
        setActionError(res.error);
        return;
      }
      applyState({
        profile: res.item,
        ceiling,
        sources: backendState?.sources ?? [],
        unavailableFields: backendState?.unavailableFields ?? [...UNAVAILABLE_FIELDS],
      });
      setSavedRevision(res.item.revision);
      onSaved?.(res.item);
    } catch (e) {
      setActionError(e as CallError);
    } finally {
      setSaving(false);
    }
  };

  const handleRename = async () => {
    if (!profileId) return;
    setActionError(null);
    const res = await agentsApi.rename(profileId, workspaceId, baseRevision, renameTo);
    if (res.error) {
      setActionError(res.error);
      return;
    }
    await load();
    onSaved?.(res.item);
  };

  const handleDuplicate = async () => {
    if (!profileId) return;
    setActionError(null);
    const res = await agentsApi.duplicate(profileId, duplicateId.trim(), workspaceId);
    if (res.error) {
      setActionError(res.error);
      return;
    }
    onDuplicated?.(res.item);
    onSaved?.(res.item);
  };

  const handleDelete = async () => {
    if (!profileId) return;
    setDeleteError(null);
    const res = await agentsApi.remove(profileId, baseRevision);
    setConfirmingDelete(false);
    if (res.error) {
      // 删除失败要单独说：用户刚点的是"确认删除"，此时回一句通用的
      // "配置已被别处修改"会让人以为删除成功了。
      setDeleteError(res.error);
      return;
    }
    onDeleted?.(profileId);
  };

  const sourceOf = useMemo(() => {
    const map = new Map<string, string>();
    for (const s of backendState?.sources ?? []) map.set(`${s.field}/${s.value}`, s.source);
    return map;
  }, [backendState]);

  if (phase === "loading") {
    return (
      <div className="rounded-xl border border-[color:var(--anvil-border)] p-4">
        <AsyncState phase="loading" loadingLabel={t("agents.loading")} />
      </div>
    );
  }

  if (phase === "error") {
    return (
      <div className="rounded-xl border border-[color:var(--anvil-border)] p-4">
        <AsyncState phase="error" error={loadError} onRetry={() => void load()} />
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-center gap-2">
        <h2 className="text-sm font-medium text-[color:var(--anvil-foreground)]">
          {profileId ? draft.name || profileId : t("agents.newTitle")}
        </h2>
        <span className="font-mono text-[10px] text-[color:var(--anvil-muted)]">
          {t("agents.revision", { revision: baseRevision })}
        </span>
        {savedRevision !== null ? (
          <span className="text-[11px] text-[color:var(--anvil-success)]">
            {t("agents.saved", { revision: savedRevision })}
          </span>
        ) : null}
      </header>

      {actionError ? <ErrorAlert error={actionError} /> : null}

      {deleteError ? (
        <div
          role="alert"
          data-code={deleteError.code}
          className="rounded-md border border-[color:var(--anvil-danger-border)] bg-[color:var(--anvil-danger-surface)] px-2.5 py-2 text-[11px] text-[color:var(--anvil-danger)]"
        >
          <p className="flex items-center gap-1.5">
            <AlertTriangle className="h-3.5 w-3.5" aria-hidden />
            {t("agents.deleteFailed")}
            <span className="font-mono text-[10px]">{deleteError.code}</span>
          </p>
          <p className="mt-1 break-words">{formatError(deleteError)}</p>
        </div>
      ) : null}

      {conflict ? (
        <ConflictPanel
          conflict={conflict}
          onReload={() => void load()}
          onSaveAsCopy={(id) => {
            setNewId(id);
            void (async () => {
              const res = await agentsApi.save(id, workspaceId, 0, { ...draft, id });
              if (res.error) setActionError(res.error);
              else {
                setConflict(null);
                onDuplicated?.(res.item);
                onSaved?.(res.item);
              }
            })();
          }}
        />
      ) : null}

      {backendState?.missing && backendState.missing.length > 0 ? (
        <p className="rounded-md border border-[color:var(--anvil-warning-border)] bg-[color:var(--anvil-warning-surface)] px-2.5 py-2 text-[11px] text-[color:var(--anvil-warning)]">
          {t("agents.missingRefs", { refs: backendState.missing.map((m) => m.id).join(", ") })}
        </p>
      ) : null}

      {/* ---------------------------------------------------------- 基本信息 */}
      <section className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        <Field label={t("agents.name")}>
          <input
            aria-label={t("agents.name")}
            value={draft.name}
            onChange={(e) => patch({ name: e.target.value })}
            className={inputClass}
          />
        </Field>
        <Field label={t("agents.model")} hint={t("agents.modelHint")}>
          <select
            aria-label={t("agents.model")}
            value={draft.modelRef}
            onChange={(e) => patch({ modelRef: e.target.value })}
            className={inputClass}
          >
            {draft.modelRef && !resources.models.some((m) => m.id === draft.modelRef) ? (
              <option value={draft.modelRef}>{t("agents.modelUnavailable", { ref: draft.modelRef })}</option>
            ) : null}
            {resources.models.map((m) => (
              <option key={m.id} value={m.id}>
                {m.id}
              </option>
            ))}
          </select>
          {resources.unavailable?.models ? (
            <UnavailableLine error={resources.unavailable.models} />
          ) : null}
        </Field>
      </section>

      {/* ---------------------------------------------------------- 领域缺口 */}
      <section className="space-y-1.5 rounded-lg border border-[color:var(--anvil-border)] p-3">
        <h3 className="text-[11px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
          {t("agents.domainGapTitle")}
        </h3>
        {UNAVAILABLE_FIELDS.map((field) => (
          <div
            key={field}
            data-testid={`unavailable-${field}`}
            className="flex flex-wrap items-baseline gap-2 text-[11px] text-[color:var(--anvil-muted)]"
          >
            <span className="text-[color:var(--anvil-foreground)]">{t(`agents.fieldLabels.${field}`)}</span>
            <StatusBadge status="unavailable" />
            <span>{t("agents.unavailableField")}</span>
            {field === "reasoningLevel" ? (
              <span className="font-mono text-[10px] text-[color:var(--anvil-muted)]">
                {t("agents.reasoningLevels", { levels: thinkingLevels(resources, draft.modelRef) })}
              </span>
            ) : null}
            {field === "toolIDs" ? (
              <span className="font-mono text-[10px] text-[color:var(--anvil-muted)]">
                {t("agents.toolCatalog", { count: resources.tools.length })}
              </span>
            ) : null}
          </div>
        ))}
      </section>

      {/* ---------------------------------------------------------- 引用 */}
      <section className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <RefGroup
          title={t("agents.skills")}
          items={resources.skills.map((s) => ({
            id: s.id,
            label: s.id,
            unavailable: !s.available,
          }))}
          selected={draft.skillIDs ?? []}
          sourceOf={(id) => sourceOf.get(`skillIDs/${id}`)}
          loadError={resources.unavailable?.skills}
          onChange={(ids) => patch({ skillIDs: ids.length ? ids : null })}
        />
        <RefGroup
          title={t("agents.mcp")}
          items={resources.mcp.map((m) => ({
            id: m.id,
            label: m.id,
            unavailable: m.enabled === false,
          }))}
          selected={draft.mcpIDs ?? []}
          sourceOf={(id) => sourceOf.get(`mcpIDs/${id}`)}
          loadError={resources.unavailable?.mcp}
          onChange={(ids) => patch({ mcpIDs: ids.length ? ids : null })}
        />
      </section>

      {/* ---------------------------------------------------------- 权限 */}
      <section className="space-y-2 rounded-lg border border-[color:var(--anvil-border)] p-3">
        <h3 className="text-[11px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
          {t("agents.permissions")}
        </h3>
        <p className="text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
          {t("agents.permissionsHint")}
        </p>
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <Field label={t("agents.filesystem")}>
            <select
              aria-label={t("agents.filesystem")}
              value={draft.toolPolicy.filesystem}
              onChange={(e) =>
                patch({
                  toolPolicy: { ...draft.toolPolicy, filesystem: e.target.value as FilesystemScope },
                })
              }
              className={inputClass}
            >
              {FILESYSTEM_VALUES.map((value) => (
                <option
                  key={value}
                  value={value}
                  disabled={!filesystemAtMost(value, ceiling.filesystem)}
                >
                  {t(`agents.filesystemOptions.${value}`)}
                </option>
              ))}
            </select>
          </Field>
          <div className="flex flex-col justify-end gap-1.5 text-[11px] text-[color:var(--anvil-foreground)]">
            <label className="flex items-center gap-1.5">
              <input
                type="checkbox"
                aria-label={t("agents.network")}
                checked={draft.toolPolicy.network}
                disabled={!ceiling.network}
                onChange={(e) =>
                  patch({ toolPolicy: { ...draft.toolPolicy, network: e.target.checked } })
                }
              />
              {t("agents.network")}
            </label>
            <label className="flex items-center gap-1.5">
              <input
                type="checkbox"
                aria-label={t("agents.shell")}
                checked={draft.toolPolicy.shell}
                disabled={!ceiling.shell}
                onChange={(e) => patch({ toolPolicy: { ...draft.toolPolicy, shell: e.target.checked } })}
              />
              {t("agents.shell")}
            </label>
          </div>
        </div>
        <div className="space-y-0.5 border-t border-[color:var(--anvil-border)] pt-2 text-[10px] text-[color:var(--anvil-muted)]">
          <p>{t("agents.ceilingFilesystem", { value: t(`agents.filesystemOptions.${ceiling.filesystem}`) })}</p>
          <p>
            {t("agents.ceilingSource", {
              source: sourceLabel(ceiling.sources["permissions.filesystem"]),
            })}
          </p>
          <p>
            {t("agents.ceilingNetwork", {
              value: ceiling.network ? t("agents.valueOn") : t("agents.valueOff"),
              source: sourceLabel(ceiling.sources["permissions.network"]),
            })}
          </p>
          <p>
            {t("agents.ceilingShell", {
              value: ceiling.shell ? t("agents.valueOn") : t("agents.valueOff"),
              source: sourceLabel(ceiling.sources["permissions.shell"]),
            })}
          </p>
          {!ceiling.available ? <p>{t("agents.ceilingNoLayers")}</p> : null}
        </div>
      </section>

      {/* ---------------------------------------------------------- 记忆 */}
      <section className="space-y-2 rounded-lg border border-[color:var(--anvil-border)] p-3">
        <h3 className="text-[11px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
          {t("agents.memory")}
        </h3>
        <div className="flex flex-wrap items-center gap-4 text-[11px] text-[color:var(--anvil-foreground)]">
          <label className="flex items-center gap-1.5">
            <input
              type="checkbox"
              aria-label={t("agents.memoryEnabled")}
              checked={draft.memoryPolicy.enabled}
              onChange={(e) =>
                patch({ memoryPolicy: { ...draft.memoryPolicy, enabled: e.target.checked } })
              }
            />
            {t("agents.memoryEnabled")}
          </label>
          <label className="flex items-center gap-1.5">
            {t("agents.memoryMax")}
            <input
              type="number"
              min={0}
              aria-label={t("agents.memoryMax")}
              value={draft.memoryPolicy.maxEntries}
              onChange={(e) =>
                patch({
                  memoryPolicy: {
                    ...draft.memoryPolicy,
                    maxEntries: Number(e.target.value) || 0,
                  },
                })
              }
              className="w-16 rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-1.5 py-0.5 text-[11px]"
            />
          </label>
        </div>
        <p className="text-[10px] text-[color:var(--anvil-muted)]">{t("agents.memoryScopeHint")}</p>
        <ul className="space-y-0.5 text-[10px] text-[color:var(--anvil-muted)]">
          {resources.memoryScopes.map((scope) => (
            <li key={`${scope.scope}:${scope.workspaceId}`}>
              {t("agents.memoryScopeLine", {
                scope: t(`agents.memoryScopes.${scope.scope}`),
                state: scope.enabled ? t("agents.memoryOn") : t("agents.memoryOff"),
              })}
            </li>
          ))}
          {resources.memoryScopes.length === 0 ? (
            <li>{t("agents.memoryScopeUnavailable")}</li>
          ) : null}
        </ul>
        {resources.unavailable?.memory ? <UnavailableLine error={resources.unavailable.memory} /> : null}
      </section>

      {/* ---------------------------------------------------------- 动作 */}
      <section className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          onClick={() => void handleSave()}
          disabled={saving}
          className={primaryButton}
        >
          {saving ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden />
          ) : (
            <Save className="h-3.5 w-3.5" aria-hidden />
          )}
          {saving ? t("agents.saving") : t("agents.save")}
        </button>

        {profileId ? (
          <label className="flex items-center gap-1.5 text-[11px] text-[color:var(--anvil-muted)]">
            {t("agents.renameTo")}
            <input
              aria-label={t("agents.renameTo")}
              value={renameTo}
              onChange={(e) => setRenameTo(e.target.value)}
              className={inputClass}
            />
            <button type="button" onClick={() => void handleRename()} className={ghostButton}>
              {t("agents.renameAction")}
            </button>
          </label>
        ) : null}

        {profileId ? (
          <label className="flex items-center gap-1.5 text-[11px] text-[color:var(--anvil-muted)]">
            {t("agents.duplicateId")}
            <input
              aria-label={t("agents.duplicateId")}
              value={duplicateId}
              onChange={(e) => setDuplicateId(e.target.value)}
              className={inputClass}
            />
            <button
              type="button"
              onClick={() => void handleDuplicate()}
              disabled={!duplicateId.trim()}
              className={ghostButton}
            >
              <Copy className="h-3 w-3" aria-hidden />
              {t("agents.duplicateAction")}
            </button>
          </label>
        ) : null}

        {profileId ? (
          confirmingDelete ? null : (
            <button
              type="button"
              onClick={() => setConfirmingDelete(true)}
              className={dangerButton}
            >
              <Trash2 className="h-3 w-3" aria-hidden />
              {t("agents.delete")}
            </button>
          )
        ) : null}
      </section>

      {confirmingDelete ? (
        <section
          role="group"
          aria-label={t("agents.delete")}
          className="space-y-2 rounded-md border border-[color:var(--anvil-danger-border)] bg-[color:var(--anvil-danger-surface)] p-2.5"
        >
          <p className="text-[11px] text-[color:var(--anvil-foreground)]">
            {t("agents.deleteConfirm", { name: draft.name || profileId || "" })}
          </p>
          <div className="flex items-center gap-2">
            <button type="button" onClick={() => void handleDelete()} className={dangerButton}>
              {t("agents.deleteConfirmYes")}
            </button>
            <button type="button" onClick={() => setConfirmingDelete(false)} className={ghostButton}>
              {t("common.cancel")}
            </button>
          </div>
        </section>
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------- 子组件

function ErrorAlert({ error }: { error: CallError }) {
  const params = error.params ?? {};
  const reason =
    params.ceiling !== undefined
      ? t("agents.ceilingRejected", {
          field: fieldLabel(params.field ?? ""),
          requested: filesystemLabel(params.requested ?? ""),
          ceiling: filesystemLabel(params.ceiling ?? ""),
          source: sourceLabel(params.source),
        })
      : formatError(error);
  return (
    <div
      role="alert"
      data-code={error.code}
      className="rounded-md border border-[color:var(--anvil-danger-border)] bg-[color:var(--anvil-danger-surface)] px-2.5 py-2 text-[11px] text-[color:var(--anvil-danger)]"
    >
      <p className="flex items-center gap-1.5">
        <AlertTriangle className="h-3.5 w-3.5" aria-hidden />
        <span className="font-mono text-[10px]">{error.code}</span>
      </p>
      <p className="mt-1 break-words">{reason}</p>
    </div>
  );
}

function ConflictPanel({
  conflict,
  onReload,
  onSaveAsCopy,
}: {
  conflict: ProfileConflictResult;
  onReload: () => void;
  onSaveAsCopy: (id: string) => void;
}) {
  const [copyId, setCopyId] = useState("");
  const diff = conflict.diff ?? [];
  return (
    <div
      role="alert"
      className="space-y-2 rounded-md border border-[color:var(--anvil-warning-border)] bg-[color:var(--anvil-warning-surface)] p-2.5"
    >
      <p className="flex items-center gap-1.5 text-[11px] font-medium text-[color:var(--anvil-warning)]">
        <AlertTriangle className="h-3.5 w-3.5" aria-hidden />
        {t("agents.conflictTitle")}
      </p>
      <p className="text-[11px] text-[color:var(--anvil-foreground)]">
        {t("agents.conflictNothingWritten")}
      </p>
      <p className="font-mono text-[10px] text-[color:var(--anvil-muted)]">
        {t("agents.revision", { revision: conflict.current.revision })}
      </p>
      {diff.length === 0 ? (
        <p className="text-[11px] text-[color:var(--anvil-muted)]">{t("agents.conflictNoFieldDiff")}</p>
      ) : (
        <table className="w-full text-[11px]">
          <thead>
            <tr className="text-left text-[color:var(--anvil-muted)]">
              <th className="py-0.5 pr-2 font-normal">{t("agents.conflictField")}</th>
              <th className="py-0.5 pr-2 font-normal">{t("agents.conflictMine")}</th>
              <th className="py-0.5 font-normal">{t("agents.conflictTheirs")}</th>
            </tr>
          </thead>
          <tbody>
            {diff.map((d) => (
              <tr key={d.field} className="border-t border-[color:var(--anvil-border)] align-top">
                <td className="py-1 pr-2 font-mono text-[10px]">{d.field}</td>
                <td className="py-1 pr-2">{d.mine}</td>
                <td className="py-1">{d.theirs}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <div className="flex flex-wrap items-center gap-1.5">
        <button type="button" onClick={onReload} className={ghostButton}>
          {t("agents.conflictReload")}
        </button>
        <label className="flex items-center gap-1.5 text-[11px] text-[color:var(--anvil-muted)]">
          {t("agents.conflictCopyId")}
          <input
            aria-label={t("agents.conflictCopyId")}
            value={copyId}
            onChange={(e) => setCopyId(e.target.value)}
            className={inputClass}
          />
        </label>
        <button
          type="button"
          disabled={!copyId.trim()}
          onClick={() => onSaveAsCopy(copyId.trim())}
          className={ghostButton}
        >
          {t("agents.conflictSaveCopy")}
        </button>
      </div>
    </div>
  );
}

interface RefItem {
  id: string;
  label: string;
  unavailable: boolean;
}

function RefGroup({
  title,
  items,
  selected,
  sourceOf,
  loadError,
  onChange,
}: {
  title: string;
  items: RefItem[];
  selected: string[];
  sourceOf: (id: string) => string | undefined;
  loadError?: CallError;
  onChange: (ids: string[]) => void;
}) {
  const toggle = (id: string, next: boolean) => {
    const set = new Set(selected);
    if (next) set.add(id);
    else set.delete(id);
    onChange([...set].sort());
  };
  return (
    <div className="space-y-1 rounded-lg border border-[color:var(--anvil-border)] p-3">
      <p className="text-[11px] uppercase tracking-wide text-[color:var(--anvil-muted)]">{title}</p>
      {items.length === 0 ? (
        <p className="text-[11px] text-[color:var(--anvil-muted)]">{t("agents.noResources")}</p>
      ) : (
        <ul className="space-y-0.5">
          {items.map((item) => {
            const source = sourceOf(item.id);
            return (
              <li key={item.id} className="flex flex-wrap items-center gap-1.5 text-[11px]">
                <label className="flex items-center gap-1.5 text-[color:var(--anvil-foreground)]">
                  <input
                    type="checkbox"
                    checked={selected.includes(item.id)}
                    onChange={(e) => toggle(item.id, e.target.checked)}
                  />
                  <span className="font-mono text-[10px]">{item.label}</span>
                </label>
                {source ? (
                  <span className="text-[10px] text-[color:var(--anvil-muted)]">
                    {t("agents.source", { source: sourceLabel(source) })}
                  </span>
                ) : null}
                {item.unavailable ? <StatusBadge status="unavailable" /> : null}
              </li>
            );
          })}
        </ul>
      )}
      {/* 已选但资源目录里没有的引用：仍要显示（它在 profile 里，不能说没有）。 */}
      {selected
        .filter((id) => !items.some((item) => item.id === id))
        .map((id) => (
          <p key={id} className="flex items-center gap-1.5 text-[11px] text-[color:var(--anvil-warning)]">
            <span className="font-mono text-[10px]">{id}</span>
            <StatusBadge status="unavailable" />
          </p>
        ))}
      {loadError ? <UnavailableLine error={loadError} /> : null}
    </div>
  );
}

function UnavailableLine({ error }: { error: CallError }) {
  return (
    <p className="text-[10px] text-[color:var(--anvil-muted)]">
      {t("agents.resourceUnavailable")} <span className="font-mono">{error.code}</span>
    </p>
  );
}

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">{label}</span>
      {children}
      {hint ? <span className="text-[10px] text-[color:var(--anvil-muted)]">{hint}</span> : null}
    </label>
  );
}

// ---------------------------------------------------------------- 文案映射

function sourceLabel(source: string | undefined): string {
  const key = `agents.sourceLayers.${source ?? "unavailable"}`;
  return hasKey(key) ? t(key) : t("agents.sourceLayers.unavailable");
}

function fieldLabel(field: string): string {
  // 后端用 `permissions.filesystem` 这样的点分字段名；catalog 里是嵌套 key，
  // 点分会把 lookup 带偏，所以只取叶子名（filesystem / network / shell）。
  const leaf = field.includes(".") ? field.slice(field.lastIndexOf(".") + 1) : field;
  const key = `agents.fields.${leaf}`;
  return hasKey(key) ? t(key) : field;
}

function filesystemLabel(value: string): string {
  const key = `agents.filesystemOptions.${value}`;
  return hasKey(key) ? t(key) : value;
}

function thinkingLevels(resources: AgentResources, modelRef: string): string {
  const model = resources.models.find((m) => m.id === modelRef);
  const levels = model?.thinkingLevels ?? [];
  return levels.length ? levels.join(" / ") : t("common.notAvailable");
}

const inputClass =
  "rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-2 py-1 text-[12px] text-[color:var(--anvil-foreground)] outline-none focus:border-[color:var(--anvil-focus)] disabled:opacity-50";

const primaryButton =
  "flex items-center gap-1.5 rounded-md bg-[color:var(--anvil-accent)] px-3 py-1.5 text-xs font-medium text-[color:var(--anvil-accent-foreground)] hover:opacity-90 disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]";

const ghostButton =
  "flex items-center gap-1 rounded border border-[color:var(--anvil-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-40 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]";

const dangerButton =
  "flex items-center gap-1 rounded border border-[color:var(--anvil-danger-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-danger)] hover:bg-[color:var(--anvil-danger-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-danger)]";
