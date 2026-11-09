import data from './routes.json'
import type { MessageKey } from '../i18n/catalogue'

export type ShellRouteGroup = 'primary' | 'tools'

export interface ShellRoute {
  id: string
  path: string
  source: string
  role: string
  title: string
  navLabel: string
  navKey: MessageKey
  titleKey: MessageKey
  breadcrumbKey: MessageKey
  group: ShellRouteGroup
  breadcrumbParent: string
  topbarActions: string[]
  topbarStatusKey?: MessageKey | null
  chrome: 'shell' | 'chat'
  navMeta?: string
  futureTask: string
  marker: string
}

export interface Viewport {
  name: string
  width: number
  height: number
  category: string
  mustAvoidHorizontalScroll: boolean
}

// Frozen to the prototype commit recorded in DESIGN-MANIFEST.json. The
// manifest itself lives under docs/ui/Agent-客户端产品原型 and is the source of
// truth for screens and responsive viewports.
export const PROTOTYPE_COMMIT = data.prototypeCommit

export const SHELL_ROUTES: readonly ShellRoute[] = data.screens as ShellRoute[]

export const PRIMARY_ROUTES = SHELL_ROUTES.filter((route) => route.group === 'primary')
export const TOOL_ROUTES = SHELL_ROUTES.filter((route) => route.group === 'tools')

export const WORKBENCH_ROUTE = SHELL_ROUTES[0]

export const ROUTES_BY_PATH = new Map(SHELL_ROUTES.map((route) => [route.path, route]))
export const ROUTES_BY_ID = new Map(SHELL_ROUTES.map((route) => [route.id, route]))

export const VIEWPORTS: readonly Viewport[] = [
  { name: 'mobile-compact', width: 360, height: 800, category: 'mobile', mustAvoidHorizontalScroll: true },
  { name: 'mobile-standard', width: 390, height: 844, category: 'mobile', mustAvoidHorizontalScroll: true },
  { name: 'mobile-large', width: 430, height: 932, category: 'mobile', mustAvoidHorizontalScroll: true },
  { name: 'foldable-small-tablet', width: 600, height: 960, category: 'foldable-tablet', mustAvoidHorizontalScroll: true },
  { name: 'tablet-portrait', width: 820, height: 1180, category: 'tablet', mustAvoidHorizontalScroll: true },
  { name: 'tablet-landscape', width: 1024, height: 768, category: 'tablet', mustAvoidHorizontalScroll: true },
  { name: 'laptop', width: 1366, height: 768, category: 'desktop', mustAvoidHorizontalScroll: true },
  { name: 'desktop', width: 1440, height: 900, category: 'desktop', mustAvoidHorizontalScroll: true },
  { name: 'wide', width: 1920, height: 1080, category: 'wide', mustAvoidHorizontalScroll: true },
]

export const BREAKPOINTS = {
  shellCollapse: 900,
  compact: 620,
} as const

export const DEFAULT_WINDOW = { width: 1180, height: 820, minWidth: 360, minHeight: 560 } as const

export function resolveRoute(pathname: string): ShellRoute | undefined {
  return ROUTES_BY_PATH.get(pathname)
}
