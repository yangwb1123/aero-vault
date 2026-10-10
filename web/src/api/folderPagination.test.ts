import { describe, expect, it, vi } from 'vitest'
import { VaultClient } from './vault'

describe('folder list continuation contract', () => {
  it.each(['all', 'mine'] as const)('preserves the %s view, path and opaque marker', async (view) => {
    const fetcher = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response(
      JSON.stringify({ prefix: 'docs/', items: [], has_more: false, next_marker: '' }),
      { status: 200, headers: { 'Content-Type': 'application/json' } },
    ))
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
    const marker = 'docs/中文 +%/a.txt'
    await expect(client.listFolder('docs/', view, marker)).resolves.toMatchObject({ has_more: false })
    const url = new URL(String(fetcher.mock.calls[0][0]), 'http://localhost')
    expect(url.pathname).toBe('/v1/folders')
    expect(url.searchParams.get('path')).toBe('docs/')
    expect(url.searchParams.get('marker')).toBe(marker)
    expect(url.searchParams.get('view')).toBe(view === 'mine' ? 'mine' : null)
    expect(new Headers(fetcher.mock.calls[0][1]?.headers).get('Authorization')).toBe('Bearer token')
  })
})
