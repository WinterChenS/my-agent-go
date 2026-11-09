// 模型配置的来源（M1ENV）。
//
// 为什么这一块必须存在：ANVIL_PROVIDER / ANVIL_MODEL / ANVIL_BASE_URL /
// ANVIL_MODEL_API_KEY 只在 settings.json **没写**对应字段时才生效。可"生效了"
// 与"没生效"在屏幕上如果不显示，用户改了设置而模型没变，就和"脚本静默顶掉了
// 我的配置"完全无法区分——这类假旋钮（改了没反应）花在这上面的时间远比多一块
// 面板多。
//
// 纪律：
//   - 只显示后端算出来的事实（EffectiveConfig.model），界面不推断优先级；
//   - 密钥只显示**引用**（env:ANVIL_MODEL_API_KEY），永远不回显值；
//   - 读不到就显示错误，不假装"来源是设置"。

import { useEffect, useState } from "react";
import type { CallError, ConfigFieldSource, ModelConfigProvenance } from "../../types";
import { formatError, t } from "../../i18n";
import { toCallError } from "../../app/useBackend";
import { settingsApi } from "./api";

/** 行定义：顺序固定（provider → model → baseUrl → apiKeyRef），与后端同一顺序。 */
const FIELDS: { key: keyof ModelConfigProvenance; labelKey: string }[] = [
  { key: "provider", labelKey: "settings.model.env.fields.provider" },
  { key: "model", labelKey: "settings.model.env.fields.model" },
  { key: "baseUrl", labelKey: "settings.model.env.fields.baseUrl" },
  { key: "apiKeyRef", labelKey: "settings.model.env.fields.apiKeyRef" },
];

/** sourceKey 把来源映射到文案 key；未知来源退回 none，不编造一个来源。 */
function sourceKey(field: ConfigFieldSource | undefined): string {
  switch (field?.source) {
    case "settings":
      return "settings.model.env.source.settings";
    case "env":
      return "settings.model.env.source.env";
    case "default":
      return "settings.model.env.source.default";
    default:
      return "settings.model.env.source.none";
  }
}

export function ModelProvenance() {
  const [model, setModel] = useState<ModelConfigProvenance | null>(null);
  const [error, setError] = useState<CallError | null>(null);

  useEffect(() => {
    let alive = true;
    void (async () => {
      try {
        const res = await settingsApi.effectiveConfig();
        if (!alive) return;
        if (res?.error) {
          setError(res.error);
          setModel(null);
          return;
        }
        setError(null);
        setModel(res?.model ?? null);
      } catch (e) {
        if (alive) setError(toCallError(e));
      }
    })();
    return () => {
      alive = false;
    };
  }, []);

  if (error) {
    return (
      <p
        role="alert"
        className="mb-2 rounded border border-[color:var(--anvil-danger-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-danger)]"
      >
        {t("settings.model.env.unavailable", { reason: formatError(error) })}
      </p>
    );
  }
  if (!model) return null;

  const overridden = model.overriddenByEnv ?? [];

  return (
    <section
      data-testid="model-provenance"
      className="mb-3 rounded-lg border border-[color:var(--anvil-border)] px-2.5 py-2"
    >
      <div className="flex flex-wrap items-baseline gap-x-2">
        <h2 className="text-[11px] font-medium text-[color:var(--anvil-foreground)]">
          {t("settings.model.env.title")}
        </h2>
        <span className="text-[10px] text-[color:var(--anvil-muted)]">{t("settings.model.env.hint")}</span>
      </div>

      <ul className="mt-1.5 space-y-1">
        {FIELDS.map(({ key, labelKey }) => {
          const field = model[key] as ConfigFieldSource | undefined;
          const source = field?.source ?? "none";
          const value = field?.value ?? "";
          return (
            <li key={key} data-testid={`model-provenance-${key}`} className="flex flex-wrap items-baseline gap-x-2">
              <span className="w-20 shrink-0 text-[10px] text-[color:var(--anvil-muted)]">{t(labelKey)}</span>
              <code className="break-all font-mono text-[10px] text-[color:var(--anvil-foreground)]">
                {value === "" ? t("settings.model.env.empty") : value}
              </code>
              <span
                className={
                  "shrink-0 rounded px-1 py-0.5 text-[9px] " +
                  (source === "env"
                    ? "bg-[color:var(--anvil-warning-surface)] text-[color:var(--anvil-warning)]"
                    : "bg-[color:var(--anvil-surface)] text-[color:var(--anvil-muted)]")
                }
              >
                {t(sourceKey(field))}
                {source === "env" && field?.envVar ? ` · ${field.envVar}` : ""}
              </span>
            </li>
          );
        })}
      </ul>

      {overridden.length > 0 ? (
        <p
          data-testid="model-provenance-env-warning"
          className="mt-1.5 rounded border border-[color:var(--anvil-warning-border)] px-2 py-1 text-[10px] text-[color:var(--anvil-warning)]"
        >
          {t("settings.model.env.overridden", { fields: overridden.join(", ") })}
        </p>
      ) : null}
    </section>
  );
}
