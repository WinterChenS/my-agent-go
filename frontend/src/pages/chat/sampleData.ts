import { useLocation } from 'react-router-dom'

export interface ChatSession {
  id: string
  title: string
  meta: string
  state?: 'pending'
  pinned?: boolean
}

export interface ChatProject {
  id: string
  name: string
  path: string
  open: boolean
  sessions: ChatSession[]
}

export interface ChatTool {
  icon: string
  name: string
  arg: string
  duration: string
  body: string
  status?: 'pending' | 'failed'
}

export interface ChatBullet {
  lead: string
  text: string
  code?: string
  cite?: number
}

export interface ChatProse {
  intro: string
  bullets?: ChatBullet[]
  closing?: string
}

export type SampleMessage =
  | { kind: 'user'; id: string; text: string; author: string; time: string }
  | {
      kind: 'agent'
      id: string
      agent: string
      model: string
      badge?: 'generating' | 'failed'
      thinking?: { title: string; steps: string[]; open?: boolean }
      tools?: ChatTool[]
      prose?: ChatProse
      meta?: { tokens: string; time: string }
      artifacts?: { count: number; files: string; turn: string }
      confirm?: boolean
      stream?: boolean
    }
  | {
      kind: 'state'
      id: string
      tone: 'success' | 'warn' | 'danger'
      role: 'status' | 'alert'
      title: string
      body: string
      actions?: Array<'detail' | 'retry' | 'login' | 'compress' | 'branch' | 'continue'>
    }
  | { kind: 'clarification'; id: string; title: string; body: string; note: string }

export interface ChatArtifact {
  ext: string
  name: string
  meta: string
  status: 'saved' | 'pending'
  turn: string
}

export interface ChatSource {
  num: number
  title: string
  url: string
  snippet: string
}

export interface ChatExec {
  time: string
  name: string
  sub: string
  status?: 'done' | 'pending' | 'paused'
}

export interface ChatSample {
  title: string
  project: string
  sessionTitle: string
  agent: string
  model: string
  thinkingLevel: 'off' | 'low' | 'medium' | 'high'
  permission: 'readonly' | 'medium' | 'high' | 'full'
  projects: ChatProject[]
  personal: ChatSession[]
  messages: SampleMessage[]
  attachments: Array<{ id: string; name: string; kind: 'file' | 'skill'; size?: string }>
  rail: {
    artifacts: ChatArtifact[]
    sources: ChatSource[]
    exec: ChatExec[]
    browser: { url: string; title: string; meta: string; paragraphs: string[] }
    usage: { input: string; output: string; cache: string; cost: string }
  }
  projectOptions: Array<{ name: string; path: string; sessions: number }>
}

export function useChatSample(): boolean {
  const location = useLocation()
  if (location.pathname === '/__sample/chat') return true
  return new URLSearchParams(location.search).get('sample') === 'chat'
}

export const sampleChat: ChatSample = {
  title: 'API 迁移评估：v1 → v2 兼容性',
  project: 'my-agent-go-ui',
  sessionTitle: 'API 迁移评估：v1 → v2',
  agent: '迁移工程师',
  model: 'claude-sonnet-4.5',
  thinkingLevel: 'medium',
  permission: 'medium',
  projects: [
    {
      id: 'project-agent-ui',
      name: 'my-agent-go-ui',
      path: '~/work/my-agent-go-ui',
      open: true,
      sessions: [
        { id: 'session-api-migration', title: 'API 迁移评估：v1 → v2', meta: '迁移工程师 · 刚刚', state: 'pending' },
        { id: 'session-runtime-health', title: 'CLI 运行时健康检查', meta: '系统工程师 · 2 小时前' },
        { id: 'session-responsive', title: '前端响应式回归', meta: '设计工程师 · 昨天' },
      ],
    },
    {
      id: 'project-notes',
      name: '个人知识库',
      path: '~/Documents/notes',
      open: false,
      sessions: [
        { id: 'session-reading-notes', title: '整理本周阅读笔记', meta: '文档助手 · 周一' },
        { id: 'session-topic-index', title: '合并重复主题索引', meta: '知识管家 · 10 月 28 日' },
      ],
    },
  ],
  personal: [
    { id: 'session-weekly-report', title: '周报数据核对与图表生成', meta: '数据分析师 · 1 小时前', pinned: true },
    { id: 'session-pricing', title: '竞品定价页差异整理', meta: '研究员 · 昨天' },
    { id: 'session-feishu', title: '飞书文档结构整理', meta: '文档助手 · 周一' },
    { id: 'session-pnpm', title: '依赖升级：pnpm 9 → 10', meta: '迁移工程师 · 11 月 2 日' },
    { id: 'session-meeting', title: '会议录音提取待办', meta: '文档助手 · 10 月 31 日' },
    { id: 'session-invoices', title: '发票文件批量重命名', meta: '文件助手 · 10 月 29 日' },
    { id: 'session-travel', title: '旅行路线对比', meta: '研究员 · 10 月 25 日' },
    { id: 'session-usage', title: '月度用量异常说明', meta: '数据分析师 · 10 月 20 日' },
    { id: 'session-bookmarks', title: '整理浏览器书签', meta: '文档助手 · 10 月 18 日' },
    { id: 'session-database', title: '数据库字段释义', meta: '数据分析师 · 10 月 15 日' },
  ],
  messages: [
    {
      kind: 'user',
      id: 'user-1',
      text: '读一下 api/v1/handlers 下的所有入口，评估升级到 v2 会破坏哪些调用点。重点关注鉴权和分页签名。',
      author: '陈羽',
      time: '16:02',
    },
    {
      kind: 'agent',
      id: 'agent-1',
      agent: '迁移工程师',
      model: 'claude-sonnet-4.5',
      thinking: {
        title: '已思考 5 步 · 8.2 秒',
        steps: [
          '先确认仓库里 v1 的实际调用面，而不是只看 handler 定义——真正的破坏点在调用方。',
          '鉴权中间件从 X-Api-Key 换成 Authorization: Bearer，所有自带 header 的客户端都会失败。',
          '分页从 page/size 改成游标，返回值里 total 被移除，前端分页器会拿到 undefined。',
          '读取类型定义，核对 3 处 caller 的签名是否兼容。',
          '结论需要可执行，所以直接产出迁移计划文档，而不是只列风险。',
        ],
      },
      tools: [
        {
          icon: 'search',
          name: 'search_files',
          arg: 'pattern="api/v1/**" · 命中 3 个调用点',
          duration: '1.4s',
          body: 'src/api/v1/handlers/auth.ts\nsrc/api/v1/handlers/orders.ts\nsrc/api/v1/handlers/reports.ts\nweb/src/lib/api-client.ts        ← 唯一的跨端调用方',
        },
        {
          icon: 'file',
          name: 'read_file',
          arg: 'web/src/lib/api-client.ts · 214 行',
          duration: '0.6s',
          body: "import { request } from './transport'\n\nexport async function listOrders(page: number, size: number) {\n  // v2 移除了 total 字段，分页改为游标\n  return request(`/v1/orders?page=${page}&size=${size}`)\n}",
        },
      ],
      prose: {
        intro: '有 3 处会真正断掉，其余是编译期就能发现的重命名：',
        bullets: [
          { lead: '鉴权头', text: '在 v2 被删除，客户端必须改用', code: 'Authorization: Bearer', cite: 1 },
          { lead: '分页契约', text: 'page/size 换成 cursor/limit，响应体不再返回 total，前端分页器会读到 undefined。', cite: 2 },
          { lead: '错误结构', text: '错误结构变为 { error: { code, message } }，现有 catch 分支会拿到对象。', cite: 3 },
        ],
        closing: '建议先在 api-client.ts 加一层适配，把游标包装成旧的 page/size 语义，这样 web 端可以独立上线，不依赖后端灰度窗口。',
      },
      meta: { tokens: '2,180 tokens', time: '16:02' },
      artifacts: { count: 2, files: 'callers-inventory.csv、api-client.adapter.ts', turn: 'analysis-1' },
    },
    {
      kind: 'clarification',
      id: 'clarification-1',
      title: '灰度方案按什么口径推进？',
      body: '仓库没有流量分组规则。请选择按租户白名单、10% 随机流量或仅预发布环境验证。',
      note: '只确认业务内容，不会授予工具权限',
    },
    {
      kind: 'user',
      id: 'user-2',
      text: '按这个结论写一份迁移计划，保存到 docs/migration/，要包含回滚步骤。',
      author: '陈羽',
      time: '16:09',
    },
    {
      kind: 'state',
      id: 'recovery-1',
      tone: 'success',
      role: 'status',
      title: '运行时已从检查点恢复',
      body: 'Pi Agent 在读取目录后异常退出；已恢复上下文和 2 个工具结果，未重复执行写操作。',
      actions: ['detail'],
    },
    {
      kind: 'agent',
      id: 'agent-live',
      agent: '迁移工程师',
      model: 'claude-sonnet-4.5',
      badge: 'generating',
      thinking: {
        title: '思考中 · 第 4 步 / 共 6 步',
        open: true,
        steps: [
          '文档需要三部分：影响面、分阶段上线、回滚。回滚必须具体到命令。',
          '灰度窗口按订单量取值，仓库里没有真实数据，因此写清「待填」而不是编造。',
          '写文件属于中风险操作，按当前权限设置需要用户确认。',
          '准备写入 1 个新文件，不覆盖已有文档。',
        ],
      },
      tools: [
        {
          icon: 'folder',
          name: 'list_dir',
          arg: 'docs/migration/ · 0 个文件',
          duration: '0.2s',
          body: 'docs/migration/  (empty)',
        },
        {
          icon: 'file',
          name: 'write_file',
          arg: 'docs/migration/v1-to-v2-plan.md · 新建',
          duration: '待确认',
          body: '+ # API v1 → v2 迁移计划\n+ 1. 影响面：鉴权头 / 分页契约 / 错误结构\n+ 2. 分阶段：适配层 → 灰度 10% → 全量\n+ 3. 回滚：api-client 切回 v1 路由，保留 7 天',
          status: 'pending',
        },
      ],
      prose: { intro: '已按三部分起草，回滚写到命令级。写入 docs/migration/ 需要你确认后才会执行。' },
      confirm: true,
      stream: true,
    },
    {
      kind: 'state',
      id: 'auth-1',
      tone: 'danger',
      role: 'alert',
      title: '认证失败',
      body: 'Pi Agent 的登录凭证已过期，未发送任何消息或工具请求。重新登录后可继续这个会话。',
      actions: ['login'],
    },
    {
      kind: 'state',
      id: 'context-1',
      tone: 'warn',
      role: 'status',
      title: '当前上下文接近上限',
      body: '可以压缩早期工具结果后继续，或创建分支保留完整上下文。',
      actions: ['compress', 'branch'],
    },
    {
      kind: 'state',
      id: 'stopped-1',
      tone: 'warn',
      role: 'status',
      title: '生成已由你停止',
      body: '已保留 12 个页面结果和当前检查点，可继续执行或从这里分叉。',
      actions: ['continue', 'branch'],
    },
  ],
  attachments: [
    { id: 'file-1', name: 'api-client.ts', kind: 'file', size: '12 KB' },
    { id: 'skill-1', name: '代码审查', kind: 'skill' },
  ],
  rail: {
    artifacts: [
      { ext: 'MD', name: 'v1-to-v2-plan.md', meta: '3.4 KB · 待确认写入', status: 'pending', turn: 'plan-2' },
      { ext: 'CSV', name: 'callers-inventory.csv', meta: '2.1 KB · 16:04', status: 'saved', turn: 'analysis-1' },
      { ext: 'TS', name: 'api-client.adapter.ts', meta: '1.8 KB · 16:06', status: 'saved', turn: 'analysis-1' },
      { ext: 'MD', name: 'weekly-summary.md', meta: '4.8 KB · 1 小时前', status: 'saved', turn: 'complete-1' },
      { ext: 'CSV', name: 'weekly-chart.csv', meta: '6.2 KB · 1 小时前', status: 'saved', turn: 'complete-1' },
    ],
    sources: [
      { num: 1, title: 'v2 鉴权迁移说明（内部文档）', url: 'confluence/eng/api-v2-auth', snippet: '「v2 起不再接受 X-Api-Key，统一使用 Authorization: Bearer。」' },
      { num: 2, title: '分页契约变更 RFC-118', url: 'github/eng/rfcs/blob/main/118-pagination.md', snippet: '「游标分页移除 total 字段，客户端需自行累计或改用 has_more。」' },
      { num: 3, title: '错误响应结构对齐', url: 'github/eng/api-spec/errors.yaml', snippet: '「error 对象包含 code 与 message，http status 保持不变。」' },
      { num: 4, title: 'web/src/lib/api-client.ts', url: 'repo · 本地文件 · L42-L88', snippet: 'listOrders / listReports 使用 page、size 参数，并读取响应中的 total。' },
    ],
    exec: [
      { time: '0.0s', name: '规划', sub: 'planner · 3 tool calls', status: 'done' },
      { time: '0.4s', name: 'search_files', sub: 'api/v1/** · 3 hits' },
      { time: '1.9s', name: 'read_file', sub: 'api-client.ts · 214 行' },
      { time: '2.6s', name: 'write_file', sub: 'docs/migration/v1-to-v2-plan.md', status: 'pending' },
      { time: '—', name: '继续生成', sub: '等待用户授权', status: 'paused' },
    ],
    browser: {
      url: 'anvil://repo/docs/migration/v1-to-v2-plan.md',
      title: 'API v1 → v2 迁移计划',
      meta: 'docs/migration/v1-to-v2-plan.md · 草稿 · 3.4 KB',
      paragraphs: [
        '鉴权头 X-Api-Key 删除；分页由 page/size 改为 cursor/limit；错误结构变为嵌套对象。共 3 处调用点需要改动。',
        '适配层合入 → 灰度 10% 流量 → 观察 48 小时 → 全量。灰度窗口按订单量决定，待填实测值。',
        'api-client.ts 切回 /v1 路由，保留双写 7 天；文档级回滚使用 git revert。',
      ],
    },
    usage: { input: '18,420', output: '2,940', cache: '12,100', cost: '$0.31' },
  },
  projectOptions: [
    { name: 'my-agent-go-ui', path: '~/work/my-agent-go-ui', sessions: 3 },
    { name: '个人知识库', path: '~/Documents/notes', sessions: 2 },
  ],
}
