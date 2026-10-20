import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { VaultClient } from '../api/vault'
import { downloadBlob } from '../download'
import { useObjectDownload } from './useObjectDownload'

vi.mock('../download', () => ({ downloadBlob: vi.fn(async () => undefined) }))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let mounted: { container: HTMLDivElement; root: Root } | undefined
let transfer: ReturnType<typeof useObjectDownload>
beforeEach(() => vi.mocked(downloadBlob).mockReset().mockResolvedValue(undefined))
afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
})
function deferred<T>() {
  let resolve: (value: T) => void = () => undefined
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}
function Probe({ client }: { client: VaultClient }) {
  transfer = useObjectDownload(client, 'probe')
  return <span>{transfer.pending?.key}</span>
}
async function render(fetcher: typeof fetch) {
  const container = document.createElement('div')
  document.body.append(container)
  mounted = { container, root: createRoot(container) }
  const client = new VaultClient('/v1', () => 'token', fetcher)
  await act(async () => mounted?.root.render(<Probe client={client} />))
}
async function settle() { await act(async () => { await new Promise((resolve) => window.setTimeout(resolve, 0)) }) }

describe('shared object download preparation', () => {
  it('rejects even a retained cancel callback once browser handoff has begun', async () => {
    const response = deferred<Response>()
    const handoff = deferred<void>()
    let signal: AbortSignal | undefined
    vi.mocked(downloadBlob).mockImplementationOnce(() => handoff.promise)
    await render((async (_input, init) => { signal = init?.signal as AbortSignal; return response.promise }) as typeof fetch)
    let running: Promise<void> | undefined
    act(() => { running = transfer.download({ key: 'one.txt' }) })
    const cancel = transfer.cancel
    await act(async () => { response.resolve(new Response('bytes')); await response.promise })
    await settle()
    expect(downloadBlob).toHaveBeenCalledTimes(1)
    expect(transfer.canCancel).toBe(false)
    act(() => cancel())
    expect(signal?.aborted).toBe(false)
    expect(transfer.stopping).toBe(false)
    expect(transfer.pending?.key).toBe('one.txt')
    await act(async () => { handoff.resolve(); await running })
    expect(transfer.pending).toBeUndefined()
    expect(transfer.notice?.text).toContain('文件已交给浏览器处理')
  })

  it('drops a body resolved after user cancellation, not only late response headers', async () => {
    const body = deferred<Blob>()
    const response = new Response('ignored body')
    vi.spyOn(response, 'blob').mockReturnValue(body.promise)
    let signal: AbortSignal | undefined
    await render((async (_input, init) => { signal = init?.signal as AbortSignal; return response }) as typeof fetch)
    let running: Promise<void> | undefined
    act(() => { running = transfer.download({ key: 'one.txt' }) })
    await settle()
    expect(response.blob).toHaveBeenCalledTimes(1)
    act(() => transfer.cancel())
    expect(signal?.aborted).toBe(true)
    await act(async () => { body.resolve(new Blob(['late body'])); await running })
    expect(downloadBlob).not.toHaveBeenCalled()
    expect(transfer.pending).toBeUndefined()
    expect(transfer.notice?.tone).toBe('warning')
  })

})

describe('shared object download target and delivery', () => {
  it('captures exact version and key at start even if the caller mutates the target later', async () => {
    const response = deferred<Response>()
    const fetcher = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => response.promise)
    await render(fetcher as typeof fetch)
    const target = { key: ' docs/a.txt ', versionID: 'v/1?%' }
    let running: Promise<void> | undefined
    act(() => { running = transfer.download(target) })
    target.key = 'other.txt'
    target.versionID = 'v2'
    await act(async () => { response.resolve(new Response('version bytes')); await running })
    expect(fetcher.mock.calls[0][0]).toBe('/v1/files/%20docs/a.txt%20?representation=content&version=v%2F1%3F%25')
    expect(fetcher.mock.calls[0][1]?.signal).toBeInstanceOf(AbortSignal)
    expect(downloadBlob).toHaveBeenCalledWith(expect.any(Blob), 'a.txt -v_1?%')
    expect(transfer.pending).toBeUndefined()
  })

  it('reports browser delivery failure without success or automatic network retry', async () => {
    const fetcher = vi.fn(async () => new Response('bytes'))
    await render(fetcher as typeof fetch)
    vi.mocked(downloadBlob).mockRejectedValueOnce(new Error('browser cannot download'))
    await act(async () => transfer.download({ key: 'one.txt' }))
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect(transfer.pending).toBeUndefined()
    expect(transfer.error).toBe('browser cannot download')
    expect(transfer.notice?.tone).toBe('danger')
    expect(transfer.notice?.text).not.toContain('文件已交给浏览器处理')
  })
})
