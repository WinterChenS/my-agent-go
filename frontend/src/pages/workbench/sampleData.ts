// Sample-only prototype data. Never rendered by the normal `/` entry; it is
// reachable only through `/?sample=workbench` for design comparison and tests.
export interface WorkbenchStat {
  key: 'sessions' | 'toolCalls' | 'tokens' | 'pending'
  value: string
  sub: string
}

export interface WorkbenchChat {
  title: string
  meta: string
  status?: 'pending'
}

export interface WorkbenchArtifact {
  extension: string
  name: string
  meta: string
  status: 'draft' | 'published'
}

export interface WorkbenchAgent {
  initial: string
  name: string
  meta: string
}

export interface WorkbenchSchedule {
  time: string
  title: string
  sub: string
  state: 'done' | 'now' | 'upcoming'
}

export interface WorkbenchStatusRow {
  labelKey: 'workbench.status.runtime' | 'workbench.status.mcp' | 'workbench.status.connectors'
  value: string
}

export const sampleWorkbench = {
  greetingName: '陈羽',
  stats: [
    { key: 'sessions', value: '34', sub: 'workbench.stats.sessionsDelta' },
    { key: 'toolCalls', value: '1,284', sub: 'workbench.stats.successRate' },
    { key: 'tokens', value: '6.2M', sub: 'workbench.stats.tokensSub' },
    { key: 'pending', value: '2', sub: 'workbench.stats.pendingSub' },
  ] satisfies WorkbenchStat[],
  activity: {
    weeks: 53,
    activeDays: '298',
    completed: '1,489',
    streak: '11',
  },
  chats: [
    {
      title: 'API 迁移评估：v1 → v2 兼容性',
      meta: '迁移工程师 · claude-sonnet-4.5 · 12 分钟前 · 18 轮',
      status: 'pending',
    },
    {
      title: '周报数据核对与图表生成',
      meta: '数据分析师 · gpt-5.1 · 1 小时前 · 9 轮 · 产出 3 个文件',
    },
    {
      title: '整理竞品定价页并输出对比',
      meta: '研究员 · gemini-3-pro · 昨天 18:40 · 24 轮 · 引用 11 条',
    },
    {
      title: '飞书文档结构整理',
      meta: '文档助手 · claude-haiku-4.5 · 周一 09:12 · 6 轮',
    },
  ] satisfies WorkbenchChat[],
  artifacts: [
    {
      extension: 'MD',
      name: 'v1-to-v2-migration-plan.md',
      meta: 'API 迁移评估 · 18.4 KB · 7 分钟前',
      status: 'draft',
    },
    {
      extension: 'HTML',
      name: 'weekly-report-2025-w45.html',
      meta: '周报数据核对 · 42.1 KB · 1 小时前',
      status: 'published',
    },
    {
      extension: 'CSV',
      name: 'pricing-diff.csv',
      meta: '竞品定价对比 · 6.8 KB · 昨天',
      status: 'draft',
    },
  ] satisfies WorkbenchArtifact[],
  agents: [
    { initial: '迁', name: '迁移工程师', meta: 'claude-sonnet-4.5 · 4 个 Skill' },
    { initial: '数', name: '数据分析师', meta: 'gpt-5.1 · 6 个 Skill' },
    { initial: '研', name: '研究员', meta: 'gemini-3-pro · 浏览器 + 引用' },
  ] satisfies WorkbenchAgent[],
  schedules: [
    { time: '09:00', title: '每日站会纪要汇总', sub: '已完成 · 1 分 42 秒', state: 'done' },
    { time: '16:30', title: '依赖漏洞扫描', sub: '16 分钟后运行 · 输出到资源库', state: 'now' },
    { time: '21:00', title: '飞书文档同步', sub: '每周一至周五', state: 'upcoming' },
  ] satisfies WorkbenchSchedule[],
  status: [
    { labelKey: 'workbench.status.runtime', value: 'sandbox-02 · 空闲' },
    { labelKey: 'workbench.status.mcp', value: '3 / 4 已连接' },
    { labelKey: 'workbench.status.connectors', value: '5 个可用' },
  ] satisfies WorkbenchStatusRow[],
  quota: '11 月 8 日',
}

export type SampleWorkbench = typeof sampleWorkbench
