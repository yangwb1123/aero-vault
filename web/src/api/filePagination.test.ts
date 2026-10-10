import { describe, expect, it, vi } from 'vitest'
import { VaultClient } from './vault'
import { listNextFilePage } from './filePagination'

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

  it('keeps the mine view in initial, folder and continuation requests', async () => {
    const fetcher = vi.fn(async (_input: RequestInfo | URL) => new Response(
      JSON.stringify({ objects: [], has_more: false }), { status: 200 },
    ))
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
    await client.listFiles('docs/', true, '', 'mine')
    await listNextFilePage(client, 'docs/', true, 'docs/a.txt', 'mine')
    await client.listFolder('docs/', 'mine')
    expect(fetcher.mock.calls.map(([input]) => String(input))).toEqual([
      '/v1/files?limit=200&view=mine&prefix=docs%2F&deleted=true',
      '/v1/files?limit=200&marker=docs%2Fa.txt&view=mine&prefix=docs%2F&deleted=true',
      '/v1/folders?view=mine&path=docs%2F',
    ])
  })
})
