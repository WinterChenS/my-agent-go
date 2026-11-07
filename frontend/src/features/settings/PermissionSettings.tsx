// 权限设置（F04 的真实上限）。
//
// 这一节只展示**后端算出来的上限**：文件系统/网络/shell 三档来自
// `AgentProfileCeiling`（系统 ∩ 工作区），并逐字段给出"这个上限是谁定的"。
// 界面不做二次推断，也不提供后端不支持的能力：
//   - 网络在 V1 恒为关闭（上限来自系统策略），所以这里没有"打开网络"的开关，
//     只有一句"不可用"和它的来源；
//   - shell 的默认值来自 settings.shellAllowed（真实字段），保存走 SaveSettings；
//   - 保存是否成功**由后端快照判定**，不由点击判定（失败不能是绿的）。

import { useCallback, useEffect, useState } from "react";
import type { CallError, Settings } from "../../types";
import { t } from "../../i18n";
import { AsyncState } from "../../components/states/AsyncState";
import { toCallError } from "../../app/useBackend";
import { useShell } from "../../app/shell";
import { settingsApi, type AgentCeiling } from "./api";
import {
  SettingsSection,
  BUTTON_CLASS,
  LABEL_CLASS,
  MONO_CLASS,
  ROW_CLASS,
  type SectionState,
} from "./Section";

/** 上限来源词表来自配置层（system/user/default/workspace），未知值原样显示。 */
function sourceLabel(source: string | undefined): string {
  if (!source) return "";
  const key = `settings.permissions.sources.${source}`;
  return key === t(key) ? source : t(key);
}

function grantedLabel(value: boolean): string {
  return value ? t("settings.permissions.allowed") : t("settings.permissions.denied");
}

function sameSettings(a: Settings, b: Settings): boolean {
  return (
    a.piPath === b.piPath &&
    a.nodePath === b.nodePath &&
    a.provider === b.provider &&
    a.baseUrl === b.baseUrl &&
    a.model === b.model &&
    a.apiKey === b.apiKey &&
    a.modelRef === b.modelRef &&
    a.workspace === b.workspace &&
    a.shellAllowed === b.shellAllowed
  );
}

export function PermissionSettings() {
  const shell = useShell();
  const workspaceId = shell.currentWorkspace?.id ?? "";

  const [phase, setPhase] = useState<SectionState>("loading");
  const [error, setError] = useState<CallError | null>(null);
  const [ceiling, setCeiling] = useState<AgentCeiling | null>(null);
  const [pending, setPending] = useState<Settings | null>(null);
  const [saving, setSaving] = useState(false);

  const load = useCallback(async () => {
    setPhase("loading");
    setError(null);
    try {
      const res = await settingsApi.agentProfileCeiling(workspaceId);
      if (res?.error) {
        setError(res.error);
        setPhase("error");
        return;
      }
      setCeiling(res?.ceiling ?? null);
      setPhase("success");
    } catch (e) {
      setError(toCallError(e));
      setPhase("error");
    }
  }, [workspaceId]);

  useEffect(() => {
    void load();
  }, [load]);

  const settings = shell.settings;
  // "已保存"只由后端快照判定：设置里确实是这一版，且没有失败遗留。
  const saved = Boolean(
    pending && settings && !shell.actionError && sameSettings(settings, pending)
  );

  async function saveShell(next: Settings) {
    setPending(next);
    setSaving(true);
    try {
      await shell.saveSettings(next);
    } catch (e) {
      // 真实 App 把失败收敛成 actionError；这里兜住"直接抛"的实现。
      setError(toCallError(e));
    } finally {
      setSaving(false);
    }
  }

  const sources = ceiling?.sources ?? {};
  const actionError = shell.actionError ?? error;

  return (
    <SettingsSection
      id="permissions"
      titleKey="settings.permissions.title"
      hintKey="settings.permissions.hint"
      state={phase}
    >
      {phase === "loading" ? <AsyncState phase="loading" /> : null}
      {phase === "error" ? (
        <AsyncState phase="error" error={error} onRetry={() => void load()} />
      ) : null}

      {phase === "success" && ceiling ? (
        <dl className="grid grid-cols-1 gap-1.5 text-[11px] sm:grid-cols-3">
          <div className={ROW_CLASS}>
            <dt className={LABEL_CLASS}>{t("settings.permissions.filesystem")}</dt>
            <dd data-testid="permissions-filesystem-ceiling" className="mt-0.5 text-[color:var(--anvil-foreground)]">
              {ceiling.filesystem}
            </dd>
            <dd
              data-testid="permissions-filesystem-source"
              className="text-[10px] text-[color:var(--anvil-muted)]"
            >
              {sourceLabel(sources["permissions.filesystem"])}
            </dd>
          </div>
          <div className={ROW_CLASS}>
            <dt className={LABEL_CLASS}>{t("settings.permissions.network")}</dt>
            <dd data-testid="permissions-network-ceiling" className="mt-0.5 text-[color:var(--anvil-foreground)]">
              {grantedLabel(ceiling.network)}
            </dd>
            <dd
              data-testid="permissions-network-source"
              className="text-[10px] text-[color:var(--anvil-muted)]"
            >
              {sourceLabel(sources["permissions.network"])}
            </dd>
          </div>
          <div className={ROW_CLASS}>
            <dt className={LABEL_CLASS}>{t("settings.permissions.shell")}</dt>
            <dd data-testid="permissions-shell-ceiling" className="mt-0.5 text-[color:var(--anvil-foreground)]">
              {grantedLabel(ceiling.shell)}
            </dd>
            <dd
              data-testid="permissions-shell-source"
              className="text-[10px] text-[color:var(--anvil-muted)]"
            >
              {sourceLabel(sources["permissions.shell"])}
            </dd>
          </div>
        </dl>
      ) : null}

      {phase === "success" ? (
        <div className="mt-2.5 space-y-2">
          <p data-testid="permissions-network-note" className="text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
            {t("settings.permissions.networkUnavailable")}
          </p>
          <label className="flex items-start gap-2 text-[11px] text-[color:var(--anvil-foreground)]">
            <input
              type="checkbox"
              aria-label={t("settings.permissions.shellDefault")}
              checked={Boolean(settings?.shellAllowed)}
              disabled={saving || !settings}
              onChange={(e) => {
                if (!settings) return;
                void saveShell({ ...settings, shellAllowed: e.target.checked });
              }}
              className="mt-0.5 h-3.5 w-3.5 accent-[color:var(--anvil-accent)]"
            />
            <span>
              {t("settings.permissions.shellDefault")}
              <span className="ml-1 text-[10px] text-[color:var(--anvil-muted)]">
                {t("settings.permissions.shellDefaultHint")}
              </span>
            </span>
          </label>
          {saved ? (
            <span
              data-testid="settings-permissions-saved"
              role="status"
              className="text-[11px] text-[color:var(--anvil-success)]"
            >
              {t("settings.permissions.saved")}
            </span>
          ) : null}
          {actionError ? <AsyncState phase="error" error={actionError} /> : null}
        </div>
      ) : null}

      <div className="mt-3">
        <p className={LABEL_CLASS}>{t("settings.permissions.workspacesTitle")}</p>
        <p className="mt-0.5 text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
          {t("settings.permissions.workspacesHint")}
        </p>
        <ul className="mt-1.5 space-y-1.5">
          {shell.workspaces.map((ws) => (
            <li key={ws.id} data-testid={`workspace-${ws.id}`} className={ROW_CLASS}>
              <div className="flex items-center gap-2">
                <span className="min-w-0 flex-1 truncate text-[12px] text-[color:var(--anvil-foreground)]">
                  {ws.name}
                </span>
                <span className="shrink-0 text-[10px] text-[color:var(--anvil-muted)]">
                  {ws.trusted ? t("settings.permissions.trusted") : t("settings.permissions.untrusted")}
                </span>
                <button
                  type="button"
                  className={BUTTON_CLASS}
                  onClick={() => void shell.trustWorkspace(ws.id, !ws.trusted)}
                >
                  {ws.trusted ? t("settings.permissions.untrust") : t("settings.permissions.trust")}
                </button>
              </div>
              <div className={MONO_CLASS}>{ws.canonicalPath}</div>
            </li>
          ))}
        </ul>
        {shell.workspaces.length === 0 ? (
          <AsyncState
            phase="empty"
            emptyTitle={t("settings.permissions.workspacesEmptyTitle")}
            emptyHint={t("settings.permissions.workspacesEmptyHint")}
          />
        ) : null}
      </div>
    </SettingsSection>
  );
}
