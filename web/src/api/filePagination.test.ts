import { describe, expect, it, vi } from 'vitest'
import { VaultClient } from './vault'

describe('file list pagination', () => {
  it('sends the continuation marker without losing prefix or deleted filters', async () => {
    const fetcher = vi.fn(async (_input: RequestInfo | URL) => new Response(
      JSON.stringify({ objects: [], has_more: false }),
      { status: 200, headers: { 'Content-Type': 'application/json' } },
    ))
    const client = new VaultClient('/v1', () => '', fetcher as typeof fetch)

    await client.listFiles('docs/', true, 'docs/a.txt')

    expect(String(fetcher.mock.calls[0][0])).toBe(
      '/v1/files?limit=200&marker=docs%2Fa.txt&prefix=docs%2F&deleted=true',
    )
  })
})
