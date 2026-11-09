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
import { formatDate, formatError, t } from "../../i18n";
import { AsyncState } from "../../components/states/AsyncState";
import { toCallError } from "../../app/useBackend";
import { useShell } from "../../app/shell";
import { workspaceTrustTime } from "../../app/trustLog";
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
  // 添加目录：选目录不是授权，所以这一步只要求"它是个存在的目录"。
  const [newPath, setNewPath] = useState("");
  const [adding, setAdding] = useState(false);
  const [addError, setAddError] = useState<CallError | null>(null);
  // 刚撤销信任的那一个：撤销不打断已经在跑的 run，这句话必须说出来。
  const [revokedId, setRevokedId] = useState("");

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

  /**
   * 添加目录（WTRUST）：只调 AddWorkspace，不碰信任。
   *
   * “我要在这里工作”与“允许在这里执行命令”是两件事（规格第 ① 条）：
   * 添加时就要求信任，等于把选目录变成一次授权，用户会以为自己已经允许执行了。
   */
  async function addDirectory() {
    const path = newPath.trim();
    if (!path || adding) return;
    setAdding(true);
    setAddError(null);
    try {
      await shell.addWorkspace(path);
      setNewPath("");
    } catch (e) {
      setAddError(toCallError(e));
    } finally {
      setAdding(false);
    }
  }

  /**
   * 信任 / 撤销。
   *
   * 成功后**不改本地结论**：壳层会重读列表与当前工作区，行状态与聊天页的就绪
   * 都从后端事实上自己翻。撤销记一笔 id，用于给出“当前运行不受影响”的说明。
   */
  async function setTrust(id: string, trusted: boolean) {
    setRevokedId("");
    await shell.trustWorkspace(id, trusted);
    if (!trusted) setRevokedId(id);
  }

  // 撤销说明只在**列表里真的变成未信任**时才挂出来：撤销失败（CAS 冲突/无权限）
  // 仍显示“已撤销”等于把没发生的事说成发生了。
  const revokedRow = revokedId ? shell.workspaces.find((ws) => ws.id === revokedId) : undefined;
  const showRevokedNote = Boolean(revokedRow && !revokedRow.trusted);

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

        {/* 添加目录：先选目录，信任留到真要执行时。 */}
        <div className="mt-2 space-y-1">
          <p className="text-[11px] text-[color:var(--anvil-foreground)]">
            {t("settings.permissions.addTitle")}
          </p>
          <p className="text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
            {t("settings.permissions.addHint")}
          </p>
          <div className="flex items-center gap-1.5">
            <input
              data-workspace-add-input=""
              aria-label={t("settings.permissions.workspacePath")}
              placeholder={t("settings.permissions.addPlaceholder")}
              value={newPath}
              onChange={(e) => setNewPath(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  void addDirectory();
                }
              }}
              className="min-w-0 flex-1 rounded-md border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 font-mono text-[11px] text-[color:var(--anvil-foreground)] outline-none focus:border-[color:var(--anvil-accent-border)]"
            />
            <button
              type="button"
              data-workspace-add=""
              disabled={adding || newPath.trim() === ""}
              onClick={() => void addDirectory()}
              className={`${BUTTON_CLASS} disabled:cursor-not-allowed disabled:opacity-50`}
            >
              {adding ? t("settings.permissions.adding") : t("settings.permissions.add")}
            </button>
          </div>
          {addError ? (
            <p role="alert" data-workspace-add-error="" className="text-[11px] text-[color:var(--anvil-danger)]">
              {formatError(addError)}
            </p>
          ) : null}
        </div>

        {/* 已信任与未信任都列出来：只列已信任会让人以为其它目录不存在。 */}
        <ul className="mt-2 space-y-1.5">
          {shell.workspaces.map((ws) => {
            const trustTime = ws.trusted ? workspaceTrustTime(ws.id) : "";
            return (
              <li key={ws.id} data-testid={`workspace-${ws.id}`} data-workspace-trusted={String(ws.trusted)} className={ROW_CLASS}>
                <div className="flex items-center gap-2">
                  <span className="min-w-0 flex-1 truncate text-[12px] text-[color:var(--anvil-foreground)]">
                    {ws.name}
                  </span>
                  <span
                    data-workspace-state=""
                    className="shrink-0 text-[10px] text-[color:var(--anvil-muted)]"
                  >
                    {ws.trusted ? t("settings.permissions.trusted") : t("settings.permissions.untrusted")}
                  </span>
                  <button
                    type="button"
                    className={BUTTON_CLASS}
                    aria-label={
                      ws.trusted
                        ? t("settings.permissions.untrustAria", { path: ws.canonicalPath })
                        : t("settings.permissions.trustAria", { path: ws.canonicalPath })
                    }
                    onClick={() => void setTrust(ws.id, !ws.trusted)}
                  >
                    {ws.trusted ? t("settings.permissions.untrust") : t("settings.permissions.trust")}
                  </button>
                </div>
                <div className={MONO_CLASS}>{ws.canonicalPath}</div>
                {ws.trusted ? (
                  <div data-workspace-trusted-at="" className="text-[10px] text-[color:var(--anvil-muted)]">
                    {trustTime
                      ? t("settings.permissions.trustedAt", { time: formatDate(trustTime) })
                      : t("settings.permissions.trustedAtUnknown")}
                  </div>
                ) : null}
              </li>
            );
          })}
        </ul>

        {/* 撤销语义：撤销不打断已经在跑的那一轮，下一条消息才会重新被拦。 */}
        {showRevokedNote ? (
          <p
            role="status"
            data-workspace-revoked-note={revokedId}
            className="mt-1.5 text-[11px] leading-relaxed text-[color:var(--anvil-warning)]"
          >
            {t("settings.permissions.revokedNote")}
          </p>
        ) : null}

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
