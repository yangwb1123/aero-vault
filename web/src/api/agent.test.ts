import { describe, expect, it, vi } from 'vitest'
import { normalizeAgentResponse } from './agent'
import { VaultClient } from './vault'

describe('agent API', () => {
  it('runs the agent with Snaplink bearer authentication', async () => {
    const payload = {
      answer: 'The handbook is in docs/handbook.md.',
      model: 'model-a',
      steps: [{ tool: 'search', args: { query: 'handbook', k: 5 }, result: '[#1] docs/handbook.md' }],
    }
    const fetcher = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
      new Response(JSON.stringify(payload), { headers: { 'Content-Type': 'application/json' } }),
    )
    const client = new VaultClient('/v1', () => 'snaplink-token', fetcher as typeof fetch)

    await expect(client.runAgent('Find the handbook')).resolves.toEqual(payload)
    expect(String(fetcher.mock.calls[0][0])).toBe('/v1/agent')
    expect(fetcher.mock.calls[0][1]?.method).toBe('POST')
    expect(JSON.parse(String(fetcher.mock.calls[0][1]?.body))).toEqual({ query: 'Find the handbook' })
    const headers = new Headers(fetcher.mock.calls[0][1]?.headers)
    expect(headers.get('Authorization')).toBe('Bearer snaplink-token')
    expect(headers.get('Content-Type')).toBe('application/json')
  })

  it('normalizes a nil Go step slice for direct answers', () => {
    expect(normalizeAgentResponse({ answer: 'Direct answer', model: 'model-a', steps: null })).toEqual({
      answer: 'Direct answer', model: 'model-a', steps: [],
    })
  })
})
