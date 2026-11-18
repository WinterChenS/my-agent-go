import { cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, describe, expect, it } from 'vitest'
import { LanguageProvider } from '../../../i18n/LanguageProvider'
import type { LanguageEnvironment } from '../../../i18n/language'
import type { SessionEvent } from '../../../chat/client'
import { buildTurnParts, type LiveSubAgent } from '../../../chat/useChatSession'
import { SubAgentCard } from '../ChatPage'

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

describe('sub-agent card', () => {
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

  it('shows the agent, its state and its task while collapsed', () => {
    render(wrap(<SubAgentCard agent={agent} />))
    const card = screen.getByTestId('chat-subagent')
    expect(card.getAttribute('data-status')).toBe('running')
    expect(within(card).getByText('scout')).toBeInTheDocument()
    expect(within(card).getByText('gpt-6-astra')).toBeInTheDocument()
    expect(within(card).getByText('运行中')).toBeInTheDocument()
    expect(within(card).getByText('count the rust files')).toBeInTheDocument()
    // The transcript is what the disclosure is for; it stays closed until asked.
    expect(within(card).queryByText('there are 42 files')).toBeNull()
  })

  it('reveals the child transcript on demand, marking tools and reasoning', async () => {
    render(wrap(<SubAgentCard agent={agent} />))
    await userEvent.click(screen.getByRole('button', { name: '展开子 Agent 详情' }))
    expect(screen.getByText('there are 42 files')).toBeInTheDocument()
    expect(screen.getByText('bash')).toBeInTheDocument()
    expect(screen.getByText('42')).toBeInTheDocument()
    expect(screen.getByText('思考')).toBeInTheDocument()
  })

  it('shows the failure reason instead of pretending it finished', () => {
    render(wrap(<SubAgentCard agent={{ ...agent, status: 'failed', error: 'model quota exhausted' }} />))
    const card = screen.getByTestId('chat-subagent')
    expect(within(card).getByText('失败')).toBeInTheDocument()
    expect(within(card).getByText('model quota exhausted')).toBeInTheDocument()
  })

  it('renders the final answer once it exists', () => {
    render(wrap(<SubAgentCard agent={{ ...agent, status: 'completed', result: '42 files under src' }} />))
    expect(screen.getByText('已完成')).toBeInTheDocument()
    expect(screen.getByText('42 files under src')).toBeInTheDocument()
  })
})
