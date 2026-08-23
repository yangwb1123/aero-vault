import { describe, expect, it, vi } from 'vitest'
import { VaultClient } from './vault'

describe('VaultClient access APIs', () => {
  it('creates, lists, and revokes object shares', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (init?.method === 'POST') {
        expect(JSON.parse(String(init.body))).toMatchObject({ key: 'docs/a.txt', ttl_seconds: 900 })
        return jsonResponse({ share: { id: 's1' }, token: 'secret', url: 'https://vault/share/secret' }, 201)
      }
      if (init?.method === 'DELETE') return new Response(null, { status: 204 })
      expect(path).toBe('/v1/shares?bucket=default&key=docs%2Fa.txt')
      return jsonResponse({ shares: [{ id: 's1' }] })
    })
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)

    await expect(client.createShare({
      key: 'docs/a.txt', allow_preview: true, allow_download: true, ttl_seconds: 900,
    })).resolves.toMatchObject({ token: 'secret' })
    await expect(client.listShares('docs/a.txt')).resolves.toEqual([{ id: 's1' }])
    await client.revokeShare('s1')
    expect(String(fetcher.mock.calls[2][0])).toBe('/v1/shares/s1')
  })

  it('uses encoded resource paths for assets and ACL entries', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path === '/v1/assets' && init?.method === 'POST') return jsonResponse({ asset: { id: 'a1' }, url: '/public/assets/blog/hero.jpg' }, 201)
      if (path === '/v1/assets') return jsonResponse({ assets: [{ id: 'a1', slug: 'blog/hero.jpg' }] })
      if (path.startsWith('/v1/assets/')) return new Response(null, { status: 204 })
      if (path.startsWith('/v1/access/acl?')) return jsonResponse({ entries: [{ id: 'acl1' }] })
      if (path === '/v1/access/acl' && init?.method === 'PUT') return jsonResponse({ entries: [{ id: 'acl2' }] }, 201)
      return new Response(null, { status: 204 })
    })
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)

    await client.publishAsset('images/hero.jpg', 'blog/hero.jpg')
    await expect(client.listAssets()).resolves.toEqual([{ id: 'a1', slug: 'blog/hero.jpg' }])
    await client.unpublishAsset('blog/hero.jpg')
    await expect(client.listACL('images/hero.jpg', 'object')).resolves.toEqual([{ id: 'acl1' }])
    await client.putACL({
      key: 'images/hero.jpg', resource_kind: 'object', principal_type: 'user',
      principal_id: 'alice', actions: ['object:read'], effect: 'allow', inherit: false,
    })
    await client.deleteACL('acl/1')
    expect(String(fetcher.mock.calls[2][0])).toBe('/v1/assets/blog/hero.jpg')
    expect(String(fetcher.mock.calls[5][0])).toBe('/v1/access/acl/acl%2F1')
  })

  it('requests a gzip archive without replacing the media type', async () => {
    const fetcher = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      expect(new Headers(init?.headers).get('Accept')).toBe('application/gzip')
      return new Response('archive', { status: 200, headers: { 'Content-Type': 'application/gzip' } })
    })
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
    await expect(client.exportArchive('docs/')).resolves.toBeInstanceOf(Blob)
    expect(String(fetcher.mock.calls[0][0])).toBe('/v1/exports/archive?bucket=default&prefix=docs%2F')
  })

  it('manages department hierarchy and Aero ID subject membership', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path === '/v1/admin/departments' && init?.method === 'POST') {
        expect(JSON.parse(String(init.body))).toEqual({ name: 'Platform', parent_id: 'parent/1' })
        return jsonResponse({ id: 'd1', name: 'Platform' }, 201)
      }
      if (path === '/v1/admin/departments') return jsonResponse({ departments: [{ id: 'd1' }] })
      if (path.endsWith('/members/aero%2Fuser') && init?.method === 'PUT') {
        expect(JSON.parse(String(init.body))).toEqual({ role: 'manager' })
        return jsonResponse({ role: 'manager' })
      }
      if (init?.method === 'DELETE') return new Response(null, { status: 204 })
      return jsonResponse({ department: { id: 'd1' }, members: [] })
    })
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)

    await expect(client.listDepartments()).resolves.toEqual([{ id: 'd1' }])
    await client.createDepartment('Platform', 'parent/1')
    await expect(client.getDepartment('dept/1')).resolves.toMatchObject({ members: [] })
    await client.putDepartmentMember('dept/1', 'aero/user', 'manager')
    await client.deleteDepartmentMember('dept/1', 'aero/user')
    await client.deleteDepartment('dept/1')
    expect(fetcher.mock.calls.map((call) => String(call[0]))).toEqual([
      '/v1/admin/departments',
      '/v1/admin/departments',
      '/v1/admin/departments/dept%2F1',
      '/v1/admin/departments/dept%2F1/members/aero%2Fuser',
      '/v1/admin/departments/dept%2F1/members/aero%2Fuser',
      '/v1/admin/departments/dept%2F1',
    ])
  })
})

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}
