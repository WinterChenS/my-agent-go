import { readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { TOKEN_GROUPS } from '../tokens'

const srcRoot = resolve(process.cwd(), 'src')
const css = readFileSync(join(srcRoot, 'design', 'tokens.css'), 'utf8')

function varsFor(selector: string): Record<string, string> {
  const start = css.indexOf(selector)
  if (start < 0) throw new Error(`missing block ${selector}`)
  const open = css.indexOf('{', start)
  const close = css.indexOf('}', open)
  if (open < 0 || close < 0) throw new Error(`unterminated block ${selector}`)
  const body = css.slice(open + 1, close)
  const values: Record<string, string> = {}
  for (const match of body.matchAll(/(--[a-z0-9-]+):\s*([^;]+);/g)) {
    values[match[1]] = match[2].trim()
  }
  return values
}

const light = varsFor(':root')
const dark = varsFor("[data-theme='dark']")

describe('dark theme tokens', () => {
  it('defines every colour token in both themes', () => {
    for (const name of TOKEN_GROUPS.color) {
      expect(light[name], `light ${name}`).toBeTruthy()
      expect(dark[name], `dark ${name}`).toBeTruthy()
    }
  })

  it('keeps non-colour tokens byte-identical between themes', () => {
    for (const group of ['radius', 'shadow', 'spacing', 'typography', 'motion', 'layout'] as const) {
      for (const name of TOKEN_GROUPS[group]) {
        expect(light[name], `light ${name}`).toBeTruthy()
        expect(dark[name], `dark ${name} must not be redefined`).toBeUndefined()
      }
    }
  })

  it('changes only colour tokens in the dark block', () => {
    const colour = new Set<string>(TOKEN_GROUPS.color)
    for (const name of Object.keys(dark)) {
      expect(colour.has(name), `${name} is not a colour token`).toBe(true)
    }
  })

  it('sets the colour-scheme per theme', () => {
    expect(css).toContain('color-scheme: light;')
    expect(css).toContain('color-scheme: dark;')
  })

  it('documents the dark-theme derivation and contrast', () => {
    const docs = readFileSync(join(srcRoot, 'design', 'DARK-THEME.md'), 'utf8')
    expect(docs).toContain('oklch(16% 0.012 255)')
    expect(docs).toContain('16.8:1')
    expect(docs.toLowerCase()).toContain('contrast')
  })
})
