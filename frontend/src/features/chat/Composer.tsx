// 输入区（U02 / D2b）。
//
// 解剖来自原型 `assets/studio.css` 1086–1160 + `assets/pages.css` 的
// .composer-project-row / .composer-attachments / .attachment-drawer：
//
//   composer-wrap（padding 10px 26px 14px，向上渐隐）
//     ├─ 项目行（胶囊：文件夹图标 + 项目名）
//     └─ composer（max-width 760，radius 12，border-strong，shadow）
//          ├─ textarea（2 行，13.5px/1.65，padding 13px 14px 4px）
//          ├─ 附件行（有附件才出现）
//          ├─ 抽屉（+ 打开：路径输入 / 文件 / 目录 / Skill）
//          └─ bar：+ 28×28 · 模型 · 思考级别 · 权限摘要 · 停止/发送
//     └─ foot（提示 + 快捷键）
//
// 这里有三个“不能假装”的地方：
//
// 1. **附件以 ResolveAttachments 的结果为准。**
//    用户输入/选择的是一个路径字符串，它只是候选定位串；真正进 run 的是后端复核
//    后给出的能力 ID。被拒的每一个文件都带着结构化原因逐条显示——一个坏文件不能
//    变成“全都失败”，也不能被静默丢掉。
//
// 2. **Profile / 模型 / 思考级别是后端事实，不是这里的选择器状态。**
//    原型在这一行放的是三个下拉框；产品侧**没有**按轮次传 model/thinking 的入口，
//    所以这里只显示后端给出的事实（启动快照）并把缺失写成“未知”。
//    做一个点了没用的下拉框才是假动作。
//
// 3. **停止是一次真实请求。** 点下去只是“已请求停止”，界面切到 stopping 由后端
//    abort 接受之后才发生；停止期间不接受新消息（否则界面上会同时出现两轮）。

import { useCallback, useMemo, useRef, useState } from "react";
import { Brain, Folder, Paperclip, Plus, Shield, Sparkles, Square, ArrowUp } from "lucide-react";
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
  /**
   * 发送前提还在检查时的轻量状态（UX 回归修复）。
   *
   * 与 blockedHint 分开：这只是在说“还在算”，不是“你缺东西”，
   * 因此**不**禁用输入区——挂载时锁住输入会让页面看起来卡死。
   */
  checkingHint?: string | null;
  /** 内置 fixture（脚本化回放）标记；null = 用的是真实 provider。 */
  fixture?: FixtureSignal | null;
  /** 项目行显示的项目名（来自壳层的当前工作区）；空 = 显示“未绑定项目”。 */
  projectName?: string;
  /** 项目行的路径（悬停提示）；空则不显示。 */
  projectPath?: string;
}

/** 把用户给的路径收敛成一个 pending 附件；空路径不算。 */
function pendingFromPath(path: string, source: PendingAttachment["source"]): PendingAttachment | null {
  const trimmed = path.trim();
  if (!trimmed) return null;
  const normalized = trimmed.replace(/\\/g, "/");
  const base = normalized.split("/").filter(Boolean).pop() ?? trimmed;
  return { path: trimmed, displayName: base, source };
}

/**
 * 快捷键提示里的修饰键。
 *
 * 原型写的是 ⌘（导出包来自 macOS）。产品六目标里有四个 Windows/Linux 目标，
 * 在那些平台上把 Ctrl 画成 ⌘ 是**说错了一件事**：用户按 ⌘ 什么都不会发生。
 */
function modifierKey(): string {
  if (typeof navigator === "undefined") return "Ctrl";
  const platform = navigator.platform || navigator.userAgent || "";
  return /Mac|iPhone|iPad|iPod/.test(platform) ? "⌘" : "Ctrl";
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
  checkingHint = null,
  fixture = null,
  projectName = "",
  projectPath = "",
}: ComposerProps) {
  const [pathInput, setPathInput] = useState("");
  const [pending, setPending] = useState<PendingAttachment[]>([]);
  const [accepted, setAccepted] = useState<ChatAttachmentMeta[]>([]);
  const [rejected, setRejected] = useState<ChatAttachmentRejection[]>([]);
  const [resolving, setResolving] = useState(false);
  const [resolveError, setResolveError] = useState<CallError | null>(null);
  const [skillId, setSkillId] = useState("");
  const [drawerOpen, setDrawerOpen] = useState(false);
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
    setDrawerOpen(false);
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

  const hasAttachments = accepted.length > 0 || rejected.length > 0;
  const footHint = blockedHint ?? checkingHint ?? (stopRequested ? t("chat.composer.stopRequested") : null);

  return (
    <div data-composer="" className="achat-composer-wrap">
      {/* 项目行：只显示事实（当前工作区）。产品没有“在对话里换项目”的后端入口，
          所以这里不做成一个点了没用的下拉。 */}
      <div className="achat-project-row">
        <div className="achat-project-control">
          <span
            className="achat-project-trigger"
            data-composer-project=""
            title={projectPath || undefined}
            role="group"
            aria-label={t("chat.composer.project")}
          >
            <Folder size={12} aria-hidden />
            <span>{projectName || t("chat.composer.noProject")}</span>
          </span>
        </div>
      </div>

      <form
        className="achat-composer"
        onSubmit={(e) => {
          e.preventDefault();
          send();
        }}
      >
        <label htmlFor="composer-input" className="sr-only">
          {t("chat.composer.placeholder")}
        </label>
        <textarea
          id="composer-input"
          ref={textareaRef}
          value={value}
          rows={2}
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={onKeyDown}
          placeholder={t("chat.composer.placeholder")}
          disabled={stopRequested || blockedHint !== null}
        />

        {/* 附件行：没有附件时整行不出现（原型 hidden） */}
        {hasAttachments ? (
          <div className="achat-composer-attachments" data-composer-attachments="" aria-live="polite">
            {accepted.map((item) => (
              <span
                key={item.id}
                data-attachment-accepted={item.id}
                className="achat-attachment"
                data-kind={item.mediaType?.startsWith("application/x-anvil-skill") ? "skill" : "file"}
              >
                <Paperclip size={11} aria-hidden />
                <b style={{ fontWeight: 550, maxWidth: 130, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                  {item.displayName}
                </b>
                <span>{formatBytes(item.size)}</span>
                <span className="achat-mono">{item.sha256.slice(0, 8)}</span>
                <span data-attachment-status={item.status} title={t("chat.composer.accepted")}>
                  {t("chat.composer.accepted")}
                </span>
                <button
                  type="button"
                  onClick={() => removeAccepted(item.id)}
                  aria-label={t("chat.composer.remove")}
                  title={t("chat.composer.remove")}
                >
                  ×
                </button>
              </span>
            ))}
            {rejected.map((item) => (
              <span
                key={`${item.path}:${item.code}`}
                data-attachment-rejected={item.code}
                className="achat-attachment-rejected"
              >
                <span>
                  <b style={{ fontWeight: 550 }}>{item.displayName || item.path}</b>
                  {" · "}
                  {t("chat.composer.rejected")}
                  <span data-attachment-reason="" style={{ display: "block", marginTop: 1 }}>
                    {t("chat.composer.rejectedReason", { code: item.code, message: item.message })}
                  </span>
                </span>
              </span>
            ))}
          </div>
        ) : null}

        {/* 附件抽屉：由「+」打开（原型 attachment-drawer） */}
        {drawerOpen ? (
          <div
            id="composer-attachment-drawer"
            className="achat-attachment-drawer"
            data-attachment-drawer=""
            role="group"
            aria-label={t("chat.composer.attachmentsMenu")}
          >
            <div className="achat-attachment-group">
              <span className="achat-attachment-label">{t("chat.composer.filesOrDirectory")}</span>
              <input
                className="achat-attachment-input"
                type="text"
                value={pathInput}
                onChange={(e) => setPathInput(e.target.value)}
                placeholder={t("chat.composer.pathPlaceholder")}
                aria-label={t("chat.composer.pathPlaceholder")}
              />
              <div className="achat-attachment-actions">
                <button type="button" className="achat-btn achat-btn-sm" onClick={() => addPath("file")}>
                  {t("chat.composer.addFile")}
                </button>
                <button type="button" className="achat-btn achat-btn-sm" onClick={() => addPath("directory")}>
                  {t("chat.composer.addDirectory")}
                </button>
              </div>
            </div>

            {skills.length > 0 ? (
              <div className="achat-attachment-group">
                <label className="achat-attachment-label" htmlFor="composer-skill">
                  {t("chat.composer.skillPick")}
                </label>
                <select
                  id="composer-skill"
                  className="achat-attachment-select"
                  value={skillId}
                  onChange={(e) => setSkillId(e.target.value)}
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
                  className="achat-btn achat-btn-sm"
                  onClick={addSkill}
                  disabled={!skillId}
                  style={{ alignSelf: "flex-start" }}
                >
                  {t("chat.composer.addSkill")}
                </button>
              </div>
            ) : null}

            {resolving ? <p className="achat-attachment-note">{t("chat.composer.resolving")}</p> : null}
            {!hasAttachments && !resolving ? (
              <p className="achat-attachment-note">{t("chat.composer.noAttachments")}</p>
            ) : null}
            <p className="achat-attachment-note">{t("chat.composer.attachmentsHint")}</p>
            {resolveError ? (
              <p role="alert" className="achat-attachment-note" style={{ color: "var(--anvil-danger)" }}>
                {t("chat.composer.resolveFailed", { code: resolveError.code })}
              </p>
            ) : null}
          </div>
        ) : null}

        {/* bar：+ · 模型 · 思考级别 · 权限摘要 · 停止/发送 */}
        <div className="achat-composer-bar">
          <button
            type="button"
            className="achat-plus"
            data-composer-plus=""
            aria-label={t("chat.composer.attachmentsMenu")}
            title={t("chat.composer.attachmentsMenu")}
            aria-expanded={drawerOpen}
            aria-controls="composer-attachment-drawer"
            onClick={() => setDrawerOpen((v) => !v)}
          >
            <Plus size={14} aria-hidden />
          </button>

          <span className="achat-ctl" title={t("chat.composer.modelHint")}>
            <Sparkles size={13} aria-hidden />
            <span data-composer-model="" className="achat-ctl-mono">
              {modelLabel || t("common.unknown")}
            </span>
            {fixture ? (
              <span
                data-fixture-badge=""
                title={t("chat.composer.fixtureHint")}
                className="achat-badge achat-badge-warn"
              >
                {t("chat.composer.fixtureBadge")}
              </span>
            ) : null}
          </span>

          <span className="achat-ctl" title={t("chat.composer.profileHint")}>
            <Brain size={13} aria-hidden />
            <span data-composer-thinking="">{t("chat.composer.thinkingUnknown")}</span>
          </span>

          <span className="achat-ctl" title={t("chat.composer.permission")} data-composer-permission="">
            <Shield size={13} aria-hidden />
            <span data-permission-summary="">
              {t("chat.composer.permission")}
              {" · "}
              {permissionText}
            </span>
          </span>

          <div className="achat-composer-actions">
            <button
              type="button"
              className="achat-btn achat-btn-sm"
              onClick={onStop}
              disabled={!busy || stopRequested}
            >
              <Square size={11} aria-hidden />
              {stopRequested ? t("chat.composer.stopping") : t("chat.composer.stop")}
            </button>
            <button
              type="submit"
              className="achat-send"
              disabled={!canSend}
              aria-label={t("chat.composer.send")}
              title={t("chat.composer.send")}
            >
              <ArrowUp size={15} aria-hidden />
            </button>
          </div>
        </div>
      </form>

      {/* foot：提示 + 快捷键（原型的 .composer-foot） */}
      <div className="achat-composer-foot">
        {footHint !== null ? (
          <span
            data-composer-blocked={blockedHint !== null ? "" : undefined}
            data-composer-checking={checkingHint !== null && blockedHint === null ? "" : undefined}
            data-composer-readonly={stopRequested ? "" : undefined}
            className={blockedHint !== null ? "achat-hint-warn" : undefined}
            role="status"
          >
            {footHint}
          </span>
        ) : (
          <span>{t("chat.composer.footHint")}</span>
        )}
        <span className="achat-composer-foot-spacer" />
        <span className="achat-kbdset">
          <kbd className="achat-kbd">{modifierKey()}</kbd>
          <kbd className="achat-kbd">↵</kbd>
          {t("chat.composer.footSend")}
        </span>
        <span className="achat-kbdset">
          <kbd className="achat-kbd">⇧</kbd>
          <kbd className="achat-kbd">↵</kbd>
          {t("chat.composer.footNewline")}
        </span>
      </div>

      {sendError ? (
        <p role="alert" className="achat-hint achat-hint-danger" style={{ maxWidth: 760, margin: "6px auto 0" }}>
          {formatError(sendError)}
        </p>
      ) : null}

      {/* profile 仍是后端事实：不给下拉，只在有值时以 title 说明（读屏可及）。 */}
      {profile ? (
        <p className="achat-hint sr-only" data-composer-profile="">
          {t("chat.composer.profile")}
          {" · "}
          {profile.name || profile.id}
        </p>
      ) : null}
    </div>
  );
}
