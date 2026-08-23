import { requestHeaders } from './headers'

interface ErrorEnvelope {
  error?: { code?: string; message?: string; request_id?: string } | string
}

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
    let envelope: ErrorEnvelope = {}
    try {
      envelope = (await response.json()) as ErrorEnvelope
    } catch {
      // Non-JSON reverse proxies still become a bounded status error.
    }
    const error = typeof envelope.error === 'object' ? envelope.error : undefined
    throw new VaultApiError(
      response.status,
      error?.code ?? 'HTTPError',
      error?.message ?? `Aero Vault 请求失败（HTTP ${response.status}）`,
      error?.request_id,
    )
  }
}
