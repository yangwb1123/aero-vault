import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { VaultClient, type VaultObject } from '../api/vault'
import { downloadBlob } from '../download'
import { FilesPage } from './FilesPage'

vi.mock('../download', () => ({ downloadBlob: vi.fn(async () => undefined) }))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
const object: VaultObject = { bucket: 'default', key: 'one.txt', size: 8, etag: 'one', backend: 'local',
  created_at: '2026-10-12T00:00:00Z', updated_at: '2026-10-12T00:00:00Z' }
let mounted: { container: HTMLDivElement; root: Root } | undefined

beforeEach(() => vi.mocked(downloadBlob).mockClear())
afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
})
function deferred() {
  let resolve: (response: Response) => void = () => undefined
  const promise = new Promise<Response>((accept) => { resolve = accept })
  return { promise, resolve }
}
function json(data: unknown) { return new Response(JSON.stringify(data), { headers: { 'Content-Type': 'application/json' } }) }
function fixture(read: (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>, key = object.key) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost')
    if (url.pathname === '/v1/folders') return json({ prefix: url.searchParams.get('path') ?? '', items: [] })
    if (url.pathname === '/v1/files') return json({ objects: [{ ...object, key: `${url.searchParams.get('prefix') ?? ''}${key}` }], has_more: false })
    return read(input, init)
  })
}
async function render(client: VaultClient, strict = false) {
  if (!mounted) {
    const container = document.createElement('div')
    document.body.append(container)
    mounted = { container, root: createRoot(container) }
  }
  const page = <FilesPage client={client} onOpenObject={() => undefined} />
  await act(async () => mounted?.root.render(strict ? <React.StrictMode>{page}</React.StrictMode> : page))
  return mounted.container
}
function button(container: HTMLElement, text: string) {
  const found = [...container.querySelectorAll('button')].find((entry) => entry.textContent?.trim() === text)
  if (!found) throw new Error(`missing ${text}`)
  return found
}
async function click(target: HTMLElement) { await act(async () => target.click()) }
async function finish(pending: ReturnType<typeof deferred>, response = new Response('file bytes')) {
  await act(async () => { pending.resolve(response); await pending.promise; await new Promise((resolve) => window.setTimeout(resolve, 0)) })
}
async function navigate(container: HTMLElement, prefix: string) {
  const input = container.querySelector<HTMLInputElement>('input[placeholder="目录路径，例如 team/docs/"]')!
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, prefix)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
  await click(button(container, '进入'))
}

describe('FilesPage object download context', () => {
  it('does not deliver a late blob after the active client changes', async () => {
    const pending = deferred()
    let signal: AbortSignal | undefined
    const fetcher = fixture((_input, init) => { signal = init?.signal as AbortSignal; return pending.promise })
    const container = await render(new VaultClient('/v1', () => 'old-token', fetcher as typeof fetch))
    await click(button(container, '下载'))
    const nextFetch = fixture(async () => new Response('new bytes'))
    await render(new VaultClient('/v1', () => 'new-token', nextFetch as typeof fetch))
    expect(signal?.aborted).toBe(true)
    expect(new Headers(nextFetch.mock.calls[0][1]?.headers).get('Authorization')).toBe('Bearer new-token')
    await finish(pending)
    expect(downloadBlob).not.toHaveBeenCalled()
    expect(container.textContent).not.toContain('文件已交给浏览器处理')
  })

  it('preserves the complete key, credentials and signal without reloading read-only lists', async () => {
    const key = ' 100% report.txt '
    const read = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response('bytes'))
    const fetcher = fixture(read, key)
    const container = await render(new VaultClient('/v1', () => 'token', fetcher as typeof fetch))
    const before = fetcher.mock.calls.length
    await click(button(container, '下载'))
    expect(read).toHaveBeenCalledTimes(1)
    const [input, init] = read.mock.calls[0]
    expect(input).toBe('/v1/files/%20100%25%20report.txt%20?representation=content')
    expect(init?.method ?? 'GET').toBe('GET')
    expect(init?.credentials).toBe('same-origin')
    expect(new Headers(init?.headers).get('Authorization')).toBe('Bearer token')
    expect(init?.signal).toBeInstanceOf(AbortSignal)
    expect(init?.signal?.aborted).toBe(false)
    expect(downloadBlob).toHaveBeenCalledWith(expect.any(Blob), key)
    expect(fetcher).toHaveBeenCalledTimes(before + 1)
    expect(container.textContent).toContain('这不代表已保存到本地')
  })

})

describe('FilesPage download preparation cancellation', () => {
  it('blocks same-tick duplicate requests and retains the lock until an ignored abort settles', async () => {
    const pending = deferred()
    let signal: AbortSignal | undefined
    const read = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => { signal = init?.signal as AbortSignal; return pending.promise })
    const container = await render(new VaultClient('/v1', () => 'token', fixture(read) as typeof fetch))
    await act(async () => { button(container, '下载').click(); button(container, '下载').click() })
    expect(read).toHaveBeenCalledTimes(1)
    expect(button(container, '删除').disabled).toBe(true)
    await click(button(container, '取消下载准备'))
    expect(signal?.aborted).toBe(true)
    expect(container.textContent).toContain('正在停止下载准备')
    expect(button(container, '取消下载准备').disabled).toBe(true)
    expect(button(container, '下载').disabled).toBe(true)
    await finish(pending)
    expect(downloadBlob).not.toHaveBeenCalled()
    expect(container.textContent).toContain('下载准备已取消，未交给浏览器')
    expect(button(container, '下载').disabled).toBe(false)
  })

  it('handles AbortError without a danger alert or automatic retry, and allows an explicit retry', async () => {
    let requests = 0
    const read = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
      if (++requests > 1) return Promise.resolve(new Response('retry bytes'))
      return new Promise<Response>((_resolve, reject) => init?.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError'))))
    })
    const container = await render(new VaultClient('/v1', () => 'token', fixture(read) as typeof fetch))
    await click(button(container, '下载'))
    await click(button(container, '取消下载准备'))
    expect(read).toHaveBeenCalledTimes(1)
    expect(downloadBlob).not.toHaveBeenCalled()
    expect(container.querySelector('[data-iris-alert-tone="danger"]')).toBeNull()
    expect(container.textContent).toContain('服务端可能已读取并记录访问')
    await click(button(container, '下载'))
    expect(read).toHaveBeenCalledTimes(2)
    expect(downloadBlob).toHaveBeenCalledTimes(1)
    expect(container.textContent).not.toContain('下载准备已取消')
  })

})

describe('FilesPage download directory and view isolation', () => {
  it('keeps A→B→A fenced and does not let an old finally unlock a new download', async () => {
    const old = deferred()
    const next = deferred()
    const signals: AbortSignal[] = []
    const read = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
      signals.push(init?.signal as AbortSignal)
      return signals.length === 1 ? old.promise : next.promise
    })
    const client = new VaultClient('/v1', () => 'token', fixture(read) as typeof fetch)
    const container = await render(client)
    await click(button(container, '下载'))
    await navigate(container, 'docs/')
    await navigate(container, '')
    expect(signals[0].aborted).toBe(true)
    await click(button(container, '下载'))
    await finish(old)
    expect(downloadBlob).not.toHaveBeenCalled()
    expect(button(container, '下载').disabled).toBe(true)
    expect(signals[1].aborted).toBe(false)
    await finish(next)
    expect(downloadBlob).toHaveBeenCalledTimes(1)
    expect(button(container, '下载').disabled).toBe(false)
  })

  it.each(['mine', 'recycle'])('invalidates preparation when switching to %s', async (view) => {
    const pending = deferred()
    let signal: AbortSignal | undefined
    const fetcher = fixture((_input, init) => { signal = init?.signal as AbortSignal; return pending.promise })
    const container = await render(new VaultClient('/v1', () => 'token', fetcher as typeof fetch))
    await click(button(container, '下载'))
    if (view === 'mine') await click(button(container, '我的文件'))
    else await click(container.querySelector<HTMLElement>('.deleted-toggle input')!)
    expect(signal?.aborted).toBe(true)
    await finish(pending)
    expect(downloadBlob).not.toHaveBeenCalled()
    expect(container.textContent).not.toContain('文件已交给浏览器处理')
    expect(container.textContent).not.toContain('下载准备已取消')
    expect(container.textContent).toContain(view === 'mine' ? '仅列出当前身份拥有' : '恢复')
  })

})

describe('FilesPage download unmount and server errors', () => {
  it('discards a response after unmount even if the transport ignores abort', async () => {
    const pending = deferred()
    let signal: AbortSignal | undefined
    const fetcher = fixture((_input, init) => { signal = init?.signal as AbortSignal; return pending.promise })
    const container = await render(new VaultClient('/v1', () => 'token', fetcher as typeof fetch))
    await click(button(container, '下载'))
    act(() => mounted?.root.unmount())
    mounted = undefined
    expect(signal?.aborted).toBe(true)
    await finish(pending)
    expect(downloadBlob).not.toHaveBeenCalled()
    container.remove()
  })

  it.each([403, 404])('shows the current %s error without delivery, fallback or list reload', async (status) => {
    const read = vi.fn(async () => json({ error: { message: 'download denied' } }))
    read.mockImplementation(async () => new Response(JSON.stringify({ error: { message: 'download denied' } }), { status }))
    const fetcher = fixture(read)
    const container = await render(new VaultClient('/v1', () => 'token', fetcher as typeof fetch))
    const before = fetcher.mock.calls.length
    await click(button(container, '下载'))
    expect(read).toHaveBeenCalledTimes(1)
    expect(fetcher).toHaveBeenCalledTimes(before + 1)
    expect(downloadBlob).not.toHaveBeenCalled()
    expect(container.querySelector('[data-iris-alert-tone="danger"]')?.textContent).toContain('download denied')
    expect(button(container, '下载').disabled).toBe(false)
  })

})

describe('FilesPage download client and effect replay', () => {
  it('does not display a prior-client error after a credential switch', async () => {
    const pending = deferred()
    const oldFetch = fixture(() => pending.promise)
    const container = await render(new VaultClient('/v1', () => 'old', oldFetch as typeof fetch))
    await click(button(container, '下载'))
    const nextFetch = fixture(async () => new Response('new bytes'))
    await render(new VaultClient('/v1', () => 'new', nextFetch as typeof fetch))
    await finish(pending, new Response(JSON.stringify({ error: { message: 'old tenant denied' } }), { status: 403 }))
    expect(container.textContent).not.toContain('old tenant denied')
    expect(downloadBlob).not.toHaveBeenCalled()
    await click(button(container, '下载'))
    expect(downloadBlob).toHaveBeenCalledTimes(1)
  })

  it('supports StrictMode effect replay and a fresh download after cancellation', async () => {
    const pending = deferred()
    const read = vi.fn(async () => pending.promise)
    const container = await render(new VaultClient('/v1', () => 'token', fixture(read) as typeof fetch), true)
    await click(button(container, '下载'))
    await click(button(container, '取消下载准备'))
    await finish(pending)
    expect(downloadBlob).not.toHaveBeenCalled()
    read.mockImplementation(async () => new Response('fresh bytes'))
    await click(button(container, '下载'))
    expect(read).toHaveBeenCalledTimes(2)
    expect(downloadBlob).toHaveBeenCalledTimes(1)
    expect(button(container, '下载').disabled).toBe(false)
  })
})
