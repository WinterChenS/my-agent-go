// 设置。
//
// UI01 要求"缺 Pi 或凭证时仍能打开设置和历史，发起任务时给出修复路径"。
// 所以这一页从不假设运行时就绪：所有字段都能编辑和保存，
// 与 Pi 相关的只在底部作为只读诊断信息展示。

import { useState } from "react";
import { Loader2, Save } from "lucide-react";
import type { CallError, Settings, Status, Workspace } from "../../types";
import { ErrorState } from "../../components/StateViews";
import { managedSkillsLabel, skillAutoDiscoveryLabel } from "../../runState";

export interface SettingsViewProps {
  /** 由外层保证非空：设置读不到时根本不会渲染这一页。 */
  settings: Settings;
  status: Status | null;
  workspaces: Workspace[];
  saving: boolean;
  error: CallError | null;
  onSave: (s: Settings) => Promise<void>;
  onTrust: (id: string, trusted: boolean) => Promise<void>;
}

export function SettingsView({
  settings,
  status,
  workspaces,
  saving,
  error,
  onSave,
  onTrust,
}: SettingsViewProps) {
  const [draft, setDraft] = useState<Settings>(settings);
  const [saved, setSaved] = useState(false);

  const patch = (p: Partial<Settings>) => {
    setSaved(false);
    setDraft((d) => ({ ...d, ...p }));
  };

  const isolation = (status?.isolation ?? "").split(" ").filter(Boolean);

  return (
    <div className="mx-auto max-w-3xl space-y-5 px-4 py-5">
      <section>
        <header className="mb-2">
          <h2 className="text-sm font-medium text-[color:var(--anvil-foreground)]">运行环境</h2>
          <p className="mt-0.5 text-[11px] text-[color:var(--anvil-muted)]">
            Pi 与本机 Node 的位置。留空时使用内置默认值。
          </p>
        </header>
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <Field label="Pi 路径" value={draft.piPath} onChange={(v) => patch({ piPath: v })} mono />
          <Field label="Node 路径" value={draft.nodePath} onChange={(v) => patch({ nodePath: v })} mono />
          <Field label="Provider" value={draft.provider} onChange={(v) => patch({ provider: v })} />
          <Field label="Base URL" value={draft.baseUrl} onChange={(v) => patch({ baseUrl: v })} mono />
          <Field label="Model" value={draft.model} onChange={(v) => patch({ model: v })} />
          <Field
            label="API Key"
            type="password"
            value={draft.apiKey}
            onChange={(v) => patch({ apiKey: v })}
            hint="仅旧路径生效；从资源库选模型后改由系统凭证库保存"
          />
        </div>
        {status?.modelRef ? (
          <p className="mt-2 rounded border border-[color:var(--anvil-accent-border)] bg-[color:var(--anvil-accent-surface)] px-2 py-1 text-[10px] text-[color:var(--anvil-accent)]">
            当前模型来自资源库（{status.modelRef}）：上面的 Provider / Base URL / Model / API Key
            不再生效，密钥由系统凭证库提供。
          </p>
        ) : (
          <p className="mt-2 text-[10px] text-[color:var(--anvil-muted)]">
            这里配置的是"还没有从资源库选模型"时的兜底端点，API Key 会以明文写进 settings.json。
            正式使用请在资源库里定义模型并设为当前。
          </p>
        )}
        <label className="mt-2 flex items-center gap-1.5 text-[11px] text-[color:var(--anvil-muted)]">
          <input
            type="checkbox"
            checked={draft.shellAllowed}
            onChange={(e) => patch({ shellAllowed: e.target.checked })}
            className="h-3.5 w-3.5 accent-sky-500"
          />
          允许 shell 工具（每次调用仍需审批）
        </label>
      </section>

      <section>
        <header className="mb-2">
          <h2 className="text-sm font-medium text-[color:var(--anvil-foreground)]">工作区</h2>
          <p className="mt-0.5 text-[11px] text-[color:var(--anvil-muted)]">
            agent 只在这个目录里读写。未信任的工作区不能执行任何命令。
          </p>
        </header>

        <Field
          label="当前工作目录"
          value={draft.workspace}
          onChange={(v) => patch({ workspace: v })}
          mono
          hint="修改后保存即可；目录会被自动登记到下面的列表里"
        />

        <ul className="mt-2 space-y-1">
          {workspaces.map((ws) => (
            <li
              key={ws.id}
              className="flex items-center gap-2 rounded-md border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-2.5 py-1.5"
            >
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-1.5">
                  <span className="truncate text-[12px] text-[color:var(--anvil-foreground)]">{ws.name}</span>
                  <span
                    className={
                      "shrink-0 rounded px-1 py-0.5 text-[9px] " +
                      (ws.trusted
                        ? "bg-[color:var(--anvil-success-surface)] text-[color:var(--anvil-success)]"
                        : "bg-[color:var(--anvil-warning-surface)] text-[color:var(--anvil-warning)]")
                    }
                  >
                    {ws.trusted ? "已信任" : "未信任"}
                  </span>
                </div>
                <div className="truncate font-mono text-[10px] text-[color:var(--anvil-muted)]" title={ws.canonicalPath}>
                  {ws.canonicalPath}
                </div>
              </div>
              <button
                onClick={() => void onTrust(ws.id, !ws.trusted)}
                className="shrink-0 rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
              >
                {ws.trusted ? "取消信任" : "信任"}
              </button>
            </li>
          ))}
          {workspaces.length === 0 && (
            <li className="rounded-md border border-dashed border-[color:var(--anvil-border)] px-2.5 py-2 text-[11px] text-[color:var(--anvil-muted)]">
              还没有登记任何工作区。在上面的输入框填写路径并保存即可。
            </li>
          )}
        </ul>
      </section>

      {error && <ErrorState error={error} title="设置" />}

      <div className="flex items-center gap-2">
        <button
          onClick={async () => {
            await onSave(draft);
            setSaved(true);
          }}
          disabled={saving}
          className="flex items-center gap-1.5 rounded-md bg-[color:var(--anvil-accent)] px-3 py-1.5 text-xs font-medium text-[color:var(--anvil-background)] hover:bg-[color:var(--anvil-accent)] disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
        >
          {saving ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden />
          ) : (
            <Save className="h-3.5 w-3.5" aria-hidden />
          )}
          {saving ? "保存中…" : "保存设置"}
        </button>
        {saved && !error && (
          <span className="text-[11px] text-[color:var(--anvil-success)]" role="status">
            已保存
          </span>
        )}
      </div>

      <section className="rounded-lg border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-3 py-2.5">
        <h2 className="text-[12px] text-[color:var(--anvil-muted)]">诊断（只读）</h2>
        <dl className="mt-1.5 space-y-1 text-[11px]">
          <DiagRow label="平台" value={status?.platform ?? "—"} />
          <DiagRow label="agentDir" value={status?.agentDir ?? "—"} mono />
          <DiagRow label="应用数据" value={status?.appRoot ?? "—"} mono />
          <DiagRow label="当前模型来源" value={status?.modelRef ? `资源库 ${status.modelRef}` : "设置字段（明文路径）"} />
          <DiagRow
            label="实际运行模型"
            value={
              status?.runtimeProvider
                ? `${status.runtimeProvider}/${status.runtimeModel}`
                : "尚未启动"
            }
            mono
          />
          {status?.runtimePending && (
            <DiagRow label="模型变更" value="已保存，等当前 run 结束后生效" />
          )}
          {/* 技能同样是两段事实：托管显式加载的清单，与 Pi 自动发现的开关。
              自动发现到的清单产品拿不到，只报开关；缺数据时写“未知”。 */}
          <DiagRow
            label="托管技能"
            value={managedSkillsLabel(status?.runtimeSkills ?? [])}
            mono
            testId="settings-managed-skills"
          />
          <DiagRow
            label="自动发现"
            value={skillAutoDiscoveryLabel(status?.skillAutoDiscovery)}
            testId="settings-skill-autodiscovery"
          />
          {status?.skillsPending && (
            <DiagRow label="技能变更" value="已保存，等当前 run 结束后生效" />
          )}
          <DiagRow label="隔离参数" value={isolation.join(" ") || "—"} mono />
        </dl>
        <p className="mt-1.5 text-[10px] leading-relaxed text-[color:var(--anvil-muted)]">
          隔离参数是实际传给 Pi 的那一份，包括显式加载的策略扩展。
          与会话目录相关的 flag 不在其中，因为它每次运行不同。
          "托管技能"只列我们用 --skill 显式加载的条目；Pi 自己发现到的技能清单拿不到，
          所以只能报自动发现的开关状态。
        </p>
      </section>
    </div>
  );
}

function Field({
  label,
  value,
  onChange,
  mono,
  hint,
  type = "text",
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  mono?: boolean;
  hint?: string;
  type?: string;
}) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-[10px] uppercase tracking-wide text-[color:var(--anvil-muted)]">{label}</span>
      <input
        type={type}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className={
          "rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1.5 text-[12px] text-[color:var(--anvil-foreground)] outline-none focus:border-[color:var(--anvil-accent-border)] " +
          (mono ? "font-mono text-[11px]" : "")
        }
      />
      {hint && <span className="text-[10px] text-[color:var(--anvil-muted)]">{hint}</span>}
    </label>
  );
}

function DiagRow({
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
    <div className="flex gap-2" data-testid={testId}>
      <dt className="w-20 shrink-0 text-[color:var(--anvil-muted)]">{label}</dt>
      <dd className={"min-w-0 flex-1 break-all text-[color:var(--anvil-muted)] " + (mono ? "font-mono text-[10px]" : "")}>
        {value}
      </dd>
    </div>
  );
}
