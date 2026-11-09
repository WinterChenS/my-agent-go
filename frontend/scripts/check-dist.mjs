// R0-01 build assertion: dist/index.html must reference existing local assets.
import { existsSync, readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

const dist = resolve(process.cwd(), 'dist')
const indexPath = join(dist, 'index.html')
if (!existsSync(indexPath)) {
  console.error(`check-dist: missing ${indexPath}; run npm run build first`)
  process.exit(1)
}
const html = readFileSync(indexPath, 'utf8')
if (!html.includes('id="root"')) {
  console.error('check-dist: index.html has no #root mount')
  process.exit(1)
}
const refs = [...html.matchAll(/(?:src|href)="([^"]+)"/g)].map((match) => match[1])
const local = refs.filter((ref) => ref.startsWith('/assets/') || ref.startsWith('./assets/'))
if (local.length === 0) {
  console.error('check-dist: index.html references no built assets')
  process.exit(1)
}
for (const ref of local) {
  const file = join(dist, ref.replace(/^\//, ''))
  if (!existsSync(file)) {
    console.error(`check-dist: referenced asset is missing: ${ref}`)
    process.exit(1)
  }
}
if (!local.some((ref) => ref.endsWith('.js'))) {
  console.error('check-dist: no JavaScript bundle referenced')
  process.exit(1)
}
console.log(`check-dist: index.html + ${local.length} asset reference(s) verified`)
