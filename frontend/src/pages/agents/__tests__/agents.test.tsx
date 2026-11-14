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

describe('agents normal entry', () => {
  it('renders the live binding manager and is honest without the desktop host', async () => {
    renderAt('/agents')
    const page = screen.getByTestId('agents-page')
    expect(page).toHaveAttribute('data-mode', 'live')
    expect(screen.getByRole('heading', { name: 'Agent' })).toBeInTheDocument()
    // No host, no runtimes: the page says so instead of offering a dead form.
    await waitFor(() => expect(page).toHaveTextContent('当前没有连接桌面宿主'))
    expect(screen.getByRole('button', { name: '新建 Agent' })).toBeDisabled()
    expect(screen.queryByTestId('sample-banner')).toBeNull()
    for (const fixture of ['迁移工程师', '数据分析师', 'claude-sonnet-4.5', 'migration-engineer']) {
      expect(screen.queryByText(fixture), `fixture ${fixture}`).toBeNull()
    }
  })

  it('keeps the live manager honest in English', async () => {
    window.localStorage.setItem('anvil.language', 'en-US')
    renderAt('/agents')
    await waitFor(() => expect(document.documentElement.lang).toBe('en-US'))
    expect(screen.getByRole('heading', { name: 'Agents' })).toBeInTheDocument()
    await waitFor(() =>
      expect(screen.getByTestId('agents-page')).toHaveTextContent('The desktop host is not connected'),
    )
    expect(screen.getByRole('button', { name: 'New agent' })).toBeDisabled()
  })
})

describe('agents sample entry', () => {
  it('renders both groups, the selected detail and five tabs', () => {
    renderAt('/?sample=agents')
    expect(screen.getByTestId('sample-banner')).toHaveTextContent('原型样例数据')
    const list = screen.getByTestId('agents-list')
    expect(within(list).getByText('主 Agent · 4')).toBeInTheDocument()
    expect(within(list).getByText('子 Agent · 2')).toBeInTheDocument()
    expect(within(list).getAllByTestId('agent-item')).toHaveLength(6)
    expect(within(list).getByText('迁移工程师')).toBeInTheDocument()
    expect(within(list).getByText('数据分析师')).toBeInTheDocument()
    expect(within(list).getByText('研究员')).toBeInTheDocument()
    expect(within(list).getByText('文档助手')).toBeInTheDocument()
    expect(within(list).getByText('代码审计员')).toBeInTheDocument()
    expect(within(list).getByText('测试生成器')).toBeInTheDocument()
    expect(within(list).getAllByText('只读')).toHaveLength(2)
    expect(within(list).getAllByText(/委派自 迁移工程师/)).toHaveLength(2)

    const detail = screen.getByTestId('agents-detail')
    expect(within(detail).getByRole('heading', { name: '迁移工程师' })).toBeInTheDocument()
    expect(within(detail).getAllByText('负责代码库迁移、依赖升级与兼容性评估。').length).toBeGreaterThan(0)
    expect(within(detail).getAllByText('Pi Agent').length).toBeGreaterThan(0)
    expect(within(detail).getAllByText('claude-sonnet-4.5').length).toBeGreaterThan(0)
    expect(within(detail).getByText('思考 · 中')).toBeInTheDocument()
    expect(within(detail).getByText('权限 · 中风险确认')).toBeInTheDocument()
    expect(within(detail).getByText('4 skills')).toBeInTheDocument()
    expect(within(detail).getAllByRole('tab')).toHaveLength(5)
  })

  it('switches all five tabs and updates the selected agent', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=agents')
    const detail = screen.getByTestId('agents-detail')
    expect(within(detail).getByText('基础配置')).toBeInTheDocument()
    expect(screen.getByTestId('agent-field-name')).toHaveValue('迁移工程师')
    expect(screen.getByTestId('agent-field-slug')).toHaveValue('migration-engineer')

    await user.click(within(detail).getByRole('tab', { name: '指令与工具' }))
    expect(within(detail).getByText('System prompt')).toBeInTheDocument()
    expect(within(detail).getByText('read_file')).toBeInTheDocument()
    expect(within(detail).getByText('挂载 Skill · 4')).toBeInTheDocument()

    await user.click(within(detail).getByRole('tab', { name: '子 Agent 2' }))
    expect(within(detail).getByText(/子 Agent 继承主 Agent 的权限上限/)).toBeInTheDocument()
    expect(within(detail).getByText('代码审计员')).toBeInTheDocument()
    expect(within(detail).getByText('测试生成器')).toBeInTheDocument()

    await user.click(within(detail).getByRole('tab', { name: '权限边界' }))
    expect(within(detail).getByText('中风险确认')).toBeInTheDocument()
    expect(within(detail).getByText('可写目录')).toBeInTheDocument()

    await user.click(within(detail).getByRole('tab', { name: '运行记录' }))
    expect(within(detail).getByText('API 迁移评估：v1 → v2')).toBeInTheDocument()
    expect(within(detail).getByText('待确认')).toBeInTheDocument()

    const list = screen.getByTestId('agents-list')
    await user.click(within(list).getByRole('button', { name: /数据分析师/ }))
    expect(within(detail).getByRole('heading', { name: '数据分析师' })).toBeInTheDocument()
    expect(within(detail).getAllByText('gpt-5.1').length).toBeGreaterThan(0)
    expect(screen.getByTestId('agent-field-name')).toHaveValue('数据分析师')
  })

  it('filters the list and shows an honest no-match state', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=agents')
    const list = screen.getByTestId('agents-list')
    const filter = within(list).getByTestId('agent-filter')
    await user.type(filter, '数据')
    expect(within(list).getAllByTestId('agent-item')).toHaveLength(1)
    expect(within(list).getByText('数据分析师')).toBeInTheDocument()
    await user.clear(filter)
    await user.type(filter, '不存在')
    expect(within(list).getByText('没有匹配的 Agent')).toBeInTheDocument()
    expect(within(list).queryAllByTestId('agent-item')).toHaveLength(0)
  })

  it('validates the basic form and keeps unconnected actions honest', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=agents')
    const name = screen.getByTestId('agent-field-name')
    const slug = screen.getByTestId('agent-field-slug')
    const save = screen.getByTestId('agent-save')
    await user.clear(name)
    await user.click(save)
    expect(screen.getByTestId('agent-error-name')).toHaveTextContent('请填写 Agent 名称')

    await user.type(name, '迁移工程师')
    await user.clear(slug)
    await user.type(slug, 'Bad Slug!')
    await user.click(save)
    expect(screen.getByTestId('agent-error-slug')).toHaveTextContent('标识只能包含小写字母、数字和连字符')

    await user.clear(slug)
    await user.type(slug, 'migration-engineer')
    await user.click(save)
    expect(screen.getByTestId('agent-notice')).toHaveTextContent('样例模式：保存未接入')

    await user.click(screen.getByRole('button', { name: '发起对话' }))
    expect(screen.getByTestId('agent-notice')).toHaveTextContent('样例模式：发起对话未接入')
  })

  it('toggles the memory and compaction switches with a sample notice', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=agents')
    const memory = screen.getByTestId('agent-switch-memory')
    expect(memory).toHaveAttribute('aria-checked', 'true')
    await user.click(memory)
    expect(memory).toHaveAttribute('aria-checked', 'false')
    expect(screen.getByTestId('agent-notice')).toHaveTextContent('样例模式：设置切换未接入')

    const compact = screen.getByTestId('agent-switch-compact')
    await user.click(compact)
    expect(compact).toHaveAttribute('aria-checked', 'false')
  })

  it('opens and validates the new-agent dialog, then closes with Escape and focus return', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=agents')
    const opener = screen.getByRole('button', { name: '新建 Agent' })
    await user.click(opener)
    let dialog = screen.getByRole('dialog', { name: '新建 Agent' })
    await user.click(within(dialog).getByRole('button', { name: '创建' }))
    expect(within(dialog).getByText('请填写 Agent 名称')).toBeInTheDocument()
    await user.type(within(dialog).getByLabelText('名称'), '发布检查员')
    await user.click(within(dialog).getByRole('button', { name: '创建' }))
    expect(screen.getByTestId('agent-notice')).toHaveTextContent('样例模式：创建未接入')
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '新建 Agent' })).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(opener))
  })

  it('validates and submits the import dialog', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=agents')
    const opener = screen.getByRole('button', { name: '导入' })
    await user.click(opener)
    const dialog = screen.getByRole('dialog', { name: '导入 Agent' })
    await user.click(within(dialog).getByRole('button', { name: '导入' }))
    expect(within(dialog).getByText('请选择要导入的文件')).toBeInTheDocument()
    const file = new File(['{"name":"Imported"}'], 'agent.json', { type: 'application/json' })
    await user.upload(within(dialog).getByLabelText('选择文件'), file)
    expect(within(dialog).getByText('agent.json')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: '导入' }))
    expect(screen.getByTestId('agent-notice')).toHaveTextContent('样例模式：导入未接入')
    await user.keyboard('{Escape}')
    await waitFor(() => expect(document.activeElement).toBe(opener))
  })
})

describe('agents i18n', () => {
  it('translates tabs, fields and validation into English', async () => {
    const user = userEvent.setup()
    window.localStorage.setItem('anvil.language', 'en-US')
    renderAt('/?sample=agents')
    await waitFor(() => expect(document.documentElement.lang).toBe('en-US'))
    const detail = screen.getByTestId('agents-detail')
    expect(within(detail).getByRole('tab', { name: 'Basic configuration' })).toBeInTheDocument()
    expect(within(detail).getByRole('tab', { name: 'Instructions & tools' })).toBeInTheDocument()
    expect(within(detail).getByRole('tab', { name: 'Sub-agents 2' })).toBeInTheDocument()
    expect(within(detail).getByRole('tab', { name: 'Permission boundaries' })).toBeInTheDocument()
    expect(within(detail).getByRole('tab', { name: 'Run history' })).toBeInTheDocument()
    expect(within(detail).getByText('Agent runtime')).toBeInTheDocument()
    expect(screen.getByTestId('agent-save')).toHaveTextContent('Save')
    await user.clear(screen.getByTestId('agent-field-name'))
    await user.click(screen.getByTestId('agent-save'))
    expect(screen.getByTestId('agent-error-name')).toHaveTextContent('Enter an Agent name')
  })
})

describe('agents responsive contract', () => {
  it('declares wide two-column and compact single-column layouts', () => {
    const css = readFileSync(resolve(process.cwd(), 'src/pages/agents/agents.css'), 'utf8')
    expect(css).toContain('grid-template-columns: 268px minmax(0, 1fr)')
    expect(css).toContain('@media (max-width: 900px)')
    expect(css).toContain('@media (max-width: 620px)')
    expect(css).toContain('min-width: 0')
  })
})
