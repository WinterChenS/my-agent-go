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

describe('skills normal entry', () => {
  it('renders empty list/detail, three tabs and disabled actions without fixtures', () => {
    renderAt('/skills')
    expect(screen.getByTestId('skills-page')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Skill' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '导入 Skill' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '新建 Skill' })).toBeDisabled()
    expect(screen.getByTestId('skill-empty-list')).toHaveTextContent('尚未接入 Skill')
    expect(screen.getByTestId('skill-empty-detail')).toHaveTextContent('尚未接入 Skill 详情')
    expect(screen.getByRole('tab', { name: '已安装' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: '市场' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: '草稿' })).toBeInTheDocument()
    expect(screen.getByTestId('skill-save')).toBeDisabled()
    expect(screen.getByTestId('skill-enable')).toBeDisabled()
    expect(screen.getByTestId('skill-test-run')).toBeDisabled()
    expect(screen.queryByTestId('sample-banner')).toBeNull()
    for (const fixture of ['代码审查', '周报生成', 'v2.3', '9 个已安装', '97%', 'handoff-report']) {
      expect(screen.queryByText(fixture), `fixture ${fixture}`).toBeNull()
    }
  })

  it('shows no fixed counts and keeps actions unavailable in English', async () => {
    window.localStorage.setItem('anvil.language', 'en-US')
    renderAt('/skills')
    await waitFor(() => expect(document.documentElement.lang).toBe('en-US'))
    expect(screen.getByRole('heading', { name: 'Skills' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Installed' })).toBeInTheDocument()
    expect(screen.getByTestId('skill-empty-list')).toHaveTextContent('No Skills connected yet')
    expect(screen.getByRole('button', { name: 'Import Skill' })).toBeDisabled()
  })
})

describe('skills sample entry', () => {
  it('renders the three counted tabs and the installed list', () => {
    renderAt('/?sample=skills')
    expect(screen.getByTestId('sample-banner')).toHaveTextContent('原型样例数据')
    expect(screen.getByRole('tab', { name: '已安装 9' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: '市场' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: '草稿 2' })).toBeInTheDocument()
    expect(screen.getAllByTestId('skill-item')).toHaveLength(9)
    expect(screen.getAllByText('代码审查').length).toBeGreaterThan(0)
    expect(screen.getAllByText('周报生成').length).toBeGreaterThan(0)
    expect(screen.getByText('v2.3 · 内置')).toBeInTheDocument()

    const detail = screen.getByTestId('skill-detail')
    expect(within(detail).getByRole('heading', { name: '代码审查' })).toBeInTheDocument()
    expect(within(detail).getByText('97%')).toBeInTheDocument()
    expect(within(detail).getAllByTestId('skill-workflow-step').length).toBeGreaterThanOrEqual(4)
    expect(within(detail).getByTestId('skill-checks')).toHaveTextContent('SKILL.md 结构完整')
    expect(within(detail).getByTestId('skill-checks')).toHaveTextContent('依赖均已授权')
  })

  it('switches tabs, selects market/drafts and shows their content', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=skills')
    await user.click(screen.getByRole('tab', { name: '市场' }))
    expect(screen.getAllByTestId('skill-item')).toHaveLength(6)
    expect(screen.getAllByText('SQL 优化').length).toBeGreaterThan(0)
    expect(screen.getByText('v2.1 · 官方')).toBeInTheDocument()
    await user.click(screen.getByRole('tab', { name: '草稿 2' }))
    expect(screen.getAllByTestId('skill-item')).toHaveLength(2)
    expect(screen.getAllByText('值班交接单').length).toBeGreaterThan(0)
    expect(screen.getAllByText('API 变更通知').length).toBeGreaterThan(0)
    await user.click(screen.getByRole('tab', { name: '已安装 9' }))
    expect(screen.getByText('票单分诊')).toBeInTheDocument()
  })

  it('expands a workflow step and runs both success and failure test states', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=skills')
    const detail = screen.getByTestId('skill-detail')
    const step = within(detail).getAllByTestId('skill-workflow-step')[0]
    expect(step).toHaveAttribute('aria-expanded', 'false')
    await user.click(within(step).getByRole('button'))
    expect(step).toHaveAttribute('aria-expanded', 'true')
    expect(step).toHaveTextContent('读取今日告警与处理状态')

    await user.click(within(detail).getByRole('button', { name: '运行测试' }))
    await waitFor(() => expect(within(detail).getByTestId('skill-test-success')).toBeInTheDocument())
    expect(within(detail).getByTestId('skill-enable-after-test')).not.toBeDisabled()
    await user.click(within(detail).getByTestId('skill-enable-after-test'))
    expect(screen.getByTestId('skill-notice')).toHaveTextContent('样例模式：启用未接入')

    await user.click(within(detail).getByRole('button', { name: '模拟失败' }))
    expect(within(detail).getByTestId('skill-test-failure')).toHaveAttribute('role', 'alert')
    expect(within(detail).getByTestId('skill-enable-after-test')).toBeDisabled()
  })

  it('filters the list and reports an empty result', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=skills')
    const search = screen.getByTestId('skill-search')
    await user.type(search, '周报')
    expect(screen.getAllByTestId('skill-item')).toHaveLength(1)
    expect(screen.getAllByText('周报生成').length).toBeGreaterThan(0)
    await user.clear(search)
    await user.type(search, '不存在')
    expect(screen.getByText('没有匹配的 Skill')).toBeInTheDocument()
    expect(screen.queryAllByTestId('skill-item')).toHaveLength(0)
  })

  it('walks the create flow with three stages and validates each step', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=skills')
    const opener = screen.getByRole('button', { name: '新建 Skill' })
    await user.click(opener)
    const dialog = screen.getByRole('dialog', { name: '创建可复用能力' })
    expect(within(dialog).getByText('第 1 / 3 步')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: '继续' }))
    expect(within(dialog).getByText('请填写 Skill 名称')).toBeInTheDocument()
    await user.type(within(dialog).getByLabelText('名称'), '值班交接单')
    await user.type(within(dialog).getByLabelText('标识'), 'Handoff Report!')
    await user.click(within(dialog).getByRole('button', { name: '继续' }))
    expect(within(dialog).getByText('标识只能包含小写字母、数字和连字符')).toBeInTheDocument()
    await user.clear(within(dialog).getByLabelText('标识'))
    await user.type(within(dialog).getByLabelText('标识'), 'handoff-report')
    await user.type(within(dialog).getByLabelText('版本'), 'v1')
    await user.click(within(dialog).getByRole('button', { name: '继续' }))
    expect(within(dialog).getByText('版本格式应为 v1.2.3')).toBeInTheDocument()
    await user.clear(within(dialog).getByLabelText('版本'))
    await user.type(within(dialog).getByLabelText('版本'), 'v1.0.0')
    await user.click(within(dialog).getByRole('button', { name: '继续' }))
    expect(within(dialog).getByText('第 2 / 3 步')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: '继续' }))
    expect(within(dialog).getByText('请填写至少一个触发条件')).toBeInTheDocument()
    await user.type(within(dialog).getByLabelText('触发条件'), '用户提到交接时')
    await user.click(within(dialog).getByRole('button', { name: '继续' }))
    expect(within(dialog).getByText('第 3 / 3 步')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: '上一步' }))
    expect(within(dialog).getByText('第 2 / 3 步')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: '继续' }))
    await user.click(within(dialog).getByRole('button', { name: '发布 Skill' }))
    expect(screen.getByTestId('skill-notice')).toHaveTextContent('样例模式：发布未接入')
    await user.keyboard('{Escape}')
    await waitFor(() => expect(document.activeElement).toBe(opener))
  })

  it('validates URL and file import tabs and closes with focus return', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=skills')
    const opener = screen.getByRole('button', { name: '导入 Skill' })
    await user.click(opener)
    const dialog = screen.getByRole('dialog', { name: '导入 Skill' })
    expect(within(dialog).getByRole('tab', { name: '从链接' })).toHaveAttribute('aria-selected', 'true')
    await user.click(within(dialog).getByRole('button', { name: '交给 Agent' }))
    expect(within(dialog).getByText('请填写 Skill 链接')).toBeInTheDocument()
    await user.type(within(dialog).getByLabelText('Skill 链接'), 'not-a-url')
    await user.click(within(dialog).getByRole('button', { name: '交给 Agent' }))
    expect(within(dialog).getByText('请输入有效的 http/https 链接')).toBeInTheDocument()
    await user.clear(within(dialog).getByLabelText('Skill 链接'))
    await user.type(within(dialog).getByLabelText('Skill 链接'), 'https://github.com/owner/repo')
    await user.click(within(dialog).getByRole('button', { name: '交给 Agent' }))
    expect(screen.getByTestId('skill-notice')).toHaveTextContent('样例模式：导入未接入')

    await user.click(within(dialog).getByRole('tab', { name: '从文件' }))
    await user.click(within(dialog).getByRole('button', { name: '导入' }))
    expect(within(dialog).getByText('请选择要导入的文件')).toBeInTheDocument()
    await user.upload(within(dialog).getByLabelText('选择文件'), new File(['# skill'], 'SKILL.md', { type: 'text/markdown' }))
    await user.click(within(dialog).getByRole('button', { name: '导入' }))
    expect(screen.getByTestId('skill-notice')).toHaveTextContent('样例模式：导入未接入')
    await user.keyboard('{Escape}')
    await waitFor(() => expect(document.activeElement).toBe(opener))
  })

  it('translates tabs, checks and validation into English', async () => {
    const user = userEvent.setup()
    window.localStorage.setItem('anvil.language', 'en-US')
    renderAt('/?sample=skills')
    await waitFor(() => expect(document.documentElement.lang).toBe('en-US'))
    expect(screen.getByRole('tab', { name: 'Installed 9' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Market' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Drafts 2' })).toBeInTheDocument()
    const detail = screen.getByTestId('skill-detail')
    expect(within(detail).getByText('SKILL.md structure is valid')).toBeInTheDocument()
    expect(within(detail).getByText('Dependencies are authorized')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'New Skill' }))
    const dialog = screen.getByRole('dialog', { name: 'Create reusable capability' })
    await user.click(within(dialog).getByRole('button', { name: 'Continue' }))
    expect(within(dialog).getByText('Enter a Skill name')).toBeInTheDocument()
  })
})

describe('skills responsive contract', () => {
  it('declares wide two-column and compact single-column layouts', () => {
    const css = readFileSync(resolve(process.cwd(), 'src/pages/skills/skills.css'), 'utf8')
    expect(css).toContain('grid-template-columns: 360px minmax(0, 1fr)')
    expect(css).toContain('@media (max-width: 900px)')
    expect(css).toContain('@media (max-width: 620px)')
    expect(css).toContain('min-width: 0')
  })
})
