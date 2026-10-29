import { describe, expect, it, vi } from 'vitest'
import { VaultApiError, VaultTransport } from './transport'
import { VaultClient } from './vault'

class TestTransport extends VaultTransport {
  send(init?: RequestInit): Promise<Response> {
    return this.request('/resource', init)
  }

  read(): Promise<unknown> {
    return this.json('/resource')
  }
}

function responseFor(body: unknown, status = 403, requestId = 'header-id'): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', 'X-Request-ID': requestId },
  })
}

function transportFor(response: Response): TestTransport {
  return new TestTransport('/v1', () => '', vi.fn(async () => response) as typeof fetch)
}

describe('VaultTransport error compatibility and request IDs', () => {
  it('preserves the envelope status, code, message and preferred request ID', async () => {
    const message = '没有权限读取 team/百分比% + ?# 空格.txt'
    const transport = transportFor(responseFor({
      error: { code: 'AccessDenied', message, request_id: 'envelope-id' },
    }))

    await expect(transport.send()).rejects.toEqual(
      new VaultApiError(403, 'AccessDenied', message, 'envelope-id'),
    )
  })

  it.each([undefined, null, '', ' \t ', 42])(
    'uses the response header when the envelope request ID is absent or invalid (%j)',
    async (requestId) => {
      const transport = transportFor(responseFor({
        error: { code: 'NotFound', message: '对象不存在', request_id: requestId },
      }, 404))

      await expect(transport.send()).rejects.toEqual(
        new VaultApiError(404, 'NotFound', '对象不存在', 'header-id'),
      )
    },
  )

  it('keeps valid empty code and message strings compatible', async () => {
    const transport = transportFor(responseFor({ error: { code: '', message: '' } }))
    await expect(transport.send()).rejects.toEqual(new VaultApiError(403, '', '', 'header-id'))
  })

})

describe('VaultTransport malformed responses', () => {
  it.each([
    'null', '[]', '"proxy failure"', '{', '{}', '{"error":null}',
    '{"error":[]}', '{"error":42}', '{"error":"legacy failure"}',
    '{"error":{"code":403,"message":{"detail":"private"},"request_id":[]}}',
    '<!doctype html><h1>Bad gateway</h1><p>Bearer proxy-secret</p>',
  ])('bounds malformed and non-JSON failures instead of exposing their bodies (%s)', async (body) => {
    const transport = transportFor(new Response(body, {
      status: 502,
      headers: { 'X-Request-ID': 'proxy-request' },
    }))

    await expect(transport.send()).rejects.toEqual(
      new VaultApiError(502, 'HTTPError', 'Aero Vault 请求失败（HTTP 502）', 'proxy-request'),
    )
  })

  it('leaves the request ID empty when neither source provides one', async () => {
    const response = new Response('{"error":{"message":"failed"}}', { status: 500 })
    await expect(transportFor(response).send()).rejects.toEqual(
      new VaultApiError(500, 'HTTPError', 'failed'),
    )
  })

  it('does not reinterpret a successful response', async () => {
    const response = responseFor({ objects: [] }, 200)
    await expect(transportFor(response).send()).resolves.toBe(response)
    await expect(transportFor(response).read()).resolves.toEqual({ objects: [] })
  })
})

describe('VaultTransport network errors and cancellation', () => {
  it('propagates plain network errors unchanged without retry', async () => {
    const reason = new TypeError('Failed to fetch')
    const fetcher = vi.fn(async () => { throw reason })
    const transport = new TestTransport('/v1', () => '', fetcher as typeof fetch)

    await expect(transport.send()).rejects.toBe(reason)
    expect(fetcher).toHaveBeenCalledTimes(1)
  })

  it('forwards the signal and preserves fetch AbortError identity without retry', async () => {
    const reason = new DOMException('aborted', 'AbortError')
    const controller = new AbortController()
    const fetcher = vi.fn(async (_path: RequestInfo | URL, init?: RequestInit) => {
      expect(init?.signal).toBe(controller.signal)
      throw reason
    })
    const transport = new TestTransport('/v1', () => '', fetcher as typeof fetch)
    controller.abort()

    await expect(transport.send({ signal: controller.signal })).rejects.toBe(reason)
    expect(fetcher).toHaveBeenCalledTimes(1)
  })

  it('preserves AbortError while reading an error response body', async () => {
    const reason = new DOMException('body read cancelled', 'AbortError')
    const response = responseFor({ error: { message: 'unread' } })
    vi.spyOn(response, 'json').mockRejectedValueOnce(reason)

    await expect(transportFor(response).send()).rejects.toBe(reason)
  })
})

type WriteMethod = 'PUT' | 'POST' | 'DELETE'

function write(client: VaultClient, method: WriteMethod): Promise<void> {
  if (method === 'PUT') return client.upload('docs/a.txt', new File(['bytes'], 'a.txt'), 'attempt-one')
  if (method === 'POST') return client.createFolder('docs/new')
  return client.deleteFile('docs/a.txt')
}

const rejectedWrites = (['PUT', 'POST', 'DELETE'] as const).flatMap((method) =>
  [401, 403, 409, 413, 429, 503].map((status) => ({ method, status })))

describe('VaultClient never automatically replays writes', () => {
  it.each(rejectedWrites)('issues $method once after HTTP $status', async ({ method, status }) => {
    const fetcher = vi.fn(async (_path: RequestInfo | URL, _init?: RequestInit) =>
      responseFor({ error: { code: 'Rejected', message: 'not completed' } }, status))
    const token = vi.fn(() => 'synthetic-test-token')
    const client = new VaultClient('/v1', token, fetcher as typeof fetch)

    await expect(write(client, method)).rejects.toEqual(
      new VaultApiError(status, 'Rejected', 'not completed', 'header-id'),
    )
    await new Promise((resolve) => window.setTimeout(resolve, 0))

    expect(fetcher).toHaveBeenCalledTimes(1)
    expect(fetcher.mock.calls[0][1]?.method).toBe(method)
    expect(fetcher.mock.calls[0][1]?.credentials).toBe('same-origin')
    expect(token).toHaveBeenCalledTimes(1)
    if (method === 'PUT') {
      expect(new Headers(fetcher.mock.calls[0][1]?.headers).get('Idempotency-Key')).toBe('attempt-one')
    }
  })

  it.each(['PUT', 'POST', 'DELETE'] as const)('issues %s once after a network failure', async (method) => {
    const reason = new TypeError('network disconnected; outcome unknown')
    const fetcher = vi.fn(async () => { throw reason })
    const client = new VaultClient('/v1', () => '', fetcher as typeof fetch)

    await expect(write(client, method)).rejects.toBe(reason)
    await new Promise((resolve) => window.setTimeout(resolve, 0))
    expect(fetcher).toHaveBeenCalledTimes(1)
  })
})

describe('writes do not schedule delayed retries', () => {
  it.each(['PUT', 'POST', 'DELETE'] as const)('does not schedule a throttled %s replay', async (method) => {
    vi.useFakeTimers()
    try {
      const fetcher = vi.fn(async () => responseFor({ error: { code: 'Throttled', message: 'wait' } }, 429))
      const client = new VaultClient('/v1', () => '', fetcher as typeof fetch)
      await expect(write(client, method)).rejects.toEqual(new VaultApiError(429, 'Throttled', 'wait', 'header-id'))
      await vi.runAllTimersAsync()
      expect(fetcher).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it.each(['PUT', 'POST', 'DELETE'] as const)('does not replay a cancelled %s', async (method) => {
    const reason = new DOMException('outcome unknown', 'AbortError')
    const fetcher = vi.fn(async () => { throw reason })
    const client = new VaultClient('/v1', () => '', fetcher as typeof fetch)
    await expect(write(client, method)).rejects.toBe(reason)
    expect(fetcher).toHaveBeenCalledTimes(1)
  })
})
