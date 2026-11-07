// 技能导入对话框（U04 / RES05）。
//
// 两个入口共用一层壳，但动作是分开的：
//   - 从链接导入：先 InspectSkillLink（只解析清单），把每条拒绝原因逐项展示，
//     用户确认后把暂存路径交给 ImportSkill；
//   - 本地新建：CreateSkill 直接落盘，名称是稳定引用（非法名称由后端拒绝）。
//
// 界面不推断成功：导入/新建的结论来自 Go 返回值；被拒绝时列原因，不给"重试"
// 以外的假动作。

import { useState } from "react";
import { formatError, t } from "../../i18n";
import type { CallError } from "../../types";
import {
  libraryApi,
  type SkillLinkInspection,
  type SkillLinkRejection,
} from "./api";

/** 把规则 ID 取成当前语言文案；缺 key 时不显示规则原文当成文案。 */
export function rejectLabel(rejection: SkillLinkRejection): string {
  const params = rejection.params ?? {};
  const key = `library.reject.${rejection.rule.toLowerCase()}`;
  return t(key, { ...params });
}

export interface SkillImportDialogProps {
  workspaceId: string;
  mode: "link" | "create";
  onImported: () => void;
  onClose: () => void;
}

export function SkillImportDialog({ workspaceId, mode, onImported, onClose }: SkillImportDialogProps) {
  const [url, setUrl] = useState("");
  const [name, setName] = useState("");
  const [inspecting, setInspecting] = useState(false);
  const [creating, setCreating] = useState(false);
  const [importing, setImporting] = useState(false);
  const [inspection, setInspection] = useState<SkillLinkInspection | null>(null);
  const [error, setError] = useState<CallError | null>(null);

  const rejections = inspection?.rejections ?? [];
  const canImport = Boolean(inspection?.stagedPath) && rejections.length === 0 && !importing;

  async function inspect() {
    setError(null);
    setInspection(null);
    setInspecting(true);
    try {
      const res = await libraryApi.inspectSkillLink(url.trim());
      if (res.error) {
        setError(res.error);
        return;
      }
      setInspection(res);
    } catch (e) {
      setError(asCallError(e));
    } finally {
      setInspecting(false);
    }
  }

  async function confirmImport() {
    if (!inspection?.stagedPath) return;
    setError(null);
    setImporting(true);
    try {
      const res = await libraryApi.importSkill(inspection.stagedPath, "user", workspaceId);
      if (res.error) {
        setError(res.error);
        return;
      }
      onImported();
      onClose();
    } catch (e) {
      setError(asCallError(e));
    } finally {
      setImporting(false);
    }
  }

  async function createLocal() {
    setError(null);
    setCreating(true);
    try {
      const res = await libraryApi.createSkill(name.trim(), "user", workspaceId);
      if (res.error) {
        setError(res.error);
        return;
      }
      onImported();
      onClose();
    } catch (e) {
      setError(asCallError(e));
    } finally {
      setCreating(false);
    }
  }

  return (
    <section
      aria-label={mode === "link" ? t("library.import.title") : t("library.import.createTitle")}
      className="space-y-3 rounded-lg border border-[color:var(--anvil-border)] p-3"
    >
      <h2 className="text-[12px] font-medium text-[color:var(--anvil-foreground)]">
        {mode === "link" ? t("library.import.title") : t("library.import.createTitle")}
      </h2>

      {mode === "link" ? (
        <>
          <label className="block text-[11px] text-[color:var(--anvil-muted)]">
            {t("library.import.urlLabel")}
            <input
              type="url"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              placeholder={t("library.import.urlPlaceholder")}
              aria-label={t("library.import.urlLabel")}
              className="mt-1 w-full rounded-md border border-[color:var(--anvil-border)] bg-transparent px-2 py-1 text-[12px] text-[color:var(--anvil-foreground)]"
            />
          </label>
          <p className="text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
            {t("library.import.rulesHint")}
          </p>
          <div className="flex flex-wrap gap-2">
            <button
              type="button"
              onClick={() => void inspect()}
              disabled={inspecting || url.trim() === ""}
              className="rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-50"
            >
              {inspecting ? t("library.import.inspecting") : t("library.import.inspect")}
            </button>
            <button
              type="button"
              onClick={onClose}
              className="rounded-md px-2.5 py-1 text-[11px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)]"
            >
              {t("common.cancel")}
            </button>
          </div>
        </>
      ) : (
        <>
          <label className="block text-[11px] text-[color:var(--anvil-muted)]">
            {t("library.import.createNameLabel")}
            <input
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t("library.import.createNamePlaceholder")}
              aria-label={t("library.import.createNameLabel")}
              className="mt-1 w-full rounded-md border border-[color:var(--anvil-border)] bg-transparent px-2 py-1 text-[12px] text-[color:var(--anvil-foreground)]"
            />
          </label>
          <div className="flex flex-wrap gap-2">
            <button
              type="button"
              onClick={() => void createLocal()}
              disabled={creating || name.trim() === ""}
              className="rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-50"
            >
              {creating ? t("library.import.creating") : t("library.import.create")}
            </button>
            <button
              type="button"
              onClick={onClose}
              className="rounded-md px-2.5 py-1 text-[11px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)]"
            >
              {t("common.cancel")}
            </button>
          </div>
        </>
      )}

      {error ? (
        <p role="alert" className="rounded-md border border-[color:var(--anvil-danger-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-danger)]">
          {formatError(error)}
        </p>
      ) : null}

      {rejections.length > 0 ? (
        <div
          data-testid="link-rejections"
          className="rounded-md border border-[color:var(--anvil-danger-border)] p-2"
        >
          <p className="text-[11px] font-medium text-[color:var(--anvil-danger)]">
            {t("library.import.rejectTitle", { count: rejections.length })}
          </p>
          <ul className="mt-1 list-disc space-y-0.5 pl-4">
            {rejections.map((r, i) => (
              <li key={`${r.rule}-${i}`} className="text-[11px] text-[color:var(--anvil-foreground)]">
                {rejectLabel(r)}
              </li>
            ))}
          </ul>
          <p className="mt-1 text-[10px] text-[color:var(--anvil-muted)]">{t("library.import.rejectHint")}</p>
        </div>
      ) : null}

      {inspection && rejections.length === 0 ? (
        <div className="space-y-2 rounded-md border border-[color:var(--anvil-border)] p-2">
          <p className="text-[11px] font-medium text-[color:var(--anvil-foreground)]">
            {t("library.import.manifest")}
          </p>
          <dl className="grid grid-cols-2 gap-x-3 gap-y-0.5 text-[11px]">
            <dt className="text-[color:var(--anvil-muted)]">{t("library.import.name")}</dt>
            <dd className="text-[color:var(--anvil-foreground)]">{inspection.name}</dd>
            <dt className="text-[color:var(--anvil-muted)]">{t("library.import.description")}</dt>
            <dd className="text-[color:var(--anvil-foreground)]">{inspection.description}</dd>
            <dt className="text-[color:var(--anvil-muted)]">{t("library.import.form")}</dt>
            <dd className="text-[color:var(--anvil-foreground)]">{inspection.form}</dd>
            <dt className="text-[color:var(--anvil-muted)]">{t("library.labels.files")}</dt>
            <dd className="text-[color:var(--anvil-foreground)]">{inspection.files}</dd>
            <dt className="text-[color:var(--anvil-muted)]">{t("library.labels.size")}</dt>
            <dd className="text-[color:var(--anvil-foreground)]">{inspection.bytes}</dd>
            <dt className="text-[color:var(--anvil-muted)]">{t("library.labels.requires")}</dt>
            <dd className="text-[color:var(--anvil-foreground)]">
              {(inspection.requires ?? []).length > 0
                ? (inspection.requires ?? []).join(", ")
                : t("common.notAvailable")}
            </dd>
            <dt className="text-[color:var(--anvil-muted)]">{t("library.labels.redirects")}</dt>
            <dd className="text-[color:var(--anvil-foreground)]">{(inspection.redirects ?? []).length}</dd>
          </dl>
          <Warnings warnings={inspection.warnings ?? []} />
          <p className="text-[10px] text-[color:var(--anvil-muted)]">{t("library.import.stagedHint")}</p>
          <button
            type="button"
            onClick={() => void confirmImport()}
            disabled={!canImport}
            className="rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-50"
          >
            {importing ? t("library.import.importing") : t("library.import.confirm")}
          </button>
        </div>
      ) : null}
    </section>
  );
}

/** 归档里的脚本/重定向等提示逐条显示：它们不是拒绝，但用户要知道。 */
function Warnings({ warnings }: { warnings: string[] }) {
  if (warnings.length === 0) {
    return <p className="text-[10px] text-[color:var(--anvil-muted)]">{t("library.import.noWarnings")}</p>;
  }
  return (
    <div>
      <p className="text-[10px] font-medium text-[color:var(--anvil-warning)]">
        {t("library.import.warningTitle")}
      </p>
      <ul className="mt-0.5 list-disc space-y-0.5 pl-4">
        {warnings.map((w) => (
          <li key={w} className="text-[10px] text-[color:var(--anvil-foreground)]">
            {warningLabel(w)}
          </li>
        ))}
      </ul>
      <p className="mt-0.5 text-[10px] text-[color:var(--anvil-muted)]">{t("library.import.warningBody")}</p>
    </div>
  );
}

/** 提示是 `script:<rel>` 这样的结构化前缀，翻译在前端发生。 */
export function warningLabel(raw: string): string {
  if (raw.startsWith("script:")) {
    return t("library.caution.script", { path: raw.slice("script:".length) });
  }
  return t("library.caution.unknown", { detail: raw });
}

function asCallError(e: unknown): CallError {
  if (typeof e === "object" && e !== null && "code" in e) {
    return e as CallError;
  }
  return { code: "INTERNAL_ERROR", message: String(e), retryable: false };
}
