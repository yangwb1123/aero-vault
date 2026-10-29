import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { VaultClient, type VaultObject } from '../api/vault'
import { FilesPage } from './FilesPage'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
type Location = { path: string; view: 'all' | 'mine'; deleted: boolean }
const rootLocation: Location = { path: '', view: 'all', deleted: false }
const literalName = ' 资料%2F+?# '
const literalPath = `${literalName}/`
let mounted: { container: HTMLDivElement; root: Root } | undefined

const object = (key: string): VaultObject => ({
  bucket: 'default', key, size: 4, etag: 'etag', backend: 'local',
  created_at: '2026-10-06T10:00:00Z', updated_at: '2026-10-06T10:00:00Z',
})
function json(body: unknown): Response {
  return new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
}
function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve: (value: T) => void = () => undefined
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}
function button(container: HTMLElement, label: string): HTMLButtonElement {
  const found = [...container.querySelectorAll('button')].find((item) => item.textContent?.trim() === label)
  if (!found) throw new Error(`missing button ${label}`)
  return found
}
function input(container: HTMLElement): HTMLInputElement {
  const found = container.querySelector<HTMLInputElement>('input[placeholder="目录路径，例如 team/docs/"]')
  if (!found) throw new Error('missing directory input')
  return found
}
async function settle(): Promise<void> {
  await act(async () => { await new Promise((resolve) => window.setTimeout(resolve, 0)) })
}
async function click(element: HTMLElement): Promise<void> {
  await act(async () => { element.click(); await Promise.resolve() })
  await settle()
}
function render(client: VaultClient, location?: Location, change = vi.fn(), open = vi.fn()): HTMLElement {
  if (!mounted) {
    const container = document.createElement('div')
    document.body.append(container)
    mounted = { container, root: createRoot(container) }
  }
  act(() => mounted?.root.render(<FilesPage client={client} location={location}
    onLocationChange={change} onOpenObject={open} />))
  return mounted.container
}
afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
  vi.restoreAllMocks()
})

describe('literal directory navigation', () => {
  it('uses server segments, direct-file filtering and breadcrumbs without trimming or decoding', async () => {
    const fetcher = vi.fn(async (request: RequestInfo | URL) => {
      const url = new URL(String(request), 'http://localhost')
      const path = url.searchParams.get('path') ?? ''
      if (url.pathname === '/v1/folders') return json({ prefix: path,
        items: path ? [] : [{ name: literalName, type: 'folder' }] })
      const prefix = url.searchParams.get('prefix') ?? ''
      return json({ objects: prefix ? [object(`${literalPath}  file%2F+?# `), object(`${literalPath}nested/a`)] : [], has_more: false })
    })
    const container = render(new VaultClient('/v1', () => '', fetcher as typeof fetch))
    await settle()
    await click(container.querySelector<HTMLElement>('.folder-open')!)
    expect(input(container).value).toBe(literalPath)
    expect(container.querySelector('tbody')?.textContent).toContain(`${literalPath}  file%2F+?# `)
    expect(container.querySelector('tbody')?.textContent).not.toContain('nested/a')
    expect(container.querySelector('.folder-crumbs')?.textContent).toContain(literalName)
    const urls = fetcher.mock.calls.map(([request]) => new URL(String(request), 'http://localhost'))
    expect(urls.some((url) => url.searchParams.get('prefix') === literalPath)).toBe(true)
    expect(urls.some((url) => url.searchParams.get('path') === literalPath)).toBe(true)
  })

  it('retains manual input normalization independently of literal directory navigation', async () => {
    const fetcher = vi.fn(async (request: RequestInfo | URL) => json(String(request).includes('/files?')
      ? { objects: [], has_more: false } : { prefix: '', items: [] }))
    const container = render(new VaultClient('/v1', () => '', fetcher as typeof fetch))
    await settle()
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input(container), '/ team// docs /')
      input(container).dispatchEvent(new Event('input', { bubbles: true }))
    })
    await click(button(container, '进入'))
    expect(input(container).value).toBe('team/docs/')
    expect(fetcher.mock.calls.some(([request]) => new URL(String(request), 'http://localhost').searchParams.get('prefix') === 'team/docs/')).toBe(true)
  })
})

function uploadFixture(rootFolder?: string) {
  const fetcher = vi.fn(async (request: RequestInfo | URL, init?: RequestInit) => {
    if (init?.method === 'PUT') return new Response(null, { status: 204 })
    const url = new URL(String(request), 'http://localhost')
    if (url.pathname === '/v1/files') return json({ objects: [], has_more: false })
    if (url.pathname === '/v1/folders') return json({ prefix: url.searchParams.get('path') ?? '',
      items: !url.searchParams.has('path') && rootFolder ? [{ name: rootFolder, type: 'folder' }] : [] })
    throw new Error(`unexpected request ${request}`)
  })
  return { client: new VaultClient('/v1', () => '', fetcher as typeof fetch), fetcher }
}
async function dropFile(container: HTMLElement, file: File): Promise<void> {
  const zone = container.querySelector('[data-iris-file-upload-zone]')
  if (!zone) throw new Error('missing upload dropzone')
  const event = new Event('drop', { bubbles: true, cancelable: true })
  Object.defineProperty(event, 'dataTransfer', { value: { files: [file] } })
  await act(async () => { zone.dispatchEvent(event) })
}

describe('literal directory upload targets', () => {
  it.each([' docs ', literalName, '  '])('blocks unsupported server-returned prefixes instead of uploading to a trimmed Key: %j', async (name) => {
    const { client, fetcher } = uploadFixture(name)
    const container = render(client)
    await settle()
    await click(container.querySelector<HTMLElement>('.folder-open')!)
    expect(input(container).value).toBe(`${name}/`)
    const file = new File(['content'], 'a.txt', { type: 'text/plain' })
    // Exercise any exposed upload control, so the old wrong-target PUT is observable.
    if (container.querySelector('[data-iris-file-upload-zone]')) {
      await dropFile(container, file)
      await click(button(container, '上传 1 个文件'))
    }
    expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'PUT')
      .map(([request]) => decodeURIComponent(new URL(String(request), 'http://localhost').pathname))).toEqual([])
    expect(container.querySelector('[data-iris-file-upload-zone]')).toBeNull()
    expect(container.textContent).toContain('当前目录不支持上传')
    expect(container.textContent).toContain('去除空白')
    await click(button(container, '根目录'))
    expect(input(container).value).toBe('')
    await dropFile(container, file)
    await click(button(container, '上传 1 个文件'))
    const writes = fetcher.mock.calls.filter(([, init]) => init?.method === 'PUT')
    expect(writes.map(([request]) => String(request))).toEqual(['/v1/files/a.txt'])
    expect(writes[0][1]?.body).toBe(file)
  })
})

describe('upload prefix compatibility', () => {
  it.each(['docs /', `team/${literalPath}`])('uploads to the exact supported literal prefix %j', async (path) => {
    const { client, fetcher } = uploadFixture()
    const container = render(client, { ...rootLocation, path })
    await settle()
    const file = new File(['content'], ' a%2F+?#.txt ', { type: 'text/plain' })
    await dropFile(container, file)
    await click(button(container, '上传 1 个文件'))
    const writes = fetcher.mock.calls.filter(([, init]) => init?.method === 'PUT')
    expect(writes.map(([request]) => decodeURIComponent(new URL(String(request), 'http://localhost').pathname)))
      .toEqual([`/v1/files/${path}${file.name}`])
    expect(writes[0][1]?.body).toBe(file)
  })

  it('retains normalization of manually edited upload prefixes', async () => {
    const { client, fetcher } = uploadFixture()
    const container = render(client)
    await settle()
    const prefix = container.querySelector<HTMLInputElement>('input[placeholder="team/project/"]')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(prefix, ' docs ')
      prefix.dispatchEvent(new Event('input', { bubbles: true }))
    })
    const file = new File(['content'], 'a.txt')
    await dropFile(container, file)
    await click(button(container, '上传 1 个文件'))
    const writes = fetcher.mock.calls.filter(([, init]) => init?.method === 'PUT')
    expect(writes.map(([request]) => String(request))).toEqual(['/v1/files/docs/a.txt'])
    expect(writes[0][1]?.body).toBe(file)
  })
})

describe('literal directory commands', () => {
  it('preserves the literal parent for creation and exact server segments for recursive removal', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const child = ' child%2F+?# '
    const fetcher = vi.fn(async (request: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'POST') return json({})
      if (init?.method === 'DELETE') return json({ deleted: 1, failed: 0 })
      return json(String(request).includes('/files?') ? { objects: [], has_more: false }
        : { prefix: literalPath, items: [{ name: child, type: 'folder' }] })
    })
    const client = new VaultClient('/v1', () => '', fetcher as typeof fetch)
    const container = render(client, { ...rootLocation, path: literalPath })
    await settle()
    const name = container.querySelector<HTMLInputElement>('input[placeholder="新目录名称"]')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(name, '/ new// name /')
      name.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await click(button(container, '创建目录'))
    await click(container.querySelector<HTMLElement>('.folder-item .text-danger')!)
    const mutations = fetcher.mock.calls.filter(([, init]) => init?.method === 'POST' || init?.method === 'DELETE')
    expect(mutations.map(([request, init]) => [init?.method,
      decodeURIComponent(new URL(String(request), 'http://localhost').pathname)])).toEqual([
      ['POST', `/v1/folders/${literalPath}new/name/`], ['DELETE', `/v1/folders/${literalPath}${child}/`],
    ])
    expect(window.confirm).toHaveBeenCalledWith(`将目录 ${literalPath}${child}/ 及其中全部对象移入回收站？`)
  })
})

describe('externally controlled file locations', () => {
  it('loads the supplied directory/view/recycle scope and reports only whitelisted location changes', async () => {
    const fetcher = vi.fn(async (request: RequestInfo | URL) => json(String(request).includes('/folders')
      ? { prefix: '', items: [] } : { objects: [object(`${literalPath}  `)], has_more: false }))
    const client = new VaultClient('/v1', () => '', fetcher as typeof fetch)
    const location: Location = { path: literalPath, view: 'mine', deleted: true }
    const change = vi.fn()
    const open = vi.fn()
    const container = render(client, location, change, open)
    await settle()
    expect(input(container).value).toBe(literalPath)
    expect(button(container, '我的文件').getAttribute('aria-pressed')).toBe('true')
    expect(fetcher).toHaveBeenCalledTimes(1)
    const url = new URL(String(fetcher.mock.calls[0]?.[0]), 'http://localhost')
    expect(Object.fromEntries(url.searchParams)).toEqual({ limit: '200', prefix: literalPath, deleted: 'true', view: 'mine' })
    await click(button(container, '详情'))
    expect(open).toHaveBeenCalledWith(`${literalPath}  `, true)
    await click(button(container, '可访问文件'))
    expect(change).toHaveBeenLastCalledWith({ ...location, view: 'all' })
    expect(button(container, '我的文件').getAttribute('aria-pressed')).toBe('true')
    render(client, { ...location, view: 'all' }, change, open)
    await settle()
    expect(button(container, '可访问文件').getAttribute('aria-pressed')).toBe('true')
  })

})

describe('controlled location read fences', () => {
  it('hides old rows, selection, sharing and continuation results immediately on a location change', async () => {
    const continuation = deferred<Response>()
    const next = deferred<Response>()
    const fetcher = vi.fn(async (request: RequestInfo | URL) => {
      const url = new URL(String(request), 'http://localhost')
      if (url.pathname === '/v1/folders') return json({ prefix: '', items: [] })
      if (url.searchParams.has('marker')) return continuation.promise
      if (url.searchParams.get('prefix')) return next.promise
      return json({ objects: [object('old.txt')], has_more: true, next_marker: 'old.txt' })
    })
    const client = new VaultClient('/v1', () => '', fetcher as typeof fetch)
    const container = render(client, rootLocation)
    await settle()
    await click(container.querySelector<HTMLElement>('[aria-label="选择 old.txt"]')!)
    await click(button(container, '分享'))
    await click(button(container, '加载更多'))
    render(client, { path: 'next/', view: 'mine', deleted: true })
    expect(container.textContent).not.toContain('old.txt')
    expect(container.textContent).not.toContain('已选择')
    expect(container.querySelector('tbody')).toBeNull()
    await act(async () => {
      continuation.resolve(json({ objects: [object('stale.txt')], has_more: false }))
      next.resolve(json({ objects: [object('next/current.txt')], has_more: false }))
    })
    await settle()
    expect(container.textContent).toContain('next/current.txt')
    expect(container.textContent).not.toContain('stale.txt')
    expect(container.textContent).not.toContain('对象分享')
  })

})

describe('controlled location mutation fences', () => {
  it('does not continue queued restores or show their completion after an external scope change', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const pending = deferred<Response>()
    const fetcher = vi.fn(async (request: RequestInfo | URL) => String(request).endsWith('/restore')
      ? pending.promise : json(String(request).includes('/files?')
        ? { objects: [object('a.txt'), object('b.txt')], has_more: false } : { prefix: '', items: [] }))
    const client = new VaultClient('/v1', () => '', fetcher as typeof fetch)
    const container = render(client, { ...rootLocation, deleted: true })
    await settle()
    await click(container.querySelector<HTMLElement>('[aria-label="选择已加载对象"]')!)
    await click(button(container, '恢复所选'))
    render(client, { path: 'next/', view: 'mine', deleted: false })
    await settle()
    const count = fetcher.mock.calls.length
    await act(async () => { pending.resolve(json({ status: 'restored' })); await pending.promise })
    await settle()
    expect(fetcher.mock.calls).toHaveLength(count)
    expect(fetcher.mock.calls.filter(([request]) => String(request).endsWith('/restore'))).toHaveLength(1)
    expect(container.textContent).not.toContain('已恢复')
    expect(container.textContent).not.toContain('已选择')
  })
})
