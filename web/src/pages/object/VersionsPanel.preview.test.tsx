import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { VaultClient } from '../../api/vault'
import { VersionsPanel } from './VersionsPanel'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
const versions = ['v/old', 'v/new'].map((version_id) => ({ version_id, size: 20, etag: version_id, updated_at: '2026-09-26T00:00:00Z' }))
let mounted: { container: HTMLDivElement; root: Root } | undefined
const originalText = Object.getOwnPropertyDescriptor(Blob.prototype, 'text')
// jsdom omits Blob.text; mirror the browser API with its FileReader.
beforeEach(() => Object.defineProperty(Blob.prototype, 'text', { configurable: true, value: function (this: Blob) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result))
    reader.onerror = () => reject(reader.error)
    reader.readAsText(this)
  })
} }))

afterEach(() => {
  if (originalText) Object.defineProperty(Blob.prototype, 'text', originalText)
  else Reflect.deleteProperty(Blob.prototype, 'text')
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
})
function json(value: unknown) { return new Response(JSON.stringify(value), { headers: { 'Content-Type': 'application/json' } }) }
function text(value: string) { return new Response(value, { status: 206, headers: { 'Content-Type': 'text/plain' } }) }
function deferred() {
  let resolve: (response: Response) => void = () => undefined
  const promise = new Promise<Response>((accept) => { resolve = accept })
  return { promise, resolve }
}
async function render(client: VaultClient, objectKey = ' docs/100% report.txt ', strict = false) {
  if (!mounted) {
    const container = document.createElement('div')
    document.body.append(container)
    mounted = { container, root: createRoot(container) }
  }
  const panel = <VersionsPanel client={client} objectKey={objectKey} />
  await act(async () => mounted?.root.render(strict ? <React.StrictMode>{panel}</React.StrictMode> : panel))
  return mounted.container
}
function button(container: HTMLElement, label: string): HTMLButtonElement {
  const found = [...container.querySelectorAll('button')].find((entry) => entry.textContent?.trim() === label)
  if (!found) throw new Error(`missing ${label}`)
  return found
}
function rowButton(container: HTMLElement, id: string, label: string) {
  const row = [...container.querySelectorAll('tbody tr')].find((entry) => entry.querySelector('strong')?.textContent === id)
  if (!row) throw new Error(`missing ${id}`)
  return button(row as HTMLElement, label)
}
async function flushReader() {
  await act(async () => { await new Promise((resolve) => window.setTimeout(resolve, 0)) })
  await act(async () => { await new Promise((resolve) => window.setTimeout(resolve, 0)) })
}
async function click(target: HTMLElement) { await act(async () => target.click()); await flushReader() }

describe('VersionsPanel fixed preview', () => {
  it('does not preload bytes; selection shows exact ID and escaped partial text', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => String(input).endsWith('/versions') ? json({ versions }) : text('<script>old text</script>'))
    const container = await render(new VaultClient('/v1', () => 'token', fetcher as typeof fetch))
    expect(fetcher).toHaveBeenCalledTimes(1)
    await click(rowButton(container, 'v/old', '预览'))
    expect(String(fetcher.mock.calls[1][0])).toBe('/v1/files/%20docs/100%25%20report.txt%20?representation=content&version=v%2Fold')
    expect(container.textContent).toContain('历史版本预览')
    expect(container.textContent).toContain('Version ID：v/old')
    expect(container.textContent).toContain('前 4 KiB')
    expect(container.querySelector('pre')?.textContent).toBe('<script>old text</script>')
    expect(container.querySelector('script')).toBeNull()
  })

  it('drops and aborts a slow prior-version preview when another version is selected', async () => {
    const pending = deferred()
    let signal: AbortSignal | undefined
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith('/versions')) return json({ versions })
      if (String(input).includes('v%2Fold')) { signal = init?.signal as AbortSignal; return pending.promise }
      return text('new selection')
    })
    const container = await render(new VaultClient('/v1', () => 'token', fetcher as typeof fetch))
    await click(rowButton(container, 'v/old', '预览'))
    await click(rowButton(container, 'v/new', '预览'))
    expect(signal?.aborted).toBe(true)
    expect(container.querySelector('pre')?.textContent).toBe('new selection')
    await act(async () => { pending.resolve(text('late old bytes')); await pending.promise })
    await flushReader()
    expect(container.textContent).not.toContain('late old bytes')
    expect(container.textContent).toContain('Version ID：v/new')
  })

  it.each(['close', 'refresh'])('clears an active preview on %s and ignores its late completion', async (action) => {
    const pending = deferred()
    let signal: AbortSignal | undefined
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith('/versions')) return json({ versions })
      signal = init?.signal as AbortSignal
      return pending.promise
    })
    const container = await render(new VaultClient('/v1', () => 'token', fetcher as typeof fetch))
    await click(rowButton(container, 'v/old', '预览'))
    await click(button(container, action === 'close' ? '关闭版本预览' : '刷新版本'))
    expect(signal?.aborted).toBe(true)
    await act(async () => { pending.resolve(text('late bytes')); await pending.promise })
    await flushReader()
    expect(container.textContent).not.toContain('late bytes')
    expect(container.textContent).not.toContain('关闭版本预览')
    expect(fetcher).toHaveBeenCalledTimes(action === 'refresh' ? 3 : 2)
  })

  it('does not provide actions for delete markers but permits reads of locked versions', async () => {
    const records = [...versions, { ...versions[0], version_id: 'marker', is_delete_marker: true, deleted_at: '2026-09-26T00:00:00Z' },
      { ...versions[0], version_id: 'locked', locked_until: '2036-01-01T00:00:00Z' },
      { ...versions[0], version_id: 'soft', deleted_at: '2026-09-26T00:00:00Z' }]
    const fetcher = vi.fn(async (input: RequestInfo | URL) => String(input).endsWith('/versions') ? json({ versions: records }) : text('locked text'))
    const container = await render(new VaultClient('/v1', () => 'token', fetcher as typeof fetch))
    expect(rowButton(container, 'marker', '预览').disabled).toBe(true)
    expect(rowButton(container, 'marker', '下载').disabled).toBe(true)
    expect(rowButton(container, 'locked', '预览').disabled).toBe(false)
    expect(rowButton(container, 'locked', '下载').disabled).toBe(false)
    expect(rowButton(container, 'soft', '预览').disabled).toBe(false)
    expect(rowButton(container, 'soft', '下载').disabled).toBe(false)
    await click(rowButton(container, 'locked', '预览'))
    expect(container.querySelector('pre')?.textContent).toBe('locked text')
  })

  it('renders a rejected pin without requesting the latest object', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => String(input).endsWith('/versions') ? json({ versions })
      : new Response(JSON.stringify({ error: { message: '历史版本不可读' } }), { status: 403 }))
    const container = await render(new VaultClient('/v1', () => 'token', fetcher as typeof fetch))
    await click(rowButton(container, 'v/old', '预览'))
    expect(container.textContent).toContain('历史版本不可读')
    expect(fetcher).toHaveBeenCalledTimes(2)
    expect(container.querySelector('pre')).toBeNull()
  })

  it('clears a selected preview and pending bytes on client/object switches', async () => {
    const pending = deferred()
    let signal: AbortSignal | undefined
    const oldFetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith('/versions')) return json({ versions })
      signal = init?.signal as AbortSignal
      return pending.promise
    })
    const oldClient = new VaultClient('/v1', () => 'old', oldFetch as typeof fetch)
    const container = await render(oldClient)
    await click(rowButton(container, 'v/old', '预览'))
    const nextFetch = vi.fn(async () => json({ versions }))
    await render(new VaultClient('/v1', () => 'new', nextFetch as typeof fetch), 'other.txt')
    expect(signal?.aborted).toBe(true)
    await act(async () => { pending.resolve(text('old tenant bytes')); await pending.promise })
    await flushReader()
    expect(container.textContent).not.toContain('old tenant bytes')
    expect(container.textContent).not.toContain('关闭版本预览')
    expect(nextFetch).toHaveBeenCalledTimes(1)
  })

  it('works after StrictMode effect replay without unpinned reads', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => String(input).endsWith('/versions') ? json({ versions }) : text('strict preview'))
    const container = await render(new VaultClient('/v1', () => 'token', fetcher as typeof fetch), 'docs/a.txt', true)
    await click(rowButton(container, 'v/old', '预览'))
    expect(container.querySelector('pre')?.textContent).toBe('strict preview')
    expect(fetcher.mock.calls.filter(([input]) => !String(input).endsWith('/versions')).every(([input]) => String(input).endsWith('?representation=content&version=v%2Fold'))).toBe(true)
  })
})
