// 统一工具目录（C04）。
//
// 每一行必须同时回答：来自哪条通道（builtin/extension/mcp/harness）、
// 当前是否可达（enabled + capabilityState）、调用要不要审批（policy）。
//
// MCP 的工具名与状态来自 Pi 的诊断输出（`pi mcp list --json`）。诊断失败或
// 服务未连接时，目录只显示"清单未知"的占位行并标 unknown，**不**伪造工具名，
// 也不把未知显示成可用。

import { useCallback, useEffect, useMemo, useState } from "react";
import { RefreshCw, Wrench } from "lucide-react";
import { api } from "../../api";
import { toCallError } from "../../app/useBackend";
import type { CallError, ToolDescriptor, ToolListResult } from "../../types";
import { EmptyState, ErrorState, LoadingState } from "../../components/StateViews";

const SOURCE_LABEL: Record<string, string> = {
  builtin: "内置（Pi）",
  extension: "扩展",
  mcp: "外部 MCP",
  harness: "自有 MCP",
};

const SOURCE_ORDER = ["builtin", "extension", "mcp", "harness"];

const CAPABILITY_LABEL: Record<string, string> = {
  available: "可用",
  deferred: "需 tool_search",
  hidden: "已隐藏",
  unknown: "未知",
  unavailable: "不可用",
  disabled: "已禁用",
};

const POLICY_LABEL: Record<string, string> = {
  approval: "逐次审批",
  auto: "自动放行",
  denied: "拒绝",
};

function toneFor(tool: ToolDescriptor): "emerald" | "slate" | "rose" | "amber" {
  if (!tool.enabled) {
    return tool.capabilityState === "disabled" || tool.capabilityState === "hidden"
      ? "slate"
      : "rose";
  }
  return tool.capabilityState === "deferred" ? "amber" : "emerald";
}

export function ToolsSection() {
  const [data, setData] = useState<ToolListResult | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<CallError | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await api.tools();
      if (res.error) {
        setError(res.error);
        setData(null);
      } else {
        setData(res);
      }
    } catch (e) {
      setError(toCallError(e));
      setData(null);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const items = data?.items ?? [];
  const groups = useMemo(() => {
    const bySource = new Map<string, ToolDescriptor[]>();
    for (const item of items) {
      const key = item.source || "unknown";
      const list = bySource.get(key) ?? [];
      list.push(item);
      bySource.set(key, list);
    }
    return Array.from(bySource.entries()).sort((a, b) => {
      const ai = SOURCE_ORDER.indexOf(a[0]);
      const bi = SOURCE_ORDER.indexOf(b[0]);
      return (ai < 0 ? 99 : ai) - (bi < 0 ? 99 : bi);
    });
  }, [items]);

  return (
    <section>
      <header className="mb-2 flex items-start justify-between gap-2">
        <div>
          <h2 className="text-sm font-medium text-[color:var(--anvil-foreground)]">工具目录</h2>
          <p className="mt-0.5 text-[11px] text-[color:var(--anvil-muted)]">
            工具的有效状态来自配置与 Pi 的诊断：hidden / 已禁用 / 诊断失败都显示为不可用。
            未知 MCP 工具默认逐次审批，不信任 readOnlyHint。
          </p>
        </div>
        <button
          onClick={() => void load()}
          disabled={loading}
          className="flex shrink-0 items-center gap-1 rounded border border-[color:var(--anvil-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface)] disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
        >
          <RefreshCw className="h-3 w-3" aria-hidden /> 刷新
        </button>
      </header>

      {error && (
        <div className="mb-2">
          <ErrorState error={error} onRetry={() => void load()} title="工具目录读取失败" />
        </div>
      )}

      {loading && items.length === 0 ? (
        <LoadingState label="读取工具目录…" />
      ) : items.length === 0 ? (
        !error && (
          <EmptyState
            title="工具目录为空"
            hint="当前没有任何可列出的工具。MCP 服务的工具需要先诊断（Pi 连接成功后才会出现）；扩展工具清单由 Pi 运行时注册，产品无法静态枚举。"
            icon={<Wrench className="h-4 w-4" aria-hidden />}
          />
        )
      ) : (
        <div className="space-y-2">
          {groups.map(([source, tools]) => (
            <div key={source}>
              <div className="mb-1 flex items-center gap-1.5 text-[10px] text-[color:var(--anvil-muted)]">
                <span className="font-medium text-[color:var(--anvil-muted)]">
                  {SOURCE_LABEL[source] ?? source}
                </span>
                <span>· {tools.length}</span>
                {source === "extension" && (
                  <span className="text-[color:var(--anvil-muted)]">（工具清单未知，需在运行记录中查看实际调用）</span>
                )}
              </div>
              <ul className="space-y-1">
                {tools.map((tool) => (
                  <li
                    key={tool.source + "/" + tool.name}
                    className="rounded-md border border-[color:var(--anvil-border)] bg-[color:var(--anvil-surface)] px-2.5 py-1.5"
                  >
                    <div className="flex items-center gap-2">
                      <code className="min-w-0 flex-1 truncate font-mono text-[11px] text-[color:var(--anvil-foreground)]">
                        {tool.name}
                      </code>
                      <Badge tone={toneFor(tool)}>
                        {CAPABILITY_LABEL[tool.capabilityState] ?? tool.capabilityState}
                      </Badge>
                      <Badge tone="slate">{POLICY_LABEL[tool.policy as string] ?? tool.policy}</Badge>
                      {tool.server && <Badge tone="slate">{tool.server}</Badge>}
                    </div>
                    {tool.description && (
                      <div className="mt-0.5 line-clamp-2 text-[10px] text-[color:var(--anvil-muted)]">
                        {tool.description}
                      </div>
                    )}
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

function Badge({
  tone,
  children,
}: {
  tone: "emerald" | "slate" | "rose" | "amber";
  children: string;
}) {
  const cls = {
    emerald: "bg-[color:var(--anvil-success-surface)] text-[color:var(--anvil-success)]",
    slate: "bg-[color:var(--anvil-surface)] text-[color:var(--anvil-muted)]",
    rose: "bg-[color:var(--anvil-danger-surface)] text-[color:var(--anvil-danger)]",
    amber: "bg-[color:var(--anvil-warning-surface)] text-[color:var(--anvil-warning)]",
  }[tone];
  return <span className={`shrink-0 rounded px-1 py-0.5 text-[9px] ${cls}`}>{children}</span>;
}
