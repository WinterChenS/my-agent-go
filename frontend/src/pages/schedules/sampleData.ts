import { useLocation } from 'react-router-dom'

export type ScheduleState = 'enabled' | 'paused'
/** `never` is live-only: a real schedule that has not run yet. */
export type RunLabel = 'success' | 'warn' | 'partial' | 'skipped' | 'never'

export interface RunRecord {
  time: string
  label: RunLabel
  duration: string
}

export interface ScheduleTask {
  id: string
  /** Prototype content, kept verbatim (design fixtures, not UI chrome). */
  name: string
  meta: string
  badgeChar: string
  cron: string
  when: string
  agent: string
  model: string
  state: ScheduleState
  permissionKey: string
  prompt: string
  output: string
  pendingNote?: string
  lastLabel: RunLabel
  lastDuration: string
  runs: RunRecord[]
}

export function useSchedulesSample(): boolean {
  const location = useLocation()
  if (location.pathname === '/__sample/schedules') return true
  return new URLSearchParams(location.search).get('sample') === 'schedules'
}

export const sampleScheduleStats = [
  { key: 'enabled' as const, value: '5', subKey: 'schedules.stats.enabledSub' },
  { key: 'today' as const, value: '11', subKey: 'schedules.stats.todaySub' },
  { key: 'success' as const, value: '98.2%', subKey: 'schedules.stats.successSub' },
  { key: 'duration' as const, value: '1m52s', subKey: 'schedules.stats.durationSub' },
]

export const sampleSchedules: ScheduleTask[] = [
  {
    id: 'standup',
    name: '每日站会纪要汇总',
    meta: '读取飞书会议记录 → 产出摘要与待办',
    badgeChar: '文',
    cron: '0 9 * * 1-5',
    when: '工作日 09:00 · 下次 明天 09:00',
    agent: '文档助手',
    model: 'claude-haiku-4.5',
    state: 'enabled',
    permissionKey: 'schedules.permission.medium',
    prompt:
      '读取「产品/站会」下今天的三份会议记录，按项目分组提炼：结论、待办、阻塞。待办需标注负责人与截止日期，缺省时写「未指派」。输出 Markdown 到资源库，并把未指派的待办发到邮箱。',
    output: '资源库 / 会议纪要',
    lastLabel: 'success',
    lastDuration: '1m42s',
    runs: [
      { time: '今天 09:00', label: 'success', duration: '1m42s' },
      { time: '昨天 09:00', label: 'success', duration: '1m38s' },
      { time: '周一 09:00', label: 'success', duration: '2m05s' },
    ],
  },
  {
    id: 'vuln-scan',
    name: '依赖漏洞扫描',
    meta: '扫描 work/ 下 4 个仓库的 lockfile',
    badgeChar: '安',
    cron: '30 16 * * *',
    when: '每天 16:30 · 下次 16 分钟后',
    agent: '安全审计',
    model: 'gpt-5.1',
    state: 'enabled',
    permissionKey: 'schedules.permission.full',
    prompt:
      '扫描 ~/work 下 4 个仓库的 package-lock.json 与 go.sum，对比 OSV 数据库。按严重度排序输出表格：包名、当前版本、修复版本、可利用条件。发现 critical 时直接创建 issue 并 @ 值班人。',
    output: '资源库 / 安全报告',
    pendingNote: '待运行',
    lastLabel: 'warn',
    lastDuration: '6m08s',
    runs: [
      { time: '昨天 16:30', label: 'warn', duration: '6m08s' },
      { time: '前天 16:30', label: 'success', duration: '4m51s' },
      { time: '11/03 16:30', label: 'success', duration: '5m12s' },
    ],
  },
  {
    id: 'pricing-watch',
    name: '竞品定价页巡检',
    meta: '抓取 5 家定价页，变更超过 2% 时告警',
    badgeChar: '研',
    cron: '0 10 * * 1',
    when: '每周一 10:00 · 下次 11/10 10:00',
    agent: '研究员',
    model: 'gemini-3-pro',
    state: 'enabled',
    permissionKey: 'schedules.permission.high',
    prompt:
      '抓取 Figma、Notion、Linear、Airtable、Miro 的定价页，截取团队版区块。与上周快照逐项对比价格、席位阶梯与功能裁剪，变化超过 2% 时生成变更摘要并发到 #product-intel。',
    output: '资源库 / 竞品研究',
    lastLabel: 'success',
    lastDuration: '3m10s',
    runs: [
      { time: '11/03 10:00', label: 'success', duration: '3m10s' },
      { time: '10/27 10:00', label: 'success', duration: '2m58s' },
      { time: '10/20 10:00', label: 'skipped', duration: '—' },
    ],
  },
  {
    id: 'feishu-sync',
    name: '飞书文档同步',
    meta: '拉取「产品/需求池」到本地索引',
    badgeChar: '文',
    cron: '0 21 * * 1-5',
    when: '工作日 21:00 · 下次 今天 21:00',
    agent: '文档助手',
    model: 'claude-haiku-4.5',
    state: 'enabled',
    permissionKey: 'schedules.permission.medium',
    prompt:
      '拉取飞书「产品/需求池」下所有文档，抽取标题、状态、负责人、最近编辑时间，写入 ~/.anvil/index/feishu.json。仅读取，不修改远端内容。',
    output: '本地索引',
    lastLabel: 'success',
    lastDuration: '48s',
    runs: [
      { time: '昨天 21:00', label: 'success', duration: '48s' },
      { time: '周一 21:00', label: 'success', duration: '52s' },
      { time: '上周五 21:00', label: 'success', duration: '51s' },
    ],
  },
  {
    id: 'weekly-report',
    name: '周报数据核对',
    meta: '对比埋点与数据仓库的 DAU 口径',
    badgeChar: '数',
    cron: '30 17 * * 5',
    when: '每周五 17:30 · 下次 11/14 17:30',
    agent: '数据分析师',
    model: 'gpt-5.1',
    state: 'enabled',
    permissionKey: 'schedules.permission.medium',
    prompt:
      '对比埋点表与数据仓库的 DAU、留存与转化口径，列出差异超过 5% 的指标。对差异给出可能原因（去重口径、时区、埋点缺失）。生成核对表并附 SQL 片段。',
    output: '资源库 / 数据核对',
    pendingNote: '部分失败',
    lastLabel: 'partial',
    lastDuration: '2m35s',
    runs: [
      { time: '11/07 17:30', label: 'partial', duration: '2m35s' },
      { time: '10/31 17:30', label: 'success', duration: '2m12s' },
      { time: '10/24 17:30', label: 'success', duration: '2m28s' },
    ],
  },
  {
    id: 'archive',
    name: '会话产出归档',
    meta: '把 30 天前的产出物移入冷存储',
    badgeChar: '系',
    cron: '0 2 * * *',
    when: '每天 02:00 · 已暂停',
    agent: '系统任务',
    model: '无模型',
    state: 'paused',
    permissionKey: 'schedules.permission.builtin',
    prompt: '把 30 天前未被引用的产出物移入冷存储，保留元数据与引用关系。仅移动文件，不删除。',
    output: '冷存储',
    lastLabel: 'success',
    lastDuration: '12s',
    runs: [
      { time: '今天 02:00', label: 'success', duration: '12s' },
      { time: '昨天 02:00', label: 'success', duration: '11s' },
      { time: '11/04 02:00', label: 'success', duration: '13s' },
    ],
  },
]

/** 7-day run counts, in the prototype's order (11/02 → today). */
export const sampleTrend = [
  { label: '11/02', planned: 30, manual: 0 },
  { label: '11/03', planned: 40, manual: 0 },
  { label: '11/04', planned: 20, manual: 0 },
  { label: '11/05', planned: 50, manual: 0 },
  { label: '11/06', planned: 40, manual: 0 },
  { label: '11/07', planned: 30, manual: 0 },
  { label: 'schedules.trend.today', planned: 15, manual: 0 },
]

export const scheduleAgents = ['文档助手', '安全审计', '研究员', '数据分析师', '系统任务']
export const formAgents = ['文档助手', '研究员', '数据分析师', '迁移工程师', '安全审计']
export const scheduleTimezones = ['Asia/Shanghai (UTC+8)', 'UTC', 'America/Los_Angeles (UTC-8)']
export const schedulePermissions = [
  'schedules.form.permission.medium',
  'schedules.form.permission.high',
  'schedules.form.permission.full',
]
export const scheduleOutputs = [
  'schedules.form.output.library',
  'schedules.form.output.inline',
  'schedules.form.output.mail',
]
