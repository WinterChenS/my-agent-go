// 技能资源列表与编辑器（C03）。
//
// 这一块最容易被做成“看起来启用就算启用”。所以界面上每条技能都显式区分
// 三件事：打算启用吗（enabled）、现在能用吗（available + 原因）、
// 运行中的那一轮有没有把托管技能用 --skill 显式加载（pending/runtimeSkills）。
// 三者混成一句“已启用”，用户就再也分不清“没生效”是配置问题还是没重启。
//
// 注意 runtimeSkills 只是“托管且启用、被显式加载”的那几条，不是 Pi 实际可见的
// 全部技能：2026-11-04 起 Pi 会自己发现并加载本地技能，那份清单我们拿不到。

import { useState } from "react";
import { FolderInput, Puzzle } from "lucide-react";
import { api } from "../../api";
import type { CallError, SkillResource } from "../../types";
import { toCallError } from "../../app/useBackend";
import { EmptyState } from "../../components/StateViews";

export interface SkillsSectionProps {
  skills: SkillResource[];
  revision: number;
  /** 配置里的技能集合与运行中的不一致。 */
  pending: boolean;
  activeRun: boolean;
  runtimeSkills: string[];
  workspaceId: string;
  onChanged: () => void;
}

export function SkillsSection({
  skills,
  revision,
  pending,
  activeRun,
  runtimeSkills,
  workspaceId,
  onChanged,
}: SkillsSectionProps) {
  const [path, setPath] = useState("");
  const [scope, setScope] = useState<"user" | "workspace">("user");
  const [error, setError] = useState<CallError | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [confirming, setConfirming] = useState<string | null>(null);
  const [editing, setEditing] = useState<{ id: string; content: string } | null>(null);

  async function importSkill() {
    setError(null);
    setNotice(null);
    if (!path.trim()) {
      setNotice("请填写本地技能目录（或 SKILL.md 的路径）。导入会把内容复制进产品目录，不改源目录。");
      return;
    }
    setBusy("import");
    try {
      const res = await api.importSkill(path.trim(), scope, workspaceId);
      if (res.error) {
        setError(res.error);
        return;
      }
      setPath("");
      setNotice(`已导入 ${res.item.name}。${res.pending ? "下次运行生效。" : ""}`);
      onChanged();
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusy(null);
    }
  }

  async function toggle(s: SkillResource) {
    setError(null);
    setNotice(null);
    setBusy(s.id);
    try {
      const res = await api.setSkillEnabled(s.id, revision, !s.enabled);
      if (res.error) {
        setError(res.error);
        return;
      }
      onChanged();
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusy(null);
    }
  }

  async function remove(id: string) {
    setError(null);
    setBusy(id);
    try {
      const res = await api.deleteSkill(revision, id);
      if (res.error) {
        setError(res.error);
        return;
      }
      setConfirming(null);
      onChanged();
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusy(null);
    }
  }

  async function openEditor(id: string) {
    setError(null);
    setBusy(id);
    try {
      const res = await api.readSkill(id);
      if (res.error) {
        setError(res.error);
        return;
      }
      // 编辑的是磁盘上的最新内容，而不是列表里那份快照。
      setEditing({ id, content: res.content });
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusy(null);
    }
  }

  async function saveEditor() {
    if (!editing) return;
    setError(null);
    setBusy(editing.id);
    try {
      const res = await api.saveSkill(editing.id, revision, editing.content);
      if (res.error) {
        setError(res.error);
        return;
      }
      setEditing(null);
      setNotice("已保存。改动会在下一次运行时加载。");
      onChanged();
    } catch (e) {
      setError(toCallError(e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <section>
      <header className="mb-2">
        <h2 className="text-sm font-medium text-[color:var(--anvil-foreground)]">技能</h2>
        <p className="mt-0.5 text-[11px] text-[color:var(--anvil-muted)]">
          从本地导入，复制进产品管理目录（源目录不会被改动）。托管的技能只有「启用」的
          会通过 --skill 显式交给 Pi；Pi 自己的本地技能发现保持开启，它可能另外加载你
          机器上已有的技能，同名副本也不会被这里的开关挡住（运行记录里分开写这两件事）。
        </p>
      </header>

      {pending && (
        <p
          role="status"
          className="mb-2 rounded border border-[color:var(--anvil-warning-border)] bg-[color:var(--anvil-warning-surface)] px-2 py-1.5 text-[11px] text-[color:var(--anvil-warning)]"
        >
          {activeRun
            ? "技能改动已保存。当前还有 run 在跑，它仍使用旧技能集合；结束后的下一次运行生效。"
            : "技能改动已保存，下一次运行生效。"}
        </p>
      )}

      <div className="mb-2 flex flex-col gap-1.5 rounded-lg border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] p-2">
        <label className="text-[10px] text-[color:var(--anvil-muted)]" htmlFor="skill-import-path">
          本地路径（技能目录或 SKILL.md）
        </label>
        <div className="flex gap-1.5">
          <input
            id="skill-import-path"
            aria-label="技能导入路径"
            value={path}
            onChange={(e) => setPath(e.target.value)}
            placeholder="D:\\skills\\my-skill"
            className="min-w-0 flex-1 rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] placeholder:text-[color:var(--anvil-muted)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
          />
          <select
            aria-label="技能作用域"
            value={scope}
            onChange={(e) => setScope(e.target.value as "user" | "workspace")}
            className="rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-1.5 py-1 text-[11px] text-[color:var(--anvil-foreground)]"
          >
            <option value="user">用户级</option>
            <option value="workspace">仅当前工作区</option>
          </select>
          <button
            onClick={() => void importSkill()}
            disabled={busy === "import"}
            className="flex shrink-0 items-center gap-1 rounded border border-[color:var(--anvil-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
          >
            <FolderInput className="h-3 w-3" aria-hidden /> 导入
          </button>
        </div>
        <p className="text-[10px] text-[color:var(--anvil-muted)]">
          导入只复制文件，不执行技能里的任何脚本，也不会自动安装它声明的依赖命令。
        </p>
      </div>

      {error && (
        <p role="alert" className="mb-2 text-[11px] text-[color:var(--anvil-danger)]">
          <span className="font-mono text-[10px]">{error.code}</span> {error.message}
        </p>
      )}
      {notice && !error && (
        <p role="status" className="mb-2 text-[11px] text-[color:var(--anvil-muted)]">
          {notice}
        </p>
      )}

      {skills.length === 0 ? (
        <EmptyState
          title="还没有技能"
          hint="技能是一份 SKILL.md（frontmatter 里要有 name 与 description）。导入后 Pi 才可能在合适的任务里调用它。"
          icon={<Puzzle className="h-4 w-4" aria-hidden />}
        />
      ) : (
        <ul className="space-y-1">
          {skills.map((s) => (
            <li key={s.id} className="rounded-md border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-2.5 py-2">
              <div className="flex items-center gap-2">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <span className="truncate text-[12px] text-[color:var(--anvil-foreground)]">{s.name || s.id}</span>
                    <Badge tone={s.enabled ? "emerald" : "slate"}>
                      {s.enabled ? "已启用" : "已禁用"}
                    </Badge>
                    <Badge tone="slate">{s.scope === "user" ? "用户级" : "仅工作区"}</Badge>
                    {!s.available && <Badge tone="rose">不可用</Badge>}
                    {pending && runtimeSkills.includes(s.id) && (
                      <Badge tone="amber">运行中仍是旧集合</Badge>
                    )}
                  </div>
                  <div className="mt-0.5 line-clamp-2 text-[10px] text-[color:var(--anvil-muted)]">
                    {s.description || "（无描述）"}
                  </div>
                  <div className="truncate text-[10px] text-[color:var(--anvil-muted)]">
                    {s.id} · {s.origin} · {s.hash || "-"} · {formatBytes(s.bytes)}
                    {s.dependencies && s.dependencies.length > 0
                      ? ` · 依赖 ${s.dependencies.join(", ")}`
                      : ""}
                  </div>
                  {!s.available && s.unavailableReason && (
                    <div className="mt-0.5 text-[10px] text-[color:var(--anvil-danger)]">{s.unavailableReason}</div>
                  )}
                </div>
                <div className="flex shrink-0 items-center gap-1">
                  <button
                    onClick={() => void openEditor(s.id)}
                    disabled={busy === s.id}
                    className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-40 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
                  >
                    编辑
                  </button>
                  <button
                    onClick={() => void toggle(s)}
                    disabled={busy === s.id}
                    className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-40 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
                  >
                    {s.enabled ? "禁用" : "启用"}
                  </button>
                  {confirming === s.id ? (
                    <>
                      <button
                        onClick={() => void remove(s.id)}
                        disabled={busy === s.id}
                        className="rounded border border-[color:var(--anvil-danger-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-danger)] hover:bg-[color:var(--anvil-danger-surface)] disabled:opacity-40"
                      >
                        确认删除
                      </button>
                      <button
                        onClick={() => setConfirming(null)}
                        className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)]"
                      >
                        取消
                      </button>
                    </>
                  ) : (
                    <button
                      onClick={() => setConfirming(s.id)}
                      className="rounded border border-[color:var(--anvil-border)] px-2 py-0.5 text-[11px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
                    >
                      删除
                    </button>
                  )}
                </div>
              </div>

              {editing?.id === s.id && (
                <div className="mt-2 border-t border-[color:var(--anvil-border)] pt-2">
                  <label className="text-[10px] text-[color:var(--anvil-muted)]" htmlFor={"skill-body-" + s.id}>
                    SKILL.md 内容
                  </label>
                  <textarea
                    id={"skill-body-" + s.id}
                    aria-label="SKILL.md 内容"
                    value={editing.content}
                    onChange={(e) => setEditing({ id: s.id, content: e.target.value })}
                    rows={12}
                    className="mt-1 w-full rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] p-2 font-mono text-[11px] leading-relaxed text-[color:var(--anvil-foreground)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
                  />
                  <div className="mt-1 flex items-center gap-1.5">
                    <button
                      onClick={() => void saveEditor()}
                      disabled={busy === s.id}
                      className="rounded bg-[color:var(--anvil-accent)] px-2.5 py-1 text-[11px] font-medium text-[color:var(--anvil-background)] hover:bg-[color:var(--anvil-accent)] disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
                    >
                      保存
                    </button>
                    <button
                      onClick={() => setEditing(null)}
                      className="rounded border border-[color:var(--anvil-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-muted)] hover:bg-[color:var(--anvil-surface)]"
                    >
                      取消
                    </button>
                    <span className="text-[10px] text-[color:var(--anvil-muted)]">
                      name 不能改名（它是目录名与稳定引用）；要改名请重新导入。
                    </span>
                  </div>
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function Badge({ tone, children }: { tone: "emerald" | "slate" | "rose" | "amber"; children: string }) {
  const cls = {
    emerald: "bg-[color:var(--anvil-success-surface)] text-[color:var(--anvil-success)]",
    slate: "bg-[color:var(--anvil-surface)] text-[color:var(--anvil-muted)]",
    rose: "bg-[color:var(--anvil-danger-surface)] text-[color:var(--anvil-danger)]",
    amber: "bg-[color:var(--anvil-warning-surface)] text-[color:var(--anvil-warning)]",
  }[tone];
  return <span className={`shrink-0 rounded px-1 py-0.5 text-[9px] ${cls}`}>{children}</span>;
}

function formatBytes(n: number): string {
  if (!n) return "-";
  if (n >= 1024) return `${Math.round(n / 1024)} KiB`;
  return `${n} B`;
}
