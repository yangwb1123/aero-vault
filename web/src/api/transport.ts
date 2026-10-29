import { requestHeaders } from './headers'

export class VaultApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly requestId = '',
  ) {
    super(message)
    this.name = 'VaultApiError'
  }
}

export class VaultTransport {
  constructor(
    protected readonly base: string,
    protected readonly token: () => string,
    protected readonly fetcher: typeof fetch = window.fetch.bind(window),
  ) {}

  protected async json<T>(path: string, init?: RequestInit): Promise<T> {
    const response = await this.request(path, init)
    return (await response.json()) as T
  }

  protected async request(path: string, init: RequestInit = {}): Promise<Response> {
    const headers = requestHeaders(init.headers, this.token())
    const response = await this.fetcher(`${this.base}${path}`, {
      ...init,
      headers,
      credentials: 'same-origin',
    })
    if (response.ok) return response
    throw await responseError(response)
  }
}

async function responseError(response: Response): Promise<VaultApiError> {
  let body: unknown
  try {
    body = await response.json()
  } catch (reason) {
    if (isAbortError(reason)) throw reason
    // Non-JSON reverse proxies still become a bounded status error.
  }
  const error = errorFields(body)
  const requestId = typeof error.request_id === 'string' && error.request_id.trim()
    ? error.request_id
    : response.headers.get('X-Request-ID') ?? ''
  return new VaultApiError(
    response.status,
    typeof error.code === 'string' ? error.code : 'HTTPError',
    typeof error.message === 'string' ? error.message : `Aero Vault 请求失败（HTTP ${response.status}）`,
    requestId,
  )
}

function errorFields(body: unknown): Record<string, unknown> {
  if (!body || typeof body !== 'object' || !('error' in body)) return {}
  const error = body.error
  return error && typeof error === 'object' && !Array.isArray(error)
    ? error as Record<string, unknown>
    : {}
}

function isAbortError(reason: unknown): boolean {
  return typeof reason === 'object' && reason !== null && 'name' in reason && reason.name === 'AbortError'
}
