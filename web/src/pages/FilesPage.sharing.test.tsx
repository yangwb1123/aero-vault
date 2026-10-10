import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { VaultClient, type VaultObject } from '../api/vault'
import { FilesPage } from './FilesPage'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
const key = ' 100% report.txt '
const file = (key: string): VaultObject => ({
  bucket: 'default', key, size: 4, etag: 'etag', backend: 'local',
  created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-01T00:00:00Z',
})
const freshShare = { share: { id: 'fresh-share' }, token: 'secret', url: 'https://vault.example/share/secret' }
let mounted: { container: HTMLDivElement; root: Root } | undefined

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
  vi.restoreAllMocks()
})

function response(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } })
}

function createClient(create: () => Promise<Response> = async () => response(freshShare)) {
  const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost')
    if (url.pathname === '/v1/shares') return init?.method === 'POST' ? create() : response({ shares: [] })
    return response(url.pathname === '/v1/files'
      ? { objects: [file(key), file('other.txt')], has_more: false } : { prefix: url.searchParams.get('path') ?? '', items: [] })
  })
  return { client: new VaultClient('/v1', () => 'token', fetcher as typeof fetch), fetcher }
}

async function render(client: VaultClient): Promise<HTMLElement> {
  if (!mounted) {
    const container = document.createElement('div')
    document.body.append(container)
    mounted = { container, root: createRoot(container) }
  }
  await act(async () => mounted?.root.render(<FilesPage client={client} onOpenObject={() => undefined} />))
  return mounted.container
}

function button(container: HTMLElement, label: string): HTMLButtonElement {
  const result = [...container.querySelectorAll('button')].find((item) => item.textContent?.trim() === label)
  if (!result) throw new Error(`missing ${label}`)
  return result
}

function shareButton(container: HTMLElement, key: string): HTMLButtonElement {
  const row = [...container.querySelectorAll('tr')].find((item) => item.querySelector('strong')?.textContent === key)
  if (!row) throw new Error(`missing row ${key}`)
  return button(row, '分享')
}

async function click(element: HTMLElement): Promise<void> {
  await act(async () => element.click())
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve: (value: T) => void = () => undefined
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

describe('FilesPage object sharing entry', () => {
  it('uses the row bucket and exact Key with existing share endpoints', async () => {
    const { client, fetcher } = createClient()
    const container = await render(client)
    await click(shareButton(container, key))
    expect(container.textContent).toContain(`分享：${key}`)
    const input = container.querySelector<HTMLInputElement>('input[placeholder="docs/report.pdf"]')!
    expect(input.value).toBe(key)
    expect(input.readOnly).toBe(true)
    const getShare = fetcher.mock.calls.find(([input]) => String(input).startsWith('/v1/shares?'))!
    const query = new URL(String(getShare[0]), 'http://localhost').searchParams
    expect(query.get('bucket')).toBe('default')
    expect(query.get('key')).toBe(key)
    await click(button(container, '创建分享'))
    const request = fetcher.mock.calls.find(([, init]) => init?.method === 'POST')!
    expect(JSON.parse(String(request[1]?.body))).toMatchObject({ bucket: 'default', key, ttl_seconds: 3600 })
    expect(new Headers(request[1]?.headers).get('Authorization')).toBe('Bearer token')
    expect(container.querySelector('a')?.href).toBe(freshShare.url)
    expect(fetcher.mock.calls.every(([input]) => String(input).startsWith('/v1/'))).toBe(true)
    await click(button(container, '关闭分享'))
    expect(container.querySelector('[aria-label="对象分享"]')).toBeNull()
  })

  it('does not show a delayed create after closing and opening another file', async () => {
    const pending = deferred<Response>()
    const { client, fetcher } = createClient(() => pending.promise)
    const container = await render(client)
    await click(shareButton(container, key))
    await click(button(container, '创建分享'))
    await click(button(container, '关闭分享'))
    await click(shareButton(container, 'other.txt'))
    await act(async () => { pending.resolve(response(freshShare)); await pending.promise })
    expect(container.textContent).toContain('分享：other.txt')
    expect(container.querySelector('a')).toBeNull()
    const queries = fetcher.mock.calls.filter(([input, init]) => String(input).startsWith('/v1/shares?') && !init?.method)
    expect(queries).toHaveLength(2)
  })

  it('closes sharing when changing mine/all or the client', async () => {
    const container = await render(createClient().client)
    await click(shareButton(container, key))
    await click(button(container, '我的文件'))
    expect(container.querySelector('[aria-label="对象分享"]')).toBeNull()
    await click(shareButton(container, key))
    await render(createClient().client)
    expect(container.querySelector('[aria-label="对象分享"]')).toBeNull()
  })

  it('closes sharing when entering another directory', async () => {
    const container = await render(createClient().client)
    await click(shareButton(container, key))
    const input = container.querySelector<HTMLInputElement>('input[placeholder="目录路径，例如 team/docs/"]')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, 'docs/')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await click(button(container, '进入'))
    expect(container.querySelector('[aria-label="对象分享"]')).toBeNull()
  })

  it('closes sharing and removes the entry in the recycle bin', async () => {
    const container = await render(createClient().client)
    await click(shareButton(container, key))
    const toggle = container.querySelector<HTMLElement>('.deleted-toggle [role="switch"]')
      ?? container.querySelector<HTMLElement>('.deleted-toggle input')
    await click(toggle!)
    expect(container.querySelector('[aria-label="对象分享"]')).toBeNull()
    expect([...container.querySelectorAll('button')].some((item) => item.textContent?.trim() === '分享')).toBe(false)
    expect(container.textContent).toContain('恢复')
  })

  it('clears the panel when a file action refreshes the listing', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const container = await render(createClient().client)
    await click(shareButton(container, key))
    await click(button(container, '创建分享'))
    expect(container.querySelector('a')?.href).toBe(freshShare.url)
    await click(button(container, '删除'))
    expect(container.querySelector('[aria-label="对象分享"]')).toBeNull()
    expect(container.querySelector('a')).toBeNull()
  })

  it('shows create denial without showing a copyable link', async () => {
    const { client } = createClient(async () => response({ error: { code: 'AccessDenied', message: '不允许分享' } }, 403))
    const container = await render(client)
    await click(shareButton(container, key))
    await click(button(container, '创建分享'))
    expect(container.textContent).toContain('不允许分享')
    expect(container.querySelector('a')).toBeNull()
  })
})
