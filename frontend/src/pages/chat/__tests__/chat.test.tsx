import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { AppRoutes } from '../../../App'

function renderAt(url: string) {
  return render(
    <MemoryRouter initialEntries={[url]}>
      <AppRoutes />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  window.localStorage.clear()
  document.documentElement.lang = 'zh-CN'
  document.title = ''
})

afterEach(cleanup)

describe('chat normal entry', () => {
  it('renders a real empty state with no fixture conversation', async () => {
    renderAt('/chat')
    expect(screen.getByTestId('chat-page')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: '返回工作台' })).toHaveAttribute('href', '/')
    expect(screen.getByRole('heading', { name: '尚未接入会话' })).toBeInTheDocument()
    expect(screen.getByTestId('chat-empty')).toHaveTextContent('尚未接入运行时/会话')
    expect(screen.queryByTestId('sample-banner')).toBeNull()
    expect(screen.getByTestId('chat-composer')).toBeDisabled()
    expect(screen.getByTestId('chat-send')).toBeDisabled()
    expect(screen.getByTestId('chat-stop')).toBeDisabled()
    expect(screen.getByTestId('chat-history-empty')).toHaveTextContent('尚未接入会话')
    expect(screen.getAllByRole('tab')).toHaveLength(4)
    expect(screen.queryByTestId('nav-count-chat')).toBeNull()
    expect(screen.queryByTestId('nav-tool-meta-library')).toBeNull()
    for (const fixture of [
      'API 迁移评估',
      '迁移工程师',
      '陈羽',
      'gpt-5.1',
      'claude-sonnet-4.5',
      'v1-to-v2-plan.md',
      '1 项待澄清',
      '1 项待授权',
    ]) {
      expect(screen.queryByText(fixture), `fixture ${fixture}`).toBeNull()
    }
  })

  it('keeps the normal shell free of sample navigation counts and metadata', async () => {
    const user = userEvent.setup()
    renderAt('/chat')
    await user.click(screen.getByRole('button', { name: '工具与自动化' }))
    expect(screen.queryByTestId('nav-count-chat')).toBeNull()
    expect(screen.queryByTestId('nav-tool-meta-library')).toBeNull()
    expect(screen.queryByText('28 个输出物')).toBeNull()
    expect(screen.queryByText('9 个已安装')).toBeNull()
    expect(screen.queryByText('3 个服务')).toBeNull()
    expect(screen.queryByText('5 个连接')).toBeNull()
    expect(screen.queryByText('6 个任务')).toBeNull()
  })

  it('keeps history search honest without fixture results', async () => {
    const user = userEvent.setup()
    renderAt('/chat')
    const search = screen.getByTestId('chat-history-search')
    await user.type(search, '迁移')
    expect(screen.getByTestId('chat-history-empty')).toHaveTextContent('没有匹配的会话')
  })
})

describe('chat sample entry', () => {
  it('renders the prototype conversation, exceptions, rail and history', () => {
    renderAt('/?sample=chat')
    expect(screen.getByTestId('sample-banner')).toHaveTextContent('原型样例数据')
    expect(screen.getByRole('heading', { name: 'API 迁移评估：v1 → v2 兼容性' })).toBeInTheDocument()
    expect(screen.getAllByText('my-agent-go-ui').length).toBeGreaterThan(0)
    expect(screen.getAllByText('迁移工程师').length).toBeGreaterThan(0)
    expect(screen.getAllByText('claude-sonnet-4.5').length).toBeGreaterThan(0)
    expect(screen.getByText('1 项待澄清')).toBeInTheDocument()
    expect(screen.getByText('1 项待授权')).toBeInTheDocument()

    expect(screen.getByTestId('chat-message-list')).toBeInTheDocument()
    expect(screen.getAllByTestId('chat-message-user')).toHaveLength(2)
    expect(screen.getAllByTestId('chat-thinking')[0]).toHaveTextContent('已思考 5 步')
    expect(screen.getAllByTestId('chat-tool').length).toBeGreaterThanOrEqual(2)
    expect(screen.getByTestId('chat-turn-artifacts')).toHaveTextContent('callers-inventory.csv')
    expect(screen.getByText('search_files')).toBeInTheDocument()
    expect(screen.getByRole('status', { name: '运行时已从检查点恢复' })).toBeInTheDocument()
    expect(screen.getByRole('alert', { name: '认证失败' })).toBeInTheDocument()
    expect(screen.getByRole('status', { name: '当前上下文接近上限' })).toBeInTheDocument()
    expect(screen.getByRole('status', { name: '生成已由你停止' })).toBeInTheDocument()
    expect(screen.getByRole('alert', { name: '需要确认：写入 1 个文件' })).toBeInTheDocument()
    expect(screen.getByRole('status', { name: 'THIL · 内容确认' })).toBeInTheDocument()

    expect(screen.getAllByRole('tab')).toHaveLength(4)
    expect(screen.getByRole('tab', { name: '浏览器' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: '输出物 3' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: '引用 4' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: '执行' })).toBeInTheDocument()

    expect(screen.getByTestId('chat-history')).toHaveTextContent('历史会话')
    expect(screen.getByTestId('chat-history')).toHaveTextContent('项目会话')
    expect(screen.getByTestId('chat-history')).toHaveTextContent('普通会话')
    expect(screen.getByText('API 迁移评估：v1 → v2')).toBeInTheDocument()
    expect(screen.getByText('周报数据核对与图表生成')).toBeInTheDocument()
  })

  it('reproduces the prototype shell counts and tool metadata only in sample mode', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=chat')
    expect(screen.getByTestId('nav-count-chat')).toHaveTextContent('4')
    await user.click(screen.getByRole('button', { name: '工具与自动化' }))
    expect(screen.getByTestId('nav-tool-meta-library')).toHaveTextContent('28 个输出物')
    expect(screen.getByTestId('nav-tool-meta-skills')).toHaveTextContent('9 个已安装')
    expect(screen.getByTestId('nav-tool-meta-mcp')).toHaveTextContent('3 个服务')
    expect(screen.getByTestId('nav-tool-meta-connectors')).toHaveTextContent('5 个连接')
    expect(screen.getByTestId('nav-tool-meta-schedules')).toHaveTextContent('6 个任务')
  })

  it('filters history, expands projects, paginates and exposes working session actions', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=chat')
    const history = screen.getByTestId('chat-history')
    const search = within(history).getByTestId('chat-history-search')
    await user.type(search, '阅读')
    expect(within(history).getByText('整理本周阅读笔记')).toBeInTheDocument()
    expect(within(history).queryByText('周报数据核对与图表生成')).toBeNull()
    await user.clear(search)
    await user.type(search, '不存在')
    expect(within(history).getByTestId('chat-history-empty')).toHaveTextContent('没有匹配的会话')
    await user.clear(search)

    const project = within(history).getByRole('button', { name: /my-agent-go-ui/ })
    expect(project).toHaveAttribute('aria-expanded', 'true')
    await user.click(project)
    expect(project).toHaveAttribute('aria-expanded', 'false')
    expect(within(history).queryByText('API 迁移评估：v1 → v2')).toBeNull()
    await user.click(project)
    expect(project).toHaveAttribute('aria-expanded', 'true')

    expect(within(history).getByText('继续滚动加载')).toBeInTheDocument()
    await user.click(within(history).getByRole('button', { name: '继续滚动加载' }))
    await user.click(within(history).getByRole('button', { name: '继续滚动加载' }))
    expect(within(history).getByText('已加载全部会话')).toBeInTheDocument()

    const actions = within(history).getAllByRole('button', { name: '会话操作' })[0]
    await user.click(actions)
    // Fixed positioning keeps the popover out of the scrolling list's layout:
    // no clipping and no scrollbar. Assert it against the real stylesheet.
    const menu = screen.getByTestId('chat-history-action-menu')
    const css = readFileSync(resolve(process.cwd(), 'src/pages/chat/chat.css'), 'utf8')
    const ruleStart = css.indexOf('.chat-history-action-menu {')
    const menuRule = css.slice(ruleStart, css.indexOf('}', ruleStart))
    expect(menuRule).toContain('position: fixed')
    expect(menuRule).not.toContain('left:')
    expect(menu.getAttribute('style')).toContain('top')
    expect(within(history).getByRole('menu')).toBeInTheDocument()
    for (const name of ['置顶 / 取消置顶', '重命名', '归档', '分支会话', '压缩上下文', '删除']) {
      expect(within(menu).getByRole('menuitem', { name })).toBeEnabled()
    }
    await user.click(within(menu).getByRole('menuitem', { name: '重命名' }))
    expect(screen.getByTestId('chat-history-notice')).toHaveTextContent('样例模式')
  })

  it('shows a labelled new-chat button and closes the popover on Escape', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=chat')
    const newChat = screen.getByTestId('chat-history-new')
    expect(newChat).toHaveTextContent('新建对话')
    expect(newChat).toBeEnabled()
    await user.click(newChat)
    expect(screen.getByTestId('chat-history-notice')).toBeInTheDocument()

    await user.click(screen.getAllByRole('button', { name: '会话操作' })[0])
    expect(screen.getByTestId('chat-history-action-menu')).toBeInTheDocument()
    await user.keyboard('{Escape}')
    expect(screen.queryByTestId('chat-history-action-menu')).toBeNull()
  })

  it('sends with Enter by default, and with Ctrl+Enter once that is chosen', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=chat')
    const composer = screen.getByTestId('chat-composer')

    // Enter sends; a line break needs Shift+Enter. The sample draft is
    // pre-filled, so the assertions look at what changed, not the whole value.
    await user.click(composer)
    const before = (composer as HTMLTextAreaElement).value
    await user.keyboard('{Shift>}{Enter}{/Shift}')
    const afterBreak = (composer as HTMLTextAreaElement).value
    expect(afterBreak).toBe(`${before}\n`)
    await user.type(composer, '继续')
    await user.keyboard('{Enter}')
    expect(screen.getByTestId('chat-notice')).toHaveTextContent('样例模式')

    cleanup()
    window.localStorage.clear()
    window.localStorage.setItem('anvil.chat.sendShortcut', 'ctrl-enter')
    renderAt('/?sample=chat')
    const composer2 = screen.getByTestId('chat-composer')
    await user.type(composer2, '继续')
    // Plain Enter is a newline in this mode...
    const beforeEnter = (composer2 as HTMLTextAreaElement).value
    await user.keyboard('{Enter}')
    expect((composer2 as HTMLTextAreaElement).value).toBe(`${beforeEnter}\n`)
    expect(screen.queryByTestId('chat-notice')).toBeNull()
    // ...and Ctrl+Enter is the send key.
    await user.keyboard('{Control>}{Enter}{/Control}')
    expect(screen.getByTestId('chat-notice')).toHaveTextContent('样例模式')
  })

  it('toggles thinking and tool cards', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=chat')
    const thinking = screen.getAllByTestId('chat-thinking')[0]
    expect(thinking).toHaveAttribute('aria-expanded', 'false')
    await user.click(within(thinking).getByRole('button'))
    expect(thinking).toHaveAttribute('aria-expanded', 'true')
    await user.click(within(thinking).getByRole('button'))
    expect(thinking).toHaveAttribute('aria-expanded', 'false')
    const tool = screen.getAllByTestId('chat-tool')[0]
    await user.click(within(tool).getByRole('button'))
    expect(tool).toHaveAttribute('aria-expanded', 'true')
  })

  it('handles composer shortcuts, attachment chips and @ mentions without faking a send', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=chat')
    const composer = screen.getByTestId('chat-composer')
    const initialUsers = screen.getAllByTestId('chat-message-user').length
    await user.type(composer, '继续')
    await user.keyboard('{Enter}')
    expect(screen.getByTestId('chat-notice')).toHaveTextContent('样例模式：消息发送未接入')
    expect(screen.getAllByTestId('chat-message-user')).toHaveLength(initialUsers)

    const remove = screen.getByRole('button', { name: '移除 api-client.ts' })
    await user.click(remove)
    expect(screen.queryByText('api-client.ts')).toBeNull()

    await user.type(composer, '@')
    const mentions = screen.getByRole('listbox', { name: '引用文件或 Skill' })
    expect(mentions).toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('listbox', { name: '引用文件或 Skill' })).toBeNull())
  })

  it('opens dialogs with Escape and restores focus to the opener', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=chat')

    const projectTrigger = screen.getByTestId('chat-project-trigger')
    await user.click(projectTrigger)
    let dialog = screen.getByRole('dialog', { name: '为当前对话选择项目' })
    const personal = within(dialog).getByRole('radio', { name: /个人知识库/ })
    await user.click(personal)
    expect(personal).toHaveAttribute('aria-checked', 'true')
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '为当前对话选择项目' })).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(projectTrigger))

    const exportTrigger = screen.getByRole('button', { name: '导出' })
    await user.click(exportTrigger)
    dialog = screen.getByRole('dialog', { name: '导出会话' })
    await user.click(within(dialog).getByRole('button', { name: '导出到本机' }))
    expect(screen.getByTestId('chat-notice')).toHaveTextContent('样例模式：导出未接入')
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '导出会话' })).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(exportTrigger))

    const more = screen.getByRole('button', { name: '更多操作' })
    await user.click(more)
    dialog = screen.getByRole('dialog', { name: '会话操作' })
    for (const label of ['从这条消息分叉新会话', '归档到资源库', '导出为 Markdown', '删除会话']) {
      expect(within(dialog).getByRole('button', { name: label })).toBeDisabled()
    }
    await user.keyboard('{Escape}')
    await waitFor(() => expect(document.activeElement).toBe(more))

    const clarify = screen.getByRole('button', { name: '查看 3 个方案' })
    await user.click(clarify)
    expect(screen.getByRole('dialog', { name: '灰度方案需要你做一个业务判断' })).toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(document.activeElement).toBe(clarify))
  })

  it('switches the four rail tabs and closes the panel with focus return', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=chat')
    const toggle = screen.getByTestId('chat-rail-toggle')
    // Closed by default: the panel is reference material, not the conversation.
    // It stays mounted (the drawer slides), so the open flag is the signal.
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(screen.getByTestId('chat-rail')).not.toHaveClass('is-open')

    await user.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    const rail = screen.getByTestId('chat-rail')
    expect(rail).toHaveClass('is-open')

    await user.click(screen.getByRole('tab', { name: '输出物 3' }))
    expect(screen.getByText('本次会话产出 · 3')).toBeInTheDocument()
    await user.click(screen.getByRole('tab', { name: '引用 4' }))
    expect(screen.getByText('引用来源 · 4')).toBeInTheDocument()
    await user.click(screen.getByRole('tab', { name: '执行' }))
    expect(screen.getByText('本轮执行 · 5 步')).toBeInTheDocument()
    await user.click(screen.getByRole('tab', { name: '浏览器' }))
    expect(screen.getByText('API v1 → v2 迁移计划')).toBeInTheDocument()

    await user.click(within(rail).getByRole('button', { name: '关闭面板' }))
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(rail).not.toHaveClass('is-open')
    await waitFor(() => expect(document.activeElement).toBe(toggle))
    await user.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
  })
})

describe('chat i18n', () => {
  it('switches the normal entry to English without Chinese UI copy', async () => {
    const user = userEvent.setup()
    renderAt('/chat')
    await user.click(screen.getByRole('button', { name: '语言' }))
    await waitFor(() => expect(document.documentElement.lang).toBe('en-US'))
    expect(screen.getByRole('heading', { name: 'No chat connected' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Back to dashboard' })).toBeInTheDocument()
    expect(screen.getByTestId('chat-composer')).toHaveAttribute('placeholder', 'Chat is unavailable until a runtime is connected')
    expect(screen.getByTestId('chat-send')).toHaveAccessibleName('Send')
    expect(screen.getByTestId('chat-history')).toHaveTextContent('History')
    expect(screen.getByTestId('chat-history-empty')).toHaveTextContent('No chats connected yet')
  })

  it('translates sample UI chrome while keeping user content intact', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=chat')
    await user.click(screen.getByRole('button', { name: '语言' }))
    await waitFor(() => expect(document.documentElement.lang).toBe('en-US'))
    expect(screen.getByRole('button', { name: 'Export' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Artifacts 3' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'More actions' })).toBeInTheDocument()
    expect(screen.getAllByText('my-agent-go-ui').length).toBeGreaterThan(0)
    expect(screen.getAllByText('迁移工程师').length).toBeGreaterThan(0)
  })
})

describe('chat responsive contract', () => {
  it('declares the wide rail, narrow drawer and compact breakpoints', () => {
    const css = readFileSync(resolve(process.cwd(), 'src/pages/chat/chat.css'), 'utf8')
    expect(css).toContain('@media (max-width: 1180px)')
    expect(css).toContain('@media (max-width: 900px)')
    expect(css).toContain('@media (max-width: 620px)')
    expect(css).toContain('min-width: 0')
    expect(css).toContain('overflow-x: hidden')
  })
})
