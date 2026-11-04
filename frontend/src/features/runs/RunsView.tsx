// 运行记录。
//
// 现在只有"当前这次运行"是真的在内存里：事件的持久化、历史列表与产物登记
// 属于 C06/C07，还没实现。所以这一页既显示当前运行的真实数字，
// 也明确写出历史为什么是空的——而不是用一个永远空着的列表假装功能已就绪。

import { Activity, CheckCircle2, XCircle } from "lucide-react";
import type { UiState } from "../../runState";
import type { Workspace } from "../../types";
import { EmptyState } from "../../components/StateViews";

const PHASE_LABEL: Record<string, string> = {
  idle: "空闲",
  queued: "排队中",
  starting: "启动中",
  running: "运行中",
  waiting_approval: "等待授权",
  stopping: "停止中",
  completed: "已完成",
  failed: "失败",
  cancelled: "已取消",
  interrupted: "已中断",
};

export function RunsView({ ui, workspace }: { ui: UiState; workspace: Workspace | null }) {
  const tools = ui.messages.flatMap((m) => m.tools);
  const failed = tools.filter((t) => t.isError).length;
  const ok = tools.length - failed;
  const running = tools.filter((t) => t.running).length;
  const seq = ui.runId ? ui.lastSeqByRun[ui.runId] ?? 0 : 0;

  return (
    <div className="mx-auto max-w-3xl space-y-4 px-4 py-5">
      <header>
        <h2 className="text-sm font-medium text-slate-200">当前运行</h2>
        <p className="mt-0.5 text-[11px] text-slate-500">
          数据直接来自事件流，不是从界面上反推的。
        </p>
      </header>

      {!ui.runId ? (
        <EmptyState
          title="还没有运行记录"
          hint="在工作台发起一次任务后，这里会显示它的状态与工具调用统计。"
        />
      ) : (
        <dl className="grid grid-cols-2 gap-x-4 gap-y-2 rounded-lg border border-white/10 bg-[#141922] px-3 py-2.5 text-[11px]">
          <Row label="run ID" value={ui.runId} mono />
          <Row label="状态" value={PHASE_LABEL[ui.phase] ?? ui.phase} />
          <Row label="工作区" value={workspace?.canonicalPath ?? "未选择"} mono />
          <Row label="事件序号" value={String(seq)} />
          <div className="col-span-2 mt-1 flex items-center gap-4 border-t border-white/5 pt-2 text-[11px]">
            <span className="flex items-center gap-1 text-emerald-400">
              <CheckCircle2 className="h-3.5 w-3.5" aria-hidden /> 工具完成 {ok}
            </span>
            <span className="flex items-center gap-1 text-rose-400">
              <XCircle className="h-3.5 w-3.5" aria-hidden /> 工具失败 {failed}
            </span>
            <span className="flex items-center gap-1 text-sky-400">
              <Activity className="h-3.5 w-3.5" aria-hidden /> 执行中 {running}
            </span>
          </div>
        </dl>
      )}

      <section className="rounded-lg border border-dashed border-white/10 px-3 py-2.5">
        <div className="flex items-center gap-2">
          <h2 className="text-[12px] text-slate-400">历史运行</h2>
          <span className="rounded bg-white/5 px-1.5 py-0.5 text-[9px] text-slate-500">
            未实现 · C06
          </span>
        </div>
        <p className="mt-1 text-[11px] leading-relaxed text-slate-600">
          事件目前只保留在内存里（最多 20000 条，用于订阅重连的 Replay），
          重启后不可查。把运行记录落到数据库、并支持按会话浏览属于 C06。
        </p>
      </section>
    </div>
  );
}

function Row({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="min-w-0">
      <dt className="text-[10px] uppercase tracking-wide text-slate-600">{label}</dt>
      <dd className={"truncate text-slate-300 " + (mono ? "font-mono text-[10px]" : "")} title={value}>
        {value}
      </dd>
    </div>
  );
}
