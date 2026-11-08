// 模型维护（MP1）：模型列表 + 本地扫描 + 供应商。
//
// 用户原话："需要可以自动读取本地已有的模型，并且可以额外添加模型和供应商。"
//
// 这一页把三件事放在一起，因为它们回答的是同一个问题——"我现在能用哪个模型、它到底通不通"：
//   - 扫描本地模型：只有回环端点；每个端点给出可达/延迟/稳定错误码；
//   - 供应商：id + 端点 + 协议；模型可以留空继承；
//   - 模型列表：编辑/删除/测试/设为当前重新有入口。
//
// 没有任何一处把"扫描失败"显示成"没有模型"：扫不通就是扫不通，错误码要露出来。

import { useCallback, useEffect, useState } from "react";
import { Download, Plus, Radar, Trash2 } from "lucide-react";
import type {
  CallError,
  CurrentModelResult,
  ModelDefinition,
  ModelOptions,
} from "../../types";
import { AsyncState } from "../../components/states/AsyncState";
import { formatError, t } from "../../i18n";
import { toCallError } from "../../app/useBackend";
import { ModelsSection } from "./ModelsSection";
import {
  IMPORT_DEFAULTS,
  modelsApi,
  type DiscoveredEndpoint,
  type DiscoveryFailure,
  type ProviderProbe,
  type ProviderSummary,
} from "./api";

/** 最近一次扫描得到的端点健康，按 baseUrl 索引。 */
export interface EndpointHealth {
  reachable: boolean;
  latencyMs: number;
  insecure?: boolean;
  error?: DiscoveryFailure;
  /** 这次健康数据来自"自动发现"还是"用户显式扫描"。 */
  source: "discovery" | "probe";
}

const EMPTY_OPTIONS: ModelOptions = { apis: [], inputKinds: ["text"], thinkingLevels: [] };

export function ModelsMaintenance() {
  const [models, setModels] = useState<ModelDefinition[]>([]);
  const [options, setOptions] = useState<ModelOptions>(EMPTY_OPTIONS);
  const [revision, setRevision] = useState(0);
  const [current, setCurrent] = useState<CurrentModelResult | null>(null);
  const [providers, setProviders] = useState<ProviderSummary[]>([]);
  const [health, setHealth] = useState<Record<string, EndpointHealth>>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<CallError | null>(null);

  // silent=true 用于保存/导入后的重读：把整块换成"正在加载"会顺手把
  // 刚显示的"已切换/已导入"提示一起卸载掉，用户会以为操作没发生。
  const load = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    setError(null);
    try {
      const [list, providerList, currentModel] = await Promise.all([
        modelsApi.list(),
        modelsApi.providers(),
        modelsApi.current(),
      ]);
      if (list.error) {
        setError(list.error);
        setModels([]);
      } else {
        setModels(list.items ?? []);
        setOptions(list.options ?? EMPTY_OPTIONS);
        setRevision(list.revision);
      }
      setProviders(providerList.error ? [] : (providerList.items ?? []));
      setCurrent(currentModel.error ? null : currentModel);
    } catch (e) {
      setError(toCallError(e));
      setModels([]);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const recordHealth = useCallback((items: DiscoveredEndpoint[], source: EndpointHealth["source"]) => {
    if (items.length === 0) return;
    setHealth((prev) => {
      const next = { ...prev };
      for (const it of items) {
        next[it.baseUrl] = {
          reachable: it.reachable,
          latencyMs: it.latencyMs,
          insecure: it.insecure,
          error: it.error,
          source,
        };
      }
      return next;
    });
  }, []);

  const recordProbe = useCallback((probe: ProviderProbe) => {
    setHealth((prev) => ({
      ...prev,
      [probe.baseUrl]: {
        reachable: probe.reachable,
        latencyMs: probe.latencyMs,
        insecure: probe.insecure,
        error: probe.error,
        source: "probe",
      },
    }));
  }, []);

  // 当前模型的来源必须说清楚：设置里的旧字段、资源库模型，还是尚未配置。
  const sourceKey =
    current?.source === "resources"
      ? "settings.model.source.resources"
      : current?.source === "settings"
        ? "settings.model.source.settings"
        : "settings.model.source.none";

  const phase = loading ? "loading" : error ? "error" : "success";

  return (
    <AsyncState
      phase={phase}
      error={error}
      onRetry={phase === "error" ? () => void load() : undefined}
      emptyTitle={t("library.models.empty.title")}
      emptyHint={t("library.models.empty.hint")}
    >
      <div className="space-y-4">
        <div data-testid="model-current" className="rounded-lg border border-[color:var(--anvil-border)] px-2.5 py-2">
          <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
            <span className="text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
              {t("settings.model.current")}
            </span>
            <span className="text-[12px] text-[color:var(--anvil-foreground)]">{t(sourceKey)}</span>
            {current?.ref ? (
              <code className="break-all font-mono text-[10px] text-[color:var(--anvil-muted)]">
                {current.ref}
              </code>
            ) : null}
            {current?.provider ? (
              <code className="break-all font-mono text-[10px] text-[color:var(--anvil-muted)]">
                {current.provider}/{current.model}
              </code>
            ) : null}
          </div>
        </div>

        <DiscoveryPanel
          onScanned={(items) => recordHealth(items, "discovery")}
          onImported={() => void load(true)}
        />

        <ProvidersPanel
          apis={options.apis}
          providers={providers}
          onProbed={recordProbe}
          onChanged={() => void load(true)}
        />

        <ModelsSection
          models={models}
          options={options}
          revision={revision}
          currentRef={current?.ref ?? ""}
          currentSource={current?.source ?? "none"}
          currentProvider={current?.provider ?? ""}
          currentModelName={current?.model ?? ""}
          health={health}
          onChanged={() => void load(true)}
        />
      </div>
    </AsyncState>
  );
}

// ---------------------------------------------------------------- 本地扫描

function discoveryErrorText(f: DiscoveryFailure | undefined): string {
  if (!f) return t("library.models.discovery.error");
  const key = `library.models.discovery.errors.${f.code}`;
  const text = t(key);
  // catalog 缺口：显示通用文案 + 稳定错误码，而不是把 key 直接摆在界面上。
  return text === key ? `${t("library.models.discovery.error")} (${f.code})` : text;
}

function DiscoveryPanel({
  onScanned,
  onImported,
}: {
  onScanned: (items: DiscoveredEndpoint[]) => void;
  onImported: () => void;
}) {
  const [endpoints, setEndpoints] = useState<DiscoveredEndpoint[] | null>(null);
  const [scanning, setScanning] = useState(false);
  const [error, setError] = useState<CallError | null>(null);
  const [selected, setSelected] = useState<Record<string, string[]>>({});
  const [busyKind, setBusyKind] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  async function scan() {
    setScanning(true);
    setError(null);
    setNotice(null);
    try {
      const res = await modelsApi.discover();
      if (res.error) {
        setError(res.error);
        setEndpoints([]);
        return;
      }
      const items = res.items ?? [];
      setEndpoints(items);
      setSelected({});
      onScanned(items);
    } catch (e) {
      setError(toCallError(e));
      setEndpoints([]);
    } finally {
      setScanning(false);
    }
  }

  function toggle(kind: string, id: string) {
    setSelected((prev) => {
      const cur = prev[kind] ?? [];
      const next = cur.includes(id) ? cur.filter((v) => v !== id) : [...cur, id];
      return { ...prev, [kind]: next };
    });
  }

  async function importSelected(ep: DiscoveredEndpoint) {
    const ids = selected[ep.kind] ?? [];
    if (ids.length === 0) {
      setNotice(t("library.models.notices.nothingImported"));
      return;
    }
    setBusyKind(ep.kind);
    setError(null);
    setNotice(null);
    try {
      // 自动发现只碰回环，所以不需要 insecure 确认。
      const res = await modelsApi.importModels(ep.kind, ep.baseUrl, ep.api, ids, IMPORT_DEFAULTS, false);
      if (res.error) {
        setError(res.error);
        return;
      }
      setNotice(
        t("library.models.notices.imported", {
          imported: (res.imported ?? []).length,
          updated: (res.updated ?? []).length,
        })
      );
      setSelected((prev) => ({ ...prev, [ep.kind]: [] }));
      onImported();
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusyKind(null);
    }
  }

  const reachable = (endpoints ?? []).filter((e) => e.reachable);
  const scanned = endpoints !== null;

  return (
    <section className="rounded-lg border border-[color:var(--anvil-border)] p-3">
      <header className="mb-2 flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h2 className="text-sm font-medium text-[color:var(--anvil-foreground)]">
            {t("library.models.discovery.title")}
          </h2>
          <p className="mt-0.5 text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
            {t("library.models.discovery.hint")}
          </p>
        </div>
        <button
          type="button"
          onClick={() => void scan()}
          disabled={scanning}
          className="flex shrink-0 items-center gap-1 rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-50"
        >
          <Radar className="h-3 w-3" aria-hidden />{" "}
          {scanning ? t("library.models.actions.scanning") : t("library.models.actions.scanLocal")}
        </button>
      </header>

      {error ? (
        <p role="alert" className="mb-2 rounded border border-[color:var(--anvil-danger-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-danger)]">
          {formatError(error)}
        </p>
      ) : null}
      {notice ? (
        <p role="status" className="mb-2 text-[11px] text-[color:var(--anvil-warning)]">
          {notice}
        </p>
      ) : null}

      {scanned && reachable.length === 0 ? (
        <p className="text-[11px] text-[color:var(--anvil-muted)]">
          {t("library.models.discovery.empty")}
        </p>
      ) : null}

      {scanned && (endpoints ?? []).length > 0 ? (
        <p className="mb-2 text-[10px] text-[color:var(--anvil-muted)]">
          {t("library.models.discovery.importHint")}
        </p>
      ) : null}

      <ul className="space-y-2">
        {(endpoints ?? []).map((ep) => {
          const ids = selected[ep.kind] ?? [];
          return (
            <li key={ep.kind}>
              <article
                data-testid={`endpoint-${ep.kind}`}
                className="space-y-1.5 rounded-md border border-[color:var(--anvil-border)] p-2.5"
              >
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-[12px] font-medium text-[color:var(--anvil-foreground)]">
                    {ep.kind}
                  </span>
                  <Chip
                    label={ep.reachable ? t("library.models.labels.reachable", { latency: ep.latencyMs }) : t("library.models.labels.unavailable")}
                    tone={ep.reachable ? "success" : "danger"}
                  />
                  <span className="truncate font-mono text-[10px] text-[color:var(--anvil-muted)]">
                    {ep.baseUrl}
                  </span>
                  <span className="text-[10px] text-[color:var(--anvil-muted)]">{ep.api}</span>
                  {ep.insecure ? (
                    <span className="text-[10px] text-[color:var(--anvil-warning)]">
                      {t("library.models.labels.insecure")}
                    </span>
                  ) : null}
                </div>

                {ep.error ? (
                  <p className="text-[10px] text-[color:var(--anvil-danger)]">
                    {discoveryErrorText(ep.error)}
                    {ep.error.detail ? (
                      <span className="ml-1 font-mono text-[color:var(--anvil-muted)]">
                        {ep.error.detail}
                      </span>
                    ) : null}
                  </p>
                ) : null}

                {ep.reachable ? (
                  <>
                    <ul className="space-y-0.5">
                      {(ep.models ?? []).map((m) => (
                        <li key={m.id}>
                          <label className="flex items-center gap-1.5 text-[11px] text-[color:var(--anvil-foreground)]">
                            <input
                              type="checkbox"
                              aria-label={m.id}
                              checked={ids.includes(m.id)}
                              onChange={() => toggle(ep.kind, m.id)}
                              className="h-3 w-3 accent-sky-500"
                            />
                            <span className="font-mono">{m.id}</span>
                          </label>
                        </li>
                      ))}
                    </ul>
                    <div className="flex flex-wrap items-center gap-2 pt-0.5">
                      <button
                        type="button"
                        onClick={() => void importSelected(ep)}
                        disabled={busyKind === ep.kind || ids.length === 0}
                        className="flex items-center gap-1 rounded-md border border-[color:var(--anvil-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-foreground)] disabled:opacity-40"
                      >
                        <Download className="h-3 w-3" aria-hidden />{" "}
                        {busyKind === ep.kind
                          ? t("library.models.actions.importing")
                          : t("library.models.actions.importSelected")}
                      </button>
                      <span className="text-[10px] text-[color:var(--anvil-muted)]">
                        {t("library.models.labels.modelCount", { count: (ep.models ?? []).length })}
                      </span>
                    </div>
                  </>
                ) : null}
              </article>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

// ---------------------------------------------------------------- 供应商

function ProvidersPanel({
  apis,
  providers,
  onProbed,
  onChanged,
}: {
  apis: string[];
  providers: ProviderSummary[];
  onProbed: (probe: ProviderProbe) => void;
  onChanged: () => void;
}) {
  const [formOpen, setFormOpen] = useState(false);
  const [id, setId] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [api, setApi] = useState(apis[0] ?? "openai-completions");
  const [allowInsecure, setAllowInsecure] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<CallError | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [probing, setProbing] = useState<string | null>(null);
  const [probes, setProbes] = useState<Record<string, ProviderProbe>>({});
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null);
  const [deleteError, setDeleteError] = useState<CallError | null>(null);

  const effectiveAPI = apis.includes(api) ? api : apis[0] ?? "openai-completions";

  async function save() {
    setSaving(true);
    setError(null);
    setNotice(null);
    try {
      const res = await modelsApi.saveProvider(id.trim(), baseUrl.trim(), effectiveAPI, allowInsecure);
      if (res.error) {
        setError(res.error);
        return;
      }
      setNotice(t("library.models.notices.providerSaved", { id: id.trim() }));
      setFormOpen(false);
      setId("");
      setBaseUrl("");
      setAllowInsecure(false);
      onChanged();
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setSaving(false);
    }
  }

  async function probe(p: ProviderSummary) {
    setProbing(p.id);
    setError(null);
    try {
      const res = await modelsApi.probeProvider(p.baseUrl, p.api, "", false);
      if (res.error) {
        setError(res.error);
        return;
      }
      setProbes((prev) => ({ ...prev, [p.id]: res.probe }));
      onProbed(res.probe);
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setProbing(null);
    }
  }

  async function remove(providerID: string) {
    setDeleteError(null);
    try {
      const res = await modelsApi.deleteProvider(providerID);
      if (res.error) {
        setDeleteError(res.error);
        return;
      }
      setConfirmDelete(null);
      setNotice(t("library.models.notices.providerDeleted", { id: providerID }));
      onChanged();
    } catch (e) {
      setDeleteError(toCallError(e));
    }
  }

  return (
    <section className="rounded-lg border border-[color:var(--anvil-border)] p-3">
      <header className="mb-2 flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h2 className="text-sm font-medium text-[color:var(--anvil-foreground)]">
            {t("library.models.providers.title")}
          </h2>
          <p className="mt-0.5 text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
            {t("library.models.providers.hint")}
          </p>
        </div>
        <button
          type="button"
          onClick={() => {
            setFormOpen((v) => !v);
            setError(null);
            setNotice(null);
          }}
          className="flex shrink-0 items-center gap-1 rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)]"
        >
          <Plus className="h-3 w-3" aria-hidden /> {t("library.models.actions.addProvider")}
        </button>
      </header>

      {error ? (
        <p role="alert" className="mb-2 rounded border border-[color:var(--anvil-danger-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-danger)]">
          {formatError(error)}
        </p>
      ) : null}
      {notice ? (
        <p role="status" className="mb-2 text-[11px] text-[color:var(--anvil-warning)]">
          {notice}
        </p>
      ) : null}

      {formOpen ? (
        <div className="mb-3 grid grid-cols-1 gap-2 rounded-md border border-[color:var(--anvil-border)] p-2.5 sm:grid-cols-3">
          <label className="block">
            <span className="mb-0.5 block text-[11px] text-[color:var(--anvil-muted)]">
              {t("library.models.provider.idLabel")}
            </span>
            <input
              aria-label={t("library.models.provider.idLabel")}
              value={id}
              onChange={(e) => setId(e.target.value)}
              placeholder={t("library.models.provider.idPlaceholder")}
              className="w-full rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 text-[12px] text-[color:var(--anvil-foreground)]"
            />
          </label>
          <label className="block">
            <span className="mb-0.5 block text-[11px] text-[color:var(--anvil-muted)]">
              {t("library.models.provider.baseUrlLabel")}
            </span>
            <input
              aria-label={t("library.models.provider.baseUrlLabel")}
              value={baseUrl}
              onChange={(e) => setBaseUrl(e.target.value)}
              placeholder={t("library.models.provider.baseUrlPlaceholder")}
              className="w-full rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 text-[12px] text-[color:var(--anvil-foreground)]"
            />
          </label>
          <label className="block">
            <span className="mb-0.5 block text-[11px] text-[color:var(--anvil-muted)]">
              {t("library.models.provider.apiLabel")}
            </span>
            <select
              aria-label={t("library.models.provider.apiLabel")}
              value={effectiveAPI}
              onChange={(e) => setApi(e.target.value)}
              className="w-full rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 text-[12px] text-[color:var(--anvil-foreground)]"
            >
              {apis.map((v) => (
                <option key={v} value={v}>
                  {v}
                </option>
              ))}
            </select>
          </label>
          <label className="flex items-center gap-1.5 text-[10px] text-[color:var(--anvil-warning)] sm:col-span-3">
            <input
              type="checkbox"
              checked={allowInsecure}
              onChange={(e) => setAllowInsecure(e.target.checked)}
              className="h-3 w-3 accent-sky-500"
            />
            {t("library.models.provider.allowInsecure")}
          </label>
          <div className="flex items-center gap-2 sm:col-span-3">
            <button
              type="button"
              onClick={() => void save()}
              disabled={saving || id.trim() === "" || baseUrl.trim() === ""}
              className="rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-[11px] text-[color:var(--anvil-foreground)] disabled:opacity-40"
            >
              {saving
                ? t("library.models.actions.savingProvider")
                : t("library.models.actions.saveProvider")}
            </button>
          </div>
        </div>
      ) : null}

      {providers.length === 0 ? (
        <p className="text-[11px] text-[color:var(--anvil-muted)]">
          {t("library.models.providers.empty")}
        </p>
      ) : (
        <ul className="space-y-1.5">
          {providers.map((p) => {
            const probeResult = probes[p.id];
            return (
              <li
                key={p.id}
                data-testid={`provider-${p.id}`}
                className="space-y-1 rounded-md border border-[color:var(--anvil-border)] px-2.5 py-2"
              >
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-[12px] font-medium text-[color:var(--anvil-foreground)]">
                    {p.id}
                  </span>
                  <span className="truncate font-mono text-[10px] text-[color:var(--anvil-muted)]">
                    {p.baseUrl || t("library.models.labels.providerInherited")}
                  </span>
                  <span className="text-[10px] text-[color:var(--anvil-muted)]">
                    {p.api || t("library.models.labels.providerInherited")}
                  </span>
                  <span className="text-[10px] text-[color:var(--anvil-muted)]">
                    {t("library.models.labels.modelCount", { count: (p.modelIds ?? []).length })}
                  </span>
                  {p.insecure ? (
                    <span className="text-[10px] text-[color:var(--anvil-warning)]">
                      {t("library.models.labels.insecure")}
                    </span>
                  ) : null}
                  {probeResult ? (
                    <Chip
                      label={
                        probeResult.reachable
                          ? t("library.models.labels.reachable", { latency: probeResult.latencyMs })
                          : t("library.models.labels.unreachable")
                      }
                      tone={probeResult.reachable ? "success" : "danger"}
                    />
                  ) : null}
                </div>
                {probeResult && !probeResult.reachable ? (
                  <p className="text-[10px] text-[color:var(--anvil-danger)]">
                    {discoveryErrorText(probeResult.error)}
                  </p>
                ) : null}
                {probeResult && probeResult.reachable ? (
                  <p className="text-[10px] text-[color:var(--anvil-muted)]">
                    {t("library.models.provider.probeResult", {
                      count: (probeResult.models ?? []).length,
                    })}
                  </p>
                ) : null}
                {deleteError && confirmDelete === p.id ? (
                  <p role="alert" className="text-[10px] text-[color:var(--anvil-danger)]">
                    {formatError(deleteError)}
                    {deleteError.message ? (
                      <span className="ml-1 font-mono text-[color:var(--anvil-muted)]">
                        {deleteError.message}
                      </span>
                    ) : null}
                  </p>
                ) : null}
                <div className="flex flex-wrap items-center gap-2">
                  <button
                    type="button"
                    onClick={() => void probe(p)}
                    disabled={probing === p.id}
                    className="rounded-md border border-[color:var(--anvil-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-foreground)] disabled:opacity-50"
                  >
                    {probing === p.id
                      ? t("library.models.actions.probing")
                      : t("library.models.actions.probe")}
                  </button>
                  {confirmDelete === p.id ? (
                    <>
                      <button
                        type="button"
                        onClick={() => void remove(p.id)}
                        className="flex items-center gap-1 rounded-md border border-[color:var(--anvil-danger-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-danger)]"
                      >
                        <Trash2 className="h-3 w-3" aria-hidden />{" "}
                        {t("library.models.actions.confirmDeleteProvider")}
                      </button>
                      <button
                        type="button"
                        onClick={() => {
                          setConfirmDelete(null);
                          setDeleteError(null);
                        }}
                        className="rounded-md px-2 py-0.5 text-[10px] text-[color:var(--anvil-muted)]"
                      >
                        {t("library.models.actions.cancel")}
                      </button>
                    </>
                  ) : (
                    <button
                      type="button"
                      onClick={() => {
                        setConfirmDelete(p.id);
                        setDeleteError(null);
                      }}
                      className="rounded-md px-2 py-0.5 text-[10px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)]"
                    >
                      {t("library.models.actions.deleteProvider")}
                    </button>
                  )}
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

function Chip({ label, tone }: { label: string; tone: "success" | "danger" }) {
  const cls =
    tone === "success"
      ? "border-[color:var(--anvil-success-border)] text-[color:var(--anvil-success)]"
      : "border-[color:var(--anvil-danger-border)] text-[color:var(--anvil-danger)]";
  return <span className={`rounded border px-1.5 py-0.5 text-[10px] ${cls}`}>{label}</span>;
}
