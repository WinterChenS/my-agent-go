// Capture same-condition prototype screenshots with the locally installed Edge.
// Zero dependencies: Edge headless writes a PNG for a fixed window size.
//
// Edge occasionally exits with null/non-zero after writing a valid PNG. A
// written file above the sanity threshold is therefore accepted and the exit
// code is logged as tolerated. Missing/undersized files are retried once as a
// whole suite; only a second failure exits non-zero.
import { existsSync, mkdirSync, rmSync, statSync } from 'node:fs'
import { resolve, join } from 'node:path'
import { spawnSync } from 'node:child_process'
import { pathToFileURL } from 'node:url'

const repo = resolve(process.cwd(), '..')
const args = process.argv.slice(2)
const outIndex = args.indexOf('--out')
const outDir = outIndex >= 0 && args[outIndex + 1]
  ? resolve(args[outIndex + 1])
  : join(repo, '.tmp', 'test', 'prototype')

const MIN_BYTES = 10 * 1024
const MAX_ATTEMPTS = 2
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
const screens = ['index', 'settings', 'chat', 'agents', 'skills']
const sizes = [
  { width: 1440, height: 900 },
  { width: 390, height: 844 },
  { width: 360, height: 800 },
]
const profile = join(repo, '.tmp', 'test', 'prototype', 'edge-profile')
mkdirSync(outDir, { recursive: true })
mkdirSync(profile, { recursive: true })
if (!existsSync(join(prototype, 'index.html'))) {
  console.error(`capture-prototype: prototype not found at ${prototype}`)
  process.exit(1)
}

function fileState(path) {
  const exists = existsSync(path)
  const bytes = exists ? statSync(path).size : 0
  return { exists, bytes, valid: exists && bytes > MIN_BYTES }
}

function captureOnce(screen, size) {
  const file = join(outDir, `proto-${screen}-${size.width}x${size.height}.png`)
  const url = pathToFileURL(join(prototype, `${screen}.html`)).href
  rmSync(file, { force: true })
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
  const state = fileState(file)
  const exit = result.status
  const tolerated = exit !== 0 && state.valid
  const ok = state.valid
  const detail = `screen=${screen} size=${size.width}x${size.height} edgeExit=${exit} fileExists=${state.exists} fileSize=${state.bytes} result=${tolerated ? 'tolerated' : ok ? 'ok' : 'failed'}`
  console.log(`capture-prototype: ${detail}`)
  if (tolerated) {
    console.log(`capture-prototype: tolerated Edge exit ${exit} because ${file} is ${state.bytes} bytes (>${MIN_BYTES})`)
  }
  if (!ok) {
    console.error(`capture-prototype: ${detail} (expected ${file} to be >${MIN_BYTES} bytes)`)
  }
  return { ok, tolerated, detail, exit, file, state }
}

let lastAttempts = []
for (let attempt = 1; attempt <= MAX_ATTEMPTS; attempt += 1) {
  const results = []
  let failed = false
  for (const screen of screens) {
    for (const size of sizes) {
      const result = captureOnce(screen, size)
      results.push(result)
      if (!result.ok) failed = true
    }
  }
  lastAttempts.push(results)
  if (!failed) process.exit(0)
  if (attempt < MAX_ATTEMPTS) {
    const failedDetails = results.filter((result) => !result.ok).map((result) => result.detail).join('; ')
    console.error(`capture-prototype: attempt ${attempt} failed (${failedDetails}); retrying the full suite once`)
  }
}

const failureDetails = lastAttempts.flatMap((results, index) =>
  results.filter((result) => !result.ok).map((result) => `attempt=${index + 1} ${result.detail}`),
)
console.error(`capture-prototype: failed after ${MAX_ATTEMPTS} attempts: ${failureDetails.join('; ')}`)
process.exit(1)
