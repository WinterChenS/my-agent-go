// 诊断包导出（C07 `ExportDiagnostics`）。
//
// 导出只在用户点击时发生，且只写本机诊断目录（不上传任何地方）。
// 结果里展示后端给出的路径、大小、sha256、脱敏计数与 schema 版本——
// 这些是"这份包是什么"的可核对事实；失败时只显示 code，不显示半份路径。

import { useState } from "react";
import type { CallError } from "../../types";
import { formatBytes, formatDate, formatNumber, t } from "../../i18n";
import { AsyncState } from "../../components/states/AsyncState";
import { toCallError } from "../../app/useBackend";
import { observabilityApi, type DiagnosticsExportResult } from "./api";

export interface DiagnosticsExportPanelProps {
  runIds?: string[];
  sessionId?: string;
  workspaceId?: string;
  from?: string;
  to?: string;
  target?: string;
}

export function DiagnosticsExportPanel({
  runIds = [],
  sessionId = "",
  workspaceId = "",
  from = "",
  to = "",
  target = "",
}: DiagnosticsExportPanelProps) {
  const [exporting, setExporting] = useState(false);
  const [error, setError] = useState<CallError | null>(null);
  const [result, setResult] = useState<DiagnosticsExportResult | null>(null);

  async function exportNow() {
    setExporting(true);
    setError(null);
    setResult(null);
    try {
      const res = await observabilityApi.exportDiagnostics({
        runIds,
        sessionId,
        workspaceId,
        from,
        to,
        target,
      });
      if (res?.error) {
        setError(res.error);
        return;
      }
      setResult(res ?? null);
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setExporting(false);
    }
  }

  return (
    <div className="space-y-2">
      <p className="text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
        {t("observability.export.hint")}
      </p>
      <button
        type="button"
        onClick={() => void exportNow()}
        disabled={exporting}
        className="rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface-raised)] disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
      >
        {exporting ? t("observability.export.exporting") : t("observability.export.action")}
      </button>

      {error ? <AsyncState phase="error" error={error} onRetry={() => void exportNow()} /> : null}

      {result ? (
        <dl data-testid="export-result" className="space-y-0.5 text-[10px]">
          <div className="flex gap-2">
            <dt className="w-24 shrink-0 text-[color:var(--anvil-muted)]">{t("observability.export.path")}</dt>
            <dd data-testid="export-path" className="min-w-0 flex-1 break-all font-mono">
              {result.path}
            </dd>
          </div>
          <div className="flex gap-2">
            <dt className="w-24 shrink-0 text-[color:var(--anvil-muted)]">{t("observability.export.bytes")}</dt>
            <dd className="min-w-0 flex-1">{formatBytes(result.bytes)}</dd>
          </div>
          <div className="flex gap-2">
            <dt className="w-24 shrink-0 text-[color:var(--anvil-muted)]">{t("observability.export.sha256")}</dt>
            <dd data-testid="export-sha256" className="min-w-0 flex-1 break-all font-mono">
              {result.sha256}
            </dd>
          </div>
          <div className="flex gap-2">
            <dt className="w-24 shrink-0 text-[color:var(--anvil-muted)]">
              {t("observability.export.redactions")}
            </dt>
            <dd data-testid="export-redactions" className="min-w-0 flex-1">
              {formatNumber(result.redactions)}
            </dd>
          </div>
          <div className="flex gap-2">
            <dt className="w-24 shrink-0 text-[color:var(--anvil-muted)]">{t("observability.export.runs")}</dt>
            <dd className="min-w-0 flex-1">{formatNumber(result.runs)}</dd>
          </div>
          <div className="flex gap-2">
            <dt className="w-24 shrink-0 text-[color:var(--anvil-muted)]">{t("observability.export.events")}</dt>
            <dd className="min-w-0 flex-1">{formatNumber(result.events)}</dd>
          </div>
          <div className="flex gap-2">
            <dt className="w-24 shrink-0 text-[color:var(--anvil-muted)]">
              {t("observability.export.artifacts")}
            </dt>
            <dd className="min-w-0 flex-1">{formatNumber(result.artifacts)}</dd>
          </div>
          <div className="flex gap-2">
            <dt className="w-24 shrink-0 text-[color:var(--anvil-muted)]">
              {t("observability.export.schema")}
            </dt>
            <dd data-testid="export-schema" className="min-w-0 flex-1">
              {formatNumber(result.schemaVersion)}
            </dd>
          </div>
          <div className="flex gap-2">
            <dt className="w-24 shrink-0 text-[color:var(--anvil-muted)]">
              {t("observability.export.generatedAt")}
            </dt>
            <dd className="min-w-0 flex-1">{formatDate(result.generatedAt)}</dd>
          </div>
        </dl>
      ) : null}
    </div>
  );
}
