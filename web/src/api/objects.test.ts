import { describe, expect, it, vi } from 'vitest'
import { VaultClient } from './vault'

describe('VaultClient object management APIs', () => {
  it('loads deleted files and replaces tags and metadata', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (init?.method === 'PUT') {
        const body = JSON.parse(String(init.body))
        expect(body).toEqual(path.endsWith('/tags') ? { team: 'platform' } : { owner: 'alice' })
        return jsonResponse({ status: 'ok' })
      }
      if (path.endsWith('/tags')) return jsonResponse({ tags: { team: 'platform' } })
      if (path.endsWith('/metadata')) return jsonResponse({ owner: 'alice' })
      return jsonResponse({ objects: [], has_more: false })
    })
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)

    await client.listFiles('docs/', true)
    await expect(client.getTags('docs/a b.txt')).resolves.toEqual({ team: 'platform' })
    await client.putTags('docs/a b.txt', { team: 'platform' })
    await expect(client.getMetadata('docs/a b.txt')).resolves.toEqual({ owner: 'alice' })
    await client.putMetadata('docs/a b.txt', { owner: 'alice' })
    expect(String(fetcher.mock.calls[0][0])).toBe('/v1/files?limit=200&prefix=docs%2F&deleted=true')
    expect(String(fetcher.mock.calls[1][0])).toBe('/v1/files/docs/a%20b.txt/tags')
  })

  it('lists and downloads an exact object version', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      if (String(input).endsWith('/versions')) return jsonResponse({ versions: [{ version_id: 'v/1' }] })
      return new Response('version bytes', { status: 200 })
    })
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)

    await expect(client.listVersions('docs/a.txt')).resolves.toEqual([{ version_id: 'v/1' }])
    await expect(client.downloadVersion('docs/a.txt', 'v/1')).resolves.toBeInstanceOf(Blob)
    expect(String(fetcher.mock.calls[1][0])).toBe('/v1/files/docs/a.txt?version=v%2F1')
  })

  it('manages restore, presign, and legal hold operations', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path.includes('/presign?')) return jsonResponse({ url: 'https://vault/signed', expires: '2026-01-01T00:00:00Z' })
      if (path.includes('/legal-hold?') && init?.method === 'DELETE') return new Response(null, { status: 204 })
      if (path.includes('/legal-hold?')) return jsonResponse({ error: { code: 'NotFound', message: 'missing' } }, 404)
      return jsonResponse({ status: 'ok' })
    })
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)

    await client.restoreObject('trash/a.txt')
    await expect(client.presignObject('trash/a.txt', 'get', 900)).resolves.toMatchObject({ url: 'https://vault/signed' })
    await expect(client.getLegalHold('trash/a.txt')).resolves.toBeUndefined()
    await client.putLegalHold('trash/a.txt', 'case-42')
    await client.removeLegalHold('trash/a.txt')
    expect(String(fetcher.mock.calls[1][0])).toBe('/v1/files/trash/a.txt/presign?op=get&expires=900')
    expect(JSON.parse(String(fetcher.mock.calls[3][1]?.body))).toEqual({ key: 'trash/a.txt', reason: 'case-42' })
  })
})

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}
