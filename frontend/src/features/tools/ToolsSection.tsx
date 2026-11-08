// 统一工具目录（C04）。
//
// 每一行必须同时回答：来自哪条通道（builtin/extension/mcp/harness）、
// 当前是否可达（enabled + capabilityState）、调用要不要审批（policy）。
//
// MCP 的工具名与状态来自 Pi 的诊断输出（`pi mcp list --json`）。诊断失败或
// 服务未连接时，目录只显示"清单未知"的占位行并标 unknown，**不**伪造工具名，
// 也不把未知显示成可用。
//
// 文案全部来自 catalog（`tools.*`），颜色全部来自语义 token。

import { useCallback, useEffect, useMemo, useState } from "react";
import { RefreshCw, Wrench } from "lucide-react";
import { api } from "../../api";
import { toCallError } from "../../app/useBackend";
import type { CallError, ToolDescriptor, ToolListResult } from "../../types";
import { EmptyState, ErrorState, LoadingState } from "../../components/StateViews";
import { hasKey, t } from "../../i18n";

/** 通道 → 文案 key；未知通道原样显示（那是后端新增的通道，不该被猜成别的）。 */
const SOURCE_KEY: Record<string, string> = {
  builtin: "tools.source.builtin",
  extension: "tools.source.extension",
  mcp: "tools.source.mcp",
  harness: "tools.source.harness",
};

const SOURCE_ORDER = ["builtin", "extension", "mcp", "harness"];

const CAPABILITY_KEY: Record<string, string> = {
  available: "tools.capability.available",
  deferred: "tools.capability.deferred",
  hidden: "tools.capability.hidden",
  unknown: "tools.capability.unknown",
  unavailable: "tools.capability.unavailable",
  disabled: "tools.capability.disabled",
};

const POLICY_KEY: Record<string, string> = {
  approval: "tools.policy.approval",
  auto: "tools.policy.auto",
  denied: "tools.policy.denied",
};

/**
 * 工具描述。
 *
 * 内置工具（read/write/edit/bash/tool_search）是**产品自己拥有**的工具，
 * 它们的描述属于我们的文案，因此在前端按 catalog 显示（规格 §5.1：翻译只在前端
 * 发生，后端只给稳定标识）。MCP / 扩展提供的 description 是第三方的原文，
 * 不属于我们的文案，一律原样显示——翻译别人的工具描述会让用户对着与模型实际
 * 看到的不一样的说明做决定。
 */
function descriptionOf(tool: ToolDescriptor): string {
  if (tool.source === "builtin") {
    const key = `tools.builtin.${tool.name}`;
    if (hasKey(key)) return t(key);
  }
  return tool.description ?? "";
}

/** 有 key 就翻译，没有就显示原始值：缺一条文案不是"编一个"的理由。 */
function labelOf(map: Record<string, string>, value: string): string {
  const key = map[value];
  return key ? t(key) : value;
}

function toneFor(tool: ToolDescriptor): "success" | "neutral" | "danger" | "warning" {
  if (!tool.enabled) {
    return tool.capabilityState === "disabled" || tool.capabilityState === "hidden"
      ? "neutral"
      : "danger";
  }
  return tool.capabilityState === "deferred" ? "warning" : "success";
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
          <h2 className="text-sm font-medium text-[color:var(--anvil-foreground)]">
            {t("tools.title")}
          </h2>
          <p className="mt-0.5 text-[11px] text-[color:var(--anvil-muted)]">{t("tools.subtitle")}</p>
        </div>
        <button
          onClick={() => void load()}
          disabled={loading}
          className="flex shrink-0 items-center gap-1 rounded border border-[color:var(--anvil-border)] px-2 py-1 text-[11px] text-[color:var(--anvil-foreground)] hover:bg-[color:var(--anvil-surface-raised)] disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[color:var(--anvil-focus)]"
        >
          <RefreshCw className="h-3 w-3" aria-hidden /> {t("tools.actions.refresh")}
        </button>
      </header>

      {error && (
        <div className="mb-2">
          <ErrorState error={error} onRetry={() => void load()} title={t("tools.errorTitle")} />
        </div>
      )}

      {loading && items.length === 0 ? (
        <LoadingState label={t("tools.loading")} />
      ) : items.length === 0 ? (
        !error && (
          <EmptyState
            title={t("tools.empty.title")}
            hint={t("tools.empty.hint")}
            icon={<Wrench className="h-4 w-4" aria-hidden />}
          />
        )
      ) : (
        <div className="space-y-2">
          {groups.map(([source, tools]) => (
            <div key={source}>
              <div className="mb-1 flex items-center gap-1.5 text-[10px] text-[color:var(--anvil-muted)]">
                <span className="font-medium text-[color:var(--anvil-foreground)]">
                  {labelOf(SOURCE_KEY, source)}
                </span>
                <span>· {tools.length}</span>
                {source === "extension" && (
                  <span className="text-[color:var(--anvil-muted)]">
                    {t("tools.extensionUnknown")}
                  </span>
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
                        {labelOf(CAPABILITY_KEY, tool.capabilityState)}
                      </Badge>
                      <Badge tone="neutral">{labelOf(POLICY_KEY, tool.policy as string)}</Badge>
                      {tool.server && <Badge tone="neutral">{tool.server}</Badge>}
                    </div>
                    {descriptionOf(tool) && (
                      <div className="mt-0.5 line-clamp-2 text-[10px] text-[color:var(--anvil-muted)]">
                        {descriptionOf(tool)}
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
  tone: "success" | "warning" | "danger" | "neutral";
  children: string;
}) {
  const cls = {
    success: "bg-[color:var(--anvil-success-surface)] text-[color:var(--anvil-success)]",
    warning: "bg-[color:var(--anvil-warning-surface)] text-[color:var(--anvil-warning)]",
    danger: "bg-[color:var(--anvil-danger-surface)] text-[color:var(--anvil-danger)]",
    neutral: "bg-[color:var(--anvil-surface-raised)] text-[color:var(--anvil-muted)]",
  }[tone];
  return <span className={`shrink-0 rounded px-1 py-0.5 text-[9px] ${cls}`}>{children}</span>;
}
