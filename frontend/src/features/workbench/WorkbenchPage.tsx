// 工作台页（U01 / UI05）：六段真实数据 + 明确的不可用/空态。
//
// 一条纪律在渲染层同样成立：**缺来源显示 unavailable，不显示 0**。
// 卡片上出现的每个数字都必须能追溯到后端给的 count；后端没给 count 时
// 这一格就不出现数字。
//
// 骨架屏只在请求进行中出现（AsyncState 的 loading 态），加载完成后旧数据
// 不进 DOM——否则用户会对着已过期的工作台做决定。

import { useCallback, useEffect, useState, type ReactNode } from "react";
import { AsyncState } from "../../components/states/AsyncState";
import { StatusBadge } from "../../components/status/StatusBadge";
import { formatBytes, formatDate, t } from "../../i18n";
import type { CallError } from "../../types";
import type { RunState } from "../../types";
import { workbenchApi, type WorkbenchSection, type WorkbenchSummaryResult } from "./api";

export interface WorkbenchPageProps {
  onNavigate: (route: string) => void;
}

/** 一段的可用性渲染：不可用时给状态徽标与原因，绝不给数字。 */
function SectionHead({ section }: { section: WorkbenchSection }) {
  if (section.availability === "unavailable") {
    return (
      <div className="mt-1 flex flex-wrap items-center gap-1.5">
        <StatusBadge status="unavailable" />
        {section.reasonCode ? (
          <span className="font-mono text-[10px] text-[color:var(--anvil-muted)]">
            {section.reasonCode}
          </span>
        ) : null}
      </div>
    );
  }
  if (section.count === undefined) return null;
  return (
    <span className="text-[11px] text-[color:var(--anvil-muted)]" data-testid="workbench-count">
      {section.count}
    </span>
  );
}

function Card({
  id,
  title,
  section,
  children,
}: {
  id: string;
  title: string;
  section: WorkbenchSection;
  children?: ReactNode;
}) {
  return (
    <section
      data-workbench-card={id}
      className="rounded-lg border border-[color:var(--anvil-border)] p-3"
      aria-labelledby={`workbench-${id}`}
    >
      <header className="flex items-start justify-between gap-2">
        <h2 id={`workbench-${id}`} className="text-[12px] font-medium text-[color:var(--anvil-foreground)]">
          {title}
        </h2>
        <SectionHead section={section} />
      </header>
      {section.availability === "unavailable" ? (
        <p className="mt-2 text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
          {t("workbench.unavailable")}
        </p>
      ) : (
        <div className="mt-2 space-y-1">{children}</div>
      )}
    </section>
  );
}

function EmptyRow() {
  return <p className="text-[11px] text-[color:var(--anvil-muted)]">{t("workbench.empty")}</p>;
}

/** 状态徽标只接受类型化领域状态；未知状态按 unknown 显示，不猜。 */
function runStateBadge(state: string) {
  const known = [
    "idle",
    "queued",
    "starting",
    "running",
    "waiting_approval",
    "stopping",
    "completed",
    "failed",
    "cancelled",
    "interrupted",
  ];
  return known.includes(state) ? (state as RunState) : "unknown";
}

export function WorkbenchPage({ onNavigate }: WorkbenchPageProps) {
  const [result, setResult] = useState<WorkbenchSummaryResult | null>(null);
  const [error, setError] = useState<CallError | null>(null);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const next = await workbenchApi.summary();
      setResult(next);
      if (next.error) setError(next.error);
    } catch (e) {
      // 绑定层抛出的异常没有 code：收敛成 INTERNAL_ERROR 而不是伪造一个类型。
      setError({
        code: "INTERNAL_ERROR",
        message: e instanceof Error ? e.message : String(e),
        retryable: true,
      });
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  if (loading) {
    return (
      <div className="mx-auto max-w-4xl px-4 py-6">
        <AsyncState phase="loading" />
      </div>
    );
  }

  if (error && !result) {
    return (
      <div className="mx-auto max-w-4xl px-4 py-6">
        <AsyncState phase="error" error={error} onRetry={() => void load()} />
      </div>
    );
  }

  const sum = result!;

  return (
    <div className="mx-auto max-w-4xl space-y-3 px-4 py-5">
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h1 className="text-sm font-medium text-[color:var(--anvil-foreground)]">
            {t("workbench.title")}
          </h1>
          <p className="mt-0.5 text-[11px] text-[color:var(--anvil-muted)]">
            {t("workbench.subtitle")}
          </p>
        </div>
        <button
          type="button"
          onClick={() => onNavigate("/chat")}
          className="rounded-md border border-[color:var(--anvil-border)] px-2.5 py-1 text-xs text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)]"
        >
          {t("workbench.startChat")}
        </button>
      </header>

      {error ? (
        <AsyncState phase="error" error={error} onRetry={() => void load()} />
      ) : null}

      <div className="grid gap-3 sm:grid-cols-2">
        <Card id="current-runs" title={t("workbench.currentRuns")} section={sum.currentRuns}>
          {sum.runs.length === 0 ? (
            <EmptyRow />
          ) : (
            sum.runs.map((run) => (
              <button
                key={run.runId}
                type="button"
                onClick={() => onNavigate(`/chat/${run.sessionId}`)}
                className="flex w-full items-center justify-between gap-2 rounded border border-[color:var(--anvil-border)] px-2 py-1 text-left hover:bg-[color:var(--anvil-surface)]"
              >
                <span className="truncate text-[11px] text-[color:var(--anvil-foreground)]">
                  {run.sessionTitle || t("shell.untitledSession")}
                </span>
                <StatusBadge status={runStateBadge(run.state)} />
              </button>
            ))
          )}
        </Card>

        <Card id="pending-approvals" title={t("workbench.pendingApprovals")} section={sum.pendingApprovals}>
          {sum.approvals.length === 0 ? (
            <EmptyRow />
          ) : (
            sum.approvals.map((a) => (
              <div
                key={`${a.runId}-${a.approvalId}`}
                className="flex items-center justify-between gap-2 rounded border border-[color:var(--anvil-border)] px-2 py-1"
              >
                <span className="truncate text-[11px] text-[color:var(--anvil-foreground)]">
                  {a.toolName || a.approvalId}
                </span>
                <span className="shrink-0 text-[10px] text-[color:var(--anvil-muted)]">
                  {formatDate(a.requestedAt)}
                </span>
              </div>
            ))
          )}
        </Card>

        <Card id="recent-sessions" title={t("workbench.recentSessions")} section={sum.recentSessions}>
          {sum.sessions.length === 0 ? (
            <EmptyRow />
          ) : (
            sum.sessions.map((s) => (
              <button
                key={s.sessionId}
                type="button"
                onClick={() => onNavigate(`/chat/${s.sessionId}`)}
                className="flex w-full items-center justify-between gap-2 rounded border border-[color:var(--anvil-border)] px-2 py-1 text-left hover:bg-[color:var(--anvil-surface)]"
              >
                <span className="truncate text-[11px] text-[color:var(--anvil-foreground)]">
                  {s.title || t("shell.untitledSession")}
                </span>
                <span className="shrink-0 text-[10px] text-[color:var(--anvil-muted)]">
                  {formatDate(s.updatedAt)}
                </span>
              </button>
            ))
          )}
        </Card>

        <Card id="recent-artifacts" title={t("workbench.recentArtifacts")} section={sum.recentArtifacts}>
          {sum.artifacts.length === 0 ? (
            <EmptyRow />
          ) : (
            sum.artifacts.map((a) => (
              <button
                key={a.artifactId}
                type="button"
                onClick={() => onNavigate(`/library?artifact=${encodeURIComponent(a.artifactId)}`)}
                className="flex w-full items-center justify-between gap-2 rounded border border-[color:var(--anvil-border)] px-2 py-1 text-left hover:bg-[color:var(--anvil-surface)]"
              >
                <span className="truncate text-[11px] text-[color:var(--anvil-foreground)]">
                  {a.displayName}
                </span>
                <span className="shrink-0 text-[10px] text-[color:var(--anvil-muted)]">
                  {formatBytes(a.size)}
                </span>
              </button>
            ))
          )}
        </Card>

        <Card id="schedules" title={t("workbench.schedules")} section={sum.schedules}>
          {sum.schedule.length === 0 ? (
            <EmptyRow />
          ) : (
            sum.schedule.map((s) => (
              <div
                key={s.scheduleId}
                className="flex items-center justify-between gap-2 rounded border border-[color:var(--anvil-border)] px-2 py-1"
              >
                <span className="truncate text-[11px] text-[color:var(--anvil-foreground)]">{s.name}</span>
                <span className="shrink-0 text-[10px] text-[color:var(--anvil-muted)]">
                  {s.enabled
                    ? s.nextScheduledAt
                      ? t("workbench.nextRun", { time: formatDate(s.nextScheduledAt) })
                      : t("common.notAvailable")
                    : t("workbench.disabled")}
                </span>
              </div>
            ))
          )}
        </Card>

        <Card id="health" title={t("workbench.health")} section={sum.health}>
          <ul className="space-y-0.5 text-[11px] text-[color:var(--anvil-muted)]">
            <li>{t("workbench.activeRuns", { count: sum.healthInfo.activeRuns })}</li>
            <li>{t("workbench.completed24h", { count: sum.healthInfo.completed24h })}</li>
            <li>{t("workbench.failed24h", { count: sum.healthInfo.failed24h })}</li>
            <li>
              {sum.healthInfo.lastState ? (
                <span className="flex items-center gap-1.5">
                  {t("workbench.lastRun", { state: sum.healthInfo.lastState })}
                  <StatusBadge status={runStateBadge(sum.healthInfo.lastState)} />
                </span>
              ) : (
                t("workbench.lastRunNone")
              )}
            </li>
            {sum.healthInfo.lastErrorCode ? (
              <li className="font-mono text-[10px]">
                {t("workbench.lastError", { code: sum.healthInfo.lastErrorCode })}
              </li>
            ) : null}
          </ul>
        </Card>
      </div>
    </div>
  );
}
