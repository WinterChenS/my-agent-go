// 模型资源列表。
//
// 列表只展示"定义"这一层事实：模型 ID、发给 API 的模型名、API 类型、端点、
// 是否当前在用、以及最近一次扫描给出的端点健康。能力（工具调用/视觉）只在
// 连接测试后才有结论，所以默认不显示"支持/不支持"——没测过就说支持是最容易
// 骗到自己的做法。
//
// 探测失败的端点下的模型会显示为不可用，并且不能设为当前：那正是"看起来能用、
// 一发请求就失败"的情形，界面不能替用户假装它没事。

import { useState } from "react";
import { Cpu, Plus } from "lucide-react";
import type { CallError, ModelDefinition, ModelOptions, ModelProbeResponse } from "../../types";
import { formatError, t } from "../../i18n";
import { toCallError } from "../../app/useBackend";
import { EmptyState } from "../../components/StateViews";
import { ModelEditor } from "./ModelEditor";
import { modelsApi } from "./api";
import type { EndpointHealth } from "./ModelsMaintenance";

export interface ModelsSectionProps {
  models: ModelDefinition[];
  options: ModelOptions;
  revision: number;
  currentRef: string;
  currentSource: "resources" | "settings" | "none";
  currentProvider: string;
  currentModelName: string;
  /** 最近一次扫描得到的端点健康，按 baseUrl 索引。 */
  health?: Record<string, EndpointHealth>;
  onChanged: () => void;
}

export function ModelsSection({
  models,
  options,
  revision,
  currentRef,
  currentSource,
  currentProvider,
  currentModelName,
  health,
  onChanged,
}: ModelsSectionProps) {
  const [editing, setEditing] = useState<ModelDefinition | null | undefined>(undefined);
  const [error, setError] = useState<CallError | null>(null);
  const [confirming, setConfirming] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  // 连接测试是逐条的显式动作：结果按模型显示，失败不折叠成"不可用"三个字。
  const [probes, setProbes] = useState<Record<string, ModelProbeResponse | null>>({});

  async function test(id: string) {
    setBusyId(`test:${id}`);
    setProbes((prev) => ({ ...prev, [id]: null }));
    setError(null);
    try {
      const res = await modelsApi.test(id);
      setProbes((prev) => ({ ...prev, [id]: res ?? null }));
    } catch (e) {
      setProbes((prev) => ({
        ...prev,
        [id]: {
          result: {
            reachable: false,
            toolCalling: "unknown",
            vision: "unknown",
            latencyMs: 0,
            providerId: "",
            modelId: "",
          },
          error: toCallError(e),
        },
      }));
    } finally {
      setBusyId(null);
    }
  }

  async function activate(id: string) {
    setBusyId(id);
    setError(null);
    setNotice(null);
    try {
      const res = await modelsApi.setActive(id);
      if (res.error) {
        setError(res.error);
        return;
      }
      if (res.deferred) {
        setNotice(t("library.models.notices.deferred"));
      }
      onChanged();
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusyId(null);
    }
  }

  async function remove(id: string) {
    setBusyId(id);
    setError(null);
    try {
      const res = await modelsApi.remove(revision, id);
      if (res.error) {
        setError(res.error);
        return;
      }
      setConfirming(null);
      onChanged();
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusyId(null);
    }
  }

  return (
    <section>
      <header className="mb-2 flex items-center justify-between">
        <div>
          <h2 className="text-sm font-medium text-[color:var(--anvil-foreground)]">
            {t("library.models.title")}
          </h2>
          <p className="mt-0.5 text-[11px] text-[color:var(--anvil-muted)]">
            {t("library.models.subtitle")}
          </p>
        </div>
        <button
          onClick={() => {
            setEditing(null);
            setNotice(null);
            setError(null);
          }}
          className="flex items-center gap-1 rounded-md border border-[color:var(--anvil-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
        >
          <Plus className="h-3 w-3" aria-hidden /> {t("library.models.actions.addManual")}
        </button>
      </header>

      {currentSource === "settings" && (
        <p className="mb-2 rounded border border-[color:var(--anvil-warning-border)] bg-[color:var(--anvil-warning-surface)] px-2 py-1 text-[10px] text-[color:var(--anvil-warning)]">
          {t("library.models.notices.settingsSource", {
            provider: currentProvider || t("common.unknown"),
            model: currentModelName || t("common.unknown"),
          })}
        </p>
      )}

      {error && (
        <p role="alert" className="mb-2 text-[11px] text-[color:var(--anvil-danger)]">
          <span className="font-mono text-[10px]">{error.code}</span> {formatError(error)}
          {error.message ? (
            <span className="ml-1 font-mono text-[10px] text-[color:var(--anvil-muted)]">
              {error.message}
            </span>
          ) : null}
        </p>
      )}
      {notice && (
        <p role="status" className="mb-2 text-[11px] text-[color:var(--anvil-warning)]">
          {notice}
        </p>
      )}

      {models.length === 0 && editing === undefined ? (
        <EmptyState
          title={t("library.models.empty.title")}
          hint={t("library.models.empty.hint")}
          icon={<Cpu className="h-4 w-4" aria-hidden />}
        />
      ) : (
        <ul className="space-y-1">
          {models.map((m) => {
            const isCurrent = currentRef === m.id;
            // C01 之前的条目是 {provider, baseUrl, model}，没有 providerId/modelId。
            // 它既不能编译（会生成空 provider 键），也说不清上下文窗口有多大——
            // 与其显示"上下文 0 · 输出 0"这种看起来像事实的数字，不如直说要重建。
            const isLegacy = !m.providerId || !m.modelId;
            const probe = health?.[m.baseUrl];
            const unreachable = !!probe && !probe.reachable;
            const modelProbe = probes[m.id] ?? null;
            return (
              <li
                key={m.id}
                data-testid={`model-${m.id}`}
                className="rounded-md border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-2.5 py-2"
              >
                <div className="flex items-center gap-2">
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-1.5">
                      <span className="truncate text-[12px] text-[color:var(--anvil-foreground)]">{m.id}</span>
                      {isCurrent && (
                        <span className="shrink-0 rounded bg-[color:var(--anvil-success-surface)] px-1 py-0.5 text-[9px] text-[color:var(--anvil-success)]">
                          {t("library.models.labels.current")}
                        </span>
                      )}
                      {m.enabled === false && (
                        <span className="shrink-0 rounded bg-[color:var(--anvil-surface)] px-1 py-0.5 text-[9px] text-[color:var(--anvil-muted)]">
                          {t("library.models.labels.disabled")}
                        </span>
                      )}
                      {isLegacy && (
                        <span className="shrink-0 rounded bg-[color:var(--anvil-warning-surface)] px-1 py-0.5 text-[9px] text-[color:var(--anvil-warning)]">
                          {t("library.models.labels.legacy")}
                        </span>
                      )}
                      {unreachable && (
                        <span className="shrink-0 rounded bg-[color:var(--anvil-danger-surface)] px-1 py-0.5 text-[9px] text-[color:var(--anvil-danger)]">
                          {t("library.models.labels.unavailable")}
                        </span>
                      )}
                      {!unreachable && probe ? (
                        <span className="shrink-0 rounded bg-[color:var(--anvil-success-surface)] px-1 py-0.5 text-[9px] text-[color:var(--anvil-success)]">
                          {t("library.models.labels.reachable", { latency: probe.latencyMs })}
                        </span>
                      ) : null}
                    </div>
                    {isLegacy ? (
                      <div className="truncate text-[10px] text-[color:var(--anvil-warning)]">
                        {t("library.models.labels.legacyHint")}
                      </div>
                    ) : (
                      <div className="truncate text-[10px] text-[color:var(--anvil-muted)]">
                        {m.providerId}/{m.modelId} · {m.api || t("library.models.labels.providerInherited")} ·{" "}
                        {m.baseUrl || t("library.models.labels.providerInherited")}
                      </div>
                    )}
                    {!isLegacy && (
                      <div className="truncate text-[10px] text-[color:var(--anvil-muted)]">
                        {t("library.models.labels.context", { context: m.contextWindow, max: m.maxOutput })} ·{" "}
                        {m.credentialRef
                          ? t("library.models.labels.credential", { ref: m.credentialRef })
                          : t("library.models.labels.noCredential")}
                      </div>
                    )}
                    {modelProbe ? (
                      <div
                        data-testid={`model-test-${m.id}`}
                        className="text-[10px] text-[color:var(--anvil-muted)]"
                      >
                        {modelProbe.error ? (
                          <>
                            {t("library.models.labels.unreachable")}{" "}
                            <span className="font-mono">{modelProbe.error.code}</span>{" "}
                            {formatError(modelProbe.error)}
                          </>
                        ) : modelProbe.result.reachable ? (
                          t("library.models.labels.reachable", { latency: modelProbe.result.latencyMs })
                        ) : (
                          <>
                            {t("library.models.labels.unreachable")}
                            {modelProbe.result.error ? (
                              <span className="ml-1 font-mono">{modelProbe.result.error.code}</span>
                            ) : null}
                          </>
                        )}
                      </div>
                    ) : null}
                  </div>
                  <div className="flex shrink-0 items-center gap-1">
                    <button
                      onClick={() => void test(m.id)}
                      disabled={busyId === `test:${m.id}`}
                      className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-50"
                    >
                      {busyId === `test:${m.id}`
                        ? t("library.models.actions.testing")
                        : t("library.models.actions.test")}
                    </button>
                    <button
                      onClick={() => {
                        setEditing(m);
                        setError(null);
                        setNotice(null);
                      }}
                      className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
                    >
                      {t("library.models.actions.edit")}
                    </button>
                    <button
                      onClick={() => void activate(m.id)}
                      disabled={busyId === m.id || isCurrent || isLegacy || unreachable}
                      title={
                        isLegacy
                          ? t("library.models.labels.legacyHint")
                          : unreachable
                            ? t("library.models.labels.unreachable")
                            : undefined
                      }
                      className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-40 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
                    >
                      {isCurrent ? t("library.models.actions.inUse") : t("library.models.actions.setActive")}
                    </button>
                    {confirming === m.id ? (
                      <>
                        <button
                          onClick={() => void remove(m.id)}
                          disabled={busyId === m.id}
                          className="rounded border border-[color:var(--anvil-danger-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-danger)] hover:bg-[color:var(--anvil-danger-surface)] disabled:opacity-50"
                        >
                          {t("library.models.actions.confirmDelete")}
                        </button>
                        <button
                          onClick={() => setConfirming(null)}
                          className="rounded px-1 py-0.5 text-[11px] text-[color:var(--anvil-muted)] hover:text-[color:var(--anvil-foreground)]"
                        >
                          {t("library.models.actions.cancel")}
                        </button>
                      </>
                    ) : (
                      <button
                        onClick={() => setConfirming(m.id)}
                        className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
                      >
                        {t("library.models.actions.delete")}
                      </button>
                    )}
                  </div>
                </div>
              </li>
            );
          })}
        </ul>
      )}

      {editing !== undefined && (
        <div className="mt-3 rounded-lg border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] p-3">
          <h3 className="mb-2 text-[12px] font-medium text-[color:var(--anvil-foreground)]">
            {editing
              ? t("library.models.editor.editTitle", { id: editing.id })
              : t("library.models.editor.createTitle")}
          </h3>
          <ModelEditor
            model={editing}
            options={options}
            revision={revision}
            onSaved={() => {
              setEditing(undefined);
              onChanged();
            }}
            onClose={() => setEditing(undefined)}
          />
        </div>
      )}
    </section>
  );
}
