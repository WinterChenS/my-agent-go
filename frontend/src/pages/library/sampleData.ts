import { useLocation } from 'react-router-dom'

export type AssetType = 'doc' | 'sheet' | 'page' | 'chart' | 'code'
export type AssetView = 'grid' | 'list'

export interface AssetRecord {
  id: string
  /** Prototype content, kept verbatim (design fixtures, not UI chrome). */
  name: string
  title: string
  type: AssetType
  session: string
  meta: string
  tags: string[]
  references?: number
  /** Which mock preview the thumbnail draws. */
  preview: 'page' | 'table' | 'bars' | 'code'
  previewTitle: string
  /** Extra lines used by the mock preview. */
  previewLines?: string[]
}

export function useLibrarySample(): boolean {
  const location = useLocation()
  if (location.pathname === '/__sample/library') return true
  return new URLSearchParams(location.search).get('sample') === 'library'
}

export const sampleLibraryStats = [
  { key: 'assets' as const, value: '28', subKey: 'library.stats.assetsSub' },
  { key: 'storage' as const, value: '184 MB', subKey: 'library.stats.storageSub' },
  { key: 'references' as const, value: '23', subKey: 'library.stats.referencesSub' },
  { key: 'retention' as const, value: '90 天', subKey: 'library.stats.retentionSub' },
]

export const sampleAssets: AssetRecord[] = [
  {
    id: 'migration-plan',
    name: 'v1-to-v2-migration-plan.md',
    title: 'v1 → v2 迁移计划',
    type: 'doc',
    session: 'API 迁移评估',
    meta: '18.4 KB · 7 分钟前',
    tags: ['MD', '草稿'],
    references: 3,
    preview: 'page',
    previewTitle: 'v1 → v2 迁移计划',
    previewLines: ['w70', 'w85', 'w50'],
  },
  {
    id: 'compat-matrix',
    name: 'compat-matrix.csv',
    title: '字段兼容性矩阵',
    type: 'sheet',
    session: 'API 迁移评估',
    meta: '42 行 · 22 分钟前',
    tags: ['CSV', '已核对'],
    preview: 'table',
    previewTitle: '字段兼容性矩阵',
    previewLines: ['owner_id', 'state', 'meta'],
  },
  {
    id: 'weekly-chart',
    name: 'retention-weekly.png',
    title: '本周留存率',
    type: 'chart',
    session: '周报数据核对',
    meta: '2400×1350 · 1 小时前',
    tags: ['PNG', '图表'],
    preview: 'bars',
    previewTitle: '本周留存率',
  },
  {
    id: 'pricing-page',
    name: 'pricing-comparison.html',
    title: '竞品定价对比',
    type: 'page',
    session: '竞品定价整理',
    meta: '6 个竞品 · 昨天 18:52',
    tags: ['HTML', '可分享'],
    preview: 'table',
    previewTitle: '竞品定价对比',
    previewLines: ['Linear · $8 · $14', 'Notion · $10 · $18'],
  },
  {
    id: 'ticket-cluster',
    name: 'ticket-clusters.json',
    title: '工单聚类摘要',
    type: 'sheet',
    session: '客户工单聚类',
    meta: '1.2 MB · 昨天 14:06',
    tags: ['JSON', '中间产物'],
    preview: 'code',
    previewTitle: '工单聚类摘要',
    previewLines: ['cluster 0 · 412 条  登录失败 / SSO', 'cluster 1 · 238 条  计费口径', 'cluster 2 · 155 条  导出超时'],
  },
  {
    id: 'migration-script',
    name: 'backfill-owner-id.ts',
    title: 'backfill.ts',
    type: 'code',
    session: 'API 迁移评估',
    meta: '3.1 KB · 昨天 11:20',
    tags: ['TS', '待审查'],
    preview: 'code',
    previewTitle: 'backfill.ts',
    previewLines: ['// 回填 owner_id 类型', 'export async function run(db) {', '  await db.tx(async (t) => {'],
  },
  {
    id: 'weekly-report',
    name: 'weekly-report-w45.md',
    title: '第 45 周经营周报',
    type: 'doc',
    session: '周报数据核对',
    meta: '9.7 KB · 2 小时前',
    tags: ['MD', '已发布'],
    preview: 'page',
    previewTitle: '第 45 周经营周报',
    previewLines: ['w70', 'w85', 'w50'],
  },
  {
    id: 'schema-diff',
    name: 'openapi-diff.txt',
    title: 'OpenAPI 结构差异',
    type: 'code',
    session: 'API 迁移评估',
    meta: '14 增 9 删 · 3 天前',
    tags: ['TXT', '来源 2 处'],
    preview: 'code',
    previewTitle: 'OpenAPI 结构差异',
    previewLines: ['- GET  /v1/orders/:id', '+ GET  /v2/orders/{order_id}', '+ POST /v2/orders/{id}:cancel'],
  },
]

/** Prototype tab counters (design fixtures, kept for visual parity). */
export const sampleTabs: Array<{ id: 'all' | AssetType; count: number }> = [
  { id: 'all', count: 28 },
  { id: 'doc', count: 11 },
  { id: 'sheet', count: 6 },
  { id: 'page', count: 5 },
  { id: 'chart', count: 4 },
  { id: 'code', count: 2 },
]

export const sampleSessions = ['API 迁移评估', '周报数据核对', '竞品定价整理', '客户工单聚类']

export const assetDetail = {
  id: 'migration-plan',
  name: 'v1-to-v2-migration-plan.md',
  subtitle: '由「API 迁移评估：v1 → v2 兼容性」在 7 分钟前生成 · 18.4 KB',
  typeLabel: 'Markdown 文档',
  session: 'API 迁移评估',
  origin: '写文件工具 · 2 次修订',
  references: '3 个会话',
  preview: [
    '# v1 → v2 迁移计划',
    '',
    '## 1. 会断掉的变更（必须处理）',
    '- owner_id 由 int 改为 str：影响 14 处调用',
    '- /v2/orders/{order_id} 路径参数重命名',
    '',
    '## 2. 编译期可见',
    '- Refund.state 枚举新增 partial_refund',
    '',
    '## 3. 仅命名变化',
    '- OrderMeta → OrderMetadata',
  ],
}
