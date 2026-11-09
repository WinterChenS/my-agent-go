// 模型配置诊断（MDIAG）。
//
// 为什么要有这一块：模型存得住、启动却只说「读不到有效配置：缺少必要的配置」。
// 那句话对"没设为当前"、"编译 models.json 时被跳过"、"缺 baseUrl"、"凭证引用
// 悬空"完全一样，而这几种的修复动作完全不同——用户只能一遍遍试。
//
// 纪律：
//   - 只显示后端（DiagnoseModelConfig）算出来的事实，界面不重算编译规则；
//   - 诊断是**只读**动作：不写文件、不启动 Pi、不调用模型，因此不会计费；
//   - 不展示原始 JSON：用户要的是"哪条不行、为什么"，不是自己读一遍 models.json；
//   - 密钥只有引用，没有值。
//
// 组件刻意只由点击触发（不挂在挂载上）：诊断会读凭证库，不该在打开设置页时
// 静默发生；也让"检查完再截屏/贴 issue"成为用户的主动动作。

import { useState } from "react";
import type { CallError, ModelDiagnosis as Diagnosis } from "../../types";
import { formatError, t } from "../../i18n";
import { toCallError } from "../../app/useBackend";
import { settingsApi } from "./api";

const FIELDS: { key: "providerId" | "modelId" | "baseUrl" | "api" | "credentialRef"; labelKey: string }[] = [
  { key: "providerId", labelKey: "settings.model.diagnosis.fields.provider" },
  { key: "modelId", labelKey: "settings.model.diagnosis.fields.model" },
  { key: "baseUrl", labelKey: "settings.model.diagnosis.fields.baseUrl" },
  { key: "api", labelKey: "settings.model.diagnosis.fields.api" },
  { key: "credentialRef", labelKey: "settings.model.diagnosis.fields.credentialRef" },
];

/** selectedSource 映射到文案 key；未知来源退回 none，不编造一个来源。 */
function sourceKey(source: string | undefined): string {
  switch (source) {
    case "resources":
      return "settings.model.source.resources";
    case "settings":
      return "settings.model.source.settings";
    default:
      return "settings.model.source.none";
  }
}

export function ModelDiagnosis() {
  const [busy, setBusy] = useState(false);
  const [diag, setDiag] = useState<Diagnosis | null>(null);
  const [error, setError] = useState<CallError | null>(null);

  const check = async () => {
    setBusy(true);
    try {
      const res = await settingsApi.diagnoseModelConfig();
      if (res?.error) {
        // 结构化失败（例如读不到配置）：说清是哪一步，不假装"配置没问题"。
        setError(res.error);
        setDiag(null);
        return;
      }
      setError(null);
      setDiag(res);
    } catch (e) {
      setError(toCallError(e));
      setDiag(null);
    } finally {
      setBusy(false);
    }
  };

  const models = diag?.models ?? [];
  const skipped = diag?.skipped ?? [];
  const ready = diag?.ready ?? false;

  return (
    <section data-testid="model-diagnosis" className="mb-3 rounded-lg border border-[color:var(--anvil-border)] px-2.5 py-2">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <h2 className="text-[11px] font-medium text-[color:var(--anvil-foreground)]">
          {t("settings.model.diagnosis.title")}
        </h2>
        <span className="text-[10px] text-[color:var(--anvil-muted)]">{t("settings.model.diagnosis.hint")}</span>
        <button
          type="button"
          data-testid="model-diagnosis-button"
          disabled={busy}
          onClick={() => void check()}
          className="ml-auto rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-foreground)] disabled:opacity-50"
        >
          {busy ? t("settings.model.diagnosis.checking") : t("settings.model.diagnosis.button")}
        </button>
      </div>

      {error ? (
        <p
          role="alert"
          data-testid="model-diagnosis-error"
          className="mt-1.5 rounded border border-[color:var(--anvil-danger-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-danger)]"
        >
          {t("settings.model.diagnosis.unavailable", { reason: formatError(error) })}
        </p>
      ) : null}

      {diag ? (
        <div className="mt-1.5 space-y-1.5">
          {/* 结论先行：不可用时原因必须是最醒目的一行，而不是让人翻下面的列表。 */}
          {ready ? (
            <p
              data-testid="model-diagnosis-ready"
              className="rounded border border-[color:var(--anvil-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)]"
            >
              {t("settings.model.diagnosis.ready")}
            </p>
          ) : (
            <p
              role="alert"
              data-testid="model-diagnosis-reason"
              className="rounded border border-[color:var(--anvil-danger-border)] bg-[color:var(--anvil-danger-surface)] px-2 py-1 text-[11px] font-medium text-[color:var(--anvil-danger)]"
            >
              {t("settings.model.diagnosis.notReady")}
              {diag.reason ? ` · ${diag.reason}` : ""}
            </p>
          )}

          <div data-testid="model-diagnosis-selected" className="flex flex-wrap items-baseline gap-x-2 text-[10px]">
            <span className="text-[color:var(--anvil-muted)]">{t("settings.model.diagnosis.selected")}</span>
            <code className="font-mono text-[color:var(--anvil-foreground)]">
              {diag.selectedRef
                ? diag.selectedRef
                : t("settings.model.diagnosis.selectedNone")}
            </code>
            <span className="rounded bg-[color:var(--anvil-surface)] px-1 py-0.5 text-[9px] text-[color:var(--anvil-muted)]">
              {t("settings.model.diagnosis.selectedSource")} · {t(sourceKey(diag.selectedSource))}
            </span>
          </div>

          {diag.selected ? (
            <ul data-testid="model-diagnosis-selected-fields" className="space-y-0.5">
              {FIELDS.map(({ key, labelKey }) => {
                const value = diag.selected?.[key] ?? "";
                return (
                  <li key={key} data-testid={`model-diagnosis-field-${key}`} className="flex flex-wrap items-baseline gap-x-2">
                    <span className="w-24 shrink-0 text-[10px] text-[color:var(--anvil-muted)]">{t(labelKey)}</span>
                    <code className="break-all font-mono text-[10px] text-[color:var(--anvil-foreground)]">
                      {value === "" ? t("settings.model.diagnosis.fields.empty") : value}
                    </code>
                  </li>
                );
              })}
            </ul>
          ) : null}

          <div className="text-[10px] text-[color:var(--anvil-muted)]">
            {t("settings.model.diagnosis.compiled", { count: diag.compiled })}
          </div>

          <div>
            <h3 className="text-[10px] font-medium text-[color:var(--anvil-muted)]">
              {t("settings.model.diagnosis.modelsTitle")}
            </h3>
            {models.length === 0 ? (
              <p data-testid="model-diagnosis-models-empty" className="text-[10px] text-[color:var(--anvil-muted)]">
                {t("settings.model.diagnosis.modelsEmpty")}
              </p>
            ) : (
              <ul data-testid="model-diagnosis-models" className="mt-0.5 space-y-0.5">
                {models.map((m) => {
                  const ok = m.status === "ok";
                  return (
                    <li
                      key={m.modelId}
                      data-testid={`model-diagnosis-model-${m.modelId}`}
                      className="flex flex-wrap items-baseline gap-x-2 text-[10px]"
                    >
                      <code className="font-mono text-[color:var(--anvil-foreground)]">{m.modelId}</code>
                      <span
                        className={
                          ok
                            ? "text-[color:var(--anvil-muted)]"
                            : "text-[color:var(--anvil-warning)]"
                        }
                      >
                        {ok
                          ? t("settings.model.diagnosis.status.ok")
                          : t("settings.model.diagnosis.status.skipped", { reason: m.reason ?? "" })}
                      </span>
                    </li>
                  );
                })}
              </ul>
            )}
          </div>

          {/* 跳过段只在不可用时出现：可用时它只是噪音，而不可用时它才是答案。 */}
          {!ready && skipped.length > 0 ? (
            <div data-testid="model-diagnosis-skipped">
              <h3 className="text-[10px] font-medium text-[color:var(--anvil-muted)]">
                {t("settings.model.diagnosis.skippedTitle")}
              </h3>
              <ul className="mt-0.5 space-y-0.5">
                {skipped.map((sk) => (
                  <li
                    key={sk.modelId}
                    data-testid={`model-diagnosis-skip-${sk.modelId}`}
                    className="text-[10px] text-[color:var(--anvil-warning)]"
                  >
                    {t("settings.model.diagnosis.skippedItem", { modelId: sk.modelId, reason: sk.reason })}
                  </li>
                ))}
              </ul>
            </div>
          ) : null}

          <div data-testid="model-diagnosis-models-json" className="flex flex-wrap items-baseline gap-x-2 text-[10px]">
            <span className="text-[color:var(--anvil-muted)]">{t("settings.model.diagnosis.modelsJsonPath")}</span>
            <code className="break-all font-mono text-[color:var(--anvil-foreground)]">{diag.modelsJsonPath}</code>
            <span className="text-[color:var(--anvil-muted)]">
              {diag.modelsJsonExists
                ? t("settings.model.diagnosis.modelsJsonPresent")
                : t("settings.model.diagnosis.modelsJsonAbsent")}
            </span>
          </div>
        </div>
      ) : null}
    </section>
  );
}
