import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { VaultClient, type FilePage, type VaultObject } from '../api/vault'
import { FilesPage } from './FilesPage'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const owned: VaultObject = {
  bucket: 'default', key: 'owned.txt', size: 4, etag: 'etag', backend: 'local',
  created_at: '2026-10-06T10:00:00Z', updated_at: '2026-10-06T10:00:00Z',
}
const shared = { ...owned, key: 'shared.txt' }
let mounted: { container: HTMLDivElement; root: Root } | undefined

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
})

function mount(client: VaultClient): HTMLElement {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  mounted = { container, root }
  act(() => root.render(<FilesPage client={client} onOpenObject={() => undefined} />))
  return container
}

async function settle(): Promise<void> {
  await act(async () => { await new Promise((resolve) => window.setTimeout(resolve, 0)) })
}

function findButton(container: HTMLElement, label: string): HTMLButtonElement {
  const button = [...container.querySelectorAll('button')]
    .find((candidate) => candidate.textContent?.trim() === label)
  if (!button) throw new Error(`missing button ${label}`)
  return button
}

async function click(container: HTMLElement, label: string): Promise<void> {
  await act(async () => { findButton(container, label).click(); await Promise.resolve() })
  await settle()
}

function response(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } })
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve: (value: T) => void = () => undefined
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

describe('FilesPage server-owned views', () => {
  it('uses mine for folders and every file page, and hides recursive folder deletion', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost')
      const mine = url.searchParams.get('view') === 'mine'
      if (url.pathname === '/v1/folders') return response({
        prefix: '', items: [{ name: mine ? 'mine-dir' : 'shared-dir', type: 'folder' }],
      })
      if (!mine) return response({ objects: [owned, shared], has_more: false })
      return url.searchParams.has('marker')
        ? response({ objects: [{ ...owned, key: 'second-owned.txt' }], has_more: false })
        : response({ objects: [owned], has_more: true, next_marker: owned.key })
    })
    const container = mount(new VaultClient('/v1', () => 'token', fetcher as typeof fetch))
    await settle()
    expect(container.textContent).toContain('shared.txt')
    const selectShared = container.querySelector<HTMLElement>('[aria-label="选择 shared.txt"]')
    if (!selectShared) throw new Error('missing shared file selector')
    act(() => selectShared.click())
    expect(container.textContent).toContain('已选择 1 个对象')
    await click(container, '我的文件')
    expect(container.textContent).not.toContain('已选择 1 个对象')
    expect(container.textContent).not.toContain('shared.txt')
    expect(container.textContent).not.toContain('shared-dir')
    expect(container.textContent).toContain('mine-dir')
    expect(findButton(container, '我的文件').getAttribute('aria-pressed')).toBe('true')
    expect(container.querySelector('.folder-item .text-danger')).toBeNull()
    await click(container, '加载更多')
    expect(container.textContent).toContain('second-owned.txt')
    const urls = fetcher.mock.calls.map(([input]) => new URL(String(input), 'http://localhost'))
    expect(urls.some((url) => url.pathname === '/v1/folders' && url.searchParams.get('view') === 'mine')).toBe(true)
    expect(urls.some((url) => url.searchParams.get('view') === 'mine' && url.searchParams.get('marker') === 'owned.txt')).toBe(true)
    await click(container, '可访问文件')
    expect(container.textContent).toContain('shared.txt')
    expect(container.textContent).not.toContain('second-owned.txt')
  })

  it('preserves mine when opening the recycle bin', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost')
      return response(url.pathname === '/v1/files'
        ? { objects: [owned], has_more: false } : { prefix: '', items: [] })
    })
    const container = mount(new VaultClient('/v1', () => 'token', fetcher as typeof fetch))
    await settle()
    await click(container, '我的文件')
    const toggle = container.querySelector<HTMLElement>('.deleted-toggle [role="switch"]')
      ?? container.querySelector<HTMLElement>('.deleted-toggle input')
    if (!toggle) throw new Error('missing recycle bin switch')
    await act(async () => { toggle.click(); await Promise.resolve() })
    await settle()
    expect(container.textContent).toContain('恢复')
    const last = new URL(String(fetcher.mock.calls.at(-1)?.[0]), 'http://localhost')
    expect(last.searchParams.get('view')).toBe('mine')
    expect(last.searchParams.get('deleted')).toBe('true')
  })

  it('discards an old accessible-view continuation after switching to mine', async () => {
    const pending = deferred<FilePage>()
    const fetcher = async (input: RequestInfo | URL): Promise<Response> => {
      const url = new URL(String(input), 'http://localhost')
      if (url.pathname === '/v1/folders') return response({ prefix: '', items: [] })
      if (url.searchParams.get('view') === 'mine') return response({ objects: [owned], has_more: false })
      if (url.searchParams.has('marker')) return response(await pending.promise)
      return response({ objects: [shared], has_more: true, next_marker: shared.key })
    }
    const container = mount(new VaultClient('/v1', () => 'token', fetcher as typeof fetch))
    await settle()
    act(() => findButton(container, '加载更多').click())
    await click(container, '我的文件')
    await act(async () => {
      pending.resolve({ objects: [{ ...shared, key: 'stale.txt' }], has_more: false })
      await pending.promise
    })
    expect(container.textContent).toContain('owned.txt')
    expect(container.textContent).not.toContain('stale.txt')
    expect(container.textContent).not.toContain('shared.txt')
  })

  it('shows an identity error instead of treating anonymous files as owned', async () => {
    const fetcher = async (input: RequestInfo | URL): Promise<Response> => {
      const url = new URL(String(input), 'http://localhost')
      if (url.searchParams.get('view') === 'mine') return response({
        error: { code: 'Forbidden', message: '需要有效用户身份' },
      }, 403)
      return response(url.pathname === '/v1/files'
        ? { objects: [shared], has_more: false } : { prefix: '', items: [] })
    }
    const container = mount(new VaultClient('/v1', () => '', fetcher as typeof fetch))
    await settle()
    await click(container, '我的文件')
    expect(container.textContent).toContain('需要有效用户身份')
    expect(container.textContent).not.toContain('shared.txt')
    expect(container.textContent).not.toContain('没有匹配文件')
  })
})
