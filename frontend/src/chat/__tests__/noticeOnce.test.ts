import { describe, expect, it } from 'vitest'
import type { SessionEvent } from '../client'
import { buildTurnParts, printedNotices, type TurnPart } from '../useChatSession'

/**
 * A standing notice — pi tells every turn that an MCP server still needs
 * signing in. The id is new each time; the text is the one thing that is the
 * same.
 */
function notice(runId: string, seq: number, id: string, message: string): SessionEvent {
  return {
    sessionId: 'pi-1',
    runId,
    seq,
    kind: 'extension_ui',
    at: '2026-11-20T00:00:00Z',
    payload: { id, sessionId: 'pi-1', method: 'notify', message, notifyType: 'warning' },
  }
}

const mcpWarning = 'MCP servers need attention:\n  box: needs sign-in\nRun /mcp to fix.'

function notices(parts: TurnPart[]) {
  return parts.filter((part) => part.kind === 'extension_ui')
}

describe('a notice the runtime repeats', () => {
  it('prints in the first turn only', () => {
    const shown = new Set<string>()
    const first = buildTurnParts([notice('r1', 1, 'n1', mcpWarning)], 'r1', shown)
    const second = buildTurnParts([notice('r2', 1, 'n2', mcpWarning)], 'r2', shown)
    expect(notices(first)).toHaveLength(1)
    expect(notices(second)).toHaveLength(0)
  })

  it('prints again when the words changed', () => {
    const shown = new Set<string>()
    buildTurnParts([notice('r1', 1, 'n1', mcpWarning)], 'r1', shown)
    const changed = buildTurnParts(
      [notice('r2', 1, 'n2', 'MCP servers need attention:\n  box: needs sign-in\n  slack: needs sign-in')],
      'r2',
      shown,
    )
    expect(notices(changed)).toHaveLength(1)
  })

  it('folds a repeat inside one turn as well', () => {
    const parts = buildTurnParts(
      [notice('r1', 1, 'n1', mcpWarning), notice('r1', 2, 'n2', mcpWarning)],
      'r1',
    )
    expect(notices(parts)).toHaveLength(1)
  })

  it('never folds a question, which blocks the run and is new every time', () => {
    const shown = new Set<string>()
    const ask = (runId: string, id: string): SessionEvent => ({
      sessionId: 'pi-1',
      runId,
      seq: 1,
      kind: 'extension_ui',
      at: '2026-11-20T00:00:00Z',
      payload: { id, sessionId: 'pi-1', method: 'confirm', title: '允许吗', message: '会改动工作区' },
    })
    buildTurnParts([ask('r1', 'q1')], 'r1', shown)
    const again = buildTurnParts([ask('r2', 'q2')], 'r2', shown)
    expect(notices(again)).toHaveLength(1)
    expect(notices(again)[0]).toMatchObject({ kind: 'extension_ui', request: { method: 'confirm' } })
  })

  it('reports what a transcript has already shown, so a live turn can skip it', () => {
    const keys = printedNotices([notice('r1', 1, 'n1', mcpWarning), notice('r1', 2, 'n2', 'another note')])
    const live = buildTurnParts([notice('r2', 1, 'n3', mcpWarning)], 'r2', new Set(keys))
    expect(keys.size).toBe(2)
    expect(notices(live)).toHaveLength(0)
  })
})
