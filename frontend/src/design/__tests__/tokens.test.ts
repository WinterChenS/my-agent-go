import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { REQUIRED_TOKEN_GROUPS, TOKEN_GROUPS } from '../tokens'

const srcRoot = resolve(process.cwd(), 'src')
const tokensCss = readFileSync(join(srcRoot, 'design', 'tokens.css'), 'utf8')

function filesUnder(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) {
      if (entry.name === '__tests__') continue
      out.push(...filesUnder(path))
    } else {
      out.push(path)
    }
  }
  return out
}

describe('design tokens', () => {
  it('declares every required semantic token group', () => {
    expect(REQUIRED_TOKEN_GROUPS).toEqual([
      'color',
      'radius',
      'shadow',
      'spacing',
      'typography',
      'motion',
      'layout',
    ])
    for (const group of REQUIRED_TOKEN_GROUPS) {
      for (const name of TOKEN_GROUPS[group]) {
        expect(tokensCss, `${group} token ${name}`).toContain(`${name}:`)
      }
    }
  })

  it('keeps the prototype colours and geometry', () => {
    expect(tokensCss).toContain('--bg: oklch(99% 0.002 240);')
    expect(tokensCss).toContain('--accent: oklch(58% 0.18 255);')
    expect(tokensCss).toContain('--r-md: 8px;')
    expect(tokensCss).toContain('--sidebar-w: 236px;')
    expect(tokensCss).toContain('--sidebar-w-collapsed: 60px;')
  })

  it('documents the prototype source and the no-default-theme rule', () => {
    const readme = readFileSync(join(srcRoot, 'design', 'README.md'), 'utf8')
    expect(readme).toContain('112fcae854b324d2e75fdf5c890083ef9c1d3d6e')
    expect(readme).toContain('studio.css')
    expect(readme).toContain('pages.css')
    expect(readme.toLowerCase()).toContain('raw colour values')
  })

  it('rejects raw colour values in shell code and styles.css', () => {
    const shellDir = join(srcRoot, 'shell')
    const preferencesDir = join(srcRoot, 'preferences')
    const files = [
      ...filesUnder(shellDir).filter((path) => !/\.test\.(ts|tsx)$/.test(path)),
      ...filesUnder(preferencesDir).filter((path) => !/\.test\.(ts|tsx)$/.test(path)),
      join(srcRoot, 'styles.css'),
    ]
    const rawColour = /#[0-9a-fA-F]{3,8}\b|\b(?:rgb|rgba|hsl|hsla|oklch|oklab|lab|lch)\(/
    for (const file of files) {
      if (!statSync(file).isFile()) continue
      const body = readFileSync(file, 'utf8')
      const match = body.match(rawColour)
      expect(match, `${relative(srcRoot, file)} uses a raw colour: ${match?.[0] ?? ''}`).toBeNull()
    }
  })
})
