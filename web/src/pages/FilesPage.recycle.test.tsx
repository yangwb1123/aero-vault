import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { VaultClient, type VaultObject } from '../api/vault'
import { FilesPage } from './FilesPage'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
const object = (key: string): VaultObject => ({
  bucket: 'default', key, size: 4, etag: 'etag', backend: 'local',
  created_at: '2026-10-06T10:00:00Z', updated_at: '2026-10-06T10:00:00Z',
})
let mounted: { container: HTMLDivElement; root: Root } | undefined

beforeEach(() => { vi.spyOn(window, 'confirm').mockReturnValue(true) })
afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
  vi.restoreAllMocks()
})

function render(client: VaultClient): HTMLElement {
  if (!mounted) {
    const container = document.createElement('div')
    document.body.append(container)
    mounted = { container, root: createRoot(container) }
  }
  act(() => mounted?.root.render(<FilesPage client={client} onOpenObject={() => undefined} />))
  return mounted.container
}

async function settle(): Promise<void> {
  await act(async () => { await new Promise((resolve) => window.setTimeout(resolve, 0)) })
}

function button(container: HTMLElement, label: string): HTMLButtonElement {
  const result = [...container.querySelectorAll('button')].find((item) => item.textContent?.trim() === label)
  if (!result) throw new Error(`missing ${label}`)
  return result
}

async function click(element: HTMLElement): Promise<void> {
  await act(async () => { element.click(); await Promise.resolve() })
  await settle()
}

function checkbox(container: HTMLElement, label = '选择已加载对象'): HTMLInputElement {
  const result = container.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`)
  if (!result) throw new Error(`missing checkbox ${label}`)
  return result
}

function recycleToggle(container: HTMLElement): HTMLElement {
  const toggle = container.querySelector<HTMLElement>('.deleted-toggle [role="switch"]')
    ?? container.querySelector<HTMLElement>('.deleted-toggle input')
  if (!toggle) throw new Error('missing recycle toggle')
  return toggle
}

function response(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } })
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve: (value: T) => void = () => undefined
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

async function enterRecycle(client: VaultClient): Promise<HTMLElement> {
  const container = render(client)
  await settle()
  await click(recycleToggle(container))
  return container
}

describe('recycle selection and restoration', () => {
  it('restores only checked files, clears successes and never offers destructive/tag actions', async () => {
    const remaining = new Set(['a.txt', 'b.txt', 'not-selected.txt'])
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost')
      if (url.pathname.endsWith('/restore')) {
        remaining.delete(decodeURIComponent(url.pathname.split('/').at(-2) || ''))
        return response({ status: 'restored' })
      }
      return response(url.pathname === '/v1/files'
        ? { objects: url.searchParams.get('deleted') ? [...remaining].map(object) : [], has_more: false }
        : { prefix: '', items: [] })
    })
    const container = await enterRecycle(new VaultClient('/v1', () => 'token', fetcher as typeof fetch))
    await click(checkbox(container, '选择 a.txt'))
    await click(checkbox(container, '选择 b.txt'))
    expect(container.textContent).not.toContain('批量删除')
    expect(container.textContent).not.toContain('替换标签')
    await click(button(container, '恢复所选'))
    const mutations = fetcher.mock.calls.filter(([input]) => String(input).endsWith('/restore'))
    expect(mutations.map(([input]) => String(input))).toEqual(['/v1/files/a.txt/restore', '/v1/files/b.txt/restore'])
    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining('2 个文件'))
    expect(container.textContent).toContain('已恢复 2 个文件')
    expect(container.textContent).toContain('not-selected.txt')
    expect(container.textContent).not.toContain('已选择')
  })

  it('retains only failures with API reasons and retries just those keys', async () => {
    const remaining = new Set(['ok.txt', 'failed.txt'])
    let attempts = 0
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost')
      if (url.pathname.endsWith('/failed.txt/restore') && ++attempts === 1) {
        return response({ error: { code: 'QuotaExceeded', message: '配额不足' } }, 507)
      }
      if (url.pathname.endsWith('/restore')) {
        remaining.delete(url.pathname.split('/').at(-2) || '')
        return response({ status: 'restored' })
      }
      return response(url.pathname === '/v1/files'
        ? { objects: url.searchParams.get('deleted') ? [...remaining].map(object) : [], has_more: false }
        : { prefix: '', items: [] })
    })
    const container = await enterRecycle(new VaultClient('/v1', () => 'token', fetcher as typeof fetch))
    await click(checkbox(container))
    await click(button(container, '恢复所选'))
    expect(container.textContent).toContain('已恢复 1 个文件，1 个失败')
    expect(container.textContent).toContain('failed.txt：配额不足')
    expect(checkbox(container, '选择 failed.txt').checked).toBe(true)
    expect(container.textContent).not.toContain('ok.txt')
    await click(button(container, '恢复所选'))
    const paths = fetcher.mock.calls.filter(([input]) => String(input).endsWith('/restore')).map(([input]) => String(input))
    expect(paths).toEqual(['/v1/files/ok.txt/restore', '/v1/files/failed.txt/restore', '/v1/files/failed.txt/restore'])
    expect(container.textContent).toContain('已恢复 1 个文件')
    expect(container.textContent).not.toContain('配额不足')
  })

  it('honors confirmation cancellation', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => response(String(input).includes('/files?')
      ? { objects: [object('a.txt')], has_more: false } : { prefix: '', items: [] }))
    const container = await enterRecycle(new VaultClient('/v1', () => 'token', fetcher as typeof fetch))
    await click(checkbox(container))
    vi.mocked(window.confirm).mockReturnValue(false)
    await click(button(container, '恢复所选'))
    expect(fetcher.mock.calls.some(([input]) => String(input).endsWith('/restore'))).toBe(false)
    expect(checkbox(container, '选择 a.txt').checked).toBe(true)
  })

  it('selects only loaded pages and preserves mine while loading another page', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost')
      if (url.pathname.endsWith('/restore')) return response({ status: 'restored' })
      if (url.pathname === '/v1/folders') return response({ prefix: '', items: [] })
      if (!url.searchParams.has('deleted')) return response({ objects: [], has_more: false })
      return response(url.searchParams.has('marker')
        ? { objects: [object('second.txt')], has_more: false }
        : { objects: [object('first.txt')], has_more: true, next_marker: 'first.txt' })
    })
    const container = await enterRecycle(new VaultClient('/v1', () => 'token', fetcher as typeof fetch))
    await click(button(container, '我的文件'))
    await click(checkbox(container))
    expect(container.textContent).toContain('已选择 1 个回收站文件')
    await click(button(container, '加载更多'))
    expect(checkbox(container, '选择 second.txt').checked).toBe(false)
    await click(checkbox(container, '选择 second.txt'))
    expect(container.textContent).toContain('已选择 2 个回收站文件')
    await click(button(container, '恢复所选'))
    expect(fetcher.mock.calls.filter(([input]) => String(input).endsWith('/restore'))).toHaveLength(2)
    const continuation = fetcher.mock.calls.find(([input]) => String(input).includes('marker='))
    expect(String(continuation?.[0])).toContain('view=mine')
  })

  it('disables selection and prevents duplicate submission while a restore is pending', async () => {
    const pending = deferred<Response>()
    const fetcher = vi.fn((input: RequestInfo | URL) => String(input).endsWith('/restore')
      ? pending.promise : Promise.resolve(response(String(input).includes('/files?')
        ? { objects: [object('a.txt')], has_more: false } : { prefix: '', items: [] })))
    const container = await enterRecycle(new VaultClient('/v1', () => 'token', fetcher as typeof fetch))
    await click(checkbox(container))
    await click(button(container, '恢复所选'))
    expect(checkbox(container).disabled).toBe(true)
    expect(checkbox(container, '选择 a.txt').disabled).toBe(true)
    expect(button(container, '恢复所选').disabled).toBe(true)
    await click(button(container, '恢复所选'))
    expect(fetcher.mock.calls.filter(([input]) => String(input).endsWith('/restore'))).toHaveLength(1)
    await act(async () => { pending.resolve(response({ status: 'restored' })); await pending.promise })
  })

  it.each(['view', 'path', 'client', 'mode', 'unmount'] as const)('stops queued restores after %s changes', async (scope) => {
    const pending = deferred<Response>()
    const fetcher = vi.fn((input: RequestInfo | URL) => String(input).endsWith('/restore')
      ? pending.promise : Promise.resolve(response(String(input).includes('/files?')
        ? { objects: [object('a.txt'), object('b.txt')], has_more: false } : { prefix: '', items: [] })))
    const container = await enterRecycle(new VaultClient('/v1', () => 'old-token', fetcher as typeof fetch))
    await click(checkbox(container))
    await click(button(container, '恢复所选'))
    if (scope === 'view') await click(button(container, '我的文件'))
    if (scope === 'mode') await click(recycleToggle(container))
    if (scope === 'client') {
      render(new VaultClient('/new/v1', () => 'new-token', async () => response({ objects: [], has_more: false })))
      await settle()
    }
    if (scope === 'path') {
      const input = container.querySelector<HTMLInputElement>('input[placeholder="目录路径，例如 team/docs/"]')
      if (!input) throw new Error('missing path input')
      await act(async () => {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, 'docs/')
        input.dispatchEvent(new Event('input', { bubbles: true }))
      })
      await click(button(container, '进入'))
    }
    if (scope === 'unmount') { act(() => mounted?.root.unmount()); mounted = undefined }
    const requestsAfterScopeChange = fetcher.mock.calls.length
    await act(async () => { pending.resolve(response({ status: 'restored' })); await pending.promise })
    await settle()
    expect(fetcher.mock.calls.filter(([input]) => String(input).endsWith('/restore'))).toHaveLength(1)
    expect(fetcher.mock.calls).toHaveLength(requestsAfterScopeChange)
    expect(container.textContent).not.toContain('已恢复')
    expect(container.textContent).not.toContain('已选择')
    if (scope === 'unmount') container.remove()
  })
})
