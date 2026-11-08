// 运行记录。
//
// 现在只有"当前这次运行"是真的在内存里：事件的持久化、历史列表与产物登记
// 属于 C06/C07，还没实现。所以这一页既显示当前运行的真实数字，
// 也明确写出历史为什么是空的——而不是用一个永远空着的列表假装功能已就绪。

import { Activity, CheckCircle2, XCircle } from "lucide-react";
import { managedSkillsLabel, skillAutoDiscoveryLabel } from "../../runState";
import type { RunMeta, UiState } from "../../runState";
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
        <h2 className="text-sm font-medium text-[color:var(--anvil-foreground)]">当前运行</h2>
        <p className="mt-0.5 text-[11px] text-[color:var(--anvil-muted)]">
          数据直接来自事件流，不是从界面上反推的。
        </p>
      </header>

      {!ui.runId ? (
        <EmptyState
          title="还没有运行记录"
          hint="在工作台发起一次任务后，这里会显示它的状态与工具调用统计。"
        />
      ) : (
        <dl className="grid grid-cols-2 gap-x-4 gap-y-2 rounded-lg border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-3 py-2.5 text-[11px]">
          <Row label="run ID" value={ui.runId} mono />
          <Row label="状态" value={PHASE_LABEL[ui.phase] ?? ui.phase} />
          <Row label="工作区" value={workspace?.canonicalPath ?? "未选择"} mono />
          <Row label="事件序号" value={String(seq)} />
          <SkillsRow meta={ui.runMeta} />
          {/* MOD04：这一轮真正用的是什么模型、哪个配置版本，取自启动时的快照。
              换成"当前设置里的模型"就会在用户中途换模型后撒谎。 */}
          <Row
            label="模型"
            value={
              ui.runMeta?.provider || ui.runMeta?.model
                ? `${ui.runMeta.provider}/${ui.runMeta.model}`
                : "未记录（旧版本启动的 run）"
            }
            mono
          />
          <Row
            label="配置 revision"
            value={ui.runMeta ? String(ui.runMeta.configRevision) : "未记录"}
          />
          <div className="col-span-2 mt-1 flex items-center gap-4 border-t border-[color:var(--anvil-border)] pt-2 text-[11px]">
            <span className="flex items-center gap-1 text-[color:var(--anvil-success)]">
              <CheckCircle2 className="h-3.5 w-3.5" aria-hidden /> 工具完成 {ok}
            </span>
            <span className="flex items-center gap-1 text-[color:var(--anvil-danger)]">
              <XCircle className="h-3.5 w-3.5" aria-hidden /> 工具失败 {failed}
            </span>
            <span className="flex items-center gap-1 text-[color:var(--anvil-accent)]">
              <Activity className="h-3.5 w-3.5" aria-hidden /> 执行中 {running}
            </span>
          </div>
        </dl>
      )}

      <section className="rounded-lg border border-dashed border-[color:var(--anvil-border)] px-3 py-2.5">
        <div className="flex items-center gap-2">
          <h2 className="text-[12px] text-[color:var(--anvil-muted)]">历史运行</h2>
          <span className="rounded bg-[color:var(--anvil-surface)] px-1.5 py-0.5 text-[9px] text-[color:var(--anvil-muted)]">
            未实现 · C06
          </span>
        </div>
        <p className="mt-1 text-[11px] leading-relaxed text-[color:var(--anvil-muted)]">
          事件目前只保留在内存里（最多 20000 条，用于订阅重连的 Replay），
          重启后不可查。把运行记录落到数据库、并支持按会话浏览属于 C06。
        </p>
      </section>
    </div>
  );
}

function Row({
  label,
  value,
  mono,
  testId,
}: {
  label: string;
  value: string;
  mono?: boolean;
  testId?: string;
}) {
  return (
    <div className="min-w-0" data-testid={testId}>
      <dt className="text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">{label}</dt>
      <dd className={"truncate text-[color:var(--anvil-foreground)] " + (mono ? "font-mono text-[10px]" : "")} title={value}>
        {value}
      </dd>
    </div>
  );
}

/**
 * 技能行：把两段事实分开写，不能合成一句。
 *
 *   - 托管：这一轮通过 --skill 显式加载的 ID。空 = 确实没显式加载（不是“没有技能”）。
 *   - Pi 本地自动发现：开关状态。发现到的清单产品拿不到，所以只能写“清单未知”，
 *     状态缺失时写“未知”，不能默认成“已关闭”或“已开启”。
 *
 * 旧实现只有一个托管列表，空数组直接渲染成“未加载任何技能”。2026-11-04 起
 * Pi 会自己加载用户级技能，那句话在自动发现开启时是假的，已删。
 */
function SkillsRow({ meta }: { meta: RunMeta | null }) {
  if (!meta) {
    return <Row label="技能" value="尚未启动" testId="run-skills" />;
  }
  const managed = managedSkillsLabel(meta.managedSkills);
  return (
    <div className="min-w-0" data-testid="run-skills">
      <dt className="text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">技能</dt>
      <dd className="min-w-0 text-[color:var(--anvil-foreground)]">
        <div
          className="truncate font-mono text-[10px]"
          title={managed}
          data-testid="run-managed-skills"
        >
          托管：{managed}
        </div>
        <div className="truncate text-[10px]" data-testid="run-skill-autodiscovery">
          Pi 本地自动发现：{skillAutoDiscoveryLabel(meta.skillAutoDiscovery)}
        </div>
      </dd>
    </div>
  );
}
