// Provider sign-in helper.
//
// The desktop app cannot run `pi auth login <provider>`: pi refuses without an
// interactive terminal, and re-implementing nine OAuth flows (PKCE, callback
// servers, device codes, token exchange) would drift from pi on its next
// release. This script imports the very module pi's CLI uses, runs the
// provider's own login flow, and speaks a line-delimited JSON protocol with the
// host:
//
//   stdout  {"type":"prompt","id":"1","kind":"secret","message":"…"}
//           {"type":"notify","event":{"type":"auth_url","url":"https://…"}}
//           {"type":"credential","value":{"type":"oauth","refresh":…}}
//           {"type":"error","message":"…"}
//   stdin   {"id":"1","value":"…"}
//
// The host writes the returned credential to `~/.pi/agent/auth.json`; this
// script never touches a file, so there is exactly one place that owns the
// credential store.

import { createInterface } from 'node:readline'
import { pathToFileURL } from 'node:url'

function argOf(name) {
  const index = process.argv.indexOf(name)
  return index >= 0 ? process.argv[index + 1] : undefined
}

const modulePath = argOf('--module')
const providerId = argOf('--provider')

/** One event line on stdout. Anything else would corrupt the protocol. */
function send(payload) {
  process.stdout.write(`${JSON.stringify(payload)}\n`)
}

function fail(message) {
  send({ type: 'error', message })
  process.exit(1)
}

if (!modulePath || !providerId) {
  fail('usage: pi-auth.mjs --module <pi-ai providers/all.js> --provider <id>')
}

const pending = new Map()
let sequence = 0

const lines = createInterface({ input: process.stdin, terminal: false })
lines.on('line', (line) => {
  const text = line.trim()
  if (!text) return
  let message
  try {
    message = JSON.parse(text)
  } catch {
    return
  }
  const resolve = pending.get(String(message.id))
  if (!resolve) return
  pending.delete(String(message.id))
  resolve(String(message.value ?? ''))
})

/** Ask the host for one value and wait for its answer on stdin. */
function ask(prompt) {
  const id = String(++sequence)
  const payload = {
    type: 'prompt',
    id,
    kind: prompt.type,
    message: prompt.message ?? '',
  }
  if (prompt.placeholder) payload.placeholder = prompt.placeholder
  if (Array.isArray(prompt.options)) {
    payload.options = prompt.options.map((option) => ({
      id: option.id,
      label: option.label,
      description: option.description ?? null,
    }))
  }
  send(payload)
  return new Promise((resolve) => pending.set(id, resolve))
}

const controller = new AbortController()

// SIGTERM means the host cancelled: stop the flow and exit without a credential.
process.on('SIGTERM', () => {
  controller.abort()
  process.exit(2)
})

const interaction = {
  signal: controller.signal,
  prompt: ask,
  notify: (event) => send({ type: 'notify', event }),
}

async function main() {
  let builtinProviders
  try {
    const module = await import(pathToFileURL(modulePath).href)
    builtinProviders = module.builtinProviders
  } catch (error) {
    fail(`cannot load pi's provider catalogue: ${error.message}`)
  }
  if (typeof builtinProviders !== 'function') {
    fail(`${modulePath} does not export builtinProviders(); is this the pi-ai providers bundle?`)
  }

  const provider = builtinProviders().find((entry) => entry.id === providerId)
  if (!provider) fail(`pi does not ship a provider called "${providerId}"`)

  const oauth = provider.auth && provider.auth.oauth
  if (!oauth) fail(`${providerId} has no browser sign-in; save an API key instead`)

  let credential
  try {
    // `{}` rather than no argument: some flows read login options (device id,
    // agent name) and dereference the object without guarding it.
    credential = await oauth.login(interaction, {})
  } catch (error) {
    if (controller.signal.aborted) {
      process.exit(2)
    }
    fail(error && error.message ? error.message : String(error))
  }
  if (!credential || typeof credential !== 'object') {
    fail('the sign-in flow finished without a credential')
  }
  send({ type: 'credential', value: credential })
  process.exit(0)
}

main()
