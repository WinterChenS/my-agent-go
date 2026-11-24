import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { LanguageProvider } from '../../../i18n/LanguageProvider'
import type { LanguageEnvironment } from '../../../i18n/language'
import { Markdown } from '../Markdown'

afterEach(() => cleanup())

const environment: LanguageEnvironment = {
  initialLanguage: () => 'zh-CN',
  storage: { load: async () => undefined, save: async () => undefined },
  apply: () => undefined,
}

function renderMarkdown(
  text: string,
  options: { sources?: number; onCite?: () => void } = {},
): ReturnType<typeof render> {
  return render(
    <LanguageProvider environment={environment}>
      <Markdown text={text} sources={options.sources ?? 0} onCite={options.onCite} />
    </LanguageProvider>,
  )
}

describe('markdown transcript', () => {
  it('renders headings, emphasis, inline code and lists as real elements', () => {
    renderMarkdown('# 标题\n\n**加粗** 与 *斜体* 和 `code`\n\n- 一\n- 二\n\n1. 甲\n2. 乙')
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('标题')
    expect(screen.getByText('加粗').tagName).toBe('STRONG')
    expect(screen.getByText('斜体').tagName).toBe('EM')
    expect(screen.getByText('code').tagName).toBe('CODE')
    expect(screen.getAllByRole('list')).toHaveLength(2)
    expect(screen.getAllByRole('listitem')).toHaveLength(4)
  })

  it('renders GFM tables, task lists, strikethrough and blockquotes', () => {
    renderMarkdown(
      '| 列 A | 列 B |\n| --- | --- |\n| 1 | 2 |\n\n- [x] 完成\n- [ ] 待办\n\n~~删掉~~\n\n> 引用',
    )
    expect(screen.getByRole('table')).toBeInTheDocument()
    expect(screen.getAllByRole('columnheader')).toHaveLength(2)
    expect(screen.getAllByRole('checkbox')).toHaveLength(2)
    expect(screen.getByText('删掉').tagName).toBe('DEL')
    expect(screen.getByText('引用').closest('blockquote')).not.toBeNull()
    expect(document.querySelector('.chat-table-wrap')).not.toBeNull()
  })

  it('wraps fenced code with its language and a working copy button', async () => {
    const writeText = vi.fn(async () => undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    const { container } = renderMarkdown('```ts\nconst a = 1\n```')
    expect(container.querySelector('pre > code')).toHaveClass('language-ts')
    expect(screen.getByText('ts')).toBeInTheDocument()
    const copy = screen.getByRole('button', { name: '复制代码' })
    await userEvent.click(copy)
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('const a = 1\n'))
    await waitFor(() => expect(screen.getByText('已复制')).toBeInTheDocument())
  })

  it('keeps raw HTML and dangerous URLs inert', () => {
    renderMarkdown('<script>alert(1)</script>\n\n[点我](javascript:alert(1))')
    expect(document.querySelector('script')).toBeNull()
    expect(screen.getByText('点我').getAttribute('href')).toBe('')
  })

  it('still turns [n] markers into source buttons without touching unknown ones', async () => {
    const onCite = vi.fn()
    renderMarkdown('见 [1] 与 [2]，但 [9] 保持原样', { sources: 2, onCite })
    const cites = screen.getAllByTestId('chat-cite')
    expect(cites).toHaveLength(2)
    expect(screen.getByText(/但/)).toHaveTextContent('[9]')
    await userEvent.click(cites[0])
    expect(onCite).toHaveBeenCalledTimes(1)
  })

  it('leaves plain text readable and does not invent markup', () => {
    renderMarkdown('Anvil 体验包（Windows x64，免安装）· 2026-11-15')
    expect(screen.getByText('Anvil 体验包（Windows x64，免安装）· 2026-11-15')).toBeInTheDocument()
    expect(document.querySelector('h1')).toBeNull()
  })

  it('hands http links to the host so the reader\'s own browser opens them', async () => {
    const invoke = vi.fn(async () => undefined)
    ;(window as unknown as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__ = { invoke }
    try {
      renderMarkdown('[查文档](https://example.com/docs)')
      const link = screen.getByText('查文档')
      await userEvent.click(link)
      expect(invoke).toHaveBeenCalledWith('open_external', { url: 'https://example.com/docs' })
    } finally {
      delete (window as unknown as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__
    }
  })
})
