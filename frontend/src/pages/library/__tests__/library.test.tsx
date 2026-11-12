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

describe('library normal entry', () => {
  it('renders zero statistics, no assets and disabled actions', () => {
    renderAt('/library')
    expect(screen.getByTestId('library-page')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '资源库' })).toBeInTheDocument()
    expect(screen.getAllByTestId(/^library-stat-/)).toHaveLength(4)
    expect(screen.getByTestId('library-stat-assets')).toHaveTextContent('0')
    expect(screen.getByTestId('library-stat-storage')).toHaveTextContent('0')
    expect(screen.getByTestId('library-stat-references')).toHaveTextContent('0')
    expect(screen.getByTestId('library-stat-retention')).toHaveTextContent('—')
    expect(screen.getByTestId('library-empty')).toHaveTextContent('资源库为空')
    expect(screen.getByRole('button', { name: '上传' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '导出所选' })).toBeDisabled()
    expect(screen.queryByTestId('library-card')).toBeNull()
    for (const fixture of ['v1-to-v2-migration-plan.md', '184 MB', 'retention-weekly.png']) {
      expect(screen.queryByText(fixture), `fixture ${fixture}`).toBeNull()
    }
  })

  it('stays empty in English', async () => {
    window.localStorage.setItem('anvil.language', 'en-US')
    renderAt('/library')
    await waitFor(() => expect(document.documentElement.lang).toBe('en-US'))
    expect(screen.getByRole('heading', { name: 'Library' })).toBeInTheDocument()
    expect(screen.getByTestId('library-empty')).toHaveTextContent('The library is empty')
    expect(screen.getByRole('button', { name: 'Upload' })).toBeDisabled()
  })
})

describe('library sample entry', () => {
  it('renders the statistics, eight assets and the tab counters', () => {
    renderAt('/?sample=library')
    expect(screen.getByTestId('sample-banner')).toHaveTextContent('原型样例数据')
    expect(screen.getByTestId('library-stat-assets')).toHaveTextContent('28')
    expect(screen.getByTestId('library-stat-storage')).toHaveTextContent('184 MB')
    expect(screen.getByTestId('library-stat-references')).toHaveTextContent('23')
    expect(screen.getByTestId('library-stat-retention')).toHaveTextContent('90 天')
    const cards = screen.getAllByTestId('library-card')
    expect(cards).toHaveLength(8)
    expect(cards[0]).toHaveTextContent('v1-to-v2-migration-plan.md')
    expect(cards[0]).toHaveTextContent('API 迁移评估 · 18.4 KB · 7 分钟前')
    expect(cards[0]).toHaveTextContent('引用 3')
    expect(cards[1]).toHaveTextContent('compat-matrix.csv')
    expect(cards[2]).toHaveTextContent('retention-weekly.png')
    expect(cards[5]).toHaveTextContent('backfill-owner-id.ts')
    const tabs = screen.getAllByRole('tab')
    expect(tabs).toHaveLength(6)
    expect(tabs[0]).toHaveTextContent('全部')
    expect(tabs[0]).toHaveTextContent('28')
    expect(tabs[1]).toHaveTextContent('文档')
    expect(tabs[1]).toHaveTextContent('11')
  })

  it('filters by tab, type, session and file name', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=library')
    await user.click(screen.getAllByRole('tab')[1])
    expect(screen.getAllByTestId('library-card')).toHaveLength(2)
    await user.click(screen.getAllByRole('tab')[0])
    await user.selectOptions(screen.getByLabelText('类型筛选'), 'code')
    expect(screen.getAllByTestId('library-card')).toHaveLength(2)
    await user.selectOptions(screen.getByLabelText('类型筛选'), 'all')
    await user.selectOptions(screen.getByLabelText('会话筛选'), '客户工单聚类')
    expect(screen.getAllByTestId('library-card')).toHaveLength(1)
    await user.selectOptions(screen.getByLabelText('会话筛选'), 'all')
    await user.type(screen.getByTestId('library-search'), '定价')
    expect(screen.getAllByTestId('library-card')).toHaveLength(1)
    await user.type(screen.getByTestId('library-search'), '不存在')
    expect(screen.getByText('没有匹配的产出物')).toBeInTheDocument()
  })

  it('switches between the grid and list views', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=library')
    expect(screen.getByTestId('library-assets').className).toContain('is-grid')
    await user.click(screen.getByRole('button', { name: '列表' }))
    expect(screen.getByTestId('library-assets').className).toContain('is-list')
    expect(screen.getByRole('button', { name: '列表' })).toHaveAttribute('aria-pressed', 'true')
  })

  it('opens the asset detail with real properties and a preview', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=library')
    await user.click(screen.getByRole('button', { name: '打开 v1-to-v2-migration-plan.md' }))
    const dialog = screen.getByRole('dialog', { name: 'v1-to-v2-migration-plan.md' })
    expect(dialog).toHaveTextContent('Markdown 文档')
    expect(dialog).toHaveTextContent('写文件工具 · 2 次修订')
    expect(dialog).toHaveTextContent('owner_id 由 int 改为 str')
    await user.click(within(dialog).getByRole('button', { name: /复制链接给同事/ }))
    expect(screen.getByTestId('library-notice')).toHaveTextContent('样例模式：复制链接未接入')
    await user.click(within(dialog).getByRole('button', { name: '关闭' }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'v1-to-v2-migration-plan.md' })).toBeNull())
  })

  it('exports only the selected artifacts and reports the sample notice', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=library')
    expect(screen.getByLabelText('选择 v1-to-v2-migration-plan.md')).toBeChecked()
    await user.click(screen.getByLabelText('选择 v1-to-v2-migration-plan.md'))
    const trigger = screen.getByTestId('library-export')
    await user.click(trigger)
    const dialog = screen.getByRole('dialog', { name: '导出所选产出物' })
    expect(within(dialog).getByText('已选中 1 项，导出后打包为一个压缩包。')).toBeInTheDocument()
    expect(within(dialog).getByText('compat-matrix.csv')).toBeInTheDocument()
    expect(within(dialog).queryByText('v1-to-v2-migration-plan.md')).toBeNull()
    await user.click(within(dialog).getByRole('button', { name: '导出' }))
    expect(screen.getByTestId('library-notice')).toHaveTextContent('样例模式：导出未接入')
    await waitFor(() => expect(document.activeElement).toBe(trigger))
  })

  it('opens the upload dialog with the local-index note', async () => {
    const user = userEvent.setup()
    renderAt('/?sample=library')
    await user.click(screen.getByTestId('library-upload'))
    const dialog = screen.getByRole('dialog', { name: '上传到资源库' })
    expect(dialog).toHaveTextContent('文件仅在本机索引，不会上传到模型供应商')
    await user.click(within(dialog).getByRole('button', { name: '上传' }))
    expect(screen.getByTestId('library-notice')).toHaveTextContent('样例模式：上传未接入')
  })

  it('translates statistics, tabs and the detail dialog', async () => {
    const user = userEvent.setup()
    window.localStorage.setItem('anvil.language', 'en-US')
    renderAt('/?sample=library')
    await waitFor(() => expect(document.documentElement.lang).toBe('en-US'))
    expect(screen.getByTestId('library-stat-assets')).toHaveTextContent('Artifacts')
    expect(screen.getAllByRole('tab')[0]).toHaveTextContent('All')
    await user.click(screen.getByRole('button', { name: 'Open v1-to-v2-migration-plan.md' }))
    expect(screen.getByRole('dialog', { name: 'v1-to-v2-migration-plan.md' })).toHaveTextContent('Source chat')
    expect(screen.getByRole('button', { name: 'Download' })).toBeInTheDocument()
  })
})

describe('library responsive contract', () => {
  it('declares four, three, two and one column layouts', () => {
    const css = readFileSync(resolve(process.cwd(), 'src/pages/library/library.css'), 'utf8')
    expect(css).toContain('grid-template-columns: repeat(4, minmax(0, 1fr))')
    expect(css).toContain('@media (max-width: 1280px)')
    expect(css).toContain('@media (max-width: 900px)')
    expect(css).toContain('@media (max-width: 620px)')
    expect(css).toContain('min-width: 0')
    expect(css).toContain('overflow-x: auto')
  })
})
