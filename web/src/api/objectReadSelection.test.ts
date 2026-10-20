import { describe, expect, it, vi } from 'vitest'
import { VaultClient } from './vault'

function text(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result))
    reader.onerror = () => reject(reader.error)
    reader.readAsText(blob)
  })
}

describe('explicit object content reads', () => {
  it.each(['tags', 'versions', 'acl', 'metadata', 'thumbnail'])('downloads the full %s-suffixed Key, not a subresource', async (suffix) => {
    const key = ` docs/100% report.txt /${suffix}`
    const fetcher = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
      const url = new URL(String(input), 'http://vault')
      return url.searchParams.get('representation') === 'content'
        ? new Response('literal object bytes', { headers: { 'Content-Type': 'text/plain' } })
        : new Response('{"tags":{"other":"object"}}', { headers: { 'Content-Type': 'application/json' } })
    })
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
    const controller = new AbortController()
    expect(await text(await client.download(key, controller.signal))).toBe('literal object bytes')
    const [input, init] = fetcher.mock.calls[0]
    expect(new URL(String(input), 'http://vault').pathname).toBe(`/v1/files/%20docs/100%25%20report.txt%20/${suffix}`)
    expect(init?.signal).toBe(controller.signal)
    expect(new Headers(init?.headers).get('Authorization')).toBe('Bearer token')
    expect(fetcher).toHaveBeenCalledTimes(1)
  })

  it('uses content for a text preview without changing Range or buffering another resource', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
      const representation = new URL(String(input), 'http://vault').searchParams.get('representation')
      return new Response(representation === 'content' ? 'selected text' : 'wrong metadata', {
        status: 206, headers: { 'Content-Type': 'text/plain' },
      })
    })
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
    const preview = await client.previewObject('literal/metadata')
    expect(await text(preview.body)).toBe('selected text')
    expect(preview.partial).toBe(true)
    expect((fetcher.mock.calls[0][1]?.headers as Record<string, string>).Range).toBe('bytes=0-4095')
    expect(fetcher).toHaveBeenCalledTimes(1)
  })
})

describe('explicit thumbnail and historical selection', () => {
  it.each([undefined, ' old/v1% '])('derives from the selected JPEG, not its thumbnail-named neighbour (version=%s)', async (versionID) => {
    const fetcher = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
      const url = new URL(String(input), 'http://vault')
      if (fetcher.mock.calls.length === 1) return new Response('source header', { headers: { 'Content-Type': 'image/jpeg' } })
      return new Response(url.searchParams.get('representation') === 'thumbnail' ? 'selected JPEG thumbnail' : 'wrong neighbour', {
        headers: { 'Content-Type': 'image/jpeg' },
      })
    })
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
    const controller = new AbortController()
    const preview = versionID === undefined ? await client.previewObject('photo/thumbnail', controller.signal)
      : await client.previewVersion('photo/thumbnail', versionID, controller.signal)
    expect(await text(preview.body)).toBe('selected JPEG thumbnail')
    const urls = fetcher.mock.calls.map(([input]) => new URL(String(input), 'http://vault'))
    expect(urls.map((url) => url.pathname)).toEqual(['/v1/files/photo/thumbnail', '/v1/files/photo/thumbnail'])
    expect(urls.map((url) => url.searchParams.get('representation'))).toEqual(['content', 'thumbnail'])
    expect(urls.map((url) => url.searchParams.get('version'))).toEqual([versionID ?? null, versionID ?? null])
    expect(urls[1].searchParams.get('w')).toBe('1280')
    expect(urls[1].searchParams.get('h')).toBe('960')
    expect(fetcher.mock.calls.every(([, init]) => init?.signal === controller.signal)).toBe(true)
  })

  it('selects raw historical content even for a thumbnail-suffixed Key', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => new Response(
      new URL(String(input), 'http://vault').searchParams.get('representation') === 'content' ? 'selected old bytes' : 'wrong derived image',
    ))
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
    expect(await text(await client.downloadVersion('image/thumbnail', 'v/1'))).toBe('selected old bytes')
    const url = new URL(String(fetcher.mock.calls[0][0]), 'http://vault')
    expect(url.searchParams.get('version')).toBe('v/1')
    expect(fetcher).toHaveBeenCalledTimes(1)
  })
})
