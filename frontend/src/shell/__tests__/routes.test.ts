import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import {
  PRIMARY_ROUTES,
  PROTOTYPE_COMMIT,
  SHELL_ROUTES,
  TOOL_ROUTES,
  VIEWPORTS,
  resolveRoute,
} from '../routes'

const repoRoot = resolve(process.cwd(), '..')
const manifestPath = resolve(repoRoot, 'docs/ui/Agent-客户端产品原型/DESIGN-MANIFEST.json')
const manifest = JSON.parse(readFileSync(manifestPath, 'utf8')) as {
  screens: Array<{ file: string; role: string }>
  html: string[]
  sourceFiles: { html: string[] }
  responsiveViewports: Array<{
    name: string
    width: number
    height: number
    category: string
    mustAvoidHorizontalScroll: boolean
  }>
}

const expected = [
  ['index.html', '/', '工作台', 'launcher-overview'],
  ['chat.html', '/chat', '对话', 'screen'],
  ['agents.html', '/agents', 'Agent', 'screen'],
  ['library.html', '/library', '资源库', 'screen'],
  ['skills.html', '/skills', 'Skill', 'screen'],
  ['mcp.html', '/mcp', 'MCP', 'screen'],
  ['connectors.html', '/connectors', '连接器', 'screen'],
  ['schedules.html', '/schedules', '定时任务', 'screen'],
  ['settings.html', '/settings', '设置', 'screen'],
] as const

describe('shell routes', () => {
  it('freezes the prototype commit and nine screens', () => {
    expect(PROTOTYPE_COMMIT).toBe('112fcae854b324d2e75fdf5c890083ef9c1d3d6e')
    expect(SHELL_ROUTES).toHaveLength(9)
    expect(new Set(SHELL_ROUTES.map((route) => route.path)).size).toBe(9)
  })

  it('maps every manifest HTML screen to exactly one route', () => {
    const manifestFiles = manifest.sourceFiles.html.slice().sort()
    const routeFiles = SHELL_ROUTES.map((route) => route.source).sort()
    expect(routeFiles).toEqual(manifestFiles)

    for (const [source, path, title, role] of expected) {
      const route = SHELL_ROUTES.find((candidate) => candidate.source === source)
      expect(route, `route for ${source}`).toBeDefined()
      expect(route?.path).toBe(path)
      expect(route?.title).toBe(title)
      expect(route?.role).toBe(role)
      expect(route?.marker).toBe(`R0-01 placeholder · ${path}`)
    }
  })

  it('matches the manifest screen roles', () => {
    for (const screen of manifest.screens) {
      const route = SHELL_ROUTES.find((candidate) => candidate.source === screen.file)
      expect(route, `manifest screen ${screen.file}`).toBeDefined()
      expect(route?.role).toBe(screen.role)
    }
  })

  it('keeps the prototype navigation grouping', () => {
    expect(PRIMARY_ROUTES.map((route) => route.navLabel)).toEqual(['工作台', '对话', 'Agent'])
    expect(TOOL_ROUTES.map((route) => route.navLabel)).toEqual([
      '资源库',
      'Skill',
      'MCP',
      '连接器',
      '定时任务',
      '设置',
    ])
    expect(resolveRoute('/chat')?.id).toBe('chat')
    expect(resolveRoute('/does-not-exist')).toBeUndefined()
  })

  it('freezes the nine responsive viewports from the manifest', () => {
    expect(VIEWPORTS).toEqual(manifest.responsiveViewports)
    expect(VIEWPORTS).toHaveLength(9)
    for (const viewport of VIEWPORTS) {
      expect(viewport.width).toBeGreaterThanOrEqual(360)
      expect(viewport.mustAvoidHorizontalScroll).toBe(true)
    }
  })
})
