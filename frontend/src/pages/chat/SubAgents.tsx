// Sub-agents, as the parent window shows them.
//
// A delegation is not something the parent said. Printing the child's reasoning,
// tool calls and answer inside the reply made one turn read as if it had
// stopped mid-sentence and started again — and with three children it buried
// the answer entirely. The rule now is:
//
// * the composer row carries one control with the counts — running, failed;
// * the transcript says nothing at all about the child;
// * the child's own transcript lives in a floating panel on the right, shaped
//   like the main conversation: task, reasoning, tool calls, answer.

import { useEffect, useRef } from 'react'
import type { RailSubAgent, RailSubAgentStep } from '../../chat/client'
import type { LiveSubAgent } from '../../chat/useChatSession'
import { imageSrc } from '../../chat/image'
import type { MessageKey } from '../../i18n/catalogue'
import { useI18n } from '../../i18n/LanguageProvider'
import Icon from '../../shell/Icon'
import { Markdown } from './Markdown'

/** One agent, whichever side of the wire it came from. */
export interface SubAgentView {
  id: string
  name: string
  task: string
  model: string
  status: string
  step: number | null
  /** 1-based turn that delegated it; `0` when the turn is still starting. */
  turn: number
  runId: string
  steps: RailSubAgentStep[]
  result: string
  error: string
}

export function isRunningStatus(status: string): boolean {
  return status === 'running' || status === 'pending' || status === 'waiting'
}

function fromRail(agent: RailSubAgent): SubAgentView {
  return {
    id: agent.id,
    name: agent.name,
    task: agent.task,
    model: agent.model,
    status: agent.status || 'running',
    step: agent.step ?? null,
    turn: agent.turn ?? 0,
    runId: agent.runId,
    steps: agent.steps ?? [],
    result: agent.result ?? '',
    error: agent.error ?? '',
  }
}

export function subAgentFromLive(agent: LiveSubAgent): SubAgentView {
  return {
    id: agent.id,
    name: agent.name,
    task: agent.task,
    model: agent.model,
    status: agent.status,
    step: agent.step,
    turn: 0,
    runId: '',
    // The live card and the rail describe steps the same way; the live copy is
    // narrower only because it was built for rendering.
    steps: agent.steps as RailSubAgentStep[],
    result: agent.result,
    error: agent.error,
  }
}

/**
 * Merge the stored list with the live one.
 *
 * The rail is the authority on which agents exist and which turn produced them;
 * the live stream is fresher for the agent that is still talking. Matching on
 * the runtime's id keeps them one row instead of two.
 */
export function mergeSubAgents(
  stored: RailSubAgent[] | undefined,
  live: LiveSubAgent[] | undefined,
): SubAgentView[] {
  const merged: SubAgentView[] = []
  const index = new Map<string, number>()
  for (const agent of stored ?? []) {
    index.set(agent.id, merged.length)
    merged.push(fromRail(agent))
  }
  for (const agent of live ?? []) {
    const position = index.get(agent.id)
    if (position === undefined) {
      index.set(agent.id, merged.length)
      merged.push(subAgentFromLive(agent))
      continue
    }
    const current = merged[position]
    const next = subAgentFromLive(agent)
    merged[position] = {
      ...current,
      name: next.name || current.name,
      task: next.task || current.task,
      model: next.model || current.model,
      status: next.status || current.status,
      step: next.step ?? current.step,
      steps: next.steps.length > 0 ? next.steps : current.steps,
      result: next.result || current.result,
      error: next.error || current.error,
    }
  }
  // Chronological, with the unfinished ones first: a running child is the thing
  // the reader came to the button for, and a finished one keeps its place.
  return merged.sort((left, right) => {
    const leftLive = isRunningStatus(left.status) ? 0 : 1
    const rightLive = isRunningStatus(right.status) ? 0 : 1
    if (leftLive !== rightLive) return leftLive - rightLive
    return left.turn - right.turn || left.id.localeCompare(right.id)
  })
}

function counts(agents: SubAgentView[]): { running: number; failed: number } {
  let running = 0
  let failed = 0
  for (const agent of agents) {
    if (isRunningStatus(agent.status)) running += 1
    else if (agent.status === 'failed') failed += 1
  }
  return { running, failed }
}

/** The control above the composer: what is delegated, and how it is doing. */
export function SubAgentButton({
  agents,
  open,
  onToggle,
}: {
  agents: SubAgentView[]
  open: boolean
  onToggle: () => void
}) {
  const { t } = useI18n()
  const { running, failed } = counts(agents)
  if (agents.length === 0) return null
  return (
    <button
      type="button"
      className={`chat-subagent-trigger${open ? ' is-open' : ''}${failed > 0 ? ' has-failure' : ''}`}
      data-testid="chat-subagent-trigger"
      data-running={running}
      data-failed={failed}
      aria-expanded={open}
      aria-haspopup="dialog"
      title={t('chat.subagent.panel.title')}
      onClick={onToggle}
    >
      <Icon name="layers" size={12} />
      <span className="chat-subagent-trigger-name">
        {running > 0
          ? t('chat.subagent.buttonRunning', { count: running })
          : t('chat.subagent.button')}
      </span>
      {/* The total only says something when some agents are finished; while
          everything is running it just repeats the number beside it. */}
      {agents.length > running + failed ? (
        <span className="chat-subagent-trigger-total">
          {t('chat.subagent.buttonTotal', { count: agents.length })}
        </span>
      ) : null}
      {failed > 0 ? (
        <span className="chat-subagent-trigger-failed" data-testid="chat-subagent-failed">
          <Icon name="alert" size={10} />
          {t('chat.subagent.buttonFailed', { count: failed })}
        </span>
      ) : null}
    </button>
  )
}

/** The popover: every agent that has not been archived. */
export function SubAgentList({
  agents,
  activeId,
  onOpen,
  onArchive,
  onClose,
}: {
  agents: SubAgentView[]
  activeId: string | null
  onOpen: (id: string) => void
  onArchive: (id: string) => void
  onClose: () => void
}) {
  const { t } = useI18n()
  return (
    <div
      className="chat-subagent-popover"
      data-testid="chat-subagent-popover"
      role="dialog"
      aria-label={t('chat.subagent.panel.title')}
    >
      <div className="chat-subagent-popover-head">
        <Icon name="layers" size={12} />
        <b>{t('chat.subagent.panel.title')}</b>
        <button type="button" className="chat-iconbtn" aria-label={t('chat.subagent.panel.close')} onClick={onClose}>
          <Icon name="x" size={12} />
        </button>
      </div>
      {agents.length === 0 ? (
        <p className="chat-subagent-popover-empty">{t('chat.subagent.panel.empty')}</p>
      ) : (
        <ul className="chat-subagent-list">
          {agents.map((agent) => {
            const live = isRunningStatus(agent.status)
            return (
              <li key={agent.id} className={`chat-subagent-item${agent.id === activeId ? ' is-active' : ''}`}>
                <button
                  type="button"
                  className="chat-subagent-item-main"
                  data-testid="chat-subagent-item"
                  data-agent={agent.id}
                  onClick={() => onOpen(agent.id)}
                >
                  <span className={`chat-subagent-dot${live ? ' is-live' : ''}`} data-status={agent.status} />
                  <span className="chat-subagent-item-copy">
                    <span className="chat-subagent-item-name">
                      {agent.name || t('chat.subagent.unnamed')}
                      {agent.step !== null ? (
                        <small>{t('chat.subagent.step', { step: agent.step })}</small>
                      ) : null}
                    </span>
                    <span className="chat-subagent-item-task">{agent.task || agent.result || agent.error}</span>
                    <span className="chat-subagent-item-meta">
                      {agent.turn > 0
                        ? `${t('chat.subagent.panel.turn', { turn: agent.turn })} · `
                        : ''}
                      {t(`chat.subagent.status.${agent.status}` as MessageKey)}
                    </span>
                  </span>
                </button>
                <button
                  type="button"
                  className="chat-subagent-item-archive"
                  title={t('chat.subagent.panel.archive')}
                  aria-label={t('chat.subagent.panel.archive')}
                  onClick={() => onArchive(agent.id)}
                >
                  <Icon name="archive" size={11} />
                </button>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

/** One tool call in the child's transcript. */
function ToolStep({ step }: { step: RailSubAgentStep }) {
  const raw = typeof step.args === 'string' ? step.args : step.args == null ? '' : JSON.stringify(step.args)
  // `bash` is the common case and its arguments already are a command line:
  // showing `{"command":"ls -la"}` instead of `ls -la` is noise.
  const command = (() => {
    if (!raw.trim().startsWith('{')) return ''
    try {
      const parsed = JSON.parse(raw) as { command?: unknown }
      return typeof parsed.command === 'string' ? parsed.command : ''
    } catch {
      return ''
    }
  })()
  const summary = command || raw.replace(/\s+/g, ' ').slice(0, 160)
  const output =
    typeof step.output === 'string'
      ? step.output
      : step.output == null
        ? ''
        : JSON.stringify(step.output, null, 2)
  const tone = step.ok === true ? ' is-ok' : step.ok === false ? ' is-failed' : ' is-pending'
  return (
    <div
      className={`chat-subagent-tool${tone}`}
      data-ok={step.ok === null || step.ok === undefined ? 'pending' : String(step.ok)}
    >
      <div className="chat-subagent-tool-head">
        <span className="chat-subagent-tool-dot" aria-hidden="true" />
        <b>{step.name || 'tool'}</b>
        {summary ? <code title={summary}>{summary}</code> : null}
      </div>
      {output ? <pre>{output}</pre> : null}
    </div>
  )
}

/** The floating panel: one agent's own transcript, nothing else. */
export function SubAgentDrawer({
  agent,
  onClose,
  onArchive,
  onReturn,
}: {
  agent: SubAgentView | null
  onClose: () => void
  onArchive: (id: string) => void
  /** Go back to the parent turn that delegated this child. */
  onReturn?: (runId: string, turn: number) => void
}) {
  const { t } = useI18n()
  const body = useRef<HTMLDivElement | null>(null)
  const steps = agent?.steps.length ?? 0

  // A running child keeps appending; without this the reader stays parked at
  // the top of a transcript that has moved on.
  useEffect(() => {
    const node = body.current
    if (!node) return
    node.scrollTop = node.scrollHeight
  }, [steps, agent?.id])

  if (!agent) return null
  const live = isRunningStatus(agent.status)
  const tools = agent.steps.filter((step) => step.kind === 'tool').length
  return (
    <aside
      className="chat-subagent-drawer"
      data-testid="chat-subagent-drawer"
      data-agent={agent.id}
      data-status={agent.status}
      aria-label={agent.name || t('chat.subagent.unnamed')}
    >
      <header className="chat-subagent-drawer-head">
        <span className={`chat-subagent-dot${live ? ' is-live' : ''}`} data-status={agent.status} />
        <div className="chat-subagent-drawer-title">
          <b>{agent.name || t('chat.subagent.unnamed')}</b>
          <small>
            {t(`chat.subagent.status.${agent.status}` as MessageKey)}
            {agent.turn > 0 ? ` · ${t('chat.subagent.panel.turn', { turn: agent.turn })}` : ''}
            {agent.model ? ` · ${agent.model}` : ''}
            {tools > 0 ? ` · ${t('chat.subagent.tools', { count: tools })}` : ''}
          </small>
        </div>
        <button
          type="button"
          className="chat-iconbtn"
          data-testid="chat-subagent-archive"
          title={t('chat.subagent.panel.archive')}
          aria-label={t('chat.subagent.panel.archive')}
          onClick={() => onArchive(agent.id)}
        >
          <Icon name="archive" size={12} />
        </button>
        <button
          type="button"
          className="chat-iconbtn"
          data-testid="chat-subagent-drawer-close"
          aria-label={t('chat.subagent.panel.close')}
          onClick={onClose}
        >
          <Icon name="x" size={13} />
        </button>
      </header>

      <div className="chat-subagent-drawer-body" ref={body} data-testid="chat-subagent-drawer-body">
        <p className="chat-subagent-oneway" data-testid="chat-subagent-oneway">
          <Icon name="alert" size={11} />
          <span>{t('chat.subagent.oneWay')}</span>
          {agent.runId && agent.turn > 0 && onReturn ? (
            <button
              type="button"
              className="chat-subagent-return"
              data-testid="chat-subagent-return"
              onClick={() => onReturn(agent.runId, agent.turn)}
            >
              {t('chat.subagent.return')}
            </button>
          ) : null}
        </p>
        {agent.task ? (
          <section className="chat-subagent-brief" data-testid="chat-subagent-brief">
            <span className="chat-subagent-label">{t('chat.subagent.task')}</span>
            <div className="chat-prose chat-markdown">
              <Markdown text={agent.task} sources={0} onCite={() => undefined} />
            </div>
          </section>
        ) : null}

        {agent.steps.length === 0 ? (
          <p className="chat-subagent-empty" data-testid="chat-subagent-waiting">
            {live ? (
              <>
                <span className="chat-subagent-pulse" aria-hidden="true" />
                {t('chat.subagent.empty')}
              </>
            ) : (
              t('chat.subagent.emptyDone')
            )}
          </p>
        ) : (
          agent.steps.map((step, index) =>
            step.kind === 'image' && step.image ? (
              <figure
                className="chat-subagent-image"
                key={`${agent.id}-step-${index}`}
                data-testid="chat-subagent-image"
              >
                <img src={imageSrc(step.image)} alt={t('chat.tool.imageAlt')} />
                <figcaption>{t('chat.tool.imageCaption', { tool: agent.name || 'agent' })}</figcaption>
              </figure>
            ) : step.kind === 'text' ? (
              step.channel === 'thinking' ? (
                <details className="chat-subagent-think" key={`${agent.id}-step-${index}`}>
                  <summary>
                    <Icon name="brain" size={11} />
                    {t('chat.subagent.thinking')}
                    <span className="chat-subagent-think-size">
                      {t('chat.live.thinking', { count: (step.text ?? '').length })}
                    </span>
                  </summary>
                  <div className="chat-prose chat-markdown">
                    <Markdown text={step.text ?? ''} sources={0} onCite={() => undefined} />
                  </div>
                </details>
              ) : (
                <div
                  className="chat-prose chat-markdown chat-subagent-text"
                  key={`${agent.id}-step-${index}`}
                >
                  <Markdown text={step.text ?? ''} sources={0} onCite={() => undefined} />
                </div>
              )
            ) : (
              <ToolStep key={`${agent.id}-step-${index}`} step={step} />
            ),
          )
        )}

        {agent.error ? <p className="chat-subagent-error">{agent.error}</p> : null}
        {/* The steps already end with the child's last message; printing the
            final result again would show the same paragraph twice. It is the
            only content when the transcript never arrived. */}
        {agent.result && !agent.error && agent.steps.length === 0 ? (
          <div className="chat-subagent-result">
            <span className="chat-subagent-label">{t('chat.subagent.result')}</span>
            <div className="chat-prose chat-markdown">
              <Markdown text={agent.result} sources={0} onCite={() => undefined} />
            </div>
          </div>
        ) : null}
      </div>
    </aside>
  )
}
