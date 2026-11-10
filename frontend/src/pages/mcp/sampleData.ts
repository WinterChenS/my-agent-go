import { useLocation } from 'react-router-dom'

export type McpStatus = 'connected' | 'needsAuth' | 'disconnected'
export type McpTransport = 'stdio' | 'http' | 'websocket'

export interface McpTool {
  name: string
  descKey: string
}

export type McpTag = 'stdio' | 'readonly' | 'http' | 'rowLimit' | 'writesWeb' | 'highRisk' | 'oauthPending'

export interface McpService {
  id: string
  name: string
  descriptionKey: string
  status: McpStatus
  command: string
  transport: McpTransport
  tags: McpTag[]
  tools: McpTool[]
  callsToday?: number
  lastError?: string
  averageLatency?: string
  tryAt?: string
}

export interface McpLog {
  time: string
  service: string
  ok: boolean
  result: string
  detail: string
}

export function useMcpSample(): boolean {
  const location = useLocation()
  if (location.pathname === '/__sample/mcp') return true
  return new URLSearchParams(location.search).get('sample') === 'mcp'
}

export const sampleStats = [
  { key: 'connected' as const, value: '3', subKey: 'mcp.stats.connectedSub' },
  { key: 'tools' as const, value: '19', subKey: 'mcp.stats.toolsSub' },
  { key: 'calls' as const, value: '216', subKey: 'mcp.stats.callsSub' },
  { key: 'latency' as const, value: '142ms', subKey: 'mcp.stats.latencySub' },
]

export const sampleServices: McpService[] = [
  {
    id: 'filesystem',
    name: 'filesystem',
    descriptionKey: 'mcp.service.filesystemDesc',
    status: 'connected',
    command: 'npx -y @modelcontextprotocol/server-filesystem ~/work',
    transport: 'stdio',
    tags: ['stdio', 'readonly'],
    tools: [
      { name: 'read_file', descKey: 'mcp.tool.readFile' },
      { name: 'list_directory', descKey: 'mcp.tool.listDirectory' },
      { name: 'search_files', descKey: 'mcp.tool.searchFiles' },
      { name: 'write_file', descKey: 'mcp.tool.writeFile' },
    ],
    callsToday: 148,
    lastError: '11 分钟前',
  },
  {
    id: 'postgres-analytics',
    name: 'postgres-analytics',
    descriptionKey: 'mcp.service.postgresDesc',
    status: 'connected',
    command: 'postgres://readonly@analytics.internal:5432/warehouse',
    transport: 'http',
    tags: ['http', 'rowLimit'],
    tools: [
      { name: 'list_tables', descKey: 'mcp.tool.listTables' },
      { name: 'describe_table', descKey: 'mcp.tool.describeTable' },
      { name: 'run_query', descKey: 'mcp.tool.runQuery' },
    ],
    callsToday: 52,
    averageLatency: '210ms',
  },
  {
    id: 'playwright',
    name: 'playwright',
    descriptionKey: 'mcp.service.playwrightDesc',
    status: 'needsAuth',
    command: 'http://127.0.0.1:8931/sse',
    transport: 'http',
    tags: ['http', 'writesWeb', 'highRisk'],
    tools: [
      { name: 'navigate', descKey: 'mcp.tool.navigate' },
      { name: 'screenshot', descKey: 'mcp.tool.screenshot' },
      { name: 'click', descKey: 'mcp.tool.click' },
    ],
  },
  {
    id: 'notion',
    name: 'notion',
    descriptionKey: 'mcp.service.notionDesc',
    status: 'disconnected',
    command: 'https://mcp.notion.com/sse',
    transport: 'http',
    tags: ['oauthPending'],
    tools: [],
    tryAt: '昨天 20:14',
  },
]

export const sampleLogs: McpLog[] = [
  { time: '14:32:07', service: 'filesystem', ok: true, result: 'initialized', detail: 'protocol 2025-06-18 · 4 tools' },
  { time: '14:32:06', service: 'postgres', ok: true, result: 'initialized', detail: 'protocol 2025-06-18 · 3 tools' },
  { time: '14:02:41', service: 'playwright', ok: true, result: 'initialized', detail: 'protocol 2025-06-18 · 3 tools' },
  { time: '13:58:19', service: 'notion', ok: false, result: 'oauth_required', detail: 'missing access token' },
  { time: '11:21:03', service: 'filesystem', ok: false, result: 'tool_error', detail: 'write_file · EACCES /etc/hosts' },
  { time: '09:15:52', service: 'postgres', ok: true, result: 'reconnected', detail: 'after idle timeout' },
]

export const sampleAgents = ['迁移工程师', '数据分析师', '研究员']
