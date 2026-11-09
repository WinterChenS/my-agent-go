import { readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { BREAKPOINTS, VIEWPORTS } from '../routes'

const srcRoot = resolve(process.cwd(), 'src')
const tokensCss = readFileSync(join(srcRoot, 'design', 'tokens.css'), 'utf8')
const shellCss = readFileSync(join(srcRoot, 'shell', 'shell.css'), 'utf8')
const stylesCss = readFileSync(join(srcRoot, 'styles.css'), 'utf8')

describe('shell responsiveness', () => {
  it('defines the prototype breakpoints', () => {
    expect(BREAKPOINTS.shellCollapse).toBe(900)
    expect(BREAKPOINTS.compact).toBe(620)
    expect(shellCss).toContain(`@media (max-width: ${BREAKPOINTS.shellCollapse}px)`)
    expect(shellCss).toContain(`@media (max-width: ${BREAKPOINTS.compact}px)`)
  })

  it('has no fixed width or min-width above the 360px compact viewport', () => {
    const combined = `${tokensCss}\n${shellCss}\n${stylesCss}`
    const fixed = combined.matchAll(/(?:^|[^-\w])(width|min-width):\s*(\d+)px/gm)
    for (const match of fixed) {
      expect(Number(match[2]), `${match[1]} in shell CSS`).toBeLessThanOrEqual(360)
    }
    expect(tokensCss).toContain('--sidebar-w: 236px;')
    expect(tokensCss).toContain('--sidebar-w-collapsed: 60px;')
  })

  it('covers the nine manifest viewports with no horizontal scroll requirement', () => {
    expect(VIEWPORTS.map((viewport) => `${viewport.width}x${viewport.height}`)).toEqual([
      '360x800',
      '390x844',
      '430x932',
      '600x960',
      '820x1180',
      '1024x768',
      '1366x768',
      '1440x900',
      '1920x1080',
    ])
    for (const viewport of VIEWPORTS) {
      expect(viewport.mustAvoidHorizontalScroll).toBe(true)
    }
  })
})
