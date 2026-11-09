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

describe('global search dialog', () => {
  it('opens, filters, closes on Escape and returns focus', async () => {
    const user = userEvent.setup()
    renderAt('/')
    const trigger = screen.getByRole('button', { name: '搜索' })
    await user.click(trigger)
    const dialog = screen.getByRole('dialog', { name: '全局搜索' })
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    const input = within(dialog).getByRole('searchbox')
    await user.type(input, '设置')
    expect(within(dialog).getByRole('button', { name: /设置/ })).toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '全局搜索' })).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(trigger))
  })

  it('navigates to a search result', async () => {
    const user = userEvent.setup()
    renderAt('/')
    await user.click(screen.getByRole('button', { name: '搜索' }))
    const dialog = screen.getByRole('dialog', { name: '全局搜索' })
    await user.type(within(dialog).getByRole('searchbox'), '设置')
    await user.click(within(dialog).getByRole('button', { name: /设置/ }))
    await waitFor(() => expect(screen.getByTestId('page-placeholder')).toHaveAttribute('data-route', '/settings'))
  })

  it('traps Tab inside the modal', async () => {
    const user = userEvent.setup()
    renderAt('/')
    await user.click(screen.getByRole('button', { name: '搜索' }))
    const dialog = screen.getByRole('dialog', { name: '全局搜索' })
    for (let i = 0; i < 5; i += 1) {
      await user.tab()
      expect(dialog.contains(document.activeElement)).toBe(true)
    }
  })
})

describe('workbench i18n', () => {
  it('renders English normal state without Chinese residue', async () => {
    window.localStorage.setItem('anvil.language', 'en-US')
    renderAt('/')
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Agent activity' })).toBeInTheDocument())
    expect(screen.getByRole('heading', { name: 'Continue conversations' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'New chat' })).toBeDisabled()
    expect(document.documentElement.lang).toBe('en-US')
    expect(document.body.textContent).not.toMatch(/[\u4e00-\u9fff]/)
  })
})
