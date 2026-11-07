// 观测页（U06 / UI11）。
//
// 四条纪律：
//   - 缺 usage / 缺价格 = unavailable，绝不显示 0（RUN06）；
//   - run 链路只来自后端事件记录（tools/approvals/errors/…，按 seq 排序），
//     缺哪个段落就把它列进 unavailable，不拿空数组冒充"没有发生过"；
//   - 事件很多时分页（先 50 条，可手动展开），不一次渲染全部；
//   - 导出只写本机诊断目录，界面只展示后端返回的路径/hash/脱敏计数。
//
// 已知缺口：C07 没有"列 run"的接口（Plan 里的 `RunChain` 在 Go 侧是
// `RunDetails(runId)`）。所以这里只能按 run id 打开一条 run（支持
// `anvil://observability?run=<id>` 深链），并以 unavailable 说明无法列出最近错误。
// 需要的 DTO 已登记在 U06-report.md，本任务不改 Go。

import { useCallback, useEffect, useMemo, useState } from "react";
import type { PageProps } from "../../app/navigation";
import type { CallError } from "../../types";
import { formatDate, formatNumber, hasKey, t } from "../../i18n";
import { AsyncState } from "../../components/states/AsyncState";
import { toCallError } from "../../app/useBackend";
import {
  observabilityApi,
  type ApprovalFact,
  type EventFact,
  type RunDetails,
  type ToolCallFact,
  type UsageSummary,
} from "./api";
import { DiagnosticsExportPanel } from "./DiagnosticsExport";

/** 事件链路的默认上界：一次只看 50 条，用户可以手动展开。 */
const CHAIN_LIMIT = 50;

type Phase = "loading" | "success" | "error" | "empty";

interface ChainRow {
  seq: number;
  eventId: string;
  kind: "tool" | "approval" | "error" | "compaction" | "diagnostic";
  title: string;
  detail: string;
  at: string;
}

function buildChain(details: RunDetails): ChainRow[] {
  const rows: ChainRow[] = [];
  for (const tool of details.tools ?? []) rows.push(toolRow(tool));
  for (const approval of details.approvals ?? []) rows.push(approvalRow(approval));
  for (const err of details.errors ?? []) rows.push(eventRow(err, "error"));
  for (const c of details.compactions ?? []) rows.push(eventRow(c, "compaction"));
  for (const d of details.diagnostics ?? []) rows.push(eventRow(d, "diagnostic"));
  // 链路 = 事件顺序，不是按类型分组：分组会让人看不出"审批之前的工具调用"。
  return rows.sort((a, b) => a.seq - b.seq);
}

function toolRow(tool: ToolCallFact): ChainRow {
  const state = tool.isError === true ? t("observability.tools.failed") : tool.type;
  return {
    seq: tool.seq,
    eventId: tool.eventId,
    kind: "tool",
    title: tool.toolName || t("observability.tools.unknown"),
    detail: state,
    at: tool.occurredAt,
  };
}

function approvalRow(approval: ApprovalFact): ChainRow {
  return {
    seq: approval.seq,
    eventId: approval.eventId,
    kind: "approval",
    title: approval.toolName || t("observability.approvals.unknown"),
    detail: approval.choice || approval.type,
    at: approval.occurredAt,
  };
}

function eventRow(ev: EventFact, kind: ChainRow["kind"]): ChainRow {
  return {
    seq: ev.seq,
    eventId: ev.eventId,
    kind,
    title: ev.kind || ev.type,
    detail: ev.message || ev.type,
    at: ev.occurredAt,
  };
}

// ---------------------------------------------------------------- usage

function UsageSection({ runIds }: { runIds: string[] }) {
  const runIdsKey = runIds.join(",");
  const [phase, setPhase] = useState<Phase>("loading");
  const [error, setError] = useState<CallError | null>(null);
  const [summary, setSummary] = useState<UsageSummary | null>(null);
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [appliedFrom, setAppliedFrom] = useState("");
  const [appliedTo, setAppliedTo] = useState("");

  const load = useCallback(async () => {
    setPhase("loading");
    setError(null);
    try {
      const res = await observabilityApi.usageSummary({
        from: appliedFrom,
        to: appliedTo,
        workspaceId: "",
        sessionId: "",
        runIds: runIdsKey === "" ? [] : runIdsKey.split(","),
      });
      if (res?.error) {
        setError(res.error);
        setPhase("error");
        return;
      }
      setSummary(res?.summary ?? null);
      setPhase("success");
    } catch (e) {
      setError(toCallError(e));
      setPhase("error");
    }
  }, [appliedFrom, appliedTo, runIdsKey]);

  useEffect(() => {
    void load();
  }, [load]);

  const tokensReported = summary?.tokens?.availability === "reported";
  const costReported = summary?.cost?.availability === "reported";

  return (
    <section
      data-testid="observability-usage"
      data-state={phase}
      aria-labelledby="observability-usage-title"
      className="rounded-xl border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] p-3.5"
    >
      <header className="mb-2">
        <h2 id="observability-usage-title" className="text-sm font-medium text-[color:var(--anvil-foreground)]">
          {t("observability.usage.title")}
        </h2>
        <p className="mt-0.5 text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
          {t("observability.usage.hint")}
        </p>
      </header>

      <div className="flex flex-wrap items-end gap-2">
        <label className="flex flex-col gap-1">
          <span className="text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
            {t("observability.usage.from")}
          </span>
          <input
            aria-label={t("observability.usage.from")}
            value={from}
            onChange={(e) => setFrom(e.target.value)}
            className="rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1.5 font-mono text-[11px] text-[color:var(--anvil-foreground)]"
          />
        </label>
        <label className="flex flex-col gap-1">
          <span className="text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
            {t("observability.usage.to")}
          </span>
          <input
            aria-label={t("observability.usage.to")}
            value={to}
            onChange={(e) => setTo(e.target.value)}
            className="rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1.5 font-mono text-[11px] text-[color:var(--anvil-foreground)]"
          />
        </label>
        <button
          type="button"
          onClick={() => {
            setAppliedFrom(from);
            setAppliedTo(to);
          }}
          className="rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface-raised)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
        >
          {t("observability.usage.apply")}
        </button>
      </div>

      {phase === "loading" ? <AsyncState phase="loading" /> : null}
      {phase === "error" ? (
        <AsyncState phase="error" error={error} onRetry={() => void load()} />
      ) : null}

      {phase === "success" && summary ? (
        <div className="mt-2 space-y-1.5 text-[11px]">
          <div className="flex flex-wrap gap-x-4 gap-y-1">
            <span data-testid="usage-runs">
              {t("observability.usage.runs")}
              {": "}
              {formatNumber(summary.runs)}
            </span>
            <span data-testid="usage-reported">
              {t("observability.usage.reportedRuns")}
              {": "}
              {formatNumber(summary.reportedRuns)}
            </span>
            <span data-testid="usage-unreported">
              {t("observability.usage.unreportedRuns")}
              {": "}
              {formatNumber(summary.unreportedRuns)}
            </span>
          </div>

          {tokensReported ? (
            <div data-testid="usage-tokens" className="flex flex-wrap gap-x-4 gap-y-1">
              <span data-testid="usage-tokens-input">
                {t("observability.usage.input")}
                {": "}
                {formatNumber(summary.tokens.inputTokens ?? 0)}
              </span>
              <span data-testid="usage-tokens-output">
                {t("observability.usage.output")}
                {": "}
                {formatNumber(summary.tokens.outputTokens ?? 0)}
              </span>
              <span data-testid="usage-tokens-total">
                {t("observability.usage.total")}
                {": "}
                {formatNumber(summary.tokens.totalTokens ?? 0)}
              </span>
            </div>
          ) : null}

          {!tokensReported ? null : costReported ? (
            <div data-testid="usage-cost" className="flex flex-wrap gap-x-4 gap-y-1">
              <span data-testid="usage-cost-value">
                {t("observability.usage.cost")}
                {": "}
                {formatNumber(summary.cost.costUsd ?? 0, { maximumFractionDigits: 4 })}
              </span>
              {summary.cost.priceSource ? (
                <span data-testid="usage-cost-source">
                  {t("observability.usage.priceSource")}
                  {": "}
                  {summary.cost.priceSource}
                </span>
              ) : null}
              {summary.cost.estimated ? (
                <span data-testid="usage-cost-estimated">{t("observability.usage.estimated")}</span>
              ) : null}
            </div>
          ) : (
            <div data-testid="usage-cost" className="text-[color:var(--anvil-muted)]">
              {t("observability.usage.cost")}
              {": "}
              {t("observability.usage.unavailable")}
            </div>
          )}

          {!tokensReported || summary.availability !== "reported" ? (
            <p data-testid="usage-unavailable" className="text-[color:var(--anvil-muted)]">
              {t("observability.usage.unavailable")}
            </p>
          ) : null}

          {summary.note ? (
            <p data-testid="usage-note" className="text-[10px] text-[color:var(--anvil-muted)]">
              {summary.note}
            </p>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}

// ---------------------------------------------------------------- run

function RunSection({
  runId,
  onNavigate,
}: {
  runId: string;
  onNavigate: (path: string) => void;
}) {
  const [phase, setPhase] = useState<Phase>("empty");
  const [error, setError] = useState<CallError | null>(null);
  const [details, setDetails] = useState<RunDetails | null>(null);
  const [limit, setLimit] = useState(CHAIN_LIMIT);

  const load = useCallback(async () => {
    if (!runId) {
      setDetails(null);
      setError(null);
      setPhase("empty");
      return;
    }
    setPhase("loading");
    setError(null);
    try {
      const res = await observabilityApi.runDetails(runId);
      if (res?.error) {
        setError(res.error);
        setPhase("error");
        return;
      }
      setDetails(res?.details ?? null);
      setLimit(CHAIN_LIMIT);
      setPhase("success");
    } catch (e) {
      setError(toCallError(e));
      setPhase("error");
    }
  }, [runId]);

  useEffect(() => {
    void load();
  }, [load]);

  const chain = useMemo(() => (details ? buildChain(details) : []), [details]);
  const shown = chain.slice(0, limit);
  const record = details?.record;
  const sessionId = record?.sessionId ?? "";

  return (
    <section
      data-testid="observability-run"
      data-state={phase}
      aria-labelledby="observability-run-title"
      className="rounded-xl border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] p-3.5"
    >
      <header className="mb-2">
        <h2 id="observability-run-title" className="text-sm font-medium text-[color:var(--anvil-foreground)]">
          {t("observability.run.title")}
        </h2>
        <p className="mt-0.5 text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
          {t("observability.run.hint")}
        </p>
      </header>

      {phase === "empty" ? (
        <div className="space-y-2">
          <AsyncState
            phase="empty"
            emptyTitle={t("observability.run.emptyTitle")}
            emptyHint={t("observability.run.emptyHint")}
          />
          <p data-testid="run-list-unavailable" className="text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
            {t("observability.run.listUnavailableReason")}
          </p>
        </div>
      ) : null}

      {phase === "loading" ? <AsyncState phase="loading" /> : null}
      {phase === "error" ? (
        <AsyncState phase="error" error={error} onRetry={() => void load()} />
      ) : null}

      {phase === "success" && details ? (
        <div className="space-y-2">
          <div className="flex flex-wrap items-center gap-2 text-[11px]">
            <code className="font-mono text-[color:var(--anvil-foreground)]">{details.runId}</code>
            {record?.state ? (
              <span className="text-[10px] text-[color:var(--anvil-muted)]">
                {hasKey(`status.${record.state}`) ? t(`status.${record.state}`) : record.state}
              </span>
            ) : null}
            {record?.provider ? (
              <code className="font-mono text-[10px] text-[color:var(--anvil-muted)]">
                {record.provider}/{record.model}
              </code>
            ) : null}
            <span data-testid="run-event-count" className="text-[10px] text-[color:var(--anvil-muted)]">
              {t("observability.chain.eventCount")}
              {": "}
              {formatNumber(details.eventCount)}
            </span>
            {details.lastEventId ? (
              <code className="font-mono text-[10px] text-[color:var(--anvil-muted)]">{details.lastEventId}</code>
            ) : null}
            {sessionId ? (
              <button
                type="button"
                onClick={() => onNavigate(`/chat/${sessionId}`)}
                className="rounded-md border border-[color:var(--anvil-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface-raised)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
              >
                {t("observability.run.openSession")}
              </button>
            ) : null}
          </div>

          <div data-testid="run-chain">
            <p className="text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
              {t("observability.chain.title")}
            </p>
            {chain.length === 0 ? (
              <p className="mt-1 text-[11px] text-[color:var(--anvil-muted)]">
                {t("observability.chain.empty")}
              </p>
            ) : (
              <ol className="mt-1 space-y-0.5">
                {shown.map((row) => (
                  <li
                    key={`${row.kind}-${row.seq}`}
                    data-testid={`chain-row-${row.seq}`}
                    data-seq={row.seq}
                    data-kind={row.kind}
                    className="flex flex-wrap items-baseline gap-2 rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 text-[10px]"
                  >
                    <code className="font-mono text-[color:var(--anvil-muted)]">{row.seq}</code>
                    <span className="text-[color:var(--anvil-muted)]">{row.kind}</span>
                    <span className="text-[color:var(--anvil-foreground)]">{row.title}</span>
                    <span className="min-w-0 flex-1 break-all text-[color:var(--anvil-muted)]">
                      {row.detail}
                    </span>
                    <span className="text-[color:var(--anvil-muted)]">{formatDate(row.at)}</span>
                  </li>
                ))}
              </ol>
            )}
            {chain.length > shown.length ? (
              <button
                type="button"
                onClick={() => setLimit((n) => n + CHAIN_LIMIT)}
                className="mt-1 rounded-md border border-[color:var(--anvil-border)] px-2 py-0.5 text-[10px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface-raised)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
              >
                {t("common.showMore")}
              </button>
            ) : null}
          </div>

          <div data-testid="run-unavailable" className="space-y-0.5">
            <p className="text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
              {t("observability.unavailable.title")}
            </p>
            {(details.unavailable ?? []).length === 0 ? (
              <p className="text-[11px] text-[color:var(--anvil-muted)]">{t("observability.unavailable.empty")}</p>
            ) : (
              <ul className="space-y-0.5">
                {(details.unavailable ?? []).map((u) => (
                  <li key={u.section} className="text-[10px] text-[color:var(--anvil-muted)]">
                    <code className="font-mono">{u.section}</code>
                    {" — "}
                    {u.reason}
                  </li>
                ))}
              </ul>
            )}
          </div>

          {details.artifacts && details.artifacts.length > 0 ? (
            <div data-testid="run-artifacts">
              <p className="text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">
                {t("observability.artifacts.title")}
              </p>
              <ul className="mt-0.5 space-y-0.5">
                {details.artifacts.map((a) => (
                  <li key={a.artifactId} className="flex flex-wrap gap-2 text-[10px]">
                    <span className="text-[color:var(--anvil-foreground)]">{a.displayName}</span>
                    <span className="text-[color:var(--anvil-muted)]">{a.mediaType}</span>
                    <code className="break-all font-mono text-[color:var(--anvil-muted)]">{a.sha256}</code>
                  </li>
                ))}
              </ul>
            </div>
          ) : null}

          {record?.error ? (
            <div data-testid="run-error" className="rounded border border-[color:var(--anvil-danger-border)] bg-[color:var(--anvil-danger-surface)] px-2 py-1 text-[10px] text-[color:var(--anvil-danger)]">
              <code className="font-mono">{record.error.code}</code>
              {" — "}
              {record.error.message}
            </div>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}

// ---------------------------------------------------------------- page

export function ObservabilityPage({ onNavigate, route }: PageProps) {
  const routeRun = route?.query.get("run") ?? "";
  const [runId, setRunId] = useState(routeRun);

  // 深链（anvil://observability?run=<id>）变化时跟随；页面内已经打开同一 run
  // 时不重置用户的输入。
  useEffect(() => {
    setRunId(routeRun);
  }, [routeRun]);

  const runIds = useMemo(() => (runId ? [runId] : []), [runId]);

  return (
    <div className="mx-auto max-w-3xl space-y-4 px-4 py-5">
      <header>
        <h1 className="text-sm font-medium text-[color:var(--anvil-foreground)]">
          {t("observability.title")}
        </h1>
        <p className="mt-0.5 text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
          {t("observability.subtitle")}
        </p>
      </header>

      <UsageSection runIds={runIds} />
      <RunSection runId={runId} onNavigate={onNavigate} />

      <section
        data-testid="observability-export"
        aria-labelledby="observability-export-title"
        className="rounded-xl border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] p-3.5"
      >
        <h2 id="observability-export-title" className="mb-2 text-sm font-medium text-[color:var(--anvil-foreground)]">
          {t("observability.export.title")}
        </h2>
        <DiagnosticsExportPanel runIds={runIds} />
      </section>
    </div>
  );
}
