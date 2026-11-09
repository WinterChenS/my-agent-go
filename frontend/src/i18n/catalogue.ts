export const LANGUAGES = ['zh-CN', 'en-US'] as const
export type Language = (typeof LANGUAGES)[number]
export const DEFAULT_LANGUAGE: Language = 'zh-CN'

// Source baseline: prototype assets/studio.js I18N at commit
// 112fcae854b324d2e75fdf5c890083ef9c1d3d6e, extended with the R0-01/R0-02
// shell/settings copy. Keep keys identical across catalogues.
const zhCN = {
  'nav.group.start': '开始',
  'nav.tools': '工具与自动化',
  'nav.workbench': '工作台',
  'nav.chat': '对话',
  'nav.agents': 'Agent',
  'nav.library': '资源库',
  'nav.skills': 'Skill',
  'nav.mcp': 'MCP',
  'nav.connectors': '连接器',
  'nav.schedules': '定时任务',
  'nav.settings': '设置',

  'page.workbench.title': '工作台',
  'page.chat.title': '对话',
  'page.agents.title': 'Agent',
  'page.library.title': '资源库',
  'page.skills.title': 'Skill',
  'page.mcp.title': 'MCP',
  'page.connectors.title': '连接器',
  'page.schedules.title': '定时任务',
  'page.settings.title': '设置',

  'crumb.workspace': '工作区',
  'crumb.agents': '智能体',
  'crumb.automation': '自动化',
  'crumb.system': '系统',
  'crumb.chat': '',
  'main.content': '主内容',

  'brand.subtitle': '本地智能体客户端 · v0.9',
  'runtime.title': '本地运行时',
  'runtime.status.unknown': '未检测',
  'user.name': '本地工作区',
  'user.plan': '未连接账号',

  'topbar.search': '搜索',
  'topbar.help': '帮助',
  'topbar.notice.search': '搜索将在后续任务接入',
  'topbar.notice.help': '帮助将在后续任务接入',
  'topbar.runtime.notConnected': '运行时未接入',
  'document.titleFormat': '{title} · Anvil',

  'placeholder.badge': 'R0-01 · shell',
  'placeholder.description': '共用壳已锁定 {path} 路由。页面正文由 {task} 按原型 {source} 实现，R0-01 只交付壳层。',
  'placeholder.card.title': '路由占位',
  'placeholder.card.sub': '{task} 实现页面内容',
  'placeholder.body': '当前地址 {path} 已由共用壳承载；侧栏、顶栏、滚动容器、焦点与导航行为可用。',
  'placeholder.note': '业务内容、弹窗与数据状态不在本任务范围。',
  'placeholder.fallback': '未找到路由 {path}',

  'settings.theme.title': '主题',
  'settings.theme.subtitle': '浅色、深色或跟随 Windows 外观',
  'settings.theme.system': '跟随系统',
  'settings.theme.system.hint': '跟随 Windows 外观',
  'settings.theme.light': '浅色',
  'settings.theme.light.hint': '始终使用浅色',
  'settings.theme.dark': '深色',
  'settings.theme.dark.hint': '始终使用深色',
  'settings.theme.status': '当前主题：{resolved}（{mode}）',
  'settings.theme.resolved.light': '浅色',
  'settings.theme.resolved.dark': '深色',
  'settings.theme.notice.invalid': '主题设置无效，已回退到跟随系统',
  'settings.theme.notice.readFailed': '主题设置读取失败，已回退到跟随系统',
  'settings.theme.notice.saveFailed': '主题设置保存失败，本次选择仍然生效',

  'settings.language.title': '语言',
  'settings.language.subtitle': '界面语言',
  'settings.language.zh': '简体中文',
  'settings.language.en': 'English',
  'settings.language.status': '当前语言：{language}',
  'settings.language.notice.invalid': '语言设置无效，已回退到简体中文',
  'settings.language.notice.readFailed': '语言设置读取失败，已回退到简体中文',
  'settings.language.notice.saveFailed': '语言设置保存失败，本次选择仍然生效',
  'language.quickSwitch': '语言',
}

const enUS: Record<keyof typeof zhCN, string> = {
  'nav.group.start': 'Start',
  'nav.tools': 'Tools & Automation',
  'nav.workbench': 'Dashboard',
  'nav.chat': 'Chats',
  'nav.agents': 'Agents',
  'nav.library': 'Library',
  'nav.skills': 'Skills',
  'nav.mcp': 'MCP',
  'nav.connectors': 'Connectors',
  'nav.schedules': 'Schedules',
  'nav.settings': 'Settings',

  'page.workbench.title': 'Dashboard',
  'page.chat.title': 'Chat',
  'page.agents.title': 'Agents',
  'page.library.title': 'Library',
  'page.skills.title': 'Skills',
  'page.mcp.title': 'MCP',
  'page.connectors.title': 'Connectors',
  'page.schedules.title': 'Schedules',
  'page.settings.title': 'Settings',

  'crumb.workspace': 'Workspace',
  'crumb.agents': 'Agents',
  'crumb.automation': 'Automation',
  'crumb.system': 'System',
  'crumb.chat': '',
  'main.content': 'Main content',

  'brand.subtitle': 'Local agent client · v0.9',
  'runtime.title': 'Local runtime',
  'runtime.status.unknown': 'Not detected',
  'user.name': 'Local workspace',
  'user.plan': 'No account connected',

  'topbar.search': 'Search',
  'topbar.help': 'Help',
  'topbar.notice.search': 'Search will be connected in a later task',
  'topbar.notice.help': 'Help will be connected in a later task',
  'topbar.runtime.notConnected': 'Runtime not connected',
  'document.titleFormat': '{title} · Anvil',

  'placeholder.badge': 'R0-01 · shell',
  'placeholder.description': 'The shared shell owns the {path} route. The page body will be implemented by {task} from prototype {source}; R0-01 only delivers the shell.',
  'placeholder.card.title': 'Route placeholder',
  'placeholder.card.sub': '{task} implements the page content',
  'placeholder.body': 'The shared shell hosts {path}; sidebar, topbar, scroll container, focus and navigation are available.',
  'placeholder.note': 'Business content, dialogs and data states are outside this task.',
  'placeholder.fallback': 'Unknown route {path}',

  'settings.theme.title': 'Theme',
  'settings.theme.subtitle': 'Light, dark, or follow the Windows appearance',
  'settings.theme.system': 'System',
  'settings.theme.system.hint': 'Follow the Windows appearance',
  'settings.theme.light': 'Light',
  'settings.theme.light.hint': 'Always use light',
  'settings.theme.dark': 'Dark',
  'settings.theme.dark.hint': 'Always use dark',
  'settings.theme.status': 'Current theme: {resolved} ({mode})',
  'settings.theme.resolved.light': 'Light',
  'settings.theme.resolved.dark': 'Dark',
  'settings.theme.notice.invalid': 'Invalid theme setting; reverted to System',
  'settings.theme.notice.readFailed': 'Could not read the theme setting; reverted to System',
  'settings.theme.notice.saveFailed': 'Could not save the theme setting; this choice is still active',

  'settings.language.title': 'Language',
  'settings.language.subtitle': 'Interface language',
  'settings.language.zh': 'Chinese',
  'settings.language.en': 'English',
  'settings.language.status': 'Current language: {language}',
  'settings.language.notice.invalid': 'Invalid language setting; reverted to Chinese',
  'settings.language.notice.readFailed': 'Could not read the language setting; reverted to Chinese',
  'settings.language.notice.saveFailed': 'Could not save the language setting; this choice is still active',
  'language.quickSwitch': 'Language',
}

export type MessageKey = keyof typeof zhCN

export const CATALOGUES: Record<Language, Record<MessageKey, string>> = {
  'zh-CN': zhCN,
  'en-US': enUS,
}

export const MESSAGE_KEYS = Object.keys(zhCN) as MessageKey[]

export function formatMessage(template: string, params?: Record<string, string | number>): string {
  if (!params) return template
  return template.replace(/\{([a-zA-Z0-9_.-]+)\}/g, (match, name: string) =>
    Object.prototype.hasOwnProperty.call(params, name) ? String(params[name]) : match,
  )
}

export function translate(language: Language, key: MessageKey, params?: Record<string, string | number>): string {
  const catalogue = CATALOGUES[language]
  const message = catalogue[key]
  if (message === undefined) {
    throw new Error(`missing i18n key ${key} for ${language}`)
  }
  return formatMessage(message, params)
}
