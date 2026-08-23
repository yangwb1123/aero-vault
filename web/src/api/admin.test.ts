import { describe, expect, it, vi } from 'vitest'
import { normalizeWebhookFailure } from './admin'
import { VaultClient } from './vault'

describe('admin API contracts', () => {
  it('normalizes the current Go webhook failure wire shape', () => {
    expect(normalizeWebhookFailure({
      ID: 7,
      EventID: 42,
      URL: 'https://hooks.example.test/v1',
      Attempts: 3,
      LastError: 'timeout',
      LastStatus: 502,
      NextRetryAt: '2026-08-23T10:00:00Z',
      Succeeded: false,
      DeadLettered: true,
      CreatedAt: '2026-08-23T09:00:00Z',
    })).toEqual({
      id: 7,
      eventId: 42,
      url: 'https://hooks.example.test/v1',
      attempts: 3,
      lastError: 'timeout',
      lastStatus: 502,
      nextRetryAt: '2026-08-23T10:00:00Z',
      succeeded: false,
      deadLettered: true,
      createdAt: '2026-08-23T09:00:00Z',
    })
  })

  it('uses bounded operator endpoints and retries a failed job', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
      const path = String(input)
      if (path.includes('/admin/jobs?')) return jsonResponse({ stats: { failed: 1 }, jobs: [] })
      if (path.endsWith('/admin/tenants')) return jsonResponse({ tenants: [] })
      if (path.includes('/webhook-failures?')) return jsonResponse({ failures: [] })
      return jsonResponse({ id: 17, status: 'pending' })
    })
    const client = new VaultClient('/v1', () => 'operator-token', fetcher as typeof fetch)

    await client.listAdminJobs('failed', 'index_object')
    await client.listAdminTenants()
    await client.listAdminWebhookFailures()
    await client.retryAdminJob(17)

    expect(String(fetcher.mock.calls[0][0])).toBe('/v1/admin/jobs?limit=100&status=failed&type=index_object')
    expect(String(fetcher.mock.calls[2][0])).toBe('/v1/admin/webhook-failures?limit=100')
    expect(fetcher.mock.calls[3][1]?.method).toBe('POST')
    expect(new Headers(fetcher.mock.calls[3][1]?.headers).get('Authorization')).toBe('Bearer operator-token')
  })
})

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}
