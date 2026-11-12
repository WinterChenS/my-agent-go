import { useLocation } from 'react-router-dom'

export type ConnectorStatus = 'connected' | 'needsAuth' | 'disconnected'
export type ConnectorCategory = 'mail' | 'im' | 'docs' | 'code'

export type ConnectorScope =
  | 'searchMail'
  | 'readAttachment'
  | 'sendConfirm'
  | 'sendGroup'
  | 'readConversation'
  | 'mention'
  | 'readDoc'
  | 'writeDocConfirm'
  | 'sendMessage'
  | 'readChannel'
  | 'replyThread'
  | 'queryDb'
  | 'createPageConfirm'
  | 'readRepo'
  | 'commentPr'

export type ConnectorAction = 'configure' | 'reauthorize' | 'connect'

export interface ConnectorRecord {
  id: string
  nameKey: string
  category: ConnectorCategory
  status: ConnectorStatus
  descriptionKey: string
  /** Plain technical string; `accountKey` wins when the copy needs translation. */
  account?: string
  accountKey?: string
  scopes: ConnectorScope[]
  /** Footer copy; `count` is substituted when the key carries a placeholder. */
  footKey: string
  footCount?: number
  footTime?: string
  footWarn?: boolean
  action: ConnectorAction
}

export function useConnectorsSample(): boolean {
  const location = useLocation()
  if (location.pathname === '/__sample/connectors') return true
  return new URLSearchParams(location.search).get('sample') === 'connectors'
}

export const sampleConnectorStats = [
  { key: 'connected' as const, value: '5', subKey: 'connectors.stats.connectedSub' },
  { key: 'actions' as const, value: '28', subKey: 'connectors.stats.actionsSub' },
  { key: 'calls' as const, value: '1,284', subKey: 'connectors.stats.callsSub' },
  { key: 'sync' as const, value: '2 分钟前', subKey: 'connectors.stats.syncSub' },
]

export const sampleConnectors: ConnectorRecord[] = [
  {
    id: 'mail',
    nameKey: 'connectors.mail.name',
    category: 'mail',
    status: 'connected',
    descriptionKey: 'connectors.mail.desc',
    account: 'chen.yu@anvil.dev · imap.anvil.dev:993',
    scopes: ['searchMail', 'readAttachment', 'sendConfirm'],
    footKey: 'connectors.syncAt',
    footTime: '09:41',
    action: 'configure',
  },
  {
    id: 'dingtalk',
    nameKey: 'connectors.dingtalk.name',
    category: 'im',
    status: 'connected',
    descriptionKey: 'connectors.dingtalk.desc',
    accountKey: 'connectors.dingtalk.account',
    scopes: ['sendGroup', 'readConversation', 'mention'],
    footKey: 'connectors.groupCount',
    footCount: 3,
    action: 'configure',
  },
  {
    id: 'feishu',
    nameKey: 'connectors.feishu.name',
    category: 'docs',
    status: 'connected',
    descriptionKey: 'connectors.feishu.desc',
    accountKey: 'connectors.feishu.account',
    scopes: ['readDoc', 'writeDocConfirm', 'sendMessage'],
    footKey: 'connectors.docCount',
    footCount: 12,
    action: 'configure',
  },
  {
    id: 'slack',
    nameKey: 'connectors.slack.name',
    category: 'im',
    status: 'needsAuth',
    descriptionKey: 'connectors.slack.desc',
    account: '工作区 · anvil-hq',
    scopes: ['readChannel', 'replyThread'],
    footKey: 'connectors.expiredDays',
    footCount: 2,
    footWarn: true,
    action: 'reauthorize',
  },
  {
    id: 'notion',
    nameKey: 'connectors.notion.name',
    category: 'docs',
    status: 'connected',
    descriptionKey: 'connectors.notion.desc',
    accountKey: 'connectors.notion.account',
    scopes: ['queryDb', 'createPageConfirm'],
    footKey: 'connectors.databaseCount',
    footCount: 4,
    action: 'configure',
  },
  {
    id: 'github',
    nameKey: 'connectors.github.name',
    category: 'code',
    status: 'disconnected',
    descriptionKey: 'connectors.github.desc',
    accountKey: 'connectors.github.account',
    scopes: ['readRepo', 'commentPr'],
    footKey: 'connectors.actionsAfterConnect',
    footCount: 6,
    action: 'connect',
  },
]

export interface PolicyRow {
  titleKey: string
  action: string
  permissionKey: string
  confirmKey: string
  calls: number
}

export const samplePolicies: PolicyRow[] = [
  { titleKey: 'connectors.policy.mailSearch', action: 'mail.search_messages', permissionKey: 'connectors.permission.read', confirmKey: 'connectors.confirm.none', calls: 486 },
  { titleKey: 'connectors.policy.mailSend', action: 'mail.send_message', permissionKey: 'connectors.permission.write', confirmKey: 'connectors.confirm.high', calls: 37 },
  { titleKey: 'connectors.policy.dingtalkSend', action: 'dingtalk.send_group', permissionKey: 'connectors.permission.external', confirmKey: 'connectors.confirm.medium', calls: 214 },
  { titleKey: 'connectors.policy.feishuWrite', action: 'feishu.update_doc', permissionKey: 'connectors.permission.write', confirmKey: 'connectors.confirm.medium', calls: 92 },
  { titleKey: 'connectors.policy.feishuRead', action: 'feishu.get_doc', permissionKey: 'connectors.permission.read', confirmKey: 'connectors.confirm.none', calls: 341 },
  { titleKey: 'connectors.policy.notionCreate', action: 'notion.create_page', permissionKey: 'connectors.permission.write', confirmKey: 'connectors.confirm.medium', calls: 58 },
]

export interface ActivityEntry {
  time: string
  titleKey: string
  subKey: string
  state: 'done' | 'now'
}

export const sampleActivity: ActivityEntry[] = [
  { time: '09:41', titleKey: 'connectors.activity.mail', subKey: 'connectors.activity.mailSub', state: 'done' },
  { time: '09:18', titleKey: 'connectors.activity.dingtalk', subKey: 'connectors.activity.dingtalkSub', state: 'done' },
  { time: '08:52', titleKey: 'connectors.activity.feishu', subKey: 'connectors.activity.feishuSub', state: 'done' },
  { time: '08:30', titleKey: 'connectors.activity.slack', subKey: 'connectors.activity.slackSub', state: 'now' },
]

export const sampleHealth = [
  { labelKey: 'connectors.health.mail', noteKey: 'connectors.health.mailNote', width: 72, warn: false },
  { labelKey: 'connectors.health.feishu', noteKey: 'connectors.health.feishuNote', width: 34, warn: false },
  { labelKey: 'connectors.health.slack', noteKey: 'connectors.health.slackNote', width: 4, warn: true },
]

/** Settings rows shown by the per-connector dialog (prototype parity). */
export type SettingField =
  | { kind: 'switch'; labelKey: string; labelText?: string; subKey: string; checked: boolean }
  | { kind: 'select'; labelKey: string; optionKeys: string[] }
  | { kind: 'input'; labelKey: string; value: string; readOnly?: boolean }

export interface ConnectorSettings {
  connectorId: string
  titleKey: string
  subtitleKey: string
  fields: SettingField[]
  dangerKey?: string
}

export const connectorSettings: ConnectorSettings[] = [
  {
    connectorId: 'mail',
    titleKey: 'connectors.mail.name',
    subtitleKey: 'connectors.mail.settingsSub',
    fields: [
      { kind: 'switch', labelKey: 'connectors.settings.confirmSend', subKey: 'connectors.settings.confirmSendSub', checked: true },
      { kind: 'select', labelKey: 'connectors.settings.syncInterval', optionKeys: ['connectors.settings.sync.5m', 'connectors.settings.sync.15m', 'connectors.settings.sync.manual'] },
      { kind: 'input', labelKey: 'connectors.settings.folders', value: 'INBOX, Archive/Sent' },
    ],
    dangerKey: 'connectors.settings.disconnect',
  },
  {
    connectorId: 'dingtalk',
    titleKey: 'connectors.dingtalk.name',
    subtitleKey: 'connectors.dingtalk.settingsSub',
    fields: [
      { kind: 'input', labelKey: 'connectors.settings.appKey', value: 'ding7xk2m9qpl0', readOnly: true },
      { kind: 'input', labelKey: 'connectors.settings.groups', value: '#值班, #发布通知, #数据日报' },
      { kind: 'switch', labelKey: 'connectors.settings.confirmMention', subKey: 'connectors.settings.confirmMentionSub', checked: true },
    ],
  },
  {
    connectorId: 'feishu',
    titleKey: 'connectors.feishu.name',
    subtitleKey: 'connectors.feishu.settingsSub',
    fields: [
      { kind: 'input', labelKey: 'connectors.settings.knowledgeBase', value: '产品与工程 / 研发知识库' },
      { kind: 'switch', labelKey: 'connectors.settings.confirmWrite', subKey: 'connectors.settings.confirmWriteSub', checked: true },
      { kind: 'switch', labelKey: 'connectors.settings.allowCreate', subKey: 'connectors.settings.allowCreateSub', checked: false },
    ],
  },
  {
    connectorId: 'notion',
    titleKey: 'connectors.notion.name',
    subtitleKey: 'connectors.notion.settingsSub',
    fields: [
      { kind: 'input', labelKey: 'connectors.settings.databases', value: '竞品追踪, 需求池, 周报归档, 招聘看板' },
      { kind: 'switch', labelKey: 'connectors.settings.allowDbWrite', subKey: 'connectors.settings.allowDbWriteSub', checked: false },
    ],
  },
]

export const connectorTypes = ['mail', 'dingtalk', 'feishu', 'slack', 'notion', 'github'] as const
