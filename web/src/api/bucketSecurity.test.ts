import { describe, expect, it, vi } from 'vitest'
import { parseCORSRules } from './bucketSecurity'
import { VaultClient } from './vault'

describe('bucket security API', () => {
  it('manages ACL, policy, CORS, and encryption through bounded endpoints', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (!init?.method && path.endsWith('/acl')) return jsonResponse({ acl: 'private' })
      if (!init?.method && path.endsWith('/policy')) return jsonResponse({ policy: '{"Statement":[]}' })
      if (!init?.method && path.endsWith('/cors')) return jsonResponse([])
      if (!init?.method && path.endsWith('/encryption')) return jsonResponse({ sse_algorithm: '', sse_kms_key_id: '' })
      return jsonResponse({ status: 'ok' })
    })
    const api = new VaultClient('/v1', () => 'snaplink-token', fetcher as typeof fetch).bucketSecurity

    await expect(api.getACL('project/a')).resolves.toBe('private')
    await expect(api.getPolicy('project')).resolves.toBe('{"Statement":[]}')
    await expect(api.getCORS('project')).resolves.toEqual([])
    await expect(api.getEncryption('project')).resolves.toEqual({ sse_algorithm: '', sse_kms_key_id: '' })
    await api.putACL('project', 'authenticated-read')
    await api.putPolicy('project', '{"Statement":[]}')
    await api.putCORS('project', [{ allowed_origins: ['https://app.example'], allowed_methods: ['GET'], allowed_headers: [], expose_headers: ['ETag'], max_age_seconds: 600 }])
    await api.putEncryption('project', { sse_algorithm: 'aws:kms', sse_kms_key_id: 'key-1' })

    expect(String(fetcher.mock.calls[0][0])).toBe('/v1/buckets/project%2Fa/acl')
    expect(JSON.parse(String(fetcher.mock.calls[6][1]?.body))).toEqual([{
      AllowedOrigins: ['https://app.example'], AllowedMethods: ['GET'], AllowedHeaders: [], ExposeHeaders: ['ETag'], MaxAgeSeconds: 600,
    }])
    expect(new Headers(fetcher.mock.calls[7][1]?.headers).get('Authorization')).toBe('Bearer snaplink-token')
  })

  it('validates the editable CORS JSON contract', () => {
    expect(parseCORSRules('[{"allowed_origins":["*"],"allowed_methods":["GET"]}]')).toEqual([{
      allowed_origins: ['*'], allowed_methods: ['GET'], allowed_headers: [], expose_headers: [], max_age_seconds: 0,
    }])
    expect(() => parseCORSRules('[{"allowed_origins":[],"allowed_methods":["GET"]}]')).toThrow('allowed_origins')
    expect(() => parseCORSRules('{}')).toThrow('规则数组')
  })
})

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
}
