import { describe, expect, it, vi } from 'vitest'
import { VaultClient } from './vault'

describe('VaultClient upload', () => {
  it('attaches the idempotency key and content type', async () => {
    const fetcher = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response(null, { status: 201 }))
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
    const file = new File(['content'], 'retry.txt', { type: 'text/plain' })
    const controller = new AbortController()
    await client.upload('docs/retry.txt', file, 'upload-attempt-1', controller.signal)

    const headers = new Headers(fetcher.mock.calls[0][1]?.headers)
    expect(headers.get('Idempotency-Key')).toBe('upload-attempt-1')
    expect(headers.get('Content-Type')).toBe('text/plain')
    expect(fetcher.mock.calls[0][1]?.signal).toBe(controller.signal)
  })
})
