import assert from 'node:assert/strict'
import test from 'node:test'
import { navigateAndLoad } from './web-smoke-page.mjs'

const never = () => new Promise(() => {})

test('navigation subscribes before send and waits for both navigation and load', async () => {
  const calls = []
  let finishLoad
  let finishNavigation
  const cdp = {
    once: (...args) => { calls.push(['once', ...args]); return new Promise((resolve) => { finishLoad = resolve }) },
    send: (...args) => { calls.push(['send', ...args]); return new Promise((resolve) => { finishNavigation = resolve }) },
  }
  let settled = false
  const running = navigateAndLoad(cdp, 'session', 'http://vault/ui/', 25).then(() => { settled = true })
  await Promise.resolve()
  assert.deepEqual(calls, [
    ['once', 'Page.loadEventFired', 'session', 25],
    ['send', 'Page.navigate', { url: 'http://vault/ui/' }, 'session'],
  ])
  finishLoad({})
  await Promise.resolve()
  assert.equal(settled, false)
  finishNavigation({})
  await running
  assert.equal(settled, true)
})

test('load timeout while navigation stalls rejects normally and reaches cleanup', async () => {
  let cleaned = false
  const failure = new Error('load deadline')
  try {
    await assert.rejects(navigateAndLoad({ once: async () => { throw failure }, send: never }, 'session', '/ui/', 25), failure)
  } finally { cleaned = true }
  assert.equal(cleaned, true)
})

test('navigation rejection is not a successful load or an unhandled rejection', async () => {
  const failure = new Error('navigation failed')
  const cdp = { once: never, send: async () => { throw failure } }
  await assert.rejects(navigateAndLoad(cdp, 'session', '/ui/', 25), failure)
})

test('a synchronous navigation send failure is observed alongside load failure', async () => {
  const cdp = { once: async () => { throw new Error('load failed') }, send: () => { throw new Error('send failed') } }
  await assert.rejects(navigateAndLoad(cdp, 'session', '/ui/', 25))
})
