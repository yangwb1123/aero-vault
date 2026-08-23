import { describe, expect, it, vi } from 'vitest'
import { VaultApiError, VaultClient } from './vault'

describe('folder and batch API contracts', () => {
  it('lists, creates, and recursively deletes a folder', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'POST') return jsonResponse({ key: 'team/docs/' }, 201)
      if (init?.method === 'DELETE') return jsonResponse({ deleted: 3, failed: 0 })
      return jsonResponse({ prefix: 'team/', items: [{ name: 'docs', type: 'folder' }] })
    })
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)

    await expect(client.listFolder('team/')).resolves.toMatchObject({ prefix: 'team/' })
    await client.createFolder('team/docs')
    await client.deleteFolder('team/docs')

    expect(String(fetcher.mock.calls[0][0])).toBe('/v1/folders?path=team%2F')
    expect(String(fetcher.mock.calls[1][0])).toBe('/v1/folders/team/docs')
    expect(fetcher.mock.calls[2][1]?.method).toBe('DELETE')
  })

  it('surfaces a partial recursive folder delete', async () => {
    const fetcher = vi.fn(async () => jsonResponse({ deleted: 2, failed: 1 }))
    const client = new VaultClient('/v1', () => '', fetcher as typeof fetch)
    await expect(client.deleteFolder('protected')).rejects.toEqual(
      new VaultApiError(409, 'PartialFailure', '目录中有 1 个对象未能删除'),
    )
  })

  it('maps per-object batch delete and tag results', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
      if (String(input).endsWith('/delete')) return jsonResponse({ results: [{ key: 'a.txt', deleted: true }] })
      return jsonResponse({ results: [{ key: 'a.txt' }] })
    })
    const client = new VaultClient('/v1', () => '', fetcher as typeof fetch)

    await expect(client.batchDeleteFiles(['a.txt'])).resolves.toEqual([{ key: 'a.txt', deleted: true }])
    await expect(client.batchTagFiles(['a.txt'], { team: 'platform' })).resolves.toEqual([{ key: 'a.txt' }])
    expect(JSON.parse(String(fetcher.mock.calls[1][1]?.body))).toEqual({ keys: ['a.txt'], tags: { team: 'platform' } })
  })
})

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}
