// 技能页面（U04 / UI08、RES05）：托管技能 + 扩展。
//
// 这一块最容易被做成"看起来启用就算启用"。所以界面上每条技能都显式区分
// 三件事：打算启用吗（enabled）、现在能用吗（available + 原因）、
// 运行中的那一轮有没有把托管技能用 --skill 显式加载（pending/runtimeSkills）。
// 三者混成一句"已启用"，用户就再也分不清"没生效"是配置问题还是没重启。
//
// 注意 runtimeSkills 只是"托管且启用、被显式加载"的那几条，不是 Pi 实际可见的
// 全部技能：2026-11-04 起 Pi 会自己发现并加载本地技能，那份清单我们拿不到。
//
// 文案全部来自 catalog（`skills.*` / `extensions.*`），颜色全部来自语义 token：
// 这一页在 en-US 下不得出现中文字面量，在深色主题下不得出现写死的调色板类名。

import { useState } from "react";
import { FolderInput, Puzzle } from "lucide-react";
import { api } from "../../api";
import type { CallError, Extension, SkillResource } from "../../types";
import { toCallError } from "../../app/useBackend";
import { EmptyState } from "../../components/StateViews";
import { t } from "../../i18n";

export interface SkillsPageProps {
  skills: SkillResource[];
  revision: number;
  /** 配置里的技能集合与运行中的不一致。 */
  pending: boolean;
  activeRun: boolean;
  runtimeSkills: string[];
  workspaceId: string;
  onChanged: () => void;
  extensions: Extension[];
  extensionRevision: number;
}

export function SkillsPage({
  skills,
  revision,
  pending,
  activeRun,
  runtimeSkills,
  workspaceId,
  onChanged,
  extensions,
  extensionRevision,
}: SkillsPageProps) {
  return (
    <>
      <SkillsList
        skills={skills}
        revision={revision}
        pending={pending}
        activeRun={activeRun}
        runtimeSkills={runtimeSkills}
        workspaceId={workspaceId}
        onChanged={onChanged}
      />
      <ExtensionsList extensions={extensions} revision={extensionRevision} onChanged={onChanged} />
    </>
  );
}

// ---------------------------------------------------------------- 托管技能

function SkillsList({
  skills,
  revision,
  pending,
  activeRun,
  runtimeSkills,
  workspaceId,
  onChanged,
}: Omit<SkillsPageProps, "extensions" | "extensionRevision">) {
  const [path, setPath] = useState("");
  const [scope, setScope] = useState<"user" | "workspace">("user");
  const [error, setError] = useState<CallError | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [confirming, setConfirming] = useState<string | null>(null);
  const [editing, setEditing] = useState<{ id: string; content: string } | null>(null);

  async function importSkill() {
    setError(null);
    setNotice(null);
    if (!path.trim()) {
      setNotice(t("skills.import.pathRequired"));
      return;
    }
    setBusy("import");
    try {
      const res = await api.importSkill(path.trim(), scope, workspaceId);
      if (res.error) {
        setError(res.error);
        return;
      }
      setPath("");
      setNotice(
        res.pending
          ? t("skills.import.donePending", { name: res.item.name })
          : t("skills.import.done", { name: res.item.name })
      );
      onChanged();
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusy(null);
    }
  }

  async function toggle(s: SkillResource) {
    setError(null);
    setNotice(null);
    setBusy(s.id);
    try {
      const res = await api.setSkillEnabled(s.id, revision, !s.enabled);
      if (res.error) {
        setError(res.error);
        return;
      }
      onChanged();
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusy(null);
    }
  }

  async function remove(id: string) {
    setError(null);
    setBusy(id);
    try {
      const res = await api.deleteSkill(revision, id);
      if (res.error) {
        setError(res.error);
        return;
      }
      setConfirming(null);
      onChanged();
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusy(null);
    }
  }

  async function openEditor(id: string) {
    setError(null);
    setBusy(id);
    try {
      const res = await api.readSkill(id);
      if (res.error) {
        setError(res.error);
        return;
      }
      // 编辑的是磁盘上的最新内容，而不是列表里那份快照。
      setEditing({ id, content: res.content });
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusy(null);
    }
  }

  async function saveEditor() {
    if (!editing) return;
    setError(null);
    setBusy(editing.id);
    try {
      const res = await api.saveSkill(editing.id, revision, editing.content);
      if (res.error) {
        setError(res.error);
        return;
      }
      setEditing(null);
      setNotice(t("skills.editor.saved"));
      onChanged();
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <section>
      <header className="mb-2">
        <h2 className="text-sm font-medium text-[color:var(--anvil-foreground)]">{t("skills.title")}</h2>
        <p className="mt-0.5 text-[11px] text-[color:var(--anvil-muted)]">{t("skills.subtitle")}</p>
      </header>

      {pending && (
        <p
          role="status"
          className="mb-2 rounded border border-[color:var(--anvil-warning-border)] bg-[color:var(--anvil-warning-surface)] px-2 py-1.5 text-[11px] text-[color:var(--anvil-warning)]"
        >
          {activeRun ? t("skills.pending.activeRun") : t("skills.pending.saved")}
        </p>
      )}

      <div className="mb-2 flex flex-col gap-1.5 rounded-lg border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] p-2">
        <label className="text-[10px] text-[color:var(--anvil-muted)]" htmlFor="skill-import-path">
          {t("skills.import.pathLabel")}
        </label>
        <div className="flex gap-1.5">
          <input
            id="skill-import-path"
            aria-label={t("skills.import.pathAria")}
            value={path}
            onChange={(e) => setPath(e.target.value)}
            placeholder={t("skills.import.pathPlaceholder")}
            className="min-w-0 flex-1 rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] placeholder:text-[color:var(--anvil-muted)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
          />
          <select
            aria-label={t("skills.import.scopeAria")}
            value={scope}
            onChange={(e) => setScope(e.target.value as "user" | "workspace")}
            className="rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-1.5 py-1 text-[11px] text-[color:var(--anvil-foreground)]"
          >
            <option value="user">{t("skills.scope.user")}</option>
            <option value="workspace">{t("skills.scope.workspaceOption")}</option>
          </select>
          <button
            onClick={() => void importSkill()}
            disabled={busy === "import"}
            className="flex shrink-0 items-center gap-1 rounded border border-[color:var(--anvil-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface-raised)] disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
          >
            <FolderInput className="h-3 w-3" aria-hidden /> {t("skills.import.submit")}
          </button>
        </div>
        <p className="text-[10px] text-[color:var(--anvil-muted)]">{t("skills.import.hint")}</p>
      </div>

      {error && (
        <p role="alert" className="mb-2 text-[11px] text-[color:var(--anvil-danger)]">
          <span className="font-mono text-[10px]">{error.code}</span> {error.message}
        </p>
      )}
      {notice && !error && (
        <p role="status" className="mb-2 text-[11px] text-[color:var(--anvil-muted)]">
          {notice}
        </p>
      )}

      {skills.length === 0 ? (
        <EmptyState
          title={t("skills.empty.title")}
          hint={t("skills.empty.hint")}
          icon={<Puzzle className="h-4 w-4" aria-hidden />}
        />
      ) : (
        <ul className="space-y-1">
          {skills.map((s) => (
            <li
              key={s.id}
              className="rounded-md border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-2.5 py-2"
            >
              <div className="flex items-center gap-2">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <span className="truncate text-[12px] text-[color:var(--anvil-foreground)]">
                      {s.name || s.id}
                    </span>
                    <Badge tone={s.enabled ? "success" : "neutral"}>
                      {s.enabled ? t("skills.state.enabled") : t("skills.state.disabled")}
                    </Badge>
                    <Badge tone="neutral">
                      {s.scope === "user"
                        ? t("skills.scope.user")
                        : t("skills.scope.workspaceBadge")}
                    </Badge>
                    {!s.available && <Badge tone="danger">{t("status.unavailable")}</Badge>}
                    {pending && runtimeSkills.includes(s.id) && (
                      <Badge tone="warning">{t("skills.state.staleInRun")}</Badge>
                    )}
                  </div>
                  <div className="mt-0.5 line-clamp-2 text-[10px] text-[color:var(--anvil-muted)]">
                    {s.description || t("skills.labels.noDescription")}
                  </div>
                  <div className="truncate text-[10px] text-[color:var(--anvil-muted)]">
                    {s.id} · {s.origin} · {s.hash || "-"} · {formatBytes(s.bytes)}
                    {s.dependencies && s.dependencies.length > 0
                      ? " · " + t("skills.labels.dependencies", { commands: s.dependencies.join(", ") })
                      : ""}
                  </div>
                  {!s.available && s.unavailableReason && (
                    <div className="mt-0.5 text-[10px] text-[color:var(--anvil-danger)]">
                      {s.unavailableReason}
                    </div>
                  )}
                </div>
                <div className="flex shrink-0 items-center gap-1">
                  <button
                    onClick={() => void openEditor(s.id)}
                    disabled={busy === s.id}
                    className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface-raised)] disabled:opacity-40 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
                  >
                    {t("skills.actions.edit")}
                  </button>
                  <button
                    onClick={() => void toggle(s)}
                    disabled={busy === s.id}
                    className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface-raised)] disabled:opacity-40 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
                  >
                    {s.enabled ? t("skills.actions.disable") : t("skills.actions.enable")}
                  </button>
                  {confirming === s.id ? (
                    <>
                      <button
                        onClick={() => void remove(s.id)}
                        disabled={busy === s.id}
                        className="rounded border border-[color:var(--anvil-danger-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-danger)] hover:bg-[color:var(--anvil-danger-surface)] disabled:opacity-40"
                      >
                        {t("skills.actions.deleteConfirm")}
                      </button>
                      <button
                        onClick={() => setConfirming(null)}
                        className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface-raised)]"
                      >
                        {t("common.cancel")}
                      </button>
                    </>
                  ) : (
                    <button
                      onClick={() => setConfirming(s.id)}
                      className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface-raised)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
                    >
                      {t("skills.actions.delete")}
                    </button>
                  )}
                </div>
              </div>

              {editing?.id === s.id && (
                <div className="mt-2 border-t border-[color:var(--anvil-border)] pt-2">
                  <label
                    className="text-[10px] text-[color:var(--anvil-muted)]"
                    htmlFor={"skill-body-" + s.id}
                  >
                    {t("skills.editor.contentLabel")}
                  </label>
                  <textarea
                    id={"skill-body-" + s.id}
                    aria-label={t("skills.editor.contentAria")}
                    value={editing.content}
                    onChange={(e) => setEditing({ id: s.id, content: e.target.value })}
                    rows={12}
                    className="mt-1 w-full rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] p-2 font-mono text-[11px] leading-relaxed text-[color:var(--anvil-foreground)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
                  />
                  <div className="mt-1 flex items-center gap-1.5">
                    <button
                      onClick={() => void saveEditor()}
                      disabled={busy === s.id}
                      className="rounded bg-[color:var(--anvil-accent)] px-2.5 py-1 text-[11px] font-medium text-[color:var(--anvil-background)] hover:opacity-90 disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
                    >
                      {t("common.save")}
                    </button>
                    <button
                      onClick={() => setEditing(null)}
                      className="rounded border border-[color:var(--anvil-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface-raised)]"
                    >
                      {t("common.cancel")}
                    </button>
                    <span className="text-[10px] text-[color:var(--anvil-muted)]">
                      {t("skills.editor.nameHint")}
                    </span>
                  </div>
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

// ---------------------------------------------------------------- 扩展

/**
 * 扩展区块（C03 / RES04）。
 *
 * 只有两类，规则不同，所以不用一个通用开关糊过去：
 *   - 系统策略扩展：不可关闭。界面上不画关闭按钮，绑定层也会拒绝。
 *   - 第三方可执行扩展：默认关闭。开启它等于允许任意代码在 Pi 进程里跑，
 *     但这**不会**让它的工具调用免审批——审批由策略扩展统一拦截。
 */
function ExtensionsList({
  extensions,
  revision,
  onChanged,
}: {
  extensions: Extension[];
  revision: number;
  onChanged: () => void;
}) {
  const [error, setError] = useState<CallError | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [confirming, setConfirming] = useState<string | null>(null);

  async function toggle(e: Extension) {
    setError(null);
    if (!e.enabled) {
      // 开启第三方扩展 = 允许它的代码在 Pi 进程里执行，必须是一次明确动作。
      setConfirming(e.id);
      return;
    }
    await apply(e, false);
  }

  async function apply(e: Extension, enabled: boolean) {
    setBusy(e.id);
    try {
      const res = await api.setExtensionEnabled(e.id, revision, enabled);
      if (res.error) {
        setError(res.error);
        return;
      }
      setConfirming(null);
      onChanged();
    } catch (err) {
      setError(toCallError(err));
    } finally {
      setBusy(null);
    }
  }

  return (
    <section>
      <header className="mb-2">
        <h2 className="text-sm font-medium text-[color:var(--anvil-foreground)]">
          {t("extensions.title")}
        </h2>
        <p className="mt-0.5 text-[11px] text-[color:var(--anvil-muted)]">
          {t("extensions.subtitle")}
        </p>
      </header>

      {error && (
        <p role="alert" className="mb-2 text-[11px] text-[color:var(--anvil-danger)]">
          <span className="font-mono text-[10px]">{error.code}</span> {error.message}
        </p>
      )}

      {extensions.length === 0 ? (
        <p className="rounded border border-dashed border-[color:var(--anvil-border)] px-3 py-2 text-[11px] text-[color:var(--anvil-muted)]">
          {t("extensions.empty")}
        </p>
      ) : (
        <ul className="space-y-1">
          {extensions.map((e) => (
            <li
              key={e.id}
              className="rounded-md border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-2.5 py-2"
            >
              <div className="flex items-center gap-2">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <span className="truncate text-[12px] text-[color:var(--anvil-foreground)]">
                      {e.name || e.id}
                    </span>
                    {e.system && (
                      <span className="shrink-0 rounded bg-[color:var(--anvil-accent-surface)] px-1 py-0.5 text-[9px] text-[color:var(--anvil-accent)]">
                        {t("extensions.systemBadge")}
                      </span>
                    )}
                    {!e.system && (
                      <span className="shrink-0 rounded bg-[color:var(--anvil-surface-raised)] px-1 py-0.5 text-[9px] text-[color:var(--anvil-muted)]">
                        {e.enabled
                          ? t("extensions.enabledBadge")
                          : t("extensions.disabledBadge")}
                      </span>
                    )}
                  </div>
                  <div className="truncate text-[10px] text-[color:var(--anvil-muted)]">{e.path}</div>
                  <div className="mt-0.5 text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
                    {e.note}
                  </div>
                </div>
                {e.canDisable && (
                  <div className="flex shrink-0 items-center gap-1">
                    {confirming === e.id ? (
                      <>
                        <button
                          onClick={() => void apply(e, true)}
                          disabled={busy === e.id}
                          className="rounded border border-[color:var(--anvil-warning-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-warning)] hover:bg-[color:var(--anvil-warning-surface)] disabled:opacity-40"
                        >
                          {t("extensions.confirmEnable")}
                        </button>
                        <button
                          onClick={() => setConfirming(null)}
                          className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface-raised)]"
                        >
                          {t("common.cancel")}
                        </button>
                      </>
                    ) : (
                      <button
                        onClick={() => void toggle(e)}
                        disabled={busy === e.id}
                        className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface-raised)] disabled:opacity-40 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
                      >
                        {e.enabled ? t("skills.actions.disable") : t("skills.actions.enable")}
                      </button>
                    )}
                  </div>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

// ---------------------------------------------------------------- 小组件

function Badge({
  tone,
  children,
}: {
  tone: "success" | "warning" | "danger" | "neutral";
  children: string;
}) {
  // 徽标底色用 surface-raised 而不是同名 *-surface：浅色主题下
  // `--anvil-success` 压在 `--anvil-success-surface` 上只有 4.49（AA 要 4.5），
  // 而压在 surface-raised 上四种色调都 ≥5.0（实机 UI13 的 low-contrast 发现的）。
  const cls = {
    success: "bg-[color:var(--anvil-surface-raised)] text-[color:var(--anvil-success)]",
    warning: "bg-[color:var(--anvil-surface-raised)] text-[color:var(--anvil-warning)]",
    danger: "bg-[color:var(--anvil-surface-raised)] text-[color:var(--anvil-danger)]",
    neutral: "bg-[color:var(--anvil-surface-raised)] text-[color:var(--anvil-muted)]",
  }[tone];
  return <span className={`shrink-0 rounded px-1 py-0.5 text-[9px] ${cls}`}>{children}</span>;
}

function formatBytes(n: number): string {
  if (!n) return "-";
  if (n >= 1024) return `${Math.round(n / 1024)} KiB`;
  return `${n} B`;
}
