import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { AppRoutes } from '../../../App'
import { SETTINGS_SECTIONS } from '../SettingsPage'

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

describe('settings sections', () => {
  it('declares the five prototype sections in order', () => {
    expect(SETTINGS_SECTIONS).toEqual(['general', 'models', 'runtime', 'observability', 'security'])
  })

  it('renders the general pane with the real theme and language controls', () => {
    renderAt('/settings')
    expect(screen.getByTestId('settings-page')).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1, name: '系统设置' })).toBeInTheDocument()
    expect(screen.getByTestId('settings-workspace')).toBeInTheDocument()
    expect(screen.getByTestId('theme-status')).toBeInTheDocument()
    expect(screen.getByTestId('language-status')).toBeInTheDocument()
    expect(screen.getByTestId('settings-notifications-empty')).toHaveTextContent('通知通道尚未接入（G1）')
    expect(screen.getByLabelText('本地工作区名称')).toBeDisabled()
    expect(screen.queryByTestId('settings-provider-row')).toBeNull()
  })

  it('navigates between sections and updates the URL hash', async () => {
    const user = userEvent.setup()
    renderAt('/settings')
    await user.click(screen.getByTestId('settings-nav-models'))
    await waitFor(() => expect(screen.getByTestId('settings-models-empty')).toBeInTheDocument())
    expect(screen.getByTestId('settings-nav-models')).toHaveAttribute('aria-current', 'true')
    await user.click(screen.getByTestId('settings-nav-runtime'))
    expect(screen.getByTestId('settings-runtime-empty')).toBeInTheDocument()
    await user.click(screen.getByTestId('settings-nav-observability'))
    expect(screen.getByTestId('settings-usage-empty')).toBeInTheDocument()
    await user.click(screen.getByTestId('settings-nav-security'))
    expect(screen.getByTestId('settings-security-empty')).toBeInTheDocument()
  })

  it('opens a section from a deep link', async () => {
    renderAt('/settings#models')
    expect(screen.getByTestId('settings-models-empty')).toBeInTheDocument()
    expect(screen.getByTestId('settings-nav-models')).toHaveAttribute('aria-current', 'true')
  })

  it('filters the section list and shows an empty state', async () => {
    const user = userEvent.setup()
    renderAt('/settings')
    await user.type(screen.getByTestId('settings-nav-search'), '模型')
    expect(screen.getByTestId('settings-nav-models')).toBeInTheDocument()
    expect(screen.queryByTestId('settings-nav-runtime')).toBeNull()
    await user.type(screen.getByTestId('settings-nav-search'), '不存在')
    expect(screen.getByTestId('settings-nav-empty')).toHaveTextContent('没有匹配的设置分区')
  })

  it('switches the interface language from the general pane', async () => {
    const user = userEvent.setup()
    renderAt('/settings')
    await user.selectOptions(screen.getByLabelText('界面语言'), 'en-US')
    await waitFor(() => expect(document.documentElement.lang).toBe('en-US'))
    expect(screen.getByRole('heading', { level: 1, name: 'System settings' })).toBeInTheDocument()
    expect(screen.getByTestId('settings-notifications-empty')).toHaveTextContent('Notification channels are not connected')
  })

  it('reports sample notices instead of pretending to save', async () => {
    const user = userEvent.setup()
    renderAt('/settings')
    await user.click(screen.getByRole('button', { name: '保存更改' }))
    expect(screen.getByTestId('settings-notice')).toHaveTextContent('样例模式：保存设置未接入')
  })
})

describe('settings sample entry', () => {
  it('renders the provider table, routing defaults and the runtime cards', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=settings')
    expect(screen.getByTestId('sample-banner')).toHaveTextContent('原型样例数据')
    expect(screen.getByLabelText('本地工作区名称')).toHaveValue('Anvil 个人工作区')
    await user.click(screen.getByTestId('settings-nav-models'))
    expect(screen.getAllByTestId('settings-provider-row')).toHaveLength(3)
    expect(screen.getByTestId('settings-models')).toHaveTextContent('gpt-5.1')
    expect(screen.getByTestId('settings-models')).toHaveTextContent('sk-proj-••••7K2P')
    await user.type(screen.getByLabelText('搜索供应商或模型'), 'gemini')
    expect(screen.getAllByTestId('settings-provider-row')).toHaveLength(1)
    await user.clear(screen.getByLabelText('搜索供应商或模型'))
    await user.type(screen.getByLabelText('搜索供应商或模型'), '不存在')
    expect(screen.getByText('没有匹配的供应商或模型')).toBeInTheDocument()

    await user.click(screen.getByTestId('settings-nav-runtime'))
    expect(screen.getAllByTestId('settings-runtime-card')).toHaveLength(3)
    expect(screen.getByTestId('settings-runtime')).toHaveTextContent('pi · v0.48.2')
  })

  it('keeps the isolated sample entry when switching sections', async () => {
    const user = userEvent.setup()
    renderAt('/__sample/settings')
    expect(screen.getByTestId('settings-page')).toHaveAttribute('data-sample', 'true')
    await user.click(screen.getByTestId('settings-nav-models'))
    // The section switch must not drop back to the normal entry, which would
    // hide the fixtures the native case asserts against.
    expect(screen.getByTestId('settings-page')).toHaveAttribute('data-sample', 'true')
    expect(screen.getAllByTestId('settings-provider-row')).toHaveLength(3)
  })

  it('changes the default permission mode and reports it', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=settings#runtime')
    expect(screen.getByTestId('settings-permission-summary')).toHaveTextContent('中风险与高风险确认')
    await user.click(screen.getByRole('radio', { name: /完全控制/ }))
    expect(screen.getByTestId('settings-permission-summary')).toHaveTextContent('完全控制')
    expect(screen.getByTestId('settings-notice')).toHaveTextContent('样例模式：默认权限切换为「完全控制」未接入')
  })

  it('switches the observability tabs and renders their tables', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=settings#observability')
    expect(screen.getByTestId('settings-usage')).toHaveTextContent('6.2M')
    expect(screen.getByTestId('settings-usage')).toHaveTextContent('$58.40')
    await user.click(screen.getByRole('tab', { name: '链路' }))
    expect(screen.getAllByTestId('settings-trace-row')).toHaveLength(4)
    expect(screen.getByTestId('settings-traces')).toHaveTextContent('tr_8f2c91a4')
    await user.click(screen.getByRole('tab', { name: '执行' }))
    expect(screen.getAllByTestId('settings-execution-row')).toHaveLength(4)
    expect(screen.getByTestId('settings-executions')).toHaveTextContent('shell_exec')
  })

  it('renders the security rules, audit rows and environment variables', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=settings#security')
    expect(screen.getByTestId('settings-security-rules')).toHaveTextContent('文件删除与覆盖')
    expect(screen.getByTestId('settings-audit')).toHaveTextContent('敏感参数脱敏')
    expect(screen.getByTestId('settings-data')).toHaveTextContent('清除本地运行数据')
    await user.click(screen.getByTestId('settings-nav-runtime'))
    expect(screen.getAllByTestId('settings-env-row')).toHaveLength(4)
    expect(screen.getByTestId('settings-env')).toHaveTextContent('SENTRY_AUTH_TOKEN')
  })

  it('translates the section list and the provider table', async () => {
    const user = userEvent.setup()
    window.localStorage.setItem('anvil.language', 'en-US')
    renderAt('/?sample=settings#models')
    await waitFor(() => expect(document.documentElement.lang).toBe('en-US'))
    expect(screen.getByTestId('settings-nav-models')).toHaveTextContent('Models')
    expect(screen.getByTestId('settings-models')).toHaveTextContent('Available models')
    expect(screen.getAllByTestId('settings-provider-row')[0]).toHaveTextContent('Available')
    await user.click(screen.getByTestId('settings-nav-security'))
    expect(screen.getByTestId('settings-security-rules')).toHaveTextContent('Risk rules')
  })
})

describe('settings responsive contract', () => {
  it('declares the sidebar layout, its collapse and scrollable tables', () => {
    const css = readFileSync(resolve(process.cwd(), 'src/pages/settings/settings.css'), 'utf8')
    expect(css).toContain('grid-template-columns: 188px minmax(0, 1fr)')
    expect(css).toContain('@media (max-width: 900px)')
    expect(css).toContain('@media (max-width: 620px)')
    expect(css).toContain('overflow-x: auto')
    expect(css).toContain('min-width: 0')
  })

  it('keeps the theme cards inside the general pane', () => {
    renderAt('/settings')
    expect(screen.getByRole('heading', { name: '主题' })).toBeInTheDocument()
    expect(screen.queryByTestId('settings-provider-row')).toBeNull()
  })
})
