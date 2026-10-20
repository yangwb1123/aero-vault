#!/usr/bin/env node
// web-smoke.mjs — zero-dependency headless-Chrome smoke driver for the embedded
// Aero Vault web UI.
//
// It speaks the Chrome DevTools Protocol over Node's built-in WebSocket so the
// frontend delivery gate (make web-check → make build → scripts/web-local-smoke.sh)
// needs no extra npm package beyond what web/ already installs.
//
// Per page it fails the run on:
//   * a non-2xx subresource — a stale index.html asset hash, a missing
//     runtime-config.js or a broken /ui/app/ base path is caught here;
//   * a console error or an uncaught exception (runtime breakage);
//   * a failed request (blocked/mixed-content/404 without a response);
//   * a React shell that never mounted into #root, a wrong document title, or a
//     missing window.__AERO_VAULT_WEB_CONFIG__ runtime config.
//
// usage: web-smoke.mjs --base-url <origin> --chrome <path> [--path /ui/]...
//        [--timeout ms] [--settle ms]

import { spawn } from 'node:child_process'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import process from 'node:process'
import { navigateAndLoad } from './web-smoke-page.mjs'

const USAGE = 'usage: web-smoke.mjs --base-url <origin> --chrome <path> [--path /ui/] [--timeout ms] [--settle ms]'

// Fatal finding kinds. Console warnings are reported but never fail the gate:
// third-party UI kits legitimately warn while hydrating.
const FATAL_KINDS = new Set(['console-error', 'page-exception', 'http-error', 'request-failed'])

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

function parseArgs(argv) {
  const opts = { paths: [], timeoutMs: 20_000, settleMs: 1_200, launchMs: 30_000 }
  const need = (key, i) => {
    const value = argv[i + 1]
    if (value === undefined) throw new Error(`${key} requires a value\n${USAGE}`)
    return value
  }
  for (let i = 0; i < argv.length; i++) {
    const key = argv[i]
    switch (key) {
      case '--base-url': opts.baseURL = need(key, i++).replace(/\/+$/, ''); break
      case '--chrome': opts.chrome = need(key, i++); break
      case '--path': opts.paths.push(need(key, i++)); break
      case '--timeout': opts.timeoutMs = Number(need(key, i++)); break
      case '--settle': opts.settleMs = Number(need(key, i++)); break
      case '--help': console.log(USAGE); process.exit(0); break
      default: throw new Error(`unknown argument ${key}\n${USAGE}`)
    }
  }
  if (!opts.baseURL || !opts.chrome) throw new Error(`--base-url and --chrome are required\n${USAGE}`)
  if (opts.paths.length === 0) opts.paths.push('/ui/')
  return opts
}

// ── Chrome DevTools Protocol client ────────────────────────────────────────

class CDP {
  #ws
  #nextId = 1
  #pending = new Map()
  #listeners = new Map()

  static async connect(url, timeoutMs) {
    const ws = new WebSocket(url)
    await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('timed out connecting to DevTools')), timeoutMs)
      const fail = () => { clearTimeout(timer); reject(new Error('DevTools websocket failed')) }
      ws.addEventListener('open', () => { clearTimeout(timer); resolve() }, { once: true })
      ws.addEventListener('error', fail, { once: true })
    })
    return new CDP(ws)
  }

  constructor(ws) {
    this.#ws = ws
    ws.addEventListener('message', (event) => this.#dispatch(String(event.data)))
    ws.addEventListener('close', () => {
      for (const slot of this.#pending.values()) slot.reject(new Error('DevTools connection closed'))
      this.#pending.clear()
    })
  }

  #dispatch(raw) {
    let message
    try { message = JSON.parse(raw) } catch { return }
    if (message.id !== undefined) {
      const slot = this.#pending.get(message.id)
      if (!slot) return
      this.#pending.delete(message.id)
      if (message.error) slot.reject(new Error(`${slot.method} failed: ${message.error.message}`))
      else slot.resolve(message.result)
      return
    }
    for (const listener of this.#listeners.get(message.method) ?? []) listener(message.params ?? {}, message.sessionId)
  }

  on(method, listener) {
    if (!this.#listeners.has(method)) this.#listeners.set(method, new Set())
    this.#listeners.get(method).add(listener)
    return () => this.#listeners.get(method).delete(listener)
  }

  once(method, sessionId, timeoutMs) {
    return new Promise((resolve, reject) => {
      const off = this.on(method, (params, from) => {
        if (from !== sessionId) return
        clearTimeout(timer)
        off()
        resolve(params)
      })
      const timer = setTimeout(() => { off(); reject(new Error(`timed out waiting for ${method}`)) }, timeoutMs)
    })
  }

  send(method, params = {}, sessionId) {
    const id = this.#nextId++
    const message = sessionId ? { id, method, params, sessionId } : { id, method, params }
    return new Promise((resolve, reject) => {
      this.#pending.set(id, { resolve, reject, method })
      this.#ws.send(JSON.stringify(message))
    })
  }

  close() { try { this.#ws.close() } catch { /* already closed */ } }
}

// ── Chrome process ─────────────────────────────────────────────────────────

function launchChrome(bin, profileDir) {
  const args = [
    '--headless=new',
    // This fresh, disposable profile never receives credentials. Avoid desktop
    // keyring initialization blocking its first HTTP request in headless CI.
    '--password-store=basic',
    '--remote-debugging-port=0',
    `--user-data-dir=${profileDir}`,
    '--no-first-run',
    '--no-default-browser-check',
    '--disable-extensions',
    '--disable-component-update',
    '--disable-background-networking',
    '--disable-background-timer-throttling',
    '--disable-renderer-backgrounding',
    '--disable-sync',
    '--disable-dev-shm-usage',
    '--disable-features=Translate,OptimizationHints,MediaRouter,AcceptCHFrame',
    '--hide-scrollbars',
    '--mute-audio',
    '--window-size=1440,900',
  ]
  // Chromium refuses to start its sandbox as root; CI containers and some
  // sandboxes run hooks as root, where the setuid sandbox is unavailable.
  if (typeof process.getuid === 'function' && process.getuid() === 0) {
    args.push('--no-sandbox', '--disable-setuid-sandbox')
  }
  args.push('about:blank')
  return spawn(bin, args, { stdio: ['ignore', 'pipe', 'pipe'] })
}

async function waitForDevToolsEndpoint(child, timeoutMs) {
  return new Promise((resolve, reject) => {
    let buffered = ''
    const timer = setTimeout(() => {
      reject(new Error(`chrome did not report a DevTools endpoint within ${timeoutMs}ms`))
    }, timeoutMs)
    child.stderr.on('data', (chunk) => {
      buffered += chunk
      const match = buffered.match(/DevTools listening on (ws:\/\/\S+)/)
      if (!match) return
      clearTimeout(timer)
      resolve(match[1])
    })
    child.once('exit', (code) => {
      clearTimeout(timer)
      reject(new Error(`chrome exited before DevTools was ready (code ${code})\n${buffered.trim()}`))
    })
  })
}

// ── Page probing ───────────────────────────────────────────────────────────

const PROBE = `(() => {
  const root = document.getElementById('root')
  const cfg = window.__AERO_VAULT_WEB_CONFIG__ || null
  const shell = document.querySelector('#root > *')
  return {
    title: document.title,
    lang: document.documentElement.lang,
    rootChildren: root ? root.childElementCount : -1,
    shellTag: shell ? shell.tagName.toLowerCase() : '',
    shellClass: shell ? String(shell.className || '') : '',
    text: root ? (root.innerText || '').trim().slice(0, 120) : '',
    apiBase: cfg ? cfg.apiBase : null,
    oidcLoginPath: cfg ? cfg.oidcLoginPath : null,
    scripts: Array.from(document.querySelectorAll('script[src]')).map((el) => el.getAttribute('src')),
    styles: Array.from(document.querySelectorAll('link[rel="stylesheet"]')).map((el) => el.getAttribute('href')),
    styled: shell ? getComputedStyle(shell).display !== 'inline' : false,
  }
})()`

const MOUNTED = `(() => {
  const root = document.getElementById('root')
  return !!(root && root.childElementCount > 0 && (root.textContent || '').trim().length > 0)
})()`

async function evaluate(cdp, sessionId, expression) {
  const result = await cdp.send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true }, sessionId)
  if (result.exceptionDetails) {
    throw new Error(result.exceptionDetails.exception?.description ?? result.exceptionDetails.text ?? 'evaluate failed')
  }
  return result.result?.value
}

async function waitForMount(cdp, sessionId, timeoutMs) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if ((await evaluate(cdp, sessionId, MOUNTED)) === true) return true
    await sleep(150)
  }
  return false
}

// ── Smoke run ──────────────────────────────────────────────────────────────

async function checkPage(cdp, sessionId, url, opts) {
  try {
    await navigateAndLoad(cdp, sessionId, url, opts.timeoutMs)
  } catch (error) {
    return { url, ok: false, failures: [`load event never fired (${error.message})`] }
  }
  await sleep(opts.settleMs)
  const mounted = await waitForMount(cdp, sessionId, opts.timeoutMs)
  const probe = await evaluate(cdp, sessionId, PROBE).catch((error) => ({ error: error.message }))

  const failures = []
  if (!mounted) failures.push('React shell never mounted into #root')
  if (probe.error) failures.push(`probe failed: ${probe.error}`)
  if (probe.title !== 'Aero Vault') failures.push(`unexpected document title ${JSON.stringify(probe.title)}`)
  if (probe.rootChildren < 1) failures.push(`#root has no children (rootChildren=${probe.rootChildren})`)
  if (probe.apiBase !== '/v1') failures.push(`runtime config apiBase=${JSON.stringify(probe.apiBase)} (want "/v1")`)
  if (probe.lang !== 'zh-CN') failures.push(`unexpected document lang ${JSON.stringify(probe.lang)}`)
  if (!probe.styled) failures.push('stylesheet not applied (shell display is inline or unstyled)')
  if (probe.scripts.length === 0) failures.push('no script[src] found in the served shell')
  return { url, ok: failures.length === 0, failures, probe }
}

async function main() {
  const opts = parseArgs(process.argv.slice(2))
  const profileDir = await mkdtemp(join(tmpdir(), 'aero-web-smoke-'))
  const chrome = launchChrome(opts.chrome, profileDir)
  const chromeLog = []
  chrome.stderr.on('data', (chunk) => chromeLog.push(String(chunk)))
  chrome.stdout.resume()

  const findings = []
  const seen = new Set()
  const responses = []
  const requests = new Map()
  let phase = 'startup'
  const record = (kind, detail) => {
    const key = `${phase}|${kind}|${detail}`
    if (seen.has(key)) return
    seen.add(key)
    findings.push({ phase, kind, detail })
  }

  let cdp
  try {
    const endpoint = await waitForDevToolsEndpoint(chrome, opts.launchMs)
    cdp = await CDP.connect(endpoint, opts.launchMs)
    const { targetId } = await cdp.send('Target.createTarget', { url: 'about:blank' })
    const { sessionId } = await cdp.send('Target.attachToTarget', { targetId, flatten: true })

    cdp.on('Runtime.consoleAPICalled', (params, from) => {
      if (from !== sessionId) return
      const text = (params.args ?? []).map((arg) => arg.value ?? arg.description ?? arg.type).join(' ')
      if (params.type === 'error') record('console-error', text || '(empty console.error)')
      else if (params.type === 'warning') record('console-warning', text || '(empty console.warn)')
    })
    cdp.on('Runtime.exceptionThrown', (params, from) => {
      if (from !== sessionId) return
      const details = params.exceptionDetails ?? {}
      record('page-exception', details.exception?.description ?? details.text ?? 'uncaught exception')
    })
    cdp.on('Network.requestWillBeSent', (params, from) => {
      if (from === sessionId) requests.set(params.requestId, params.request?.url ?? '')
    })
    cdp.on('Network.responseReceived', (params, from) => {
      if (from !== sessionId) return
      const { status, url, mimeType } = params.response
      responses.push({ phase, url, status, mimeType })
      if (status >= 400) record('http-error', `HTTP ${status} ${url}`)
    })
    cdp.on('Network.loadingFailed', (params, from) => {
      if (from !== sessionId) return
      const url = requests.get(params.requestId) ?? params.requestId
      record('request-failed', `${url} (${params.errorText}, ${params.type})`)
    })
    cdp.on('Log.entryAdded', (params, from) => {
      if (from !== sessionId) return
      const entry = params.entry ?? {}
      if (entry.level !== 'error') return
      if (entry.source === 'network') return // already covered by Network.responseReceived
      record('log-error', `${entry.source}: ${entry.text}${entry.url ? ` (${entry.url})` : ''}`)
    })

    for (const domain of ['Page', 'Runtime', 'Network', 'Log']) {
      await cdp.send(`${domain}.enable`, {}, sessionId)
    }

    const pages = []
    for (const path of opts.paths) {
      phase = path
      const page = await checkPage(cdp, sessionId, `${opts.baseURL}${path}`, opts)
      pages.push(page)
      console.log(`  ${page.ok ? '✅' : '❌'} ${path} ${page.probe?.title === 'Aero Vault' ? `“${page.probe.shellClass || page.probe.shellTag}”` : ''}`.trimEnd())
    }

    const failures = pages.flatMap((page) => page.failures.map((detail) => `${page.url}: ${detail}`))
    const runtimeFailures = findings.filter((finding) => FATAL_KINDS.has(finding.kind)).map((f) => `${f.phase}: ${f.detail}`)
    const warnings = findings.filter((finding) => !FATAL_KINDS.has(finding.kind))

    for (const warning of warnings) console.log(`  ⚠️  ${warning.phase}: ${warning.kind}: ${warning.detail}`)
    if (failures.length === 0 && runtimeFailures.length === 0) {
      const assets = responses.filter((r) => r.phase === opts.paths[0] && r.url.includes('/ui/app/assets/'))
      const kinds = new Set(assets.map((a) => a.mimeType))
      console.log(`  📦 ${responses.length} responses, ${assets.length} bundle/style assets (${[...kinds].join(', ') || 'none'})`)
      console.log('✅ headless UI smoke passed')
      return 0
    }
    for (const failure of [...failures, ...runtimeFailures]) console.error(`  ❌ ${failure}`)
    console.error('❌ headless UI smoke failed')
    // Chrome's own stderr is only diagnostic here: on a passing run it is
    // dominated by unrelated sandbox/inotify noise from the host.
    const tail = chromeLog.join('').trim().split('\n').slice(-8).join('\n')
    if (tail) console.error(`--- chrome stderr (tail) ---\n${tail}`)
    return 1
  } finally {
    cdp?.close()
    chrome.kill('SIGKILL')
    await rm(profileDir, { recursive: true, force: true }).catch(() => {})
  }
}

try {
  process.exitCode = await main()
} catch (error) {
  console.error(`❌ web smoke driver error: ${error.message}`)
  process.exitCode = 2
}
