// 输入区（U02 / UI06）。
//
// 这里有三个“不能假装”的地方：
//
// 1. **附件以 ResolveAttachments 的结果为准。**
//    用户输入/选择的是一个路径字符串，它只是候选定位串；真正进 run 的是后端复核
//    后给出的能力 ID。被拒的每一个文件都带着结构化原因逐条显示——一个坏文件不能
//    变成“全都失败”，也不能被静默丢掉。
//
// 2. **Profile / 模型 / 思考级别是后端事实，不是这里的选择器状态。**
//    显示的是启动快照（当前模型）与后端即将使用的 Profile；后端还没有“按轮次传
//    thinking level / profile”的入口时，这里不做一个点了没用的下拉框（那才是假动作），
//    只显示后端给出的事实并说明由谁决定。
//
// 3. **停止是一次真实请求。** 点下去只是“已请求停止”，界面切到 stopping 由后端
//    abort 接受之后才发生；停止期间不接受新消息（否则界面上会同时出现两轮）。

import { useCallback, useMemo, useRef, useState } from "react";
import { formatBytes, formatError, t } from "../../i18n";
import type { CallError, Profile, SkillResource } from "../../types";
import type { FixtureSignal } from "./readiness";
import {
  chatApi,
  type ChatAttachmentMeta,
  type ChatAttachmentRejection,
  type ChatAttachmentRequest,
} from "./api";

/** 待解析的一个附件：只有路径与显示名，尚不构成权威。 */
interface PendingAttachment {
  path: string;
  displayName: string;
  /** 来源：用户粘贴的文件/目录，或一个已托管 Skill。 */
  source: "file" | "directory" | "skill";
}

export interface PermissionSummary {
  /** 文件系统等级（config.FilesystemScope）；null = 后端没提供。 */
  filesystem: string | null;
  network: boolean | null;
  shell: boolean | null;
}

export interface ComposerProps {
  sessionId: string;
  value: string;
  onChange: (value: string) => void;
  /** 发送：只带后端已复核的附件 ID。 */
  onSend: (text: string, attachmentIds: string[]) => void;
  onStop: () => void;
  /** 停止已请求 / 后端正在停止：不接受新消息。 */
  stopRequested: boolean;
  /** 有活跃 run（可以与 stopRequested 不同：停止中仍然 busy）。 */
  busy: boolean;
  /** 这一轮实际使用的模型标签（启动快照）；空 = 未知。 */
  modelLabel: string;
  /** 当前 Profile（后端侧生效的那一个）；null = 后端没指定。 */
  profile: Profile | null;
  /** 可用 Skill（作为附件来源；加载仍由后端复核）。 */
  skills: SkillResource[];
  permission: PermissionSummary;
  /** 后端拒绝发送时的结构化原因（例如工作区未信任）。 */
  sendError?: CallError | null;
  /**
   * 发送前提未满足时的解释（UX1）；null 表示可以发送。
   *
   * 这里接的是**判定结果**而不是布尔：输入区要给用户一句能读的说明，
   * 而不是一个变灰就完事的按钮。
   */
  blockedHint?: string | null;
  /** 内置 fixture（脚本化回放）标记；null = 用的是真实 provider。 */
  fixture?: FixtureSignal | null;
}

/** 把用户给的路径收敛成一个 pending 附件；空路径不算。 */
function pendingFromPath(path: string, source: PendingAttachment["source"]): PendingAttachment | null {
  const trimmed = path.trim();
  if (!trimmed) return null;
  const normalized = trimmed.replace(/\\/g, "/");
  const base = normalized.split("/").filter(Boolean).pop() ?? trimmed;
  return { path: trimmed, displayName: base, source };
}

export function Composer({
  sessionId,
  value,
  onChange,
  onSend,
  onStop,
  stopRequested,
  busy,
  modelLabel,
  profile,
  skills,
  permission,
  sendError,
  blockedHint = null,
  fixture = null,
}: ComposerProps) {
  const [pathInput, setPathInput] = useState("");
  const [pending, setPending] = useState<PendingAttachment[]>([]);
  const [accepted, setAccepted] = useState<ChatAttachmentMeta[]>([]);
  const [rejected, setRejected] = useState<ChatAttachmentRejection[]>([]);
  const [resolving, setResolving] = useState(false);
  const [resolveError, setResolveError] = useState<CallError | null>(null);
  const [skillId, setSkillId] = useState("");
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);

  /**
   * 让后端解析当前待办附件。
   *
   * 每次解析都是**整批重放**（已有的 accepted 一起带上）：后端按内容寻址，
   * 重复解析同一文件是幂等的，也不会把已接收的附件弄丢。
   */
  const resolve = useCallback(
    async (next: PendingAttachment[]) => {
      setResolving(true);
      setResolveError(null);
      try {
        const requests: ChatAttachmentRequest[] = next.map((p) => ({
          path: p.path,
          displayName: p.displayName,
        }));
        const res = await chatApi.resolveAttachments(sessionId, requests);
        if (res.error) {
          setResolveError(res.error);
          setAccepted([]);
          setRejected([]);
          return;
        }
        setAccepted(res.items ?? []);
        // 被接收的文件从待解析列表里移除；被拒的留在列表里由用户处理。
        const rejectedPaths = new Set((res.rejected ?? []).map((r) => r.path));
        setPending(next.filter((p) => rejectedPaths.has(p.path)));
        setRejected(res.rejected ?? []);
      } catch (e) {
        // 绑定层抛错时没有 code：收敛成 INTERNAL_ERROR，不伪造类型。
        setResolveError({
          code: "INTERNAL_ERROR",
          message: e instanceof Error ? e.message : String(e),
          retryable: true,
        });
        setAccepted([]);
        setRejected([]);
      } finally {
        setResolving(false);
      }
    },
    [sessionId]
  );

  const addPath = useCallback(
    (source: PendingAttachment["source"]) => {
      const item = pendingFromPath(pathInput, source);
      if (!item) return;
      const next = [...pending, item];
      setPending(next);
      setPathInput("");
      void resolve(next);
    },
    [pathInput, pending, resolve]
  );

  const addSkill = useCallback(() => {
    const skill = skills.find((s) => s.id === skillId);
    if (!skill) return;
    const item = pendingFromPath(skill.path, "skill");
    if (!item) return;
    const next = [...pending, item];
    setPending(next);
    void resolve(next);
  }, [skillId, skills, pending, resolve]);

  const removeAccepted = useCallback((id: string) => {
    setAccepted((prev) => prev.filter((a) => a.id !== id));
  }, []);

  const canSend = blockedHint === null && !busy && !stopRequested && !resolving && value.trim() !== "";

  const send = useCallback(() => {
    if (!canSend) return;
    onSend(value, accepted.map((a) => a.id));
    // 发送成功后待办清空；accepted 也清空——它们已经随这一轮冻结进 run，
    // 下一轮要重新选择（附件引用按 run 冻结，不是会话级常驻权限）。
    setPending([]);
    setAccepted([]);
    setRejected([]);
  }, [canSend, value, accepted, onSend]);

  const onKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        send();
      }
    },
    [send]
  );

  const permissionText = useMemo(() => {
    const filesystem = permission.filesystem ?? t("chat.composer.permissionMissing");
    const network =
      permission.network === null
        ? t("chat.composer.permissionMissing")
        : permission.network
          ? t("chat.composer.permissionOn")
          : t("chat.composer.permissionOff");
    const shell =
      permission.shell === null
        ? t("chat.composer.permissionMissing")
        : permission.shell
          ? t("chat.composer.permissionOn")
          : t("chat.composer.permissionOff");
    return t("chat.composer.permissionSummary", { filesystem, network, shell });
  }, [permission]);

  return (
    <div data-composer="" className="space-y-2 border-t border-[color:var(--anvil-border)] px-3 py-2">
      {/* 后端事实：Profile / 模型 / 思考级别 / 权限摘要 */}
      <dl className="flex flex-wrap gap-x-4 gap-y-1 text-[10px] text-[color:var(--anvil-muted)]">
        <div className="flex items-center gap-1" title={t("chat.composer.profileHint")}>
          <dt>{t("chat.composer.profile")}</dt>
          <dd data-composer-profile="" className="text-[color:var(--anvil-foreground)]">
            {profile ? profile.name || profile.id : t("common.unknown")}
          </dd>
        </div>
        <div className="flex items-center gap-1" title={t("chat.composer.modelHint")}>
          <dt>{t("chat.composer.model")}</dt>
          <dd data-composer-model="" className="font-mono text-[color:var(--anvil-foreground)]">
            {modelLabel || t("common.unknown")}
          </dd>
          {fixture ? (
            <span
              data-fixture-badge=""
              title={t("chat.composer.fixtureHint")}
              className="rounded-full border border-[color:var(--anvil-warning-border)] bg-[color:var(--anvil-warning-surface)] px-1.5 py-0.5 text-[10px] text-[color:var(--anvil-warning)]"
            >
              {t("chat.composer.fixtureBadge")}
            </span>
          ) : null}
        </div>
        <div className="flex items-center gap-1">
          <dt>{t("chat.composer.thinking")}</dt>
          <dd data-composer-thinking="" className="text-[color:var(--anvil-foreground)]">
            {t("chat.composer.thinkingUnknown")}
          </dd>
        </div>
      </dl>

      <p data-permission-summary="" className="text-[10px] text-[color:var(--anvil-muted)]">
        {t("chat.composer.permission")}
        {" · "}
        {permissionText}
      </p>

      {/* 附件 */}
      <section aria-labelledby="composer-attachments" className="space-y-1.5">
        <div className="flex flex-wrap items-center gap-1.5">
          <h2
            id="composer-attachments"
            className="text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]"
          >
            {t("chat.composer.attachments")}
          </h2>
          {resolving ? (
            <span className="text-[10px] text-[color:var(--anvil-muted)]">{t("chat.composer.resolving")}</span>
          ) : null}
        </div>

        <div className="flex flex-wrap items-center gap-1.5">
          <input
            type="text"
            value={pathInput}
            onChange={(e) => setPathInput(e.target.value)}
            placeholder={t("chat.composer.pathPlaceholder")}
            aria-label={t("chat.composer.pathPlaceholder")}
            className="min-w-0 flex-1 rounded-md border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
          />
          <button
            type="button"
            onClick={() => addPath("file")}
            className="rounded-md border border-[color:var(--anvil-border)] px-2 py-1 text-[10px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
          >
            {t("chat.composer.addFile")}
          </button>
          <button
            type="button"
            onClick={() => addPath("directory")}
            className="rounded-md border border-[color:var(--anvil-border)] px-2 py-1 text-[10px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
          >
            {t("chat.composer.addDirectory")}
          </button>
        </div>

        {skills.length > 0 ? (
          <div className="flex flex-wrap items-center gap-1.5">
            <label htmlFor="composer-skill" className="text-[10px] text-[color:var(--anvil-muted)]">
              {t("chat.composer.skillPick")}
            </label>
            <select
              id="composer-skill"
              value={skillId}
              onChange={(e) => setSkillId(e.target.value)}
              className="rounded-md border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-1.5 py-1 text-[10px] text-[color:var(--anvil-foreground)]"
            >
              <option value="">{t("common.unknown")}</option>
              {skills.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name || s.id}
                </option>
              ))}
            </select>
            <button
              type="button"
              onClick={addSkill}
              disabled={!skillId}
              className="rounded-md border border-[color:var(--anvil-border)] px-2 py-1 text-[10px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:cursor-not-allowed disabled:opacity-50"
            >
              {t("chat.composer.addSkill")}
            </button>
          </div>
        ) : null}

        <p className="text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
          {t("chat.composer.attachmentsHint")}
        </p>

        {resolveError ? (
          <p role="alert" className="text-[10px] text-[color:var(--anvil-danger)]">
            {t("chat.composer.resolveFailed", { code: resolveError.code })}
          </p>
        ) : null}

        {accepted.length === 0 && rejected.length === 0 && pending.length === 0 ? (
          <p className="text-[10px] text-[color:var(--anvil-muted)]">{t("chat.composer.noAttachments")}</p>
        ) : null}

        <ul className="space-y-1">
          {accepted.map((item) => (
            <li
              key={item.id}
              data-attachment-accepted={item.id}
              className="flex flex-wrap items-center gap-1.5 rounded border border-[color:var(--anvil-border)] px-2 py-1 text-[10px]"
            >
              <span className="text-[color:var(--anvil-foreground)]">{item.displayName}</span>
              <span className="text-[color:var(--anvil-muted)]">{formatBytes(item.size)}</span>
              <span className="font-mono text-[color:var(--anvil-muted)]">{item.sha256.slice(0, 12)}</span>
              <span data-attachment-status={item.status} className="text-[color:var(--anvil-success)]">
                {t("chat.composer.accepted")}
              </span>
              <button
                type="button"
                onClick={() => removeAccepted(item.id)}
                className="ml-auto rounded border border-[color:var(--anvil-border)] px-1.5 py-0.5 text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)]"
              >
                {t("chat.composer.remove")}
              </button>
            </li>
          ))}
          {rejected.map((item) => (
            <li
              key={`${item.path}:${item.code}`}
              data-attachment-rejected={item.code}
              className="rounded border border-[color:var(--anvil-danger-border)] bg-[color:var(--anvil-danger-surface)] px-2 py-1 text-[10px]"
            >
              <span className="text-[color:var(--anvil-foreground)]">{item.displayName || item.path}</span>
              <span className="ml-1 text-[color:var(--anvil-danger)]">
                {t("chat.composer.rejected")}
              </span>
              <p data-attachment-reason="" className="mt-0.5 text-[color:var(--anvil-foreground)]">
                {t("chat.composer.rejectedReason", { code: item.code, message: item.message })}
              </p>
            </li>
          ))}
        </ul>
      </section>

      {/* 输入 */}
      <div className="flex items-end gap-2">
        <label htmlFor="composer-input" className="sr-only">
          {t("chat.composer.placeholder")}
        </label>
        <textarea
          id="composer-input"
          ref={textareaRef}
          value={value}
          rows={3}
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={onKeyDown}
          placeholder={t("chat.composer.placeholder")}
          disabled={stopRequested || blockedHint !== null}
          className="min-h-0 flex-1 resize-y rounded-md border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1.5 text-[12px] leading-relaxed text-[color:var(--anvil-foreground)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)] disabled:opacity-60"
        />
        <div className="flex shrink-0 flex-col gap-1">
          <button
            type="button"
            onClick={send}
            disabled={!canSend}
            className="rounded-md border border-[color:var(--anvil-accent-border)] bg-[color:var(--anvil-accent-surface)] px-3 py-1.5 text-xs text-[color:var(--anvil-accent)] hover:opacity-90 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)] disabled:cursor-not-allowed disabled:opacity-50"
          >
            {t("chat.composer.send")}
          </button>
          <button
            type="button"
            onClick={onStop}
            disabled={!busy || stopRequested}
            className="rounded-md border border-[color:var(--anvil-border)] px-3 py-1.5 text-xs text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)] disabled:cursor-not-allowed disabled:opacity-50"
          >
            {stopRequested ? t("chat.composer.stopping") : t("chat.composer.stop")}
          </button>
        </div>
      </div>

      {stopRequested ? (
        <p data-composer-readonly="" className="text-[10px] text-[color:var(--anvil-muted)]">
          {t("chat.composer.stopRequested")}
        </p>
      ) : null}

      {blockedHint !== null ? (
        <p data-composer-blocked="" role="status" className="text-[10px] leading-relaxed text-[color:var(--anvil-warning)]">
          {blockedHint}
        </p>
      ) : null}

      {sendError ? (
        <p role="alert" className="text-[10px] text-[color:var(--anvil-danger)]">
          {formatError(sendError)}
        </p>
      ) : null}
    </div>
  );
}
