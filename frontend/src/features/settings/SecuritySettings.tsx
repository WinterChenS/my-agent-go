// 安全设置（SEC 系列的界面侧事实）。
//
// 这里只写**可验证的事实**，不写"我们很安全"：
//   - 逐个模型报告凭证是"已存于系统凭证库"还是"未保存"——依据是
//     ModelDefinition.credentialRef，界面永远拿不到凭证本体；
//   - 凭证库本身是否可用缺少绑定（`CredentialStoreStatus`），所以显示
//     unavailable 而不是假装正常；
//   - 模型仍来自设置里的旧字段时，API Key 会以明文写进 settings.json：
//     这是真实风险，必须说出来（并且不提供任何回填/重写密钥的输入框）。

import { useCallback, useEffect, useState } from "react";
import type { CallError, ModelDefinition } from "../../types";
import { t } from "../../i18n";
import { AsyncState } from "../../components/states/AsyncState";
import { StatusBadge } from "../../components/status/StatusBadge";
import { toCallError } from "../../app/useBackend";
import { useShell } from "../../app/shell";
import { settingsApi } from "./api";
import { SettingsSection, LABEL_CLASS, MONO_CLASS, ROW_CLASS, type SectionState } from "./Section";

export function SecuritySettings() {
  const shell = useShell();
  const [phase, setPhase] = useState<SectionState>("loading");
  const [error, setError] = useState<CallError | null>(null);
  const [models, setModels] = useState<ModelDefinition[]>([]);

  const load = useCallback(async () => {
    setPhase("loading");
    setError(null);
    try {
      const res = await settingsApi.listModels();
      if (res?.error) {
        setError(res.error);
        setPhase("error");
        return;
      }
      setModels(res?.items ?? []);
      setPhase("success");
    } catch (e) {
      setError(toCallError(e));
      setPhase("error");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const usesLegacy = !shell.settings?.modelRef;

  return (
    <SettingsSection id="security" titleKey="settings.security.title" hintKey="settings.security.hint">
      <div data-testid="security-store-status" className={ROW_CLASS}>
        <div className="flex flex-wrap items-center gap-2">
          <span className={LABEL_CLASS}>{t("settings.security.storeStatus")}</span>
          <StatusBadge status="unavailable" />
        </div>
        <p className="mt-0.5 text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
          {t("settings.security.storeUnavailable")}
        </p>
      </div>

      <div className="mt-2">
        <p className={LABEL_CLASS}>{t("settings.security.credentialsTitle")}</p>
        <p className="mt-0.5 text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
          {t("settings.security.credentialsHint")}
        </p>
        {phase === "loading" ? <AsyncState phase="loading" /> : null}
        {phase === "error" ? (
          <AsyncState phase="error" error={error} onRetry={() => void load()} />
        ) : null}
        {phase === "success" ? (
          models.length === 0 ? (
            <AsyncState
              phase="empty"
              emptyTitle={t("settings.security.credentialsEmptyTitle")}
              emptyHint={t("settings.security.credentialsEmptyHint")}
            />
          ) : (
            <ul className="mt-1.5 space-y-1">
              {models.map((m) => (
                <li
                  key={m.id}
                  data-testid={`security-cred-${m.id}`}
                  className="flex flex-wrap items-baseline gap-2 text-[11px]"
                >
                  <span className="text-[color:var(--anvil-foreground)]">
                    {m.providerId}/{m.modelId}
                  </span>
                  <span className="text-[10px] text-[color:var(--anvil-muted)]">
                    {m.credentialRef
                      ? t("settings.security.credentialPresent")
                      : t("settings.security.credentialMissing")}
                  </span>
                  {m.credentialRef ? <code className={MONO_CLASS}>{m.credentialRef}</code> : null}
                </li>
              ))}
            </ul>
          )
        ) : null}
      </div>

      <div data-testid="security-legacy" className={`mt-2 ${ROW_CLASS}`}>
        <p className={LABEL_CLASS}>{t("settings.security.legacyFallback")}</p>
        <p className="mt-0.5 text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
          {usesLegacy ? t("settings.security.legacyActive") : t("settings.security.legacyInactive")}
        </p>
        <p className="mt-0.5 text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
          {t("settings.security.noSecret")}
        </p>
      </div>
    </SettingsSection>
  );
}
