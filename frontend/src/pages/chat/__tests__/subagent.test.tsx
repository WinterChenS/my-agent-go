import { cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { LanguageProvider } from '../../../i18n/LanguageProvider'
import type { LanguageEnvironment } from '../../../i18n/language'
import type { RailSubAgent, SessionEvent } from '../../../chat/client'
import { buildTurnParts, type LiveSubAgent } from '../../../chat/useChatSession'
import { SubAgentButton, SubAgentDrawer, SubAgentRow, mergeSubAgents, subAgentFromLive } from '../SubAgents'

afterEach(() => cleanup())

const environment: LanguageEnvironment = {
  initialLanguage: () => 'zh-CN',
  storage: { load: async () => undefined, save: async () => undefined },
  apply: () => undefined,
}

function wrap(node: ReactNode) {
  return <LanguageProvider environment={environment}>{node}</LanguageProvider>
}

let seq = 0
function event(kind: SessionEvent['kind'], payload: Record<string, unknown>): SessionEvent {
  seq += 1
  return {
    sessionId: 'pi-1',
    runId: 'run-a',
    seq,
    kind,
    at: '2026-11-17T00:00:00Z',
    payload,
  }
}

function agentOf(events: SessionEvent[]): LiveSubAgent {
  const part = buildTurnParts(events, 'run-a').find((entry) => entry.kind === 'agent')
  if (!part || part.kind !== 'agent') throw new Error('no agent part')
  return part.agent
}

describe('sub-agent parts', () => {
  it('folds one child into a single card instead of one card per event', () => {
    const agent = agentOf([
      event('sub_agent', {
        agentId: 'child-1',
        name: 'scout',
        task: 'count the files',
        model: 'gpt-6-astra',
        status: 'running',
        phase: 'started',
      }),
      event('sub_agent', { agentId: 'child-1', status: 'running', phase: 'text', text: 'count' }),
      event('sub_agent', { agentId: 'child-1', status: 'running', phase: 'text', text: 'ing' }),
      event('sub_agent', {
        agentId: 'child-1',
        status: 'completed',
        phase: 'completed',
        result: '42 files',
      }),
    ])
    expect(agent.id).toBe('child-1')
    expect(agent.name).toBe('scout')
    expect(agent.task).toBe('count the files')
    expect(agent.model).toBe('gpt-6-astra')
    expect(agent.status).toBe('completed')
    expect(agent.steps).toHaveLength(1)
    expect(agent.steps[0]).toMatchObject({ kind: 'text', text: 'counting' })
    expect(agent.result).toBe('42 files')
  })

  it('keeps two children apart and in the order they were announced', () => {
    const parts = buildTurnParts(
      [
        event('sub_agent', { agentId: 'a', name: 'scout', status: 'running', phase: 'started' }),
        event('sub_agent', { agentId: 'b', name: 'reviewer', status: 'running', phase: 'started' }),
        event('sub_agent', { agentId: 'a', status: 'completed', phase: 'completed' }),
      ],
      'run-a',
    )
    const agents = parts.filter((part) => part.kind === 'agent')
    expect(agents).toHaveLength(2)
    expect(agents[0].kind === 'agent' && agents[0].agent.name).toBe('scout')
    expect(agents[1].kind === 'agent' && agents[1].agent.name).toBe('reviewer')
  })

  it('closes a tool row when its result arrives and keeps text around it', () => {
    const agent = agentOf([
      event('sub_agent', { agentId: 'a', name: 'scout', status: 'running', phase: 'started' }),
      event('sub_agent', { agentId: 'a', status: 'running', phase: 'text', text: 'checking' }),
      event('sub_agent', {
        agentId: 'a',
        status: 'running',
        phase: 'tool',
        tool: { name: 'command', args: { command: 'ls' } },
      }),
      event('sub_agent', {
        agentId: 'a',
        status: 'running',
        phase: 'tool_result',
        tool: { name: 'command', ok: true, output: '42 files' },
      }),
      event('sub_agent', { agentId: 'a', status: 'running', phase: 'text', text: 'done' }),
    ])
    expect(agent.steps.map((step) => step.kind)).toEqual(['text', 'tool', 'text'])
    const tool = agent.steps[1]
    expect(tool.kind === 'tool' && tool.ok).toBe(true)
    expect(tool.kind === 'tool' && tool.output).toBe('42 files')
  })

  /// pi's extension re-sends the whole transcript; appending would triple every
  /// line, so the row replaces the content.
  it('replaces the content when the runtime sends a snapshot', () => {
    const agent = agentOf([
      event('sub_agent', { agentId: 'a', name: 'scout', status: 'running', phase: 'started' }),
      event('sub_agent', { agentId: 'a', status: 'running', phase: 'text', text: 'first pass' }),
      event('sub_agent', {
        agentId: 'a',
        status: 'running',
        phase: 'snapshot',
        steps: [
          { kind: 'text', text: 'counting files' },
          { kind: 'tool', name: 'bash', args: { command: 'ls | wc -l' } },
          { kind: 'tool_result', name: 'bash', ok: true, output: '42' },
          { kind: 'text', text: '42 files' },
        ],
      }),
    ])
    expect(agent.steps.map((step) => step.kind)).toEqual(['text', 'tool', 'text'])
    expect(agent.steps[0]).toMatchObject({ kind: 'text', text: 'counting files' })
    expect(agent.steps[1]).toMatchObject({ kind: 'tool', name: 'bash', ok: true, output: '42' })
    expect(agent.steps[2]).toMatchObject({ kind: 'text', text: '42 files' })
  })

  /// codex sends the finished message after the deltas, so the final copy must
  /// replace the tail rather than printing the answer twice.
  it('uses the final copy instead of repeating the streamed answer', () => {
    const agent = agentOf([
      event('sub_agent', { agentId: 'a', name: 'scout', status: 'running', phase: 'started' }),
      event('sub_agent', { agentId: 'a', status: 'running', phase: 'text', text: '42 fi' }),
      event('sub_agent', {
        agentId: 'a',
        status: 'running',
        phase: 'text',
        text: '42 files under src',
        final: true,
      }),
    ])
    expect(agent.steps).toHaveLength(1)
    expect(agent.steps[0]).toMatchObject({ kind: 'text', text: '42 files under src' })
  })

  it('keeps a failed child that never produced anything visible', () => {
    const agent = agentOf([
      event('sub_agent', { agentId: 'a', name: 'worker', status: 'running', phase: 'started' }),
      event('sub_agent', {
        agentId: 'a',
        status: 'failed',
        phase: 'failed',
        error: 'model quota exhausted',
      }),
    ])
    expect(agent.status).toBe('failed')
    expect(agent.error).toBe('model quota exhausted')
    expect(agent.steps).toHaveLength(0)
  })
})


const base: LiveSubAgent = {
  id: 'child',
  name: 'scout',
  model: '',
  task: '',
  status: 'running',
  parentId: '',
  step: null,
  steps: [],
  result: '',
  error: '',
  usage: null,
}
describe('sub-agent surfaces', () => {
  const agent: LiveSubAgent = {
    id: 'child-1',
    name: 'scout',
    model: 'gpt-6-astra',
    task: 'count the rust files',
    status: 'running',
    parentId: 'main',
    step: null,
    steps: [
      { kind: 'text', text: 'counting files', channel: 'thinking' },
      { kind: 'tool', name: 'bash', args: '{"command":"ls"}', ok: true, output: '42' },
      { kind: 'text', text: 'there are 42 files', channel: 'answer' },
    ],
    result: '',
    error: '',
    usage: null,
  }

  it('keeps the transcript out of the main window: the row is a pointer', () => {
    const onOpen = vi.fn()
    render(wrap(<SubAgentRow agent={subAgentFromLive(agent)} onOpen={onOpen} />))
    const row = screen.getByTestId('chat-subagent-row')
    expect(row.getAttribute('data-status')).toBe('running')
    expect(within(row).getByText('scout')).toBeInTheDocument()
    expect(within(row).getByText('gpt-6-astra')).toBeInTheDocument()
    expect(within(row).getByText('运行中')).toBeInTheDocument()
    // The child's own words live in the panel, not here.
    expect(within(row).queryByText('there are 42 files')).toBeNull()
    expect(within(row).queryByText('counting files')).toBeNull()
  })

  it('opens the panel for the agent the reader clicked', async () => {
    const onOpen = vi.fn()
    render(wrap(<SubAgentRow agent={subAgentFromLive(agent)} onOpen={onOpen} />))
    await userEvent.click(screen.getByTestId('chat-subagent-row'))
    expect(onOpen).toHaveBeenCalledWith('child-1')
  })

  it('shows the child transcript in the floating panel, tools and reasoning included', () => {
    render(wrap(<SubAgentDrawer agent={subAgentFromLive(agent)} onClose={() => undefined} onArchive={() => undefined} />))
    const panel = screen.getByTestId('chat-subagent-drawer')
    expect(within(panel).getByText('count the rust files')).toBeInTheDocument()
    expect(within(panel).getByText('there are 42 files')).toBeInTheDocument()
    expect(within(panel).getByText('bash')).toBeInTheDocument()
    expect(within(panel).getByText('42')).toBeInTheDocument()
    expect(within(panel).getByText('思考')).toBeInTheDocument()
  })

  it('shows the failure reason instead of pretending it finished', () => {
    render(
      wrap(
        <SubAgentDrawer
          agent={subAgentFromLive({ ...agent, status: 'failed', error: 'model quota exhausted' })}
          onClose={() => undefined}
          onArchive={() => undefined}
        />,
      ),
    )
    const panel = screen.getByTestId('chat-subagent-drawer')
    expect(within(panel).getByText(/失败/)).toBeInTheDocument()
    expect(within(panel).getByText('model quota exhausted')).toBeInTheDocument()
  })

  it('renders the final answer once it exists', () => {
    render(
      wrap(
        <SubAgentDrawer
          agent={subAgentFromLive({ ...agent, status: 'completed', result: '42 files under src' })}
          onClose={() => undefined}
          onArchive={() => undefined}
        />,
      ),
    )
    expect(screen.getByText(/已完成/)).toBeInTheDocument()
    expect(screen.getByText('42 files under src')).toBeInTheDocument()
  })
})

describe('sub-agent counts and merging', () => {
  const rail = (id: string, status: string, turn = 1): RailSubAgent => ({
    id,
    name: id,
    task: '',
    model: '',
    status,
    turn,
    runId: `r${turn}`,
    steps: [],
    result: '',
    error: '',
  })

  it('counts running and failed agents on the composer control', () => {
    const agents = [
      subAgentFromLive({ ...base, id: 'a', status: 'running' }),
      subAgentFromLive({ ...base, id: 'b', status: 'waiting' }),
      subAgentFromLive({ ...base, id: 'c', status: 'failed' }),
      subAgentFromLive({ ...base, id: 'd', status: 'completed' }),
    ]
    render(wrap(<SubAgentButton agents={agents} open={false} onToggle={() => undefined} />))
    const trigger = screen.getByTestId('chat-subagent-trigger')
    expect(trigger.getAttribute('data-running')).toBe('2')
    expect(trigger.getAttribute('data-failed')).toBe('1')
    expect(within(trigger).getByText('2 运行中')).toBeInTheDocument()
    expect(within(screen.getByTestId('chat-subagent-failed')).getByText('1 失败')).toBeInTheDocument()
  })

  it('merges the stored list with the live one by agent id and puts live work first', () => {
    const merged = mergeSubAgents(
      [rail('done', 'completed', 1), rail('live', 'running', 2)],
      [{ ...base, id: 'live', status: 'completed' }],
    )
    expect(merged.map((agent) => agent.id)).toEqual(['done', 'live'])
    // The live copy is fresher, and the stored turn survives the merge.
    expect(merged[1].status).toBe('completed')
    expect(merged[1].turn).toBe(2)
  })

  it('keeps an agent the rail has not seen yet', () => {
    const merged = mergeSubAgents([], [{ ...base, id: 'fresh', status: 'running' }])
    expect(merged).toHaveLength(1)
    expect(merged[0].turn).toBe(0)
  })
})
