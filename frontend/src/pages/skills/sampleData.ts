import { useLocation } from 'react-router-dom'

export type SkillSource = 'builtin' | 'local' | 'market' | 'official' | 'community'
export type SkillTab = 'installed' | 'market' | 'drafts'

export interface SkillRecord {
  id: string
  name: string
  icon: string
  version: string
  source: SkillSource
  description: string
  tags: string[]
  calls?: number
  updated?: string
  enabled?: boolean
  installs?: number
  draftNote?: string
}

export interface WorkflowStep {
  id: string
  title: string
  description: string
  tools: string[]
  status: 'done' | 'ready' | 'waiting'
}

export function useSkillsSample(): boolean {
  const location = useLocation()
  if (location.pathname === '/__sample/skills') return true
  return new URLSearchParams(location.search).get('sample') === 'skills'
}

export const installedSkills: SkillRecord[] = [
  { id: 'code-review', name: '代码审查', icon: 'code', version: 'v2.3', source: 'builtin', description: '按 diff 输出风险分级：会断掉、编译期可见、仅命名变化。附最小修复建议。', tags: ['git diff', '审查'], calls: 128, updated: '3 天前更新', enabled: true },
  { id: 'weekly-report', name: '周报生成', icon: 'file', version: 'v1.8', source: 'local', description: '汇总本周提交、工单与会议纪要，按团队模板生成周报页面并归档到资源库。', tags: ['git log', '模板'], calls: 41, updated: '1 周前更新', enabled: true },
  { id: 'research', name: '竞品调研', icon: 'globe', version: 'v3.0', source: 'market', description: '打开目标站点，抽取定价与功能矩阵，输出对比表格并保留引用截图。', tags: ['浏览器', '引用'], calls: 96, updated: '5 天前更新', enabled: true },
  { id: 'data-cleaning', name: '数据清洗', icon: 'database', version: 'v1.4', source: 'builtin', description: '识别重复行、空值口径与异常值，输出清洗脚本和清洗前后对照。', tags: ['CSV', 'SQL'], calls: 63, updated: '2 周前更新', enabled: true },
  { id: 'writing', name: '文档写作', icon: 'layers', version: 'v2.0', source: 'local', description: '统一标题层级、术语与语气，把口述内容整理成可发布的文档结构。', tags: ['Markdown', '语气'], calls: 214, updated: '昨天更新', enabled: true },
  { id: 'release-check', name: '发布检查', icon: 'check', version: 'v1.2', source: 'local', description: '按发布清单一逐项核对：版本号、变更日志、回滚脚本、监控埋点。', tags: ['发布', '清单'], calls: 27, updated: '3 周前更新', enabled: true },
  { id: 'meeting-notes', name: '会议纪要', icon: 'message', version: 'v1.6', source: 'market', description: '从录音转写中抽取决议、待办与负责人，输出可同步到日程的任务列表。', tags: ['转写', '待办'], calls: 88, updated: '6 天前更新', enabled: true },
  { id: 'dep-scan', name: '依赖扫描', icon: 'shield', version: 'v1.1', source: 'builtin', description: '扫描锁文件的已知漏洞与许可证冲突，按严重度排序并给出升级路径。', tags: ['pnpm', 'CVE'], calls: 35, updated: '4 天前更新', enabled: true },
  { id: 'ticket-triage', name: '票单分诊', icon: 'branch', version: 'v0.9', source: 'local', description: '读取工单内容，判断归属模块与优先级，草拟首轮回复但不自动发送。', tags: ['工单', '分诊'], calls: 12, updated: '2 天前更新', enabled: false },
]

export const marketSkills: SkillRecord[] = [
  { id: 'sql-optimize', name: 'SQL 优化', icon: 'database', version: 'v2.1', source: 'official', description: '读取执行计划，定位全表扫描与隐式类型转换，给出改写建议与对照耗时。', tags: ['EXPLAIN', '索引'], installs: 1240 },
  { id: 'contract-review', name: '合同审阅', icon: 'file', version: 'v1.3', source: 'community', description: '逐条比对标准条款，标出付款、违约与知识产权相关的高风险改动。', tags: ['法务', '条款'], installs: 860 },
  { id: 'public-opinion', name: '舆情摘要', icon: 'globe', version: 'v1.0', source: 'community', description: '按主题聚合公开讨论，区分事实与观点，输出带时间线的摘要。', tags: ['舆情', '摘要'], installs: 512 },
  { id: 'secret-check', name: '密钥体检', icon: 'key', version: 'v1.5', source: 'official', description: '扫描仓库中疑似泄露的凭据，给出轮换顺序与最小权限建议。', tags: ['安全', '轮换'], installs: 2038 },
  { id: 'a11y-audit', name: '无障碍审查', icon: 'eye', version: 'v1.2', source: 'community', description: '检查对比度、焦点顺序与语义标签，输出可复现的问题清单。', tags: ['a11y', '前端'], installs: 344 },
  { id: 'price-watch', name: '定价监控', icon: 'zap', version: 'v1.1', source: 'community', description: '定期抓取竞品定价页，输出变更 diff 并推送到连接器。', tags: ['抓取', '定时'], installs: 271 },
]

export const draftSkills: SkillRecord[] = [
  { id: 'handoff-report', name: '值班交接单', icon: 'file', version: '', source: 'local', description: '把当天告警、处理动作与遗留问题整理成交接单。触发条件尚未填写。', tags: [], draftNote: '需要补充触发条件' },
  { id: 'api-change-mail', name: 'API 变更通知', icon: 'message', version: '', source: 'local', description: '从 OpenAPI 差异生成变更通知邮件。邮件连接器尚未授权。', tags: [], draftNote: '依赖 1 个未授权连接器' },
]

export const workflowSteps: WorkflowStep[] = [
  { id: 'read-alerts', title: '读取告警', description: '读取今日告警与处理状态，保留告警来源与时间线。', tools: ['Sentry 连接器', '钉钉连接器'], status: 'done' },
  { id: 'classify', title: '归类与去重', description: '合并重复告警，按已解决、观察中、遗留问题分组。', tools: ['规则引擎'], status: 'done' },
  { id: 'compose', title: '生成交接单', description: '按团队模板输出值班交接单与责任人清单。', tools: ['文档写作', '资源库'], status: 'ready' },
  { id: 'archive', title: '归档到资源库', description: '写入 Markdown 交接单，等待用户确认后归档。', tools: ['资源库'], status: 'waiting' },
]

export const triggerConditions = [
  '用户提到「交接」「值班总结」「今日告警」时',
  '会话中出现 3 个以上告警处理记录时',
]

export const triggerResources = [
  { key: 'sentry', enabled: true },
  { key: 'dingtalk', enabled: true },
  { key: 'library', enabled: true },
  { key: 'terminal', enabled: false },
]

export const testInput = '今天有 4 条 Sentry 告警，其中 3 条已解决；支付回调超时仍在观察。请生成交接单。'
export const testSuccess = '隔离试运行完成：4 项检查通过，已生成 Markdown 交接单草稿。'
export const testFailure = '依赖未授权：邮件连接器尚未授权，未执行任何写入。'
