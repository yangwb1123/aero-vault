import { describe, expect, it, vi } from 'vitest'
import { VaultClient } from './vault'

function deferred() {
  let resolve: (response: Response) => void = () => undefined
  const promise = new Promise<Response>((accept) => { resolve = accept })
  return { promise, resolve }
}

describe('preview cancellation before JPEG follow-up', () => {
  it('does not start a thumbnail after a late response from a transport ignoring abort', async () => {
    const pending = deferred()
    const header = new Response('late JPEG', { headers: { 'Content-Type': 'image/jpeg' } })
    const cancel = vi.spyOn(header.body!, 'cancel')
    const fetcher = vi.fn(async () => fetcher.mock.calls.length === 1 ? pending.promise : new Response('unwanted thumbnail'))
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
    const controller = new AbortController()
    const running = client.previewVersion('photos/a.jpg', 'v/1', controller.signal)
    controller.abort()
    pending.resolve(header)
    await expect(running).rejects.toMatchObject({ name: 'AbortError' })
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect(cancel).toHaveBeenCalledTimes(1)
  })

  it('does not start a thumbnail when unused source body disposal cancels the request', async () => {
    const controller = new AbortController()
    const header = new Response('JPEG', { headers: { 'Content-Type': 'image/jpeg' } })
    vi.spyOn(header.body!, 'cancel').mockImplementation(async () => controller.abort())
    const fetcher = vi.fn(async () => header)
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
    await expect(client.previewVersion('photos/a.jpg', 'v/1', controller.signal)).rejects.toMatchObject({ name: 'AbortError' })
    expect(fetcher).toHaveBeenCalledTimes(1)
  })
})

describe('preview cancellation body boundaries', () => {
  it('rejects an already cancelled preview without calling the transport', async () => {
    const fetcher = vi.fn(async () => new Response('bytes'))
    const controller = new AbortController()
    controller.abort()
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
    await expect(client.previewObject('key', controller.signal)).rejects.toMatchObject({ name: 'AbortError' })
    expect(fetcher).not.toHaveBeenCalled()
  })

  it('disposes a late text body instead of buffering it', async () => {
    const pending = deferred()
    const response = new Response('late text', { headers: { 'Content-Type': 'text/plain' } })
    const cancel = vi.spyOn(response.body!, 'cancel')
    const blob = vi.spyOn(response, 'blob')
    const client = new VaultClient('/v1', () => 'token', vi.fn(() => pending.promise) as typeof fetch)
    const controller = new AbortController()
    const running = client.previewVersion('a.txt', 'v1', controller.signal)
    controller.abort()
    pending.resolve(response)
    await expect(running).rejects.toMatchObject({ name: 'AbortError' })
    expect(cancel).toHaveBeenCalledTimes(1)
    expect(blob).not.toHaveBeenCalled()
  })
})

describe('preview cancellation after thumbnail request', () => {
  it('disposes a late thumbnail and keeps the same version pin without further requests', async () => {
    const pending = deferred()
    const response = new Response('late thumbnail', { headers: { 'Content-Type': 'image/jpeg' } })
    const cancel = vi.spyOn(response.body!, 'cancel')
    const blob = vi.spyOn(response, 'blob')
    const controller = new AbortController()
    const fetcher = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => {
      if (fetcher.mock.calls.length === 1) return new Response('JPEG', { headers: { 'Content-Type': 'image/jpeg' } })
      controller.abort()
      return pending.promise
    })
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
    const running = client.previewVersion('photos/a.jpg', 'v/1', controller.signal)
    pending.resolve(response)
    await expect(running).rejects.toMatchObject({ name: 'AbortError' })
    expect(fetcher).toHaveBeenCalledTimes(2)
    expect(String(fetcher.mock.calls[1][0])).toContain('version=v%2F1')
    expect(cancel).toHaveBeenCalledTimes(1)
    expect(blob).not.toHaveBeenCalled()
  })

  it('does not return a Blob if cancellation occurs during body buffering', async () => {
    const controller = new AbortController()
    const response = new Response('text', { headers: { 'Content-Type': 'text/plain' } })
    vi.spyOn(response, 'blob').mockImplementation(async () => { controller.abort(); return new Blob(['late bytes']) })
    const fetcher = vi.fn(async () => response)
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
    await expect(client.previewVersion('a.txt', 'v1', controller.signal)).rejects.toMatchObject({ name: 'AbortError' })
    expect(fetcher).toHaveBeenCalledTimes(1)
  })

  it('preserves cancellation even when unused body disposal fails', async () => {
    const controller = new AbortController()
    const response = new Response('text', { headers: { 'Content-Type': 'text/plain' } })
    const cancel = vi.spyOn(response.body!, 'cancel').mockRejectedValue(new Error('cleanup failed'))
    const fetcher = vi.fn(async () => { controller.abort(); return response })
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
    await expect(client.previewObject('key', controller.signal)).rejects.toMatchObject({ name: 'AbortError' })
    expect(cancel).toHaveBeenCalledTimes(1)
    expect(fetcher).toHaveBeenCalledTimes(1)
  })
})
