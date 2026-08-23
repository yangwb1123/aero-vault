import { describe, expect, it, vi } from 'vitest'
import { VaultClient } from './vault'

describe('bucket API contracts', () => {
  it('creates, reads, and deletes a bucket through the REST surface', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith('/config')) return jsonResponse({ name: 'project', versioning: false, object_lock_seconds: 0 })
      if (path.endsWith('/stats')) return jsonResponse({ bucket: 'project', object_count: 2, total_size_bytes: 42 })
      if (init?.method === 'DELETE') return new Response(null, { status: 204 })
      return jsonResponse({ bucket: 'project' }, 201)
    })
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)

    await client.createBucket('project')
    await expect(client.getBucketConfig('project')).resolves.toMatchObject({ name: 'project' })
    await expect(client.getBucketStats('project')).resolves.toMatchObject({ object_count: 2 })
    await client.deleteBucket('project')

    expect(String(fetcher.mock.calls[0][0])).toBe('/v1/buckets/project')
    expect(fetcher.mock.calls[0][1]?.method).toBe('PUT')
    expect(fetcher.mock.calls[3][1]?.method).toBe('DELETE')
  })

  it('updates versioning, object lock, and lifecycle settings', async () => {
    const fetcher = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => jsonResponse({ status: 'ok' }))
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)

    await client.setBucketVersioning('project', true)
    await client.setBucketObjectLock('project', 3600)
    await client.setBucketLifecycle('project', { days: 30, action: 'soft_delete' })

    expect(String(fetcher.mock.calls[0][0])).toBe('/v1/buckets/project/versioning')
    expect(JSON.parse(String(fetcher.mock.calls[0][1]?.body))).toEqual({ enabled: true })
    expect(JSON.parse(String(fetcher.mock.calls[1][1]?.body))).toEqual({ seconds: 3600 })
    expect(JSON.parse(String(fetcher.mock.calls[2][1]?.body))).toEqual({ days: 30, action: 'soft_delete' })
  })
})

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}
