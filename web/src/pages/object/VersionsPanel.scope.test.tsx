import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { VaultClient } from '../../api/vault'
import { downloadBlob } from '../../download'
import { VersionsPanel } from './VersionsPanel'

vi.mock('../../download', () => ({ downloadBlob: vi.fn(async () => undefined) }))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let mounted: { container: HTMLDivElement; root: Root } | undefined
const listing = () => new Response(JSON.stringify({ versions: [
  { version_id: 'v/1', size: 10, etag: 'one', updated_at: '2026-09-26T00:00:00Z' },
  { version_id: 'v/2', size: 10, etag: 'two', updated_at: '2026-09-26T00:00:00Z' },
] }))

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
async function render(client: VaultClient, objectKey = 'docs/a.txt') {
  if (!mounted) {
    const container = document.createElement('div')
    document.body.append(container)
    mounted = { container, root: createRoot(container) }
  }
  await act(async () => mounted?.root.render(<VersionsPanel client={client} objectKey={objectKey} />))
  return mounted.container
}
function buttons(container: HTMLElement, label: string): HTMLButtonElement[] {
  return [...container.querySelectorAll('button')].filter((entry) => entry.textContent?.trim() === label)
}
async function click(target: HTMLElement) { await act(async () => target.click()) }
async function finish(pending: ReturnType<typeof deferred>, response = new Response('version bytes')) {
  await act(async () => { pending.resolve(response); await pending.promise; await new Promise((resolve) => window.setTimeout(resolve, 0)) })
}

describe('VersionsPanel download fencing', () => {
  it('blocks same-tick duplicate downloads and other downloads/refresh until settled', async () => {
    const pending = deferred()
    let signal: AbortSignal | undefined
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith('/versions')) return listing()
      signal = init?.signal as AbortSignal
      return pending.promise
    })
    const container = await render(new VaultClient('/v1', () => 'token', fetcher as typeof fetch))
    await act(async () => { buttons(container, '下载')[0].click(); buttons(container, '下载')[0].click() })
    expect(fetcher).toHaveBeenCalledTimes(2)
    expect(buttons(container, '下载').every((entry) => entry.disabled)).toBe(true)
    expect(buttons(container, '刷新版本')[0].disabled).toBe(true)
    expect(signal?.aborted).toBe(false)
    await finish(pending)
    expect(downloadBlob).toHaveBeenCalledTimes(1)
    expect(downloadBlob).toHaveBeenCalledWith(expect.any(Blob), 'a.txt-v_1')
    expect(buttons(container, '下载').every((entry) => !entry.disabled)).toBe(true)
  })

  it('does not deliver an old A response after A→B→A or clear the new pending request', async () => {
    const old = deferred()
    const next = deferred()
    let signal: AbortSignal | undefined
    let downloads = 0
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith('/versions')) return listing()
      downloads += 1
      if (downloads === 1) { signal = init?.signal as AbortSignal; return old.promise }
      return next.promise
    })
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
    const container = await render(client)
    await click(buttons(container, '下载')[0])
    await render(client, 'docs/b.txt')
    await render(client)
    expect(signal?.aborted).toBe(true)
    await click(buttons(container, '下载')[0])
    await finish(old, new Response('old response'))
    expect(downloadBlob).not.toHaveBeenCalled()
    expect(buttons(container, '下载').every((entry) => entry.disabled)).toBe(true)
    await finish(next, new Response('new response'))
    expect(downloadBlob).toHaveBeenCalledTimes(1)
    expect(buttons(container, '下载').every((entry) => !entry.disabled)).toBe(true)
  })

  it('drops prior-client download errors and credentials on a same-key client switch', async () => {
    const pending = deferred()
    let signal: AbortSignal | undefined
    const oldFetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith('/versions')) return listing()
      signal = init?.signal as AbortSignal
      return pending.promise
    })
    const container = await render(new VaultClient('/v1', () => 'old-token', oldFetch as typeof fetch))
    await click(buttons(container, '下载')[0])
    const newFetch = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => listing())
    await render(new VaultClient('/v1', () => 'new-token', newFetch as typeof fetch))
    expect(signal?.aborted).toBe(true)
    await finish(pending, new Response(JSON.stringify({ error: { message: 'old tenant forbidden' } }), { status: 403 }))
    expect(downloadBlob).not.toHaveBeenCalled()
    expect(container.textContent).not.toContain('old tenant forbidden')
    expect(new Headers(newFetch.mock.calls[0][1]?.headers).get('Authorization')).toBe('Bearer new-token')
    expect(buttons(container, '下载').every((entry) => !entry.disabled)).toBe(true)
  })

  it('aborts and prevents delivery after unmount even when the transport ignores abort', async () => {
    const pending = deferred()
    let signal: AbortSignal | undefined
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith('/versions')) return listing()
      signal = init?.signal as AbortSignal
      return pending.promise
    })
    const container = await render(new VaultClient('/v1', () => 'token', fetcher as typeof fetch))
    await click(buttons(container, '下载')[0])
    act(() => mounted?.root.unmount())
    mounted = undefined
    expect(signal?.aborted).toBe(true)
    await finish(pending)
    expect(downloadBlob).not.toHaveBeenCalled()
    container.remove()
  })
})
