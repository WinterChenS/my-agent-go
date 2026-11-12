import { useLocation } from 'react-router-dom'

export function useSettingsSample(): boolean {
  const location = useLocation()
  if (location.pathname === '/__sample/settings') return true
  return new URLSearchParams(location.search).get('sample') === 'settings'
}

export interface ProviderRow {
  id: string
  name: string
  host: string
  mark: string
  models: string[]
  key: string
}

export const sampleProviders: ProviderRow[] = [
  { id: 'openai', name: 'OpenAI', host: 'api.openai.com', mark: 'O', models: ['gpt-5.1', 'gpt-5-mini'], key: 'sk-proj-••••7K2P' },
  { id: 'anthropic', name: 'Anthropic', host: 'api.anthropic.com', mark: 'A', models: ['claude-sonnet-4.5', 'claude-haiku-4.5'], key: 'sk-ant-••••B91M' },
  { id: 'google', name: 'Google AI', host: 'generativelanguage.googleapis.com', mark: 'G', models: ['gemini-3-pro', 'gemini-3-flash'], key: 'AIza••••C4Q' },
]

export interface RuntimeCard {
  id: string
  mark: string
  name: string
  cli: string
  state: 'running' | 'installed'
  description: string
  models: string[]
  usage: string
  isDefault?: boolean
}

export const sampleRuntimes: RuntimeCard[] = [
  {
    id: 'pi-agent',
    mark: 'π',
    name: 'Pi Agent',
    cli: 'pi · v0.48.2',
    state: 'running',
    description: '当前默认运行时。支持工具调用、子 Agent、Skill、MCP 与流式思考。',
    models: ['claude-sonnet-4.5', 'gpt-5.1', 'gemini-3-pro'],
    usage: '4 个会话使用',
    isDefault: true,
  },
  {
    id: 'codex-cli',
    mark: 'C',
    name: 'Codex CLI',
    cli: 'codex · v0.77.0',
    state: 'installed',
    description: '适合代码编辑、终端执行与仓库级任务，可绑定 OpenAI 模型。',
    models: ['gpt-5.1', 'gpt-5-mini'],
    usage: '1 个 Agent 使用',
  },
  {
    id: 'claude-code',
    mark: 'A',
    name: 'Claude Code',
    cli: 'claude · v2.0.8',
    state: 'installed',
    description: '用于长上下文代码理解与多文件修改，仅显示兼容的 Anthropic 模型。',
    models: ['claude-sonnet-4.5', 'claude-haiku-4.5'],
    usage: '尚未绑定',
  },
]

export const sampleEnvVars = [
  { key: 'GITHUB_TOKEN', scope: '全部 Agent', updated: '3 天前' },
  { key: 'DATABASE_URL', scope: '数据分析师', updated: '8 天前' },
  { key: 'SENTRY_AUTH_TOKEN', scope: '安全审计', updated: '11 天前' },
  { key: 'HTTP_PROXY', scope: '浏览器工具', updated: '21 天前' },
]

export const sampleTraces = [
  { id: 'tr_8f2c91a4', session: 'API 迁移评估', agent: '迁移工程师', steps: [32, 18, 41], duration: '14.8s', state: 'warn' as const },
  { id: 'tr_33b705de', session: '周报数据核对', agent: '数据分析师', steps: [38, 27, 20], duration: '9.2s', state: 'success' as const },
  { id: 'tr_a72e190b', session: '竞品定价巡检', agent: '研究员', steps: [44, 35], duration: '21.6s', state: 'success' as const },
  { id: 'tr_c19d4b70', session: '依赖漏洞扫描', agent: '安全审计', steps: [18, 60], duration: '38.1s', state: 'danger' as const },
]

export const sampleExecutions = [
  { time: '15:42:08', action: 'write_file', subject: 'migration-plan.md', actor: '迁移工程师', policy: 'user', result: 'success' as const, duration: '182ms' },
  { time: '15:41:52', action: 'read_file', subject: 'openapi.yaml', actor: '迁移工程师', policy: 'low', result: 'success' as const, duration: '24ms' },
  { time: '15:38:10', action: 'browser_navigate', subject: '定价页', actor: '研究员', policy: 'low', result: 'success' as const, duration: '1.8s' },
  { time: '15:30:44', action: 'shell_exec', subject: 'npm audit', actor: '安全审计', policy: 'allowed', result: 'exit1' as const, duration: '38.1s' },
]

export const sampleUsage = [
  { key: 'tokens' as const, value: '6.2M', subKey: 'settings.obs.tokensSub' },
  { key: 'cost' as const, value: '$58.40', subKey: 'settings.obs.costSub' },
  { key: 'calls' as const, value: '1,284', subKey: 'settings.obs.callsSub' },
  { key: 'tools' as const, value: '846', subKey: 'settings.obs.toolsSub' },
]

export const sampleUsageBars = [
  { label: 'gpt-5.1', width: 78, note: '2.8M · $31.20' },
  { label: 'claude-sonnet-4.5', width: 52, note: '1.9M · $18.60' },
  { label: 'gemini-3-pro', width: 28, note: '0.9M · $6.40' },
  { label: 'settings.obs.other', width: 18, note: '0.6M · $2.20' },
]

export const sampleAgentBars = [
  { label: '迁移工程师', width: 72, note: '2.3M' },
  { label: '数据分析师', width: 49, note: '1.6M' },
  { label: '研究员', width: 37, note: '1.2M' },
  { label: 'settings.obs.otherAgents', width: 34, note: '1.1M' },
]

export const sampleUsageChart = [
  { label: '11/02', input: 36, output: 12 },
  { label: '11/03', input: 48, output: 16 },
  { label: '11/04', input: 42, output: 14 },
  { label: '11/05', input: 62, output: 20 },
  { label: '11/06', input: 54, output: 18 },
  { label: '11/07', input: 67, output: 22 },
  { label: 'settings.obs.today', input: 44, output: 15 },
]

export const samplePermissions = [
  { id: 'ask-all', titleKey: 'settings.perm.askAll', subKey: 'settings.perm.askAllSub', badgeKey: 'settings.perm.cautious' },
  { id: 'balanced', titleKey: 'settings.perm.balanced', subKey: 'settings.perm.balancedSub', badgeKey: 'settings.perm.recommended' },
  { id: 'high-only', titleKey: 'settings.perm.highOnly', subKey: 'settings.perm.highOnlySub' },
  { id: 'full-control', titleKey: 'settings.perm.full', subKey: 'settings.perm.fullSub', badgeKey: 'settings.perm.careful' },
]

export const modelRouteModels = ['gpt-5.1', 'claude-sonnet-4.5', 'gemini-3-pro']
export const fastRouteModels = ['claude-haiku-4.5', 'gpt-5-mini', 'gemini-3-flash']
export const timezones = ['Asia/Shanghai · UTC+8', 'UTC', 'America/Los_Angeles']
export const replyLanguageKeys = ['settings.general.reply.zh', 'settings.general.reply.en']
