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

describe('schedules normal entry', () => {
  it('renders zero statistics, no tasks and disabled actions', () => {
    renderAt('/schedules')
    expect(screen.getByTestId('schedules-page')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '定时任务' })).toBeInTheDocument()
    expect(screen.getAllByTestId(/^schedule-stat-/)).toHaveLength(4)
    expect(screen.getByTestId('schedule-stat-enabled')).toHaveTextContent('0')
    expect(screen.getByTestId('schedule-stat-today')).toHaveTextContent('0')
    expect(screen.getByTestId('schedule-stat-success')).toHaveTextContent('—')
    expect(screen.getByTestId('schedule-stat-duration')).toHaveTextContent('—')
    expect(screen.getByTestId('schedule-empty')).toHaveTextContent('尚未接入定时任务')
    expect(screen.getByTestId('schedule-detail-empty')).toHaveTextContent('尚未接入定时任务')
    expect(screen.getByRole('button', { name: '全部暂停' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '新建任务' })).toBeDisabled()
    expect(screen.queryByTestId('schedule-row')).toBeNull()
    for (const fixture of ['每日站会纪要汇总', '6m08s', '98.2%', '0 9 * * 1-5']) {
      expect(screen.queryByText(fixture), `fixture ${fixture}`).toBeNull()
    }
  })

  it('stays empty in English', async () => {
    window.localStorage.setItem('anvil.language', 'en-US')
    renderAt('/schedules')
    await waitFor(() => expect(document.documentElement.lang).toBe('en-US'))
    expect(screen.getByRole('heading', { name: 'Schedules' })).toBeInTheDocument()
    expect(screen.getByTestId('schedule-empty')).toHaveTextContent('No schedules connected yet')
    expect(screen.getByRole('button', { name: 'New schedule' })).toBeDisabled()
  })
})

describe('schedules sample entry', () => {
  it('renders the statistics, six task rows and the selected task detail', () => {
    renderAt('/?sample=schedules')
    expect(screen.getByTestId('sample-banner')).toHaveTextContent('原型样例数据')
    expect(screen.getByTestId('schedule-stat-enabled')).toHaveTextContent('5')
    expect(screen.getByTestId('schedule-stat-today')).toHaveTextContent('11')
    expect(screen.getByTestId('schedule-stat-success')).toHaveTextContent('98.2%')
    expect(screen.getByTestId('schedule-stat-duration')).toHaveTextContent('1m52s')
    const rows = screen.getAllByTestId('schedule-row')
    expect(rows).toHaveLength(6)
    expect(rows[0]).toHaveTextContent('每日站会纪要汇总')
    expect(rows[0]).toHaveTextContent('0 9 * * 1-5')
    expect(rows[0]).toHaveTextContent('文档助手')
    expect(rows[0]).toHaveTextContent('claude-haiku-4.5')
    expect(rows[0]).toHaveTextContent('成功')
    expect(rows[1]).toHaveTextContent('待运行')
    expect(rows[4]).toHaveTextContent('部分失败')
    const detail = screen.getByTestId('schedule-detail')
    expect(detail).toHaveTextContent('每日站会纪要汇总')
    expect(detail).toHaveTextContent('读取「产品/站会」下今天的三份会议记录')
    expect(detail).toHaveTextContent('资源库 / 会议纪要')
    expect(within(detail).getAllByTestId('schedule-runs')).toHaveLength(1)
    expect(detail).toHaveTextContent('今天 09:00')
    expect(screen.getByTestId('schedule-trend')).toHaveTextContent('近 7 日运行')
  })

  it('switches the detail when another row is selected', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=schedules')
    await user.click(screen.getAllByTestId('schedule-row')[1])
    const detail = screen.getByTestId('schedule-detail')
    expect(detail).toHaveTextContent('依赖漏洞扫描')
    expect(detail).toHaveTextContent('安全审计')
    expect(detail).toHaveTextContent('完全控制')
    expect(detail).toHaveTextContent('OSV')
    expect(within(detail).getByText('失败')).toBeInTheDocument()
  })

  it('filters by task name, agent and state', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=schedules')
    const search = screen.getByTestId('schedule-search')
    await user.type(search, '漏洞')
    expect(screen.getAllByTestId('schedule-row')).toHaveLength(1)
    await user.clear(search)
    await user.selectOptions(screen.getByLabelText('执行 Agent'), '研究员')
    expect(screen.getAllByTestId('schedule-row')).toHaveLength(1)
    await user.selectOptions(screen.getByLabelText('执行 Agent'), 'all')
    await user.selectOptions(screen.getByLabelText('状态'), 'paused')
    expect(screen.getAllByTestId('schedule-row')).toHaveLength(1)
    expect(screen.getByTestId('schedule-row')).toHaveTextContent('会话产出归档')
    await user.selectOptions(screen.getByLabelText('状态'), 'failed')
    expect(screen.getAllByTestId('schedule-row')).toHaveLength(2)
  })

  it('flips a task switch and reports that it is only a sample', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=schedules')
    const toggle = screen.getAllByRole('switch')[0]
    expect(toggle).toHaveAttribute('aria-checked', 'true')
    await user.click(toggle)
    expect(toggle).toHaveAttribute('aria-checked', 'false')
    expect(screen.getByTestId('schedule-notice')).toHaveTextContent('样例模式：每日站会纪要汇总 的启停未接入')
  })

  it('opens the new-task dialog, validates name and cron, and creates nothing', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=schedules')
    const opener = screen.getByTestId('schedule-new')
    await user.click(opener)
    const dialog = screen.getByRole('dialog', { name: '新建定时任务' })
    expect(within(dialog).getByText('留空时使用 Agent 的默认任务模板。')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: '创建任务' }))
    expect(within(dialog).getByRole('alert')).toHaveTextContent('请填写任务名称')
    await user.type(within(dialog).getByLabelText('任务名称'), '定价巡检')
    const cron = within(dialog).getByLabelText('计划（cron）')
    await user.clear(cron)
    await user.type(cron, '0 9 *')
    await user.click(within(dialog).getByRole('button', { name: '创建任务' }))
    expect(within(dialog).getByRole('alert')).toHaveTextContent('请填写 5 段 cron 表达式')
    await user.clear(cron)
    await user.type(cron, '0 9 * * 1-5')
    await user.click(within(dialog).getByRole('button', { name: '创建任务' }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '新建定时任务' })).toBeNull())
    expect(screen.getByTestId('schedule-notice')).toHaveTextContent('样例模式：创建任务未接入')

    await user.click(opener)
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '新建定时任务' })).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(opener))
  })

  it('translates statistics, table headers and run labels', async () => {
    window.localStorage.setItem('anvil.language', 'en-US')
    renderAt('/?sample=schedules')
    await waitFor(() => expect(document.documentElement.lang).toBe('en-US'))
    expect(screen.getByTestId('schedule-stat-enabled')).toHaveTextContent('Enabled')
    expect(screen.getByTestId('schedule-list')).toHaveTextContent('Last run')
    expect(screen.getAllByTestId('schedule-row')[0]).toHaveTextContent('Succeeded')
    expect(screen.getByTestId('schedule-detail')).toHaveTextContent('Run now')
  })
})

describe('schedules responsive contract', () => {
  it('declares a wide split, a single column below 1180 and scrollable tables', () => {
    const css = readFileSync(resolve(process.cwd(), 'src/pages/schedules/schedules.css'), 'utf8')
    expect(css).toContain('grid-template-columns: minmax(0, 1.6fr) minmax(0, 1fr)')
    expect(css).toContain('@media (max-width: 1180px)')
    expect(css).toContain('@media (max-width: 620px)')
    expect(css).toContain('overflow-x: auto')
    expect(css).toContain('min-width: 0')
  })
})
