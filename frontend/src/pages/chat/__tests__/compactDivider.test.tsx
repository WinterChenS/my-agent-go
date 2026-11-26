import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { CompactDivider } from '../ChatPage'
import { LanguageProvider } from '../../../i18n/LanguageProvider'
import type { LanguageEnvironment } from '../../../i18n/language'

afterEach(() => cleanup())

const environment: LanguageEnvironment = {
  initialLanguage: () => 'zh-CN',
  storage: { load: async () => undefined, save: async () => undefined },
  apply: () => undefined,
}

const wrap = (node: React.ReactNode) =>
  render(<LanguageProvider environment={environment}>{node}</LanguageProvider>)

describe('the compaction divider', () => {
  it('marks the running state so the ellipsis animates there and nowhere else', () => {
    wrap(<CompactDivider state="running" label="压缩上下文中" detail="正在总结…" testId="row" />)
    const row = screen.getByTestId('row')
    expect(row.className).toContain('is-running')
    expect(row).toHaveTextContent('压缩上下文中')
    expect(row).toHaveTextContent('正在总结…')
  })

  it('says what happened once the host has stored the outcome', () => {
    wrap(
      <CompactDivider
        state="done"
        label="上下文已手动压缩"
        detail="已折叠 4 条较早消息，保留最近 2 条"
        testId="row"
      />,
    )
    const row = screen.getByTestId('row')
    // Same component, same slot: only the class (and so the words) changed,
    // which is what keeps the row from disappearing when compaction finishes.
    expect(row.className).toContain('is-done')
    expect(row.className).not.toContain('is-running')
    expect(row).toHaveTextContent('已折叠 4 条较早消息，保留最近 2 条')
    expect(row.querySelectorAll('.chat-compact-rule')).toHaveLength(2)
  })

  it('keeps a no-op quiet instead of claiming the context was rewritten', () => {
    wrap(
      <CompactDivider
        state="skipped"
        label="无需压缩"
        detail="当前上下文还不到需要压缩的体量"
        testId="row"
      />,
    )
    expect(screen.getByTestId('row').className).toContain('is-skipped')
  })
})
