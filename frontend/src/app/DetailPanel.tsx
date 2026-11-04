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
        <h3 className="mb-1.5 text-[10px] font-medium uppercase tracking-wide text-slate-500">
          当前工作区
        </h3>
        {workspace ? (
          <>
            <div className="flex items-center gap-1.5">
              <span className="truncate text-[12px] text-slate-200">{workspace.name}</span>
              {workspace.trusted ? (
                <LockOpen className="h-3 w-3 shrink-0 text-emerald-400" aria-label="已信任" />
              ) : (
                <Lock className="h-3 w-3 shrink-0 text-amber-400" aria-label="未信任" />
              )}
            </div>
            <p className="mt-0.5 break-all font-mono text-[10px] leading-relaxed text-slate-500">
              {workspace.canonicalPath}
            </p>
            {!workspace.trusted && (
              <p className="mt-1 rounded bg-amber-400/10 px-1.5 py-1 text-[10px] leading-relaxed text-amber-200/80">
                未信任：不能在此执行命令。左栏的锁形按钮可以切换。
              </p>
            )}
          </>
        ) : (
          <p className="text-[10px] leading-relaxed text-slate-600">还没有选择工作区。</p>
        )}
      </section>

      <section>
        <h3 className="mb-1.5 text-[10px] font-medium uppercase tracking-wide text-slate-500">
          本次运行
        </h3>
        {ui.runId ? (
          <>
            <div className="mb-1 flex items-center gap-1.5">
              <span className={`h-1.5 w-1.5 rounded-full ${phaseMeta.tone}`} aria-hidden />
              <span className="text-slate-300">{phaseMeta.label}</span>
            </div>
            <p className="break-all font-mono text-[10px] text-slate-500">{ui.runId}</p>
            <dl className="mt-1.5 space-y-0.5 text-[10px] text-slate-500">
              <div className="flex justify-between">
                <dt>事件序号</dt>
                <dd className="font-mono text-slate-400">{seq}</dd>
              </div>
              <div className="flex justify-between">
                <dt>工具完成 / 失败</dt>
                <dd className="font-mono text-slate-400">
                  {ok} / {failed}
                </dd>
              </div>
              <div className="flex justify-between">
                <dt>工具执行中</dt>
                <dd className="font-mono text-slate-400">{running}</dd>
              </div>
              <div className="flex justify-between">
                <dt>待授权</dt>
                <dd className="font-mono text-slate-400">{ui.approvals.length}</dd>
              </div>
            </dl>
            <button
              onClick={onGoRuns}
              className="mt-1.5 text-[10px] text-sky-400 hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-400"
            >
              查看运行记录 →
            </button>
          </>
        ) : (
          <p className="text-[10px] leading-relaxed text-slate-600">还没有发起任务。</p>
        )}
      </section>

      <section>
        <h3 className="mb-1.5 text-[10px] font-medium uppercase tracking-wide text-slate-500">
          运行环境
        </h3>
        <dl className="space-y-0.5 text-[10px]">
          <div>
            <dt className="text-slate-600">provider / model</dt>
            <dd className="break-all text-slate-400">
              {status?.provider ?? "—"} / {status?.model ?? "—"}
            </dd>
          </div>
          <div>
            <dt className="text-slate-600">agentDir</dt>
            <dd className="break-all font-mono text-slate-500">{status?.agentDir ?? "—"}</dd>
          </div>
        </dl>
      </section>
    </div>
  );
}
