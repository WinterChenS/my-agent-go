// 模型设置（C02 的真实绑定）。
//
// 只展示"定义"这一层事实：provider/模型名、API 类型、端点，以及凭证是
// **存在**（credentialRef 非空）还是**没有**——凭证本体永远不会出现在这里。
// 连接测试是真实调用（可能计费），所以只有点击才发生；结果是三态：
// 可达（带真实延迟）/ 不可达 / 根本没测成（外层错误码）。

import { useCallback, useEffect, useState } from "react";
import type { CallError, CurrentModelResult, ModelDefinition, ModelProbeResponse } from "../../types";
import { formatError, t } from "../../i18n";
import { AsyncState } from "../../components/states/AsyncState";
import { toCallError } from "../../app/useBackend";
import { settingsApi } from "./api";
import {
  SettingsSection,
  BUTTON_CLASS,
  LABEL_CLASS,
  MONO_CLASS,
  ROW_CLASS,
  type SectionState,
} from "./Section";

export function ModelSettings() {
  const [phase, setPhase] = useState<SectionState>("loading");
  const [error, setError] = useState<CallError | null>(null);
  const [models, setModels] = useState<ModelDefinition[]>([]);
  const [current, setCurrent] = useState<CurrentModelResult | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [probes, setProbes] = useState<Record<string, ModelProbeResponse | null>>({});
  const [notice, setNotice] = useState<"deferred" | null>(null);

  const load = useCallback(async () => {
    setPhase("loading");
    setError(null);
    try {
      const [listRes, currentRes] = await Promise.all([
        settingsApi.listModels(),
        settingsApi.currentModel(),
      ]);
      if (listRes?.error) {
        setError(listRes.error);
        setPhase("error");
        return;
      }
      setModels(listRes?.items ?? []);
      setCurrent(currentRes?.error ? null : currentRes ?? null);
      setPhase("success");
    } catch (e) {
      setError(toCallError(e));
      setPhase("error");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function testModel(id: string) {
    setBusyId(`test:${id}`);
    setProbes((p) => ({ ...p, [id]: null }));
    try {
      const res = await settingsApi.testModel(id);
      setProbes((p) => ({ ...p, [id]: res ?? null }));
    } catch (e) {
      setProbes((p) => ({
        ...p,
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
    setBusyId(`activate:${id}`);
    setNotice(null);
    setError(null);
    try {
      const res = await settingsApi.setActiveModel(id);
      if (res?.error) {
        setError(res.error);
        return;
      }
      if (res?.deferred) setNotice("deferred");
      await load();
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusyId(null);
    }
  }

  const sourceKey =
    current?.source === "resources"
      ? "settings.model.source.resources"
      : current?.source === "settings"
        ? "settings.model.source.settings"
        : "settings.model.source.none";

  return (
    <SettingsSection id="model" titleKey="settings.model.title" hintKey="settings.model.hint" state={phase}>
      {phase === "loading" ? <AsyncState phase="loading" /> : null}
      {phase === "error" ? (
        <AsyncState phase="error" error={error} onRetry={() => void load()} />
      ) : null}
      {phase === "success" ? (
        <div className="space-y-3">
          <div data-testid="model-current" className={ROW_CLASS}>
            <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
              <span className={LABEL_CLASS}>{t("settings.model.current")}</span>
              <span className="text-[12px] text-[color:var(--anvil-foreground)]">{t(sourceKey)}</span>
              {current?.ref ? <code className={MONO_CLASS}>{current.ref}</code> : null}
              {current?.provider ? (
                <code className={MONO_CLASS}>
                  {current.provider}/{current.model}
                </code>
              ) : null}
            </div>
          </div>

          {notice === "deferred" ? (
            <p className="text-[11px] text-[color:var(--anvil-warning)]">{t("settings.model.deferred")}</p>
          ) : null}

          {error ? <AsyncState phase="error" error={error} onRetry={() => void load()} /> : null}

          <div>
            <p className={LABEL_CLASS}>{t("settings.model.listTitle")}</p>
            {models.length === 0 ? (
              <AsyncState
                phase="empty"
                emptyTitle={t("settings.model.emptyTitle")}
                emptyHint={t("settings.model.emptyHint")}
              />
            ) : (
              <ul className="mt-1.5 space-y-1.5">
                {models.map((m) => {
                  const probe = probes[m.id] ?? null;
                  const active =
                    current?.source === "resources" && current.ref !== "" && current.ref === m.id;
                  return (
                    <li key={m.id} data-testid={`model-${m.id}`} className={ROW_CLASS}>
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="text-[12px] font-medium text-[color:var(--anvil-foreground)]">
                          {m.providerId}/{m.modelId}
                        </span>
                        {active ? (
                          <span className="rounded bg-[color:var(--anvil-accent-surface)] px-1.5 py-0.5 text-[10px] text-[color:var(--anvil-accent)]">
                            {t("settings.model.active")}
                          </span>
                        ) : null}
                        <span className="text-[10px] text-[color:var(--anvil-muted)]">{m.api}</span>
                        <span className={MONO_CLASS}>{m.baseUrl}</span>
                        <span className="text-[10px] text-[color:var(--anvil-muted)]">
                          {t("settings.model.credential")}
                          {": "}
                          {m.credentialRef
                            ? t("settings.model.credentialPresent")
                            : t("settings.model.credentialMissing")}
                        </span>
                      </div>

                      <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
                        <button
                          type="button"
                          className={BUTTON_CLASS}
                          disabled={busyId === `test:${m.id}`}
                          onClick={() => void testModel(m.id)}
                        >
                          {busyId === `test:${m.id}`
                            ? t("settings.model.testing")
                            : t("settings.model.test")}
                        </button>
                        <button
                          type="button"
                          className={BUTTON_CLASS}
                          disabled={active || busyId === `activate:${m.id}`}
                          onClick={() => void activate(m.id)}
                        >
                          {active
                            ? t("settings.model.active")
                            : busyId === `activate:${m.id}`
                              ? t("settings.model.activating")
                              : t("settings.model.activate")}
                        </button>
                        {probe ? (
                          <span
                            data-testid={`model-test-${m.id}`}
                            className="text-[10px] text-[color:var(--anvil-muted)]"
                          >
                            {probe.error ? (
                              <>
                                {t("settings.model.unreachable")}
                                <code className="ml-1 font-mono">{probe.error.code}</code>
                              </>
                            ) : probe.result.reachable ? (
                              t("settings.model.reachable", { latency: probe.result.latencyMs })
                            ) : (
                              <>
                                {t("settings.model.unreachable")}
                                {probe.result.error ? (
                                  <code className="ml-1 font-mono">{probe.result.error.code}</code>
                                ) : null}
                              </>
                            )}
                            {probe.error ? (
                              <span className="ml-1">{formatError(probe.error)}</span>
                            ) : null}
                          </span>
                        ) : null}
                      </div>
                    </li>
                  );
                })}
              </ul>
            )}
          </div>
        </div>
      ) : null}
    </SettingsSection>
  );
}
