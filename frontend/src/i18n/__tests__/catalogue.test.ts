import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { CATALOGUES, MESSAGE_KEYS, formatMessage, translate } from '../catalogue'

const srcRoot = resolve(process.cwd(), 'src')

function filesUnder(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) {
      if (entry.name === '__tests__') continue
      out.push(...filesUnder(path))
    } else if (/\.(ts|tsx)$/.test(entry.name) && !/\.test\.(ts|tsx)$/.test(entry.name)) {
      out.push(path)
    }
  }
  return out
}

describe('i18n catalogue', () => {
  it('has identical key sets and no duplicate keys', () => {
    const zh = Object.keys(CATALOGUES['zh-CN']).sort()
    const en = Object.keys(CATALOGUES['en-US']).sort()
    expect(en).toEqual(zh)
    expect(new Set(MESSAGE_KEYS).size).toBe(MESSAGE_KEYS.length)
  })

  it('throws instead of silently returning a key when a message is missing', () => {
    expect(() => translate('zh-CN', 'does.not.exist' as never)).toThrow(/missing i18n key/)
  })

  it('interpolates only known parameters', () => {
    expect(formatMessage('Hello {name}', { name: 'Anvil' })).toBe('Hello Anvil')
    expect(formatMessage('Keep {unknown}', {})).toBe('Keep {unknown}')
  })

  it('has no Chinese characters in the English catalogue', () => {
    for (const [key, value] of Object.entries(CATALOGUES['en-US'])) {
      expect(value, `en-US ${key}`).not.toMatch(/[\u4e00-\u9fff]/)
    }
  })

  it('defines every route/i18n key and every literal t() key used in code', () => {
    const routes = JSON.parse(readFileSync(join(srcRoot, 'shell', 'routes.json'), 'utf8')) as {
      screens: Array<{ navKey: string; titleKey: string; breadcrumbKey: string }>
    }
    const keys = new Set(MESSAGE_KEYS)
    for (const route of routes.screens) {
      expect(keys.has(route.navKey as never), route.navKey).toBe(true)
      expect(keys.has(route.titleKey as never), route.titleKey).toBe(true)
      if (route.breadcrumbKey) expect(keys.has(route.breadcrumbKey as never), route.breadcrumbKey).toBe(true)
    }

    const literalKeys: string[] = []
    for (const file of filesUnder(join(srcRoot, 'shell')).concat(filesUnder(join(srcRoot, 'pages')), filesUnder(join(srcRoot, 'preferences')))) {
      if (!statSync(file).isFile()) continue
      const body = readFileSync(file, 'utf8')
      for (const match of body.matchAll(/\bt\(\s*['"]([a-zA-Z0-9_.-]+)['"]/g)) {
        literalKeys.push(match[1])
      }
    }
    expect(literalKeys.length).toBeGreaterThan(0)
    for (const key of literalKeys) {
      expect(keys.has(key as never), `missing t() key ${key}`).toBe(true)
    }
  })

  it('ships a bilingual catalogue document', () => {
    const doc = readFileSync(join(srcRoot, 'i18n', 'CATALOGUE.md'), 'utf8')
    for (const key of ['nav.workbench', 'settings.language.title', 'placeholder.description']) {
      expect(doc).toContain(key)
    }
  })
})
