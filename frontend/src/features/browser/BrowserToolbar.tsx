// 浏览器工具条（B07）。
//
// 两个必须守住的位置关系：
//
//  1. **停止按钮在这里**，而这里是原生网页区域**之外**（原生视图从工具条下方开始）。
//     停止按钮不能放进 slot，否则一个原生子窗口就会把它整个盖住——那时用户
//     既看不见也点不到"停下来"。
//  2. 工具条、tab 条、提示都跟随 Anvil 主题（--anvil-* token）；远端网页内容
//     一个字节都不注入主题（网页是网页，它的配色不归我们改）。

import { useEffect, useState } from "react";
import { ArrowLeft, RotateCw, Square } from "lucide-react";
import { t } from "../../i18n";
import type { BrowserControlView, BrowserTabView } from "./api";

const MODE_KEYS: Record<string, string> = {
  agent: "browser.control.agent",
  human: "browser.control.human",
  pausing: "browser.control.pausing",
  resuming: "browser.control.resuming",
  control_uncertain: "browser.control.controlUncertain",
};

const MODE_TONES: Record<string, string> = {
  agent: "text-[color:var(--anvil-success)] border-[color:var(--anvil-success)]",
  human: "text-[color:var(--anvil-accent)] border-[color:var(--anvil-accent)]",
  pausing: "text-[color:var(--anvil-warning)] border-[color:var(--anvil-warning)]",
  resuming: "text-[color:var(--anvil-warning)] border-[color:var(--anvil-warning)]",
  control_uncertain: "text-[color:var(--anvil-danger)] border-[color:var(--anvil-danger)]",
};

function modeLabel(mode: string): string {
  return t(MODE_KEYS[mode] ?? "browser.control.unknown");
}

export interface BrowserToolbarProps {
  tab: BrowserTabView | null;
  control: BrowserControlView;
  history: string[];
  canStop: boolean;
  onStop?: () => void;
  onNavigate(url: string): void;
  onBack(): void;
  onReload(): void;
  onTakeOver(): void;
  onResume(): void;
}

export function BrowserToolbar({
  tab,
  control,
  history,
  canStop,
  onStop,
  onNavigate,
  onBack,
  onReload,
  onTakeOver,
  onResume,
}: BrowserToolbarProps) {
  const [draft, setDraft] = useState(tab?.url ?? "");
  useEffect(() => {
    setDraft(tab?.url ?? "");
  }, [tab?.url]);

  const mode = control.mode || "agent";
  const tone = MODE_TONES[mode] ?? "text-[color:var(--anvil-muted)] border-[color:var(--anvil-border)]";

  return (
    <div data-browser-toolbar="" className="flex flex-col gap-0.5">
      <div className="flex items-center gap-1">
        <button
          type="button"
          aria-label={t("browser.address.back")}
          title={t("browser.address.back")}
          disabled={!tab || history.length < 2}
          onClick={onBack}
          className="rounded p-1 hover:bg-[color:var(--anvil-background)] disabled:opacity-40"
        >
          <ArrowLeft className="h-3.5 w-3.5" aria-hidden />
        </button>
        <button
          type="button"
          aria-label={t("browser.address.reload")}
          title={t("browser.address.reload")}
          disabled={!tab}
          onClick={onReload}
          className="rounded p-1 hover:bg-[color:var(--anvil-background)] disabled:opacity-40"
        >
          <RotateCw className="h-3.5 w-3.5" aria-hidden />
        </button>
        <input
          type="text"
          aria-label={t("browser.address.label")}
          placeholder={t("browser.address.placeholder")}
          spellCheck={false}
          value={draft}
          disabled={!tab}
          onChange={(event) => setDraft(event.target.value)}
          onKeyDown={(event) => {
            if (event.key !== "Enter") return;
            const url = draft.trim();
            if (!url) return;
            onNavigate(url);
          }}
          className="min-w-0 flex-1 rounded border border-[color:var(--anvil-border)] bg-[color:var(--anvil-background)] px-1.5 py-0.5 font-mono text-[10px] text-[color:var(--anvil-foreground)]"
        />
        <span
          data-browser-control={mode}
          className={"shrink-0 rounded border px-1 py-0.5 text-[10px] " + tone}
        >
          {modeLabel(mode)}
        </span>
        <button
          type="button"
          aria-label={t("browser.control.takeOver")}
          title={t("browser.control.takeOverHint")}
          disabled={!tab || !control.canTakeOver}
          onClick={onTakeOver}
          className="shrink-0 rounded border border-[color:var(--anvil-border)] px-1 py-0.5 text-[10px] hover:bg-[color:var(--anvil-background)] disabled:opacity-40"
        >
          {t("browser.control.takeOver")}
        </button>
        <button
          type="button"
          aria-label={t("browser.control.resume")}
          title={t("browser.control.resumeHint")}
          disabled={!tab || !control.canResume}
          onClick={onResume}
          className="shrink-0 rounded border border-[color:var(--anvil-border)] px-1 py-0.5 text-[10px] hover:bg-[color:var(--anvil-background)] disabled:opacity-40"
        >
          {t("browser.control.resume")}
        </button>
        {canStop ? (
          // 停止按钮固定在工具条（原生网页区域之外），任何面板状态都动不到它的位置。
          <button
            type="button"
            data-browser-stop=""
            aria-label={t("browser.stop")}
            title={t("browser.stopHint")}
            onClick={onStop}
            className="shrink-0 rounded border border-[color:var(--anvil-danger)] px-1 py-0.5 text-[10px] text-[color:var(--anvil-danger)] hover:bg-[color:var(--anvil-background)]"
          >
            <Square className="h-3 w-3" aria-hidden />
          </button>
        ) : null}
      </div>
      {control.detail ? (
        <p data-browser-control-detail="" className="truncate text-[10px] text-[color:var(--anvil-warning)]" title={control.detail}>
          {control.detail}
        </p>
      ) : null}
    </div>
  );
}
