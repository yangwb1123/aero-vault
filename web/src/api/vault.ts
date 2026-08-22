export interface VaultSession {
  authenticated: boolean
  subject_id?: string
  tenant_id: string
  principal_kind: string
  roles: string[]
  groups: string[]
  scopes: string[]
}

export interface VaultUsage {
  tenant: string
  used_bytes: number
  used_objects: number
  max_bytes: number
  max_objects: number
  updated_at: string
}

export interface VaultObject {
  bucket: string
  key: string
  size: number
  etag: string
  content_type?: string
  backend: string
  storage_class?: string
  created_at: string
  updated_at: string
}

export interface FilePage {
  objects: VaultObject[]
  next_marker?: string
  has_more: boolean
}

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

const encodeKey = (key: string): string => key.split('/').map(encodeURIComponent).join('/')

export class VaultClient {
  constructor(
    private readonly base: string,
    private readonly token: () => string,
    private readonly fetcher: typeof fetch = window.fetch.bind(window),
  ) {}

  getSession(): Promise<VaultSession> {
    return this.json<VaultSession>('/session')
  }

  getUsage(): Promise<VaultUsage> {
    return this.json<VaultUsage>('/usage')
  }

  async listBuckets(): Promise<string[]> {
    const result = await this.json<{ buckets?: string[] }>('/buckets')
    return result.buckets ?? []
  }

  listFiles(prefix = ''): Promise<FilePage> {
    const query = new URLSearchParams({ limit: '200' })
    if (prefix) query.set('prefix', prefix)
    return this.json<FilePage>(`/files?${query}`)
  }

  async upload(key: string, file: File): Promise<void> {
    await this.request(`/files/${encodeKey(key)}`, {
      method: 'PUT',
      headers: { 'Content-Type': file.type || 'application/octet-stream' },
      body: file,
    })
  }

  async deleteFile(key: string): Promise<void> {
    await this.request(`/files/${encodeKey(key)}`, { method: 'DELETE' })
  }

  async download(key: string): Promise<Blob> {
    const response = await this.request(`/files/${encodeKey(key)}`)
    return response.blob()
  }

  private async json<T>(path: string, init?: RequestInit): Promise<T> {
    const response = await this.request(path, init)
    return (await response.json()) as T
  }

  private async request(path: string, init: RequestInit = {}): Promise<Response> {
    const headers = new Headers(init.headers)
    headers.set('Accept', 'application/json')
    const token = this.token()
    if (token) headers.set('Authorization', `Bearer ${token}`)
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
