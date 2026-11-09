// Capture same-condition prototype screenshots with the locally installed Edge.
// Zero dependencies: Edge headless writes a PNG for a fixed window size.
import { existsSync, mkdirSync, statSync } from 'node:fs'
import { resolve, join } from 'node:path'
import { spawnSync } from 'node:child_process'
import { pathToFileURL } from 'node:url'

const repo = resolve(process.cwd(), '..')
const args = process.argv.slice(2)
const outIndex = args.indexOf('--out')
const outDir = outIndex >= 0 && args[outIndex + 1]
  ? resolve(args[outIndex + 1])
  : join(repo, '.tmp', 'test', 'prototype')

const candidates = [
  process.env.EDGE_BIN,
  'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe',
  'C:/Program Files/Microsoft/Edge/Application/msedge.exe',
].filter(Boolean)
const edge = candidates.find((candidate) => existsSync(candidate))
if (!edge) {
  console.error('capture-prototype: Microsoft Edge was not found (set EDGE_BIN)')
  process.exit(1)
}

const prototype = join(repo, 'docs', 'ui', 'Agent-客户端产品原型')
const screens = ['index', 'settings']
const sizes = [
  { width: 1440, height: 900 },
  { width: 390, height: 844 },
]
const profile = join(repo, '.tmp', 'test', 'prototype', 'edge-profile')
mkdirSync(outDir, { recursive: true })
mkdirSync(profile, { recursive: true })
if (!existsSync(join(prototype, 'index.html'))) {
  console.error(`capture-prototype: prototype not found at ${prototype}`)
  process.exit(1)
}

for (const screen of screens) {
  for (const size of sizes) {
    const file = join(outDir, `proto-${screen}-${size.width}x${size.height}.png`)
    const url = pathToFileURL(join(prototype, `${screen}.html`)).href
    const result = spawnSync(edge, [
      '--headless=new',
      '--disable-gpu',
      '--hide-scrollbars',
      '--force-device-scale-factor=1',
      '--no-first-run',
      `--window-size=${size.width},${size.height}`,
      '--virtual-time-budget=5000',
      `--user-data-dir=${profile}`,
      `--screenshot=${file}`,
      url,
    ], { stdio: 'inherit', timeout: 120000 })
    if (result.status !== 0) {
      console.error(`capture-prototype: Edge exited ${result.status ?? 'null'} for ${screen} ${size.width}x${size.height}`)
      process.exit(1)
    }
    if (!existsSync(file) || statSync(file).size < 1024) {
      console.error(`capture-prototype: screenshot missing or too small: ${file}`)
      process.exit(1)
    }
    console.log(`capture-prototype: ${file} (${statSync(file).size} bytes)`)
  }
}
