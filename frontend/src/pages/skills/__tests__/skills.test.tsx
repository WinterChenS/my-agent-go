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
  it('renders an empty grid with no fixtures and disabled actions', () => {
    renderAt('/skills')
    expect(screen.getByTestId('skills-page')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Skill' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '导入 Skill' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '新建 Skill' })).toBeDisabled()
    expect(screen.getByTestId('skill-empty-list')).toHaveTextContent('尚未接入 Skill')
    expect(screen.getByRole('tab', { name: '已安装' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: '市场' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: '草稿' })).toBeInTheDocument()
    expect(screen.getByTestId('skill-save')).toBeDisabled()
    expect(screen.getByTestId('skill-enable')).toBeDisabled()
    expect(screen.getByTestId('skill-test-run')).toBeDisabled()
    expect(screen.queryByTestId('sample-banner')).toBeNull()
    expect(screen.queryByTestId('skill-item')).toBeNull()
    for (const fixture of ['代码审查', '周报生成', 'v2.3', '9 个已安装', '97%', 'handoff-report']) {
      expect(screen.queryByText(fixture), `fixture ${fixture}`).toBeNull()
    }
  })

  it('shows no fixed counts in English', async () => {
    window.localStorage.setItem('anvil.language', 'en-US')
    renderAt('/skills')
    await waitFor(() => expect(document.documentElement.lang).toBe('en-US'))
    expect(screen.getByRole('heading', { name: 'Skills' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Installed' })).toBeInTheDocument()
    expect(screen.getByTestId('skill-empty-list')).toHaveTextContent('No Skills connected yet')
    expect(screen.getByRole('button', { name: 'Import Skill' })).toBeDisabled()
  })
})

describe('skills sample grid', () => {
  it('renders counted tabs and prototype card anatomy', () => {
    renderAt('/?sample=skills')
    expect(screen.getByTestId('sample-banner')).toHaveTextContent('原型样例数据')
    expect(screen.getByRole('tab', { name: '已安装 9' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: '市场' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: '草稿 2' })).toBeInTheDocument()
    const cards = screen.getAllByTestId('skill-item')
    expect(cards).toHaveLength(9)
    const review = cards[0]
    expect(within(review).getByText('代码审查')).toBeInTheDocument()
    expect(within(review).getByText('v2.3 · 内置')).toBeInTheDocument()
    expect(within(review).getByText('按 diff 输出风险分级：会断掉、编译期可见、仅命名变化。附最小修复建议。')).toBeInTheDocument()
    expect(within(review).getByText('git diff')).toBeInTheDocument()
    expect(within(review).getByText('调用 128 次')).toBeInTheDocument()
    expect(within(review).getByText('3 天前更新')).toBeInTheDocument()
    expect(within(review).getByRole('switch', { name: '代码审查 启用 Skill' })).toHaveAttribute('aria-checked', 'true')
    expect(within(review).getByRole('button', { name: '更多操作：代码审查' })).toBeInTheDocument()
  })

  it('switches tabs and keeps market/draft card actions', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=skills')
    await user.click(screen.getByRole('tab', { name: '市场' }))
    expect(screen.getAllByTestId('skill-item')).toHaveLength(6)
    expect(screen.getByText('SQL 优化')).toBeInTheDocument()
    expect(screen.getByText('v2.1 · 官方')).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: '获取' })).toHaveLength(6)
    await user.click(screen.getByRole('tab', { name: '草稿 2' }))
    expect(screen.getAllByTestId('skill-item')).toHaveLength(2)
    expect(screen.getByText('值班交接单')).toBeInTheDocument()
    expect(screen.getByText('API 变更通知')).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: '继续编辑' })).toHaveLength(2)
    await user.click(screen.getByRole('tab', { name: '已安装 9' }))
    expect(screen.getByText('票单分诊')).toBeInTheDocument()
  })

  it('filters the grid and reports an empty result', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=skills')
    const search = screen.getByTestId('skill-search')
    await user.type(search, '周报')
    expect(screen.getAllByTestId('skill-item')).toHaveLength(1)
    expect(screen.getByText('周报生成')).toBeInTheDocument()
    await user.clear(search)
    await user.type(search, '不存在')
    expect(screen.getByText('没有匹配的 Skill')).toBeInTheDocument()
    expect(screen.queryAllByTestId('skill-item')).toHaveLength(0)
  })
})

describe('skills builder', () => {
  it('opens a prefilled builder from the card and expands a workflow step', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=skills')
    const opener = screen.getByRole('button', { name: '打开 代码审查' })
    await user.click(opener)
    const dialog = screen.getByRole('dialog', { name: '创建可复用能力' })
    expect(within(dialog).getByText('第 2 / 4 步')).toBeInTheDocument()
    expect(within(dialog).getByLabelText('名称')).toHaveValue('代码审查')
    expect(within(dialog).getByLabelText('标识')).toHaveValue('code-review')
    expect(within(dialog).getByLabelText('版本')).toHaveValue('v2.3')

    await user.click(within(dialog).getByRole('button', { name: /触发与资源/ }))
    const step = within(dialog).getAllByTestId('skill-workflow-step')[0]
    expect(step).toHaveAttribute('aria-expanded', 'false')
    await user.click(within(step).getByRole('button'))
    expect(step).toHaveAttribute('aria-expanded', 'true')
    expect(step).toHaveTextContent('保留告警来源与时间线')
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '创建可复用能力' })).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(opener))
  })

  it('runs success and failure test states inside the builder', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=skills')
    await user.click(screen.getByRole('button', { name: '打开 代码审查' }))
    const dialog = screen.getByRole('dialog', { name: '创建可复用能力' })
    await user.click(within(dialog).getByRole('button', { name: /测试与发布/ }))
    await user.click(within(dialog).getByRole('button', { name: '运行测试' }))
    await waitFor(() => expect(within(dialog).getByTestId('skill-test-success')).toBeInTheDocument())
    expect(within(dialog).getByTestId('skill-enable-after-test')).not.toBeDisabled()
    await user.click(within(dialog).getByTestId('skill-enable-after-test'))
    expect(screen.getByTestId('skill-notice')).toHaveTextContent('样例模式：启用未接入')
    await user.click(within(dialog).getByRole('button', { name: '模拟失败' }))
    expect(within(dialog).getByTestId('skill-test-failure')).toHaveAttribute('role', 'alert')
    expect(within(dialog).getByTestId('skill-enable-after-test')).toBeDisabled()
  })

  it('walks four builder stages and validates name, slug, version and trigger', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=skills')
    const opener = screen.getByRole('button', { name: '新建 Skill' })
    await user.click(opener)
    const dialog = screen.getByRole('dialog', { name: '创建可复用能力' })
    expect(within(dialog).getByText('第 1 / 4 步')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: '继续' }))
    expect(within(dialog).getByText('第 2 / 4 步')).toBeInTheDocument()
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
    expect(within(dialog).getByText('第 3 / 4 步')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: '继续' }))
    expect(within(dialog).getByText('请填写至少一个触发条件')).toBeInTheDocument()
    await user.type(within(dialog).getByLabelText('触发条件'), '用户提到交接时')
    await user.click(within(dialog).getByRole('button', { name: '继续' }))
    expect(within(dialog).getByText('第 4 / 4 步')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: '上一步' }))
    expect(within(dialog).getByText('第 3 / 4 步')).toBeInTheDocument()
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
})

describe('skills i18n', () => {
  it('translates grid tabs, builder checks and validation into English', async () => {
    const user = userEvent.setup()
    window.localStorage.setItem('anvil.language', 'en-US')
    renderAt('/?sample=skills')
    await waitFor(() => expect(document.documentElement.lang).toBe('en-US'))
    expect(screen.getByRole('tab', { name: 'Installed 9' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Market' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Drafts 2' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Open 代码审查' }))
    const dialog = screen.getByRole('dialog', { name: 'Create reusable capability' })
    await user.click(within(dialog).getByRole('button', { name: /Test & publish/ }))
    expect(within(dialog).getByText('SKILL.md structure is valid')).toBeInTheDocument()
    expect(within(dialog).getByText('Dependencies are authorized')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: /Basic information/ }))
    await user.clear(within(dialog).getByLabelText('Name'))
    await user.click(within(dialog).getByRole('button', { name: 'Continue' }))
    expect(within(dialog).getByText('Enter a Skill name')).toBeInTheDocument()
  })
})

describe('skills responsive contract', () => {
  it('declares three-column, two-column and single-column grids', () => {
    const css = readFileSync(resolve(process.cwd(), 'src/pages/skills/skills.css'), 'utf8')
    expect(css).toContain('grid-template-columns: repeat(3, minmax(0, 1fr))')
    expect(css).toContain('grid-template-columns: repeat(2, minmax(0, 1fr))')
    expect(css).toContain('grid-template-columns: minmax(0, 1fr)')
    expect(css).toContain('@media (max-width: 900px)')
    expect(css).toContain('@media (max-width: 620px)')
    expect(css).toContain('min-width: 0')
  })
})
