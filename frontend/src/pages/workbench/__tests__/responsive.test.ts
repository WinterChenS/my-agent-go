import { readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const css = readFileSync(join(resolve(process.cwd(), 'src', 'pages', 'workbench'), 'workbench.css'), 'utf8')

describe('workbench responsive rules', () => {
  it('uses the prototype 4 → 2 → 1 stats grid', () => {
    expect(css).toMatch(/\.wb-stat-grid\s*\{[^}]*grid-template-columns:\s*repeat\(4,\s*minmax\(0,\s*1fr\)\)/s)
    expect(css).toMatch(/@media \(max-width: 900px\)\s*\{[\s\S]*?\.wb-stat-grid\s*\{[^}]*repeat\(2,\s*minmax\(0,\s*1fr\)\)/)
    expect(css).toMatch(/@media \(max-width: 620px\)\s*\{[\s\S]*?\.wb-stat-grid\s*\{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\)/)
  })

  it('collapses the two-column workbench split below 1180px', () => {
    expect(css).toMatch(/@media \(max-width: 1180px\)\s*\{[\s\S]*?\.wb-split\s*\{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\)/)
  })

  it('has no fixed layout width above the compact 360px viewport', () => {
    for (const match of css.matchAll(/(?:^|[^-\w])(width|min-width):\s*(\d+)px/gm)) {
      expect(Number(match[2]), match[1]).toBeLessThanOrEqual(360)
    }
  })
})
