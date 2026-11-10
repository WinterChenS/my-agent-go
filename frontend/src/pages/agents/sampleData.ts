import { useLocation } from 'react-router-dom'
import type { MessageKey } from '../../i18n/catalogue'

export interface AgentTool {
  name: string
  descKey: MessageKey
  risk: 'low' | 'medium' | 'high'
  enabled: boolean
}

export interface AgentSkill {
  nameKey: MessageKey
  descKey: MessageKey
  enabled: boolean
}

export interface SubAgent {
  name: string
  runtime: 'pi-agent' | 'codex-cli' | 'claude-code'
  model: string
  conditionKey: MessageKey
  concurrency: number
  enabled: boolean
}

export interface AgentRun {
  time: string
  chat: string
  turns: number
  tools: number
  tokens: string
  status: 'pending' | 'done' | 'archived'
}

export interface AgentRecord {
  id: string
  name: string
  badge: string
  group: 'main' | 'sub'
  runtime: 'pi-agent' | 'codex-cli' | 'claude-code'
  model: string
  description: string
  skills: number
  ready: boolean
  readonly?: boolean
  delegatedFrom?: string
  slug: string
  thinking: 'off' | 'low' | 'medium' | 'high'
  permission: 'readonly' | 'medium' | 'high' | 'full'
  context: string
  memory: boolean
  autoCompact: boolean
  writableDirs: string
  commandAllowlist: string
  network: boolean
  highRiskConfirm: boolean
}

export function useAgentsSample(): boolean {
  const location = useLocation()
  if (location.pathname === '/__sample/agents') return true
  return new URLSearchParams(location.search).get('sample') === 'agents'
}

export const sampleAgents: AgentRecord[] = [
  {
    id: 'migration-engineer',
    name: '迁移工程师',
    badge: '迁',
    group: 'main',
    runtime: 'pi-agent',
    model: 'claude-sonnet-4.5',
    description: '负责代码库迁移、依赖升级与兼容性评估。',
    skills: 4,
    ready: true,
    slug: 'migration-engineer',
    thinking: 'medium',
    permission: 'medium',
    context: '400K',
    memory: true,
    autoCompact: true,
    writableDirs: 'docs/**  src/api/**',
    commandAllowlist: 'pnpm test, pnpm lint, git diff, git status',
    network: true,
    highRiskConfirm: true,
  },
  {
    id: 'data-analyst',
    name: '数据分析师',
    badge: '数',
    group: 'main',
    runtime: 'codex-cli',
    model: 'gpt-5.1',
    description: '负责 SQL 查询、指标口径核对与图表化输出。',
    skills: 6,
    ready: false,
    slug: 'data-analyst',
    thinking: 'low',
    permission: 'readonly',
    context: '200K',
    memory: true,
    autoCompact: false,
    writableDirs: '',
    commandAllowlist: 'git status',
    network: false,
    highRiskConfirm: true,
  },
  {
    id: 'researcher',
    name: '研究员',
    badge: '研',
    group: 'main',
    runtime: 'pi-agent',
    model: 'gemini-3-pro',
    description: '负责公开资料检索、网页阅读与引用整理。',
    skills: 3,
    ready: false,
    slug: 'researcher',
    thinking: 'high',
    permission: 'medium',
    context: '1M',
    memory: false,
    autoCompact: true,
    writableDirs: 'docs/**',
    commandAllowlist: 'git status',
    network: true,
    highRiskConfirm: true,
  },
  {
    id: 'docs-assistant',
    name: '文档助手',
    badge: '文',
    group: 'main',
    runtime: 'claude-code',
    model: 'claude-haiku-4.5',
    description: '负责文档结构整理、摘要与格式统一。',
    skills: 2,
    ready: false,
    slug: 'docs-assistant',
    thinking: 'off',
    permission: 'readonly',
    context: '200K',
    memory: false,
    autoCompact: false,
    writableDirs: 'docs/**',
    commandAllowlist: '',
    network: false,
    highRiskConfirm: true,
  },
  {
    id: 'code-auditor',
    name: '代码审计员',
    badge: '审',
    group: 'sub',
    runtime: 'pi-agent',
    model: 'claude-sonnet-4.5',
    description: '由迁移工程师委派，只读审查改动并出具风险清单。',
    skills: 3,
    ready: false,
    readonly: true,
    delegatedFrom: '迁移工程师',
    slug: 'code-auditor',
    thinking: 'medium',
    permission: 'readonly',
    context: '200K',
    memory: false,
    autoCompact: false,
    writableDirs: '',
    commandAllowlist: '',
    network: false,
    highRiskConfirm: true,
  },
  {
    id: 'test-generator',
    name: '测试生成器',
    badge: '测',
    group: 'sub',
    runtime: 'codex-cli',
    model: 'gpt-5.1-mini',
    description: '由迁移工程师委派，根据 diff 生成回归测试。',
    skills: 2,
    ready: false,
    readonly: true,
    delegatedFrom: '迁移工程师',
    slug: 'test-generator',
    thinking: 'low',
    permission: 'readonly',
    context: '200K',
    memory: false,
    autoCompact: false,
    writableDirs: '',
    commandAllowlist: 'pnpm test',
    network: false,
    highRiskConfirm: true,
  },
]

export const systemPrompt = `你是一名代码迁移工程师。在改动任何文件前：
1. 先定位真实调用面，而不是只看定义；
2. 把风险按「会断掉 / 编译期可见 / 仅命名变化」分级；
3. 产出必须可执行，包含验证步骤与回滚方式；
4. 没有实测数据时写「待填」，不要编造数值。

引用仓库文件时使用 camelCase 路径，引用外部资料时给出可点击链接。`

export const sampleTools: AgentTool[] = [
  { name: 'read_file', descKey: 'agents.tool.read.desc', risk: 'low', enabled: true },
  { name: 'search_files', descKey: 'agents.tool.search.desc', risk: 'low', enabled: true },
  { name: 'write_file', descKey: 'agents.tool.write.desc', risk: 'medium', enabled: true },
  { name: 'run_shell', descKey: 'agents.tool.shell.desc', risk: 'medium', enabled: true },
  { name: 'git_push', descKey: 'agents.tool.push.desc', risk: 'high', enabled: false },
]

export const sampleSkills: AgentSkill[] = [
  { nameKey: 'agents.skill.review', descKey: 'agents.skill.reviewDesc', enabled: true },
  { nameKey: 'agents.skill.deps', descKey: 'agents.skill.depsDesc', enabled: true },
  { nameKey: 'agents.skill.docs', descKey: 'agents.skill.docsDesc', enabled: true },
  { nameKey: 'agents.skill.research', descKey: 'agents.skill.researchDesc', enabled: false },
]

export const sampleSubAgents: SubAgent[] = [
  {
    name: '代码审计员',
    runtime: 'pi-agent',
    model: 'claude-sonnet-4.5',
    conditionKey: 'agents.sub.condition.audit',
    concurrency: 2,
    enabled: true,
  },
  {
    name: '测试生成器',
    runtime: 'codex-cli',
    model: 'gpt-5.1-mini',
    conditionKey: 'agents.sub.condition.tests',
    concurrency: 3,
    enabled: true,
  },
]

export const sampleRuns: AgentRun[] = [
  { time: '今天 16:02', chat: 'API 迁移评估：v1 → v2', turns: 18, tools: 26, tokens: '21.4K', status: 'pending' },
  { time: '今天 11:20', chat: '依赖升级：pnpm 9 → 10', turns: 9, tools: 14, tokens: '12.8K', status: 'done' },
  { time: '昨天 18:40', chat: '旧接口下线影响面', turns: 24, tools: 31, tokens: '38.2K', status: 'done' },
  { time: '11 月 1 日', chat: 'Node 20 → 22 兼容性', turns: 15, tools: 22, tokens: '26.1K', status: 'archived' },
]

export const sampleRunStats = { turns: 18, tools: 26, tokens: '21.4K' }
