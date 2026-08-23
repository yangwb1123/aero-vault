import { describe, expect, it, vi } from 'vitest'
import { consumeEventStream, type VaultEvent } from './events'
import { VaultClient } from './vault'

describe('vault event stream', () => {
  it('decodes fragmented lifecycle events and ignores keepalives', async () => {
    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new TextEncoder().encode(': keepalive\n\nid: 9\nevent: created\ndata: {"id":9,"tenant":"acme","bucket":"default","key":"docs/'))
        controller.enqueue(new TextEncoder().encode('a.txt","type":"created","object_id":7,"created_at":"2026-08-23T10:00:00Z"}\n\n'))
        controller.close()
      },
    })
    const events: VaultEvent[] = []
    await consumeEventStream(new Response(body), (event) => events.push(event))
    expect(events).toEqual([{
      id: 9,
      tenant: 'acme',
      bucket: 'default',
      key: 'docs/a.txt',
      type: 'created',
      object_id: 7,
      created_at: '2026-08-23T10:00:00Z',
    }])
  })

  it('sends bearer and Last-Event-ID headers on reconnect', async () => {
    const fetcher = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response(''))
    const client = new VaultClient('/v1', () => 'snaplink-token', fetcher as typeof fetch)
    await client.openEventStream(41)
    const headers = new Headers(fetcher.mock.calls[0][1]?.headers)
    expect(headers.get('Accept')).toBe('text/event-stream')
    expect(headers.get('Authorization')).toBe('Bearer snaplink-token')
    expect(headers.get('Last-Event-ID')).toBe('41')
  })
})
