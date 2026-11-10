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

describe('workbench normal entry', () => {
  it('renders the page header, stats, activity, lists, status and actions', async () => {
    renderAt('/')
    expect(screen.getByRole('heading', { level: 1 })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '概览' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Agent 工作活跃度' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '继续对话' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '最近产出' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '我的 Agent' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '今日定时任务' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '运行状态' })).toBeInTheDocument()
    expect(screen.queryByTestId('sample-banner')).toBeNull()
    expect(screen.queryByTestId('nav-count-chat')).toBeNull()
    await waitFor(() => expect(screen.getAllByText('尚未接入运行时').length).toBeGreaterThan(0))
    expect(screen.getByText('尚未接入会话')).toBeInTheDocument()
    expect(screen.getByText('尚未接入产出物')).toBeInTheDocument()
    expect(screen.getByText('尚未接入 Agent')).toBeInTheDocument()
    expect(screen.getByText('尚未接入定时任务')).toBeInTheDocument()
  })

  it('does not show prototype fixtures in the normal entry', () => {
    renderAt('/')
    for (const fixture of ['34', '1,284', '6.2M', '298', '1,489', '陈羽', '迁移工程师', 'v1-to-v2-migration-plan.md']) {
      expect(screen.queryByText(fixture), `fixture ${fixture}`).toBeNull()
    }
  })

  it('disables actions that are not connected', async () => {
    const user = userEvent.setup()
    renderAt('/')
    const newChat = screen.getByRole('button', { name: '新建对话' })
    expect(newChat).toBeDisabled()
    expect(screen.getByRole('button', { name: '配置 Agent' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '全部会话' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '管理' })).toBeDisabled()
    for (const button of screen.getAllByRole('button', { name: '全部' })) expect(button).toBeDisabled()
    await user.click(newChat)
    expect(screen.queryByText(/样例模式/)).toBeNull()
  })
})

describe('workbench sample entry', () => {
  it('shows prototype data only through the isolated sample URL', async () => {
    renderAt('/?sample=workbench')
    expect(screen.getByTestId('sample-banner')).toHaveTextContent('原型样例数据')
    expect(screen.getByTestId('nav-count-chat')).toHaveTextContent('4')
    expect(screen.getByText('34')).toBeInTheDocument()
    expect(screen.getByText('1,284')).toBeInTheDocument()
    expect(screen.getByText('6.2M')).toBeInTheDocument()
    expect(screen.getByText('1,489')).toBeInTheDocument()
    expect(screen.getByText(/陈羽/)).toBeInTheDocument()
    expect(screen.getByText('迁移工程师')).toBeInTheDocument()
    expect(screen.getByText('v1-to-v2-migration-plan.md')).toBeInTheDocument()
    expect(screen.getByText('依赖漏洞扫描')).toBeInTheDocument()
  })

  it('also reaches the sample through the isolated path alias', () => {
    renderAt('/__sample/workbench')
    expect(screen.getByTestId('sample-banner')).toBeInTheDocument()
    expect(screen.getByText('34')).toBeInTheDocument()
  })

  it('never fakes success for sample actions', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=workbench')
    const newChat = screen.getByRole('button', { name: '新建对话' })
    expect(newChat).not.toBeDisabled()
    await user.click(newChat)
    expect(screen.getByTestId('workbench-notice')).toHaveTextContent('样例模式：业务操作未接入')
  })
})

describe('quick jump palette', () => {
  it('opens a flat 14-item listbox with the prototype sub labels', async () => {
    const user = userEvent.setup()
    renderAt('/')
    const trigger = screen.getByRole('button', { name: '搜索' })
    await user.click(trigger)
    const dialog = screen.getByRole('dialog', { name: '快速跳转' })
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    const list = within(dialog).getByRole('listbox', { name: '快速跳转' })
    const options = within(list).getAllByRole('option')
    expect(options).toHaveLength(14)
    expect(options[0]).toHaveAccessibleName(/工作台.*页面/)
    expect(options[0]).toHaveAttribute('aria-selected', 'true')
    expect(options[7]).toHaveAccessibleName(/定时任务.*自动化/)
    expect(options[9]).toHaveAccessibleName(/新建对话.*操作 · 对话/)
    expect(options[10]).toHaveAccessibleName(/新建定时任务.*操作 · 自动化/)
    expect(options[11]).toHaveAccessibleName(/添加 MCP 服务.*操作 · MCP/)
    expect(options[12]).toHaveAccessibleName(/管理本地运行时.*设置/)
    expect(options[13]).toHaveAccessibleName(/Token 用量审计.*设置/)
    expect(within(dialog).getByText('移动')).toBeInTheDocument()
    expect(within(dialog).getByText('打开')).toBeInTheDocument()
    expect(within(dialog).getByText('呼出')).toBeInTheDocument()
    expect(within(dialog).getByText('ESC')).toBeInTheDocument()
  })

  it('moves with arrows, opens with Enter, closes with Escape and returns focus', async () => {
    const user = userEvent.setup()
    renderAt('/')
    const trigger = screen.getByRole('button', { name: '搜索' })
    await user.click(trigger)
    const dialog = screen.getByRole('dialog', { name: '快速跳转' })
    const input = within(dialog).getByRole('combobox')
    await waitFor(() => expect(document.activeElement).toBe(input))
    await user.keyboard('{ArrowDown}')
    expect(within(dialog).getAllByRole('option')[1]).toHaveAttribute('aria-selected', 'true')
    await user.keyboard('{ArrowUp}')
    expect(within(dialog).getAllByRole('option')[0]).toHaveAttribute('aria-selected', 'true')
    await user.keyboard('{ArrowDown}')
    await user.keyboard('{ArrowDown}')
    expect(within(dialog).getAllByRole('option')[2]).toHaveAttribute('aria-selected', 'true')
    await user.keyboard('{Enter}')
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '快速跳转' })).toBeNull())
    await waitFor(() => expect(screen.getByTestId('agents-page')).toBeInTheDocument())
    await waitFor(() => expect(document.activeElement).toBe(trigger))
  })

  it('closes with Escape and returns focus to the toolbar trigger', async () => {
    const user = userEvent.setup()
    renderAt('/')
    const trigger = screen.getByRole('button', { name: '搜索' })
    await user.click(trigger)
    await waitFor(() => expect(screen.getByRole('dialog', { name: '快速跳转' })).toBeInTheDocument())
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '快速跳转' })).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(trigger))
  })

  it('shows the prototype empty result and disables unconnected actions', async () => {
    const user = userEvent.setup()
    renderAt('/')
    const trigger = screen.getByRole('button', { name: '搜索' })
    await user.click(trigger)
    const dialog = screen.getByRole('dialog', { name: '快速跳转' })
    const input = within(dialog).getByRole('combobox')
    await user.type(input, '不存在的关键词')
    expect(within(dialog).getByText('没有匹配的页面或操作')).toBeInTheDocument()
    await user.clear(input)
    const action = within(dialog).getByRole('option', { name: /新建对话/ })
    await user.click(action)
    expect(dialog).toBeInTheDocument()
    expect(within(dialog).getByRole('status')).toHaveTextContent('未接入：该操作将在后续任务开放')
  })

  it('keeps focus in the palette input on Tab', async () => {
    const user = userEvent.setup()
    renderAt('/')
    await user.click(screen.getByRole('button', { name: '搜索' }))
    const dialog = screen.getByRole('dialog', { name: '快速跳转' })
    const input = within(dialog).getByRole('combobox')
    await waitFor(() => expect(document.activeElement).toBe(input))
    await user.tab()
    expect(document.activeElement).toBe(input)
  })
})

describe('workbench i18n', () => {
  it('renders English normal state without Chinese residue', async () => {
    window.localStorage.setItem('anvil.language', 'en-US')
    renderAt('/')
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Agent activity' })).toBeInTheDocument())
    expect(screen.getByRole('heading', { name: 'Continue conversations' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'New chat' })).toBeDisabled()
    await waitFor(() => expect(document.documentElement.lang).toBe('en-US'))
    expect(document.body.textContent).not.toMatch(/[\u4e00-\u9fff]/)
  })
})
