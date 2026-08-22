import { describe, expect, it, vi } from 'vitest'
import { VaultApiError, VaultClient } from './vault'

describe('VaultClient', () => {
  it('uses the Snaplink bearer token and maps the files contract', async () => {
    const fetcher = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const headers = new Headers(init?.headers)
      expect(headers.get('Authorization')).toBe('Bearer snaplink-token')
      return new Response(JSON.stringify({ objects: [], has_more: false }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    })
    const client = new VaultClient('/v1', () => 'snaplink-token', fetcher as typeof fetch)
    await expect(client.listFiles('docs/')).resolves.toEqual({ objects: [], has_more: false })
    expect(String(fetcher.mock.calls[0][0])).toBe('/v1/files?limit=200&prefix=docs%2F')
  })

  it('surfaces the platform error envelope', async () => {
    const fetcher = vi.fn(async () =>
      new Response(JSON.stringify({ error: { code: 'Forbidden', message: 'denied', request_id: 'r1' } }), {
        status: 403,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    const client = new VaultClient('/v1', () => '', fetcher as typeof fetch)
    await expect(client.getSession()).rejects.toEqual(
      new VaultApiError(403, 'Forbidden', 'denied', 'r1'),
    )
  })
})
