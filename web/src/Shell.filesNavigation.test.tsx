import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { IrisProvider } from '@iris-ui-kit/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { WebConfig } from './config'
import { Shell } from './Shell'
import { parseObjectRoute, routeFromHash } from './router'
import { vaultSkin } from './skin'

vi.mock('./auth/AuthProvider', () => ({ useAuth: () => ({ login: vi.fn(), logout: vi.fn() }) }))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
const config: WebConfig = {
  apiBase: '/v1', oidcLoginPath: '/auth/oidc/login', oidcLogoutPath: '/auth/oidc/logout', allowAnonymous: true,
}
const segment = ' 资料%2F+?# '
const path = `team/${segment}/`
const key = `${path}  report%25+?#.txt `
const location = { path, view: 'mine' as const, deleted: true }
let mounted: { container: HTMLDivElement; root: Root } | undefined
let restored: boolean
let restoreReply: () => Promise<Response>
let fetcher: ReturnType<typeof vi.fn>

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
}
function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve: (value: T) => void = () => undefined
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}
function file(): object {
  return { bucket: 'default', key, size: 4, etag: 'etag', backend: 'local',
    created_at: '2026-10-06T10:00:00Z', updated_at: '2026-10-06T10:00:00Z' }
}
function folderReply(url: URL): Response {
  const current = url.searchParams.get('path') ?? ''
  const name = current === '' ? 'team' : current === 'team/' ? segment : undefined
  return json({ prefix: current, items: name === undefined ? [] : [{ name, type: 'folder' }] })
}
async function reply(request: RequestInfo | URL): Promise<Response> {
  const url = new URL(String(request), 'http://localhost')
  if (url.pathname === '/v1/session') return json({ authenticated: false, tenant_id: 'default',
    principal_kind: 'anonymous', roles: [], groups: [], scopes: [] })
  if (url.pathname === '/v1/folders') return folderReply(url)
  if (url.pathname === '/v1/files') return json({ objects: url.searchParams.get('prefix') === path &&
    !(url.searchParams.get('deleted') && restored) ? [file()] : [], has_more: false })
  if (url.pathname.endsWith('/restore')) {
    expect(decodeURIComponent(url.pathname.slice('/v1/files/'.length, -'/restore'.length))).toBe(key)
    return restoreReply()
  }
  if (url.searchParams.get('representation') === 'content') {
    expect(decodeURIComponent(url.pathname.slice('/v1/files/'.length))).toBe(key)
    return new Response('bytes', { headers: { 'Content-Type': 'application/octet-stream' } })
  }
  throw new Error(`unexpected request ${url.pathname}`)
}
beforeEach(() => {
  restored = false
  restoreReply = async () => { restored = true; return json({ status: 'restored' }) }
  fetcher = vi.fn(reply)
  vi.stubGlobal('fetch', fetcher)
  window.history.replaceState(null, '', '#/files')
})
afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
  vi.unstubAllGlobals()
  window.history.replaceState(null, '', '#/')
})
function render(): HTMLDivElement {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  mounted = { container, root }
  act(() => root.render(<IrisProvider skin={vaultSkin}><Shell config={config} /></IrisProvider>))
  return container
}
function button(container: HTMLElement, label: string): HTMLButtonElement {
  const found = [...container.querySelectorAll('button')].find((item) => item.textContent?.trim() === label)
  if (!found) throw new Error(`missing button ${label}`)
  return found
}
async function settle(): Promise<void> {
  await act(async () => { await new Promise((resolve) => window.setTimeout(resolve, 0)) })
}
async function click(element: HTMLElement): Promise<void> {
  await act(async () => { element.click(); await Promise.resolve() })
  await settle()
  await settle()
}
async function historyMove(direction: 'back' | 'forward'): Promise<void> {
  await act(async () => {
    await new Promise<void>((resolve, reject) => {
      const changed = () => { window.clearTimeout(timer); resolve() }
      const timer = window.setTimeout(() => {
        window.removeEventListener('hashchange', changed)
        reject(new Error('history did not change'))
      }, 300)
      window.addEventListener('hashchange', changed, { once: true })
      window.history[direction]()
    })
  })
  await settle()
}
async function enterContext(container: HTMLElement): Promise<void> {
  await settle()
  await click(container.querySelector<HTMLElement>('.folder-open')!)
  await click(container.querySelector<HTMLElement>('.folder-open')!)
  await click(button(container, '我的文件'))
  await click(recycleToggle(container))
}
function recycleToggle(container: HTMLElement): HTMLElement {
  const toggle = container.querySelector<HTMLElement>('.deleted-toggle [role="switch"]')
    ?? container.querySelector<HTMLElement>('.deleted-toggle input')
  if (!toggle) throw new Error('missing recycle toggle')
  return toggle
}
function expectContext(container: HTMLElement): void {
  expect(container.querySelector<HTMLInputElement>('input[placeholder="目录路径，例如 team/docs/"]')?.value).toBe(path)
  expect(button(container, '我的文件').getAttribute('aria-pressed')).toBe('true')
  expect(container.querySelector('.deleted-toggle [role="switch"]')?.getAttribute('aria-checked')).toBe('true')
  expect(container.textContent).not.toContain('已选择')
  expect(container.textContent).not.toContain('关闭分享')
}
function currentObject(): ReturnType<typeof parseObjectRoute> {
  return parseObjectRoute(routeFromHash('overview'))
}

describe('Shell contextual object navigation', () => {
  it('retains exact nested directory, mine and recycle on return and real browser Back/Forward', async () => {
    const container = render()
    await enterContext(container)
    expectContext(container)
    await click(container.querySelector<HTMLElement>(`input[aria-label="选择 ${key}"]`)!)
    await click(button(container, '详情'))
    expect(currentObject()).toEqual({ key, deleted: true, returnTo: location })
    expect(container.querySelector('h1')?.textContent).toBe(key)
    await historyMove('back')
    expectContext(container)
    expect(container.querySelector('tbody')?.textContent).toContain(key)
    await historyMove('forward')
    expect(currentObject()).toEqual({ key, deleted: true, returnTo: location })
    await click(button(container, '返回文件'))
    expectContext(container)
    expect(container.querySelector('tbody')?.textContent).toContain(key)
    expect(container.querySelector<HTMLInputElement>(`input[aria-label="选择 ${key}"]`)?.checked).toBe(false)
  })

  it('restores directory/view/recycle with Back/Forward before an object is opened', async () => {
    const container = render()
    await enterContext(container)
    await historyMove('back')
    expect(button(container, '我的文件').getAttribute('aria-pressed')).toBe('true')
    expect(container.querySelector('.deleted-toggle [role="switch"]')?.getAttribute('aria-checked')).toBe('false')
    await historyMove('back')
    expect(button(container, '可访问文件').getAttribute('aria-pressed')).toBe('true')
    await historyMove('back')
    expect(container.querySelector<HTMLInputElement>('input[placeholder="目录路径，例如 team/docs/"]')?.value).toBe('team/')
    await historyMove('forward')
    await historyMove('forward')
    await historyMove('forward')
    expectContext(container)
    expect(container.querySelector('tbody')?.textContent).toContain(key)
  })

})

describe('Shell active and legacy object navigation', () => {
  it('returns to the exact active directory and mine view', async () => {
    const container = render()
    await enterContext(container)
    await click(recycleToggle(container))
    await click(button(container, '详情'))
    expect(currentObject()).toEqual({ key, deleted: false, returnTo: { ...location, deleted: false } })
    await click(button(container, '返回文件'))
    expect(container.querySelector<HTMLInputElement>('input[placeholder="目录路径，例如 team/docs/"]')?.value).toBe(path)
    expect(button(container, '我的文件').getAttribute('aria-pressed')).toBe('true')
    expect(recycleToggle(container).getAttribute('aria-checked')).toBe('false')
    expect(container.querySelector('tbody')?.textContent).toContain(key)
  })

  it.each([false, true])('retains legacy direct object links with deleted=%s and their root return', async (deleted) => {
    const legacy = `${deleted ? 'deleted-object/' : 'object/'}${key}`
    window.history.replaceState(null, '', `#/${encodeURIComponent(legacy)}`)
    const container = render()
    await settle()
    expect(container.querySelector('h1')?.textContent).toBe(key)
    expect(currentObject()).toEqual({ key, deleted })
    await click(button(container, '返回文件'))
    expect(routeFromHash('overview')).toBe('files')
    expect(container.querySelector<HTMLInputElement>('input[placeholder="目录路径，例如 team/docs/"]')?.value).toBe('')
    expect(button(container, '可访问文件').getAttribute('aria-pressed')).toBe('true')
    expect(recycleToggle(container).getAttribute('aria-checked')).toBe('false')
  })
})

describe('Shell contextual recovery navigation', () => {
  it('keeps the internal return location through recovery and refreshes the recycle rows', async () => {
    const container = render()
    await enterContext(container)
    await click(button(container, '详情'))
    await click(button(container, '恢复对象'))
    expect(currentObject()).toEqual({ key, deleted: false, returnTo: location })
    expect(container.textContent).toContain('活动对象')
    expect(container.textContent).not.toContain('恢复对象')
    await click(button(container, '返回文件'))
    expectContext(container)
    expect(container.querySelector('tbody')?.textContent).not.toContain(key)
    expect(container.textContent).toContain('没有匹配文件')
    expect([...container.querySelectorAll('button')].some((item) => item.textContent === '恢复')).toBe(false)
    expect(fetcher.mock.calls.filter(([request]) => String(request).endsWith('/restore'))).toHaveLength(1)
    await historyMove('back')
    expect(currentObject()).toEqual({ key, deleted: false, returnTo: location })
    await historyMove('forward')
    expectContext(container)
    expect(container.querySelector('tbody')?.textContent).not.toContain(key)
  })

  it('does not navigate on a late recovery after returning to the file context', async () => {
    const pending = deferred<Response>()
    restoreReply = () => pending.promise
    const container = render()
    await enterContext(container)
    await click(button(container, '详情'))
    await click(button(container, '恢复对象'))
    await click(button(container, '返回文件'))
    const returnRoute = routeFromHash('overview')
    await act(async () => { pending.resolve(json({ status: 'restored' })); await pending.promise })
    await settle()
    await settle()
    expect(routeFromHash('overview')).toBe(returnRoute)
    expectContext(container)
    expect(fetcher.mock.calls.filter(([request]) => String(request).endsWith('/restore'))).toHaveLength(1)
  })
})

describe('Shell immediate post-recovery history', () => {
  it.each([false, true])('replaces restored details before immediate Back/Forward (legacy=%s)', async (legacy) => {
    const container = render()
    await enterContext(container)
    const recycleRoute = routeFromHash('overview')
    if (legacy) {
      act(() => { window.location.hash = `#/${encodeURIComponent(`deleted-object/${key}`)}` })
      await settle()
      await settle()
    } else {
      await click(button(container, '详情'))
    }
    await click(button(container, '恢复对象'))
    const activeObject = legacy ? { key, deleted: false } : { key, deleted: false, returnTo: location }
    expect(currentObject()).toEqual(activeObject)
    expect(container.textContent).toContain('活动对象')
    expect(container.textContent).not.toContain('恢复对象')
    await historyMove('back')
    expect(routeFromHash('overview')).toBe(recycleRoute)
    expectContext(container)
    expect(container.querySelector('tbody')?.textContent).not.toContain(key)
    expect(container.textContent).not.toContain('已软删除')
    expect([...container.querySelectorAll('button')].some((item) => item.textContent === '恢复')).toBe(false)
    await historyMove('forward')
    expect(currentObject()).toEqual(activeObject)
    expect(container.querySelector('h1')?.textContent).toBe(key)
    expect(container.textContent).toContain('活动对象')
    expect(container.textContent).not.toContain('已软删除')
    expect(container.textContent).not.toContain('恢复对象')
    expect(fetcher.mock.calls.filter(([request]) => String(request).endsWith('/restore'))).toHaveLength(1)
  })
})
