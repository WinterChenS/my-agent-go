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

describe('connectors normal entry', () => {
  it('renders zero statistics, an empty list and disabled actions', () => {
    renderAt('/connectors')
    expect(screen.getByTestId('connectors-page')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '连接器' })).toBeInTheDocument()
    expect(screen.getAllByTestId(/^connector-stat-/)).toHaveLength(4)
    expect(screen.getByTestId('connector-stat-connected')).toHaveTextContent('0')
    expect(screen.getByTestId('connector-stat-actions')).toHaveTextContent('0')
    expect(screen.getByTestId('connector-stat-calls')).toHaveTextContent('0')
    expect(screen.getByTestId('connector-stat-sync')).toHaveTextContent('—')
    expect(screen.getByTestId('connector-empty')).toHaveTextContent('尚未接入连接器')
    expect(screen.getByRole('button', { name: '校验凭证' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '添加连接器' })).toBeDisabled()
    expect(screen.queryByTestId('connector-card')).toBeNull()
    expect(screen.queryByTestId('connector-policy-row')).toBeNull()
    for (const fixture of ['chen.yu@anvil.dev', '钉钉', 'Slack', '1,284', '99.1%']) {
      expect(screen.queryByText(fixture), `fixture ${fixture}`).toBeNull()
    }
  })

  it('stays empty in English', async () => {
    window.localStorage.setItem('anvil.language', 'en-US')
    renderAt('/connectors')
    await waitFor(() => expect(document.documentElement.lang).toBe('en-US'))
    expect(screen.getByRole('heading', { name: 'Connectors' })).toBeInTheDocument()
    expect(screen.getByTestId('connector-empty')).toHaveTextContent('No connectors connected yet')
    expect(screen.getByRole('button', { name: 'Add connector' })).toBeDisabled()
  })
})

describe('connectors sample entry', () => {
  it('renders four statistics, six connectors and the policy/activity/health panels', () => {
    renderAt('/?sample=connectors')
    expect(screen.getByTestId('sample-banner')).toHaveTextContent('原型样例数据')
    expect(screen.getByTestId('connector-stat-connected')).toHaveTextContent('5')
    expect(screen.getByTestId('connector-stat-actions')).toHaveTextContent('28')
    expect(screen.getByTestId('connector-stat-calls')).toHaveTextContent('1,284')
    expect(screen.getByTestId('connector-stat-sync')).toHaveTextContent('2 分钟前')
    const cards = screen.getAllByTestId('connector-card')
    expect(cards).toHaveLength(6)
    expect(cards[0]).toHaveTextContent('邮箱')
    expect(cards[0]).toHaveTextContent('已连接')
    expect(cards[0]).toHaveTextContent('chen.yu@anvil.dev · imap.anvil.dev:993')
    expect(cards[0]).toHaveTextContent('同步 09:41')
    expect(cards[1]).toHaveTextContent('3 个群')
    expect(cards[2]).toHaveTextContent('12 个文档')
    expect(cards[3]).toHaveTextContent('需重新授权')
    expect(cards[3]).toHaveTextContent('令牌已过期 2 天')
    expect(within(cards[3]).getByRole('button', { name: '重新授权' })).toBeInTheDocument()
    expect(cards[4]).toHaveTextContent('4 个数据库')
    expect(cards[5]).toHaveTextContent('未连接')
    expect(cards[5]).toHaveTextContent('连接后可分配 6 个动作')
    expect(screen.getAllByTestId('connector-policy-row')).toHaveLength(6)
    expect(screen.getByTestId('connector-policy')).toHaveTextContent('mail.send_message')
    expect(screen.getByTestId('connector-policy')).toHaveTextContent('高风险确认')
    expect(screen.getByTestId('connector-activity')).toHaveTextContent('Slack 授权失败')
    expect(screen.getByTestId('connector-health')).toHaveTextContent('53 天后续期')
    expect(screen.getByTestId('connector-health')).toHaveTextContent('已过期')
  })

  it('filters by name, scope, category and status', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=connectors')
    const search = screen.getByTestId('connector-search')
    await user.type(search, '钉钉')
    expect(screen.getAllByTestId('connector-card')).toHaveLength(1)
    await user.clear(search)
    await user.type(search, '读取频道')
    expect(screen.getAllByTestId('connector-card')).toHaveLength(1)
    expect(screen.getByTestId('connector-card')).toHaveTextContent('Slack')
    await user.clear(search)
    await user.selectOptions(screen.getByLabelText('类别'), 'im')
    expect(screen.getAllByTestId('connector-card')).toHaveLength(2)
    await user.selectOptions(screen.getByLabelText('状态'), 'connected')
    expect(screen.getAllByTestId('connector-card')).toHaveLength(1)
    expect(screen.getByTestId('connector-card')).toHaveTextContent('钉钉')
    await user.type(search, '不存在')
    expect(screen.getByText('没有匹配的连接器')).toBeInTheDocument()
  })

  it('opens the add dialog, validates credentials and closes with Escape', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=connectors')
    const opener = screen.getByTestId('connector-add')
    await user.click(opener)
    const dialog = screen.getByRole('dialog', { name: '添加连接器' })
    expect(within(dialog).getByText('凭证写入本地钥匙串，仅在运行时注入，不进入会话记录。')).toBeInTheDocument()
    await user.type(within(dialog).getByLabelText('显示名称'), '团队邮箱')
    await user.click(within(dialog).getByRole('button', { name: '添加并验证' }))
    expect(within(dialog).getByText('请填写凭证')).toBeInTheDocument()
    await user.type(within(dialog).getByLabelText('凭证'), 'access_token=secret')
    expect(within(dialog).getByText('只读（搜索与读取）')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: '添加并验证' }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '添加连接器' })).toBeNull())
    expect(screen.getByTestId('connector-notice')).toHaveTextContent('样例模式：添加连接器未接入')

    await user.click(opener)
    expect(screen.getByRole('dialog', { name: '添加连接器' })).toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '添加连接器' })).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(opener))
  })

  it('rejects a missing display name', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=connectors')
    await user.click(screen.getByTestId('connector-add'))
    const dialog = screen.getByRole('dialog', { name: '添加连接器' })
    await user.click(within(dialog).getByRole('button', { name: '添加并验证' }))
    expect(within(dialog).getByRole('alert')).toHaveTextContent('请填写显示名称')
  })

  it('opens connector settings, toggles a switch and reports saves', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=connectors')
    await user.click(screen.getByTestId('connector-action-mail'))
    const dialog = screen.getByRole('dialog', { name: '邮箱' })
    const toggle = within(dialog).getByRole('switch', { name: '发信前确认' })
    expect(toggle).toHaveAttribute('aria-checked', 'true')
    await user.click(toggle)
    expect(toggle).toHaveAttribute('aria-checked', 'false')
    await user.click(within(dialog).getByRole('button', { name: '保存' }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '邮箱' })).toBeNull())
    expect(screen.getByTestId('connector-notice')).toHaveTextContent('样例模式：连接器设置保存未接入')
  })

  it('reports the disconnect action without pretending to succeed', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=connectors')
    await user.click(screen.getByTestId('connector-action-mail'))
    await user.click(screen.getByRole('button', { name: '断开连接' }))
    expect(screen.getByTestId('connector-notice')).toHaveTextContent('样例模式：断开连接未接入')
  })

  it('translates statistics, cards and the policy table', async () => {
    const user = userEvent.setup()
    window.localStorage.setItem('anvil.language', 'en-US')
    renderAt('/?sample=connectors')
    await waitFor(() => expect(document.documentElement.lang).toBe('en-US'))
    expect(screen.getByTestId('connector-stat-connected')).toHaveTextContent('Connected')
    expect(screen.getAllByTestId('connector-card')[0]).toHaveTextContent('Mail')
    expect(screen.getByTestId('connector-policy')).toHaveTextContent('High-risk confirmation')
    await user.click(screen.getByTestId('connector-add'))
    const dialog = screen.getByRole('dialog', { name: 'Add connector' })
    await user.click(within(dialog).getByRole('button', { name: 'Add and verify' }))
    expect(within(dialog).getByRole('alert')).toHaveTextContent('Enter a display name')
  })
})

describe('connectors responsive contract', () => {
  it('declares three, two and one column layouts with scrollable tables', () => {
    const css = readFileSync(resolve(process.cwd(), 'src/pages/connectors/connectors.css'), 'utf8')
    expect(css).toContain('grid-template-columns: repeat(3, minmax(0, 1fr))')
    expect(css).toContain('@media (max-width: 1180px)')
    expect(css).toContain('@media (max-width: 620px)')
    expect(css).toContain('@media (max-width: 900px)')
    expect(css).toContain('min-width: 0')
    expect(css).toContain('overflow-x: auto')
  })
})
