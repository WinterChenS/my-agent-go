// Sub-agents, as the parent window shows them.
//
// The transcript used to render a child's whole reasoning, tool calls and
// answer inline. A parent that delegates to three children then buries its own
// answer under three transcripts nobody asked to read. The rule now is:
//
// * the composer row carries one control with the counts — running, failed;
// * the transcript keeps one slim row per child, so the reader can see *that*
//   a delegation happened and where;
// * the child's own words live in a floating panel on the right, opened for the
//   agent the reader picked and closed without touching the conversation.

import type { RailSubAgent, RailSubAgentStep } from '../../chat/client'
import type { LiveSubAgent } from '../../chat/useChatSession'
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

/** One slim row in the transcript: the delegation is visible, the words are not. */
export function SubAgentRow({
  agent,
  onOpen,
}: {
  agent: SubAgentView
  onOpen: (id: string) => void
}) {
  const { t } = useI18n()
  const live = isRunningStatus(agent.status)
  const tone =
    agent.status === 'failed'
      ? ' chat-tone-danger'
      : agent.status === 'completed'
        ? ' chat-tone-success'
        : live
          ? ' chat-tone-warn'
          : ''
  const tools = agent.steps.filter((step) => step.kind === 'tool').length
  return (
    <button
      type="button"
      className="chat-subagent-row"
      data-testid="chat-subagent-row"
      data-agent={agent.id}
      data-status={agent.status}
      onClick={() => onOpen(agent.id)}
    >
      <span className="chat-subagent-icon">
        <Icon name="layers" size={12} />
      </span>
      <span className="chat-subagent-name">{agent.name || t('chat.subagent.unnamed')}</span>
      {agent.step !== null ? (
        <span className="chat-subagent-step">{t('chat.subagent.step', { step: agent.step })}</span>
      ) : null}
      {agent.model ? <span className="chat-subagent-model">{agent.model}</span> : null}
      <span className={`chat-badge${tone}`}>
        {t(`chat.subagent.status.${agent.status}` as MessageKey)}
      </span>
      {tools > 0 ? <span className="chat-subagent-count">{t('chat.subagent.tools', { count: tools })}</span> : null}
      <span className="chat-subagent-open">{t('chat.subagent.row.open')}</span>
      <span className="chat-tool-caret" aria-hidden="true">
        <Icon name="chevDown" size={10} />
      </span>
    </button>
  )
}

/** The floating panel: one agent's own transcript, nothing else. */
export function SubAgentDrawer({
  agent,
  onClose,
  onArchive,
}: {
  agent: SubAgentView | null
  onClose: () => void
  onArchive: (id: string) => void
}) {
  const { t } = useI18n()
  if (!agent) return null
  const live = isRunningStatus(agent.status)
  return (
    <aside
      className="chat-subagent-drawer"
      data-testid="chat-subagent-drawer"
      data-agent={agent.id}
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
      {agent.task ? (
        <p className="chat-subagent-drawer-task">
          <span>{t('chat.subagent.task')}</span>
          {agent.task}
        </p>
      ) : null}
      <div className="chat-subagent-drawer-body">
        {agent.steps.length === 0 ? (
          <p className="chat-subagent-empty">{t('chat.subagent.empty')}</p>
        ) : (
          agent.steps.map((step, index) =>
            step.kind === 'text' ? (
              <div
                className={`chat-subagent-text${step.channel === 'thinking' ? ' is-thinking' : ''}`}
                key={`${agent.id}-step-${index}`}
              >
                {step.channel === 'thinking' ? (
                  <span className="chat-subagent-label">{t('chat.subagent.thinking')}</span>
                ) : null}
                {step.text}
              </div>
            ) : (
              <div
                className="chat-subagent-tool"
                key={`${agent.id}-step-${index}`}
                data-ok={step.ok === null || step.ok === undefined ? 'pending' : String(step.ok)}
              >
                <span className="chat-subagent-tool-name">{step.name}</span>
                {step.args ? <code>{typeof step.args === 'string' ? step.args : JSON.stringify(step.args)}</code> : null}
                {step.output ? (
                  <pre>{typeof step.output === 'string' ? step.output : JSON.stringify(step.output)}</pre>
                ) : null}
              </div>
            ),
          )
        )}
        {agent.error ? <p className="chat-subagent-error">{agent.error}</p> : null}
        {agent.result && !agent.error ? (
          <div className="chat-subagent-result">
            <span className="chat-subagent-label">{t('chat.subagent.result')}</span>
            <Markdown text={agent.result} sources={0} onCite={() => undefined} />
          </div>
        ) : null}
      </div>
    </aside>
  )
}
