// 右栏详情。
//
// 只放"能核对的硬事实"：路径、信任状态、run id、事件序号、隔离目录。
// 这些是排查时真正会被追问的东西；把统计数字的推算口径也写出来，
// 免得看到"工具完成 3"却不知道是本次 run 还是全部消息累加。

import { Lock, LockOpen } from "lucide-react";
import type { UiState } from "../runState";
import type { Status, Workspace } from "../types";

export interface DetailPanelProps {
  ui: UiState;
  status: Status | null;
  workspace: Workspace | null;
  phaseMeta: { label: string; tone: string };
  onGoRuns: () => void;
}

export function DetailPanel({ ui, status, workspace, phaseMeta, onGoRuns }: DetailPanelProps) {
  const tools = ui.messages.flatMap((m) => m.tools);
  const failed = tools.filter((t) => !t.running && t.isError).length;
  const ok = tools.filter((t) => !t.running && !t.isError).length;
  const running = tools.filter((t) => t.running).length;
  const seq = ui.runId ? ui.lastSeqByRun[ui.runId] ?? 0 : 0;

  return (
    <div className="space-y-4 px-3 py-3 text-[11px]">
      <section>
        <h3 className="mb-1.5 text-[10px] font-medium uppercase tracking-wide text-[color:var(--anvil-muted)]">
          当前工作区
        </h3>
        {workspace ? (
          <>
            <div className="flex items-center gap-1.5">
              <span className="truncate text-[12px] text-[color:var(--anvil-foreground)]">{workspace.name}</span>
              {workspace.trusted ? (
                <LockOpen className="h-3 w-3 shrink-0 text-[color:var(--anvil-success)]" aria-label="已信任" />
              ) : (
                <Lock className="h-3 w-3 shrink-0 text-[color:var(--anvil-warning)]" aria-label="未信任" />
              )}
            </div>
            <p className="mt-0.5 break-all font-mono text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
              {workspace.canonicalPath}
            </p>
            {!workspace.trusted && (
              <p className="mt-1 rounded bg-[color:var(--anvil-warning-surface)] px-1.5 py-1 text-[10px] leading-relaxed text-[color:var(--anvil-warning)]">
                未信任：不能在此执行命令。左栏的锁形按钮可以切换。
              </p>
            )}
          </>
        ) : (
          <p className="text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">还没有选择工作区。</p>
        )}
      </section>

      <section>
        <h3 className="mb-1.5 text-[10px] font-medium uppercase tracking-wide text-[color:var(--anvil-muted)]">
          本次运行
        </h3>
        {ui.runId ? (
          <>
            <div className="mb-1 flex items-center gap-1.5">
              <span className={`h-1.5 w-1.5 rounded-full ${phaseMeta.tone}`} aria-hidden />
              <span className="text-[color:var(--anvil-foreground)]">{phaseMeta.label}</span>
            </div>
            <p className="break-all font-mono text-[10px] text-[color:var(--anvil-muted)]">{ui.runId}</p>
            <dl className="mt-1.5 space-y-0.5 text-[10px] text-[color:var(--anvil-muted)]">
              <div className="flex justify-between">
                <dt>事件序号</dt>
                <dd className="font-mono text-[color:var(--anvil-muted)]">{seq}</dd>
              </div>
              <div className="flex justify-between">
                <dt>工具完成 / 失败</dt>
                <dd className="font-mono text-[color:var(--anvil-muted)]">
                  {ok} / {failed}
                </dd>
              </div>
              <div className="flex justify-between">
                <dt>工具执行中</dt>
                <dd className="font-mono text-[color:var(--anvil-muted)]">{running}</dd>
              </div>
              <div className="flex justify-between">
                <dt>待授权</dt>
                <dd className="font-mono text-[color:var(--anvil-muted)]">{ui.approvals.length}</dd>
              </div>
            </dl>
            <button
              onClick={onGoRuns}
              className="mt-1.5 text-[10px] text-[color:var(--anvil-accent)] hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
            >
              查看运行记录 →
            </button>
          </>
        ) : (
          <p className="text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">还没有发起任务。</p>
        )}
      </section>

      <section>
        <h3 className="mb-1.5 text-[10px] font-medium uppercase tracking-wide text-[color:var(--anvil-muted)]">
          运行环境
        </h3>
        <dl className="space-y-0.5 text-[10px]">
          <div>
            <dt className="text-[color:var(--anvil-muted)]">provider / model</dt>
            <dd className="break-all text-[color:var(--anvil-muted)]">
              {status?.provider ?? "—"} / {status?.model ?? "—"}
            </dd>
          </div>
          <div>
            <dt className="text-[color:var(--anvil-muted)]">agentDir</dt>
            <dd className="break-all font-mono text-[color:var(--anvil-muted)]">{status?.agentDir ?? "—"}</dd>
          </div>
        </dl>
      </section>
    </div>
  );
}
