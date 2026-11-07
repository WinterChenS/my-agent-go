// 通用设置：语言与主题（U00 的偏好 CAS）。
//
// 三条纪律：
//   - 保存必须带回读到的那一版 revision；冲突显示 CONFIG_CONFLICT 并给出
//     "重新读取"，不静默覆盖另一个窗口的修改；
//   - 失败**不显示已保存**：旧实现点完按钮就打勾，写入失败也是绿的；
//   - 保存成功后立刻生效（语言 + 主题）并**再读一次后端**，界面显示的是
//     磁盘上的值，不是本地草稿。

import { useCallback, useEffect, useState } from "react";
import type { CallError, Preferences } from "../../types";
import { isLocale, setLocale, t } from "../../i18n";
import { currentMedia, isThemePreference, storeThemePreference, applyTheme } from "../../theme/theme";
import { AsyncState } from "../../components/states/AsyncState";
import { toCallError } from "../../app/useBackend";
import { settingsApi } from "./api";
import { SettingsSection, INPUT_CLASS, LABEL_CLASS, BUTTON_CLASS, type SectionState } from "./Section";

const DEFAULT_PREFERENCES: Preferences = { locale: "zh-CN", theme: "system" };

export function GeneralSettings() {
  const [phase, setPhase] = useState<SectionState>("loading");
  const [error, setError] = useState<CallError | null>(null);
  const [revision, setRevision] = useState(0);
  const [draft, setDraft] = useState<Preferences>(DEFAULT_PREFERENCES);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);

  const load = useCallback(async () => {
    setPhase("loading");
    setError(null);
    setSaved(false);
    try {
      const res = await settingsApi.getPreferences();
      if (res?.error) {
        setError(res.error);
        setPhase("error");
        return;
      }
      setDraft(res?.preferences ?? DEFAULT_PREFERENCES);
      setRevision(res?.revision ?? 0);
      setPhase("success");
    } catch (e) {
      setError(toCallError(e));
      setPhase("error");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function save() {
    setSaving(true);
    setError(null);
    setSaved(false);
    try {
      const res = await settingsApi.savePreferences(revision, draft);
      if (res?.error) {
        // 失败就是没保存：保留草稿、显示 code，绝不置"已保存"。
        setError(res.error);
        return;
      }
      // 先让本次会话生效（语言/主题即时可见），权威值仍以后端为准。
      const locale = isLocale(draft.locale) ? draft.locale : "zh-CN";
      const theme = isThemePreference(draft.theme) ? draft.theme : "system";
      setLocale(locale);
      storeThemePreference(theme);
      applyTheme(theme, currentMedia());
      // 持久化 + 重载：保存后重新读后端，确认磁盘上确实是这一版。
      const again = await settingsApi.getPreferences();
      if (again?.error) {
        setError(again.error);
        return;
      }
      setDraft(again?.preferences ?? draft);
      setRevision(again?.revision ?? revision + 1);
      setSaved(true);
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setSaving(false);
    }
  }

  return (
    <SettingsSection id="general" titleKey="settings.general.title" hintKey="settings.general.hint" state={phase}>
      {phase === "error" ? (
        <div className="space-y-2">
          <AsyncState phase="error" error={error} onRetry={() => void load()} />
          {error?.code === "CONFIG_CONFLICT" ? (
            <p className="text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
              {t("settings.general.conflictHint")}
            </p>
          ) : null}
        </div>
      ) : phase === "loading" ? (
        <AsyncState phase="loading" />
      ) : (
        <div className="space-y-3">
          <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
            <label className="flex flex-col gap-1">
              <span className={LABEL_CLASS}>{t("locale.label")}</span>
              <select
                aria-label={t("locale.label")}
                value={draft.locale}
                onChange={(e) => {
                  setSaved(false);
                  setDraft((d) => ({ ...d, locale: e.target.value }));
                }}
                className={INPUT_CLASS}
              >
                <option value="zh-CN">{t("locale.zh-CN")}</option>
                <option value="en-US">{t("locale.en-US")}</option>
              </select>
            </label>
            <label className="flex flex-col gap-1">
              <span className={LABEL_CLASS}>{t("theme.label")}</span>
              <select
                aria-label={t("theme.label")}
                value={draft.theme}
                onChange={(e) => {
                  setSaved(false);
                  setDraft((d) => ({ ...d, theme: e.target.value }));
                }}
                className={INPUT_CLASS}
              >
                <option value="system">{t("theme.system")}</option>
                <option value="light">{t("theme.light")}</option>
                <option value="dark">{t("theme.dark")}</option>
              </select>
            </label>
          </div>

          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={() => void save()}
              disabled={saving}
              className={BUTTON_CLASS}
            >
              {saving ? t("settings.general.saving") : t("settings.general.save")}
            </button>
            {saved ? (
              <span
                data-testid="settings-general-saved"
                role="status"
                className="text-[11px] text-[color:var(--anvil-success)]"
              >
                {t("settings.general.saved")}
              </span>
            ) : null}
          </div>

          {error ? (
            <div className="space-y-2">
              <AsyncState phase="error" error={error} onRetry={() => void load()} />
              {error.code === "CONFIG_CONFLICT" ? (
                <p className="text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
                  {t("settings.general.conflictHint")}
                </p>
              ) : null}
            </div>
          ) : null}
        </div>
      )}
    </SettingsSection>
  );
}
