import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import type { SessionEvent } from '../../../chat/client'
import { buildTurnParts } from '../../../chat/useChatSession'
import { LanguageProvider } from '../../../i18n/LanguageProvider'
import type { LanguageEnvironment } from '../../../i18n/language'
import { TurnPartsList } from '../ChatPage'
import { SubAgentDrawer, subAgentFromLive } from '../SubAgents'
import type { LiveSubAgent } from '../../../chat/useChatSession'

afterEach(() => cleanup())

const environment: LanguageEnvironment = {
  initialLanguage: () => 'zh-CN',
  storage: { load: async () => undefined, save: async () => undefined },
  apply: () => undefined,
}
const wrap = (node: React.ReactNode) => render(<LanguageProvider environment={environment}>{node}</LanguageProvider>)

const png = 'aGVsbG8=' // "hello" is enough for a src attribute check.
function event(seq: number, kind: SessionEvent['kind'], payload: Record<string, unknown>): SessionEvent {
  return { sessionId: 's', runId: 'r', seq, kind, at: '2026-11-20T00:00:00Z', payload }
}

describe('pictures in the timeline', () => {
  it('attaches a tool image to the tool call it came from', () => {
    const parts = buildTurnParts([
      event(1, 'text_delta', { delta: 'before' }),
      event(2, 'tool_call', { tool: 'bash', args: '{"command":"draw"}' }),
      event(3, 'tool_result', {
        tool: 'bash',
        ok: true,
        output: 'saved chart.png',
        images: [{ data: png, mimeType: 'image/png' }],
      }),
      event(4, 'text_delta', { delta: 'after' }),
    ])
    expect(parts.map((part) => part.kind)).toEqual(['text', 'tool', 'text'])
    const tool = parts[1]
    expect(tool.kind === 'tool' && tool.tool.images).toHaveLength(1)
  })

  it('draws the picture in place, without opening the tool details', () => {
    const parts = buildTurnParts([
      event(1, 'text_delta', { delta: 'before' }),
      event(2, 'tool_call', { tool: 'bash', args: '{ "command": "draw" }' }),
      event(3, 'tool_result', {
        tool: 'bash',
        ok: true,
        images: [{ data: png, mimeType: 'image/png' }],
      }),
      event(4, 'text_delta', { delta: 'after' }),
    ])
    wrap(<TurnPartsList parts={parts} />)
    const images = screen.getByTestId('chat-tool-images')
    const img = images.querySelector('img')
    expect(img).not.toBeNull()
    expect(img?.getAttribute('src')).toBe(`data:image/png;base64,${png}`)
    const cards = [...screen.getByTestId('chat-turn-parts').children]
    expect(cards.map((node) => node.textContent)).toEqual(['before', expect.stringContaining('bash'), 'after'])
  })

  it('shows a child agent picture inside its own panel', () => {
    const agent: LiveSubAgent = {
      id: 'child',
      name: 'scout',
      model: '',
      task: 'draw a chart',
      status: 'running',
      parentId: '',
      step: null,
      steps: [
        { kind: 'text', text: 'drawing', channel: 'answer' },
        { kind: 'image', image: { data: png, mimeType: 'image/png' } },
      ],
      result: '',
      error: '',
      usage: null,
    }
    wrap(<SubAgentDrawer agent={subAgentFromLive(agent)} onClose={() => undefined} onArchive={() => undefined} />)
    const img = screen.getByTestId('chat-subagent-image').querySelector('img')
    expect(img?.getAttribute('src')).toBe(`data:image/png;base64,${png}`)
  })

  it('never builds a data URI from an unknown mime type', async () => {
    const { imageSrc } = await import('../../../chat/image')
    expect(imageSrc({ data: png, mimeType: 'text/html' })).toBe(`data:image/png;base64,${png}`)
  })
})
