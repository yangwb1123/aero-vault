import { describe, expect, it, vi } from 'vitest'
import { VaultClient } from './vault'

function blobText(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result))
    reader.onerror = () => reject(reader.error)
    reader.readAsText(blob)
  })
}

describe('fixed-version object reads', () => {
  it('preserves opaque key/id and the authenticated Range/signal contract', async () => {
    const fetcher = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response('old text', {
      status: 206, headers: { 'Content-Type': 'text/plain; charset=utf-8' },
    }))
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
    const controller = new AbortController()
    const preview = await client.previewVersion(' docs/100% report.txt ', ' v/1% ', controller.signal)
    expect(await blobText(preview.body)).toBe('old text')
    expect(preview).toMatchObject({ contentType: 'text/plain', partial: true })
    expect(String(fetcher.mock.calls[0][0])).toBe('/v1/files/%20docs/100%25%20report.txt%20?representation=content&version=+v%2F1%25+')
    const init = fetcher.mock.calls[0][1]!
    const headers = new Headers(init.headers)
    expect((init.headers as Record<string, string>).Range).toBe('bytes=0-4095')
    expect(headers.get('Accept')).toBe('*/*')
    expect(headers.get('Authorization')).toBe('Bearer token')
    expect(init.credentials).toBe('same-origin')
    expect(init.signal).toBe(controller.signal)
  })

  it('pins both JPEG requests to the same ID and releases the unused Range body', async () => {
    const header = new Response('jpeg header', { status: 206, headers: { 'Content-Type': 'image/jpeg' } })
    const cancel = vi.spyOn(header.body!, 'cancel')
    const fetcher = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => fetcher.mock.calls.length === 1
      ? header : new Response('old thumbnail', { headers: { 'Content-Type': 'image/jpeg' } }))
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
    const controller = new AbortController()
    const preview = await client.previewVersion('photos/hero.jpg', 'v/1', controller.signal)
    expect(preview).toMatchObject({ contentType: 'image/jpeg', partial: false })
    expect(await blobText(preview.body)).toBe('old thumbnail')
    expect(cancel).toHaveBeenCalledTimes(1)
    expect(String(fetcher.mock.calls[0][0])).toBe('/v1/files/photos/hero.jpg?representation=content&version=v%2F1')
    expect(String(fetcher.mock.calls[1][0])).toBe('/v1/files/photos/hero.jpg?representation=thumbnail&version=v%2F1&w=1280&h=960')
    for (const [, init] of fetcher.mock.calls) {
      expect(init?.signal).toBe(controller.signal)
      expect(new Headers(init?.headers).get('Authorization')).toBe('Bearer token')
    }
  })

  it.each([403, 404])('does not read latest bytes when the pinned source returns %s', async (status) => {
    const fetcher = vi.fn(async () => new Response(JSON.stringify({ error: { message: 'version denied or missing' } }), { status }))
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
    await expect(client.previewVersion('photo.jpg', 'old')).rejects.toThrow('version denied or missing')
    expect(fetcher).toHaveBeenCalledTimes(1)
  })

  it('does not fall back to latest when the pinned thumbnail is rejected', async () => {
    const fetcher = vi.fn(async () => fetcher.mock.calls.length === 1
      ? new Response('header', { headers: { 'Content-Type': 'image/jpeg' } })
      : new Response(JSON.stringify({ error: { message: 'thumbnail forbidden' } }), { status: 403 }))
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
    await expect(client.previewVersion('photo.jpg', 'old')).rejects.toThrow('thumbnail forbidden')
    expect(fetcher).toHaveBeenCalledTimes(2)
  })

  it('rejects an empty version locally instead of issuing an unpinned read', async () => {
    const fetcher = vi.fn(async () => new Response('latest'))
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
    await expect(client.previewVersion('key', '')).rejects.toThrow('必须指定 Version ID')
    await expect(client.downloadVersion('key', '')).rejects.toThrow('必须指定 Version ID')
    expect(fetcher).not.toHaveBeenCalled()
  })

  it('passes AbortSignal through exact version downloads without changing the path', async () => {
    const fetcher = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response('old bytes'))
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
    const controller = new AbortController()
    await client.downloadVersion('docs/a b.txt', 'v/1', controller.signal)
    expect(String(fetcher.mock.calls[0][0])).toBe('/v1/files/docs/a%20b.txt?representation=content&version=v%2F1')
    expect(fetcher.mock.calls[0][1]?.signal).toBe(controller.signal)
  })
})
