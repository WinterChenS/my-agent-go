// Agent 页（U03 / UI07）：单 Agent/Profile 的产品化编辑入口。
//
// 这一页只有两件事：左栏列出真实存在的 Profile，右栏渲染选中的那个的编辑器。
// 列表与工作区来自壳层的后端快照（U01），编辑器自己按 ID 读取现场（U03 绑定）。
//
// 它刻意没有的东西：子 Agent、自动委派、第三方运行时入口（规格 §5.4）。
// 也没有"新建后自动成功"：创建同样要走 SaveAgentProfile 的校验与 CAS。

import { useCallback, useEffect, useState } from "react";
import { AsyncState } from "../../components/states/AsyncState";
import { formatError, t } from "../../i18n";
import { useShell } from "../../app/shell";
import type { CallError, ModelDefinition, Profile } from "../../types";
import { AgentEditor } from "./AgentEditor";
import { agentsApi } from "./api";

export interface AgentsPageProps {
  onNavigate?: (path: string) => void;
  route?: { query?: URLSearchParams };
}

const BLANK_PROFILE: Profile = {
  id: "",
  name: "",
  modelRef: "",
  skillIDs: null,
  mcpIDs: null,
  toolPolicy: { filesystem: "none", network: false, shell: false },
  memoryPolicy: { enabled: false, maxEntries: 0 },
  revision: 0,
};

export function AgentsPage({ onNavigate, route }: AgentsPageProps) {
  const shell = useShell();
  const selectedId = route?.query?.get("agent") ?? "";
  const workspaceId = shell.currentWorkspace?.id ?? "";

  const [models, setModels] = useState<ModelDefinition[]>([]);
  const [newId, setNewId] = useState("");
  const [newModel, setNewModel] = useState("");
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<CallError | null>(null);

  const navigate = useCallback(
    (path: string) => {
      onNavigate?.(path);
    },
    [onNavigate]
  );

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const res = await agentsApi.resources();
        if (cancelled) return;
        setModels(res.models);
        setNewModel((current) => current || (res.models[0]?.id ?? ""));
      } catch {
        // 读不到模型清单不是"没有模型"：创建按钮保持禁用，编辑器里会显示 unavailable。
        if (!cancelled) setModels([]);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const create = async () => {
    const id = newId.trim();
    setCreating(true);
    setCreateError(null);
    try {
      const res = await agentsApi.save(id, workspaceId, 0, {
        ...BLANK_PROFILE,
        id,
        name: id,
        modelRef: newModel,
      });
      if (res.error) {
        setCreateError(res.error);
        return;
      }
      setNewId("");
      shell.refreshProfiles();
      navigate(`/agents?agent=${encodeURIComponent(res.item.id)}`);
    } catch (e) {
      setCreateError(e as CallError);
    } finally {
      setCreating(false);
    }
  };

  const loading = shell.backend.loading && shell.profiles.length === 0;

  return (
    <div className="mx-auto grid max-w-4xl grid-cols-1 gap-4 px-4 py-5 lg:grid-cols-[220px_minmax(0,1fr)]">
      <aside className="space-y-3">
        <div className="space-y-1">
          <h1 className="text-sm font-medium text-[color:var(--anvil-foreground)]">
            {t("agents.title")}
          </h1>
          <p className="text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
            {t("agents.subtitle")}
          </p>
        </div>

        {loading ? (
          <AsyncState phase="loading" loadingLabel={t("agents.loading")} />
        ) : shell.backend.error ? (
          <AsyncState phase="error" error={shell.backend.error} onRetry={shell.refreshProfiles} />
        ) : shell.profiles.length === 0 ? (
          <AsyncState
            phase="empty"
            emptyTitle={t("agents.emptyTitle")}
            emptyHint={t("agents.emptyHint")}
          />
        ) : (
          <ul aria-label={t("agents.listTitle")} className="space-y-1">
            {shell.profiles.map((p) => (
              <li key={p.id}>
                <button
                  type="button"
                  onClick={() => navigate(`/agents?agent=${encodeURIComponent(p.id)}`)}
                  aria-current={p.id === selectedId ? "true" : undefined}
                  className="flex w-full items-center justify-between gap-2 rounded-md border border-[color:var(--anvil-border)] px-2 py-1.5 text-left hover:bg-[color:var(--anvil-surface)]"
                >
                  <span className="truncate text-[12px] text-[color:var(--anvil-foreground)]">
                    {p.name || p.id}
                  </span>
                  <span className="shrink-0 font-mono text-[10px] text-[color:var(--anvil-muted)]">
                    {p.modelRef || t("common.notAvailable")}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}

        <section className="space-y-1.5 rounded-lg border border-[color:var(--anvil-border)] p-2.5">
          <p className="text-[11px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
            {t("agents.newTitle")}
          </p>
          <label className="flex flex-col gap-1">
            <span className="text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
              {t("agents.newId")}
            </span>
            <input
              aria-label={t("agents.newId")}
              value={newId}
              onChange={(e) => setNewId(e.target.value)}
              className={inputCls}
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
              {t("agents.model")}
            </span>
            <select
              aria-label={t("agents.newModel")}
              value={newModel}
              onChange={(e) => setNewModel(e.target.value)}
              className={inputCls}
            >
              {models.length === 0 ? (
                <option value="">{t("agents.noResources")}</option>
              ) : (
                models.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.id}
                  </option>
                ))
              )}
            </select>
          </label>
          <p className="text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
            {t("agents.createHint")}
          </p>
          <button
            type="button"
            onClick={() => void create()}
            disabled={creating || !newId.trim() || !newModel}
            className="w-full rounded-md border border-[color:var(--anvil-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-40"
          >
            {creating ? t("agents.saving") : t("agents.create")}
          </button>
          {createError ? (
            <div role="alert" className="space-y-0.5 text-[11px] text-[color:var(--anvil-danger)]">
              <span className="font-mono text-[10px]">{createError.code}</span>
              <p>{formatError(createError)}</p>
            </div>
          ) : null}
        </section>
      </aside>

      <main className="min-w-0">
        {selectedId ? (
          <AgentEditor
            key={selectedId}
            profileId={selectedId}
            workspaceId={workspaceId}
            onSaved={() => shell.refreshProfiles()}
            onDuplicated={(p) => navigate(`/agents?agent=${encodeURIComponent(p.id)}`)}
            onDeleted={() => {
              shell.refreshProfiles();
              navigate("/agents");
            }}
          />
        ) : (
          <div className="flex h-full flex-col items-center justify-center gap-2 rounded-xl border border-dashed border-[color:var(--anvil-border)] px-6 py-10 text-center">
            <p className="text-[11px] text-[color:var(--anvil-muted)]">{t("agents.selectHint")}</p>
          </div>
        )}
      </main>
    </div>
  );
}

const inputCls =
  "rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-2 py-1 text-[12px] text-[color:var(--anvil-foreground)] outline-none focus:border-[color:var(--anvil-focus)] disabled:opacity-50";
