import { SSEDecoder, type SSEFrame } from './sse'
import type {
  ACLEntry,
  CreatedShare,
  CreateShareInput,
  Department,
  DepartmentDetail,
  DepartmentRole,
  PublicAsset,
  PublishedAsset,
  PutACLInput,
  ResourceKind,
  Share,
} from './access'

export type SearchMode = 'vector' | 'bm25' | 'hybrid'

export interface SearchHit {
  score: number
  chunk: string
  chunk_id: number
  object_id: number
  bucket: string
  object_key: string
  seq: number
  embed_model: string
}

export interface ChatResponse {
  answer: string
  model: string
  citations: SearchHit[]
}

export interface LineageEntry {
  usage_id: number
  caller: string
  query?: string
  chunk_ids: number[]
  object_ids: number[]
  request_id?: string
  created_at: string
  model?: string
  prompt_tokens?: number
  completion_tokens?: number
  total_tokens?: number
  latency_ms?: number
  cost_micros?: number
}

export interface LineageResponse {
  object_id: number
  entries: LineageEntry[]
}

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

  async createShare(input: CreateShareInput): Promise<CreatedShare> {
    return this.json<CreatedShare>('/shares', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    })
  }

  async listShares(key: string): Promise<Share[]> {
    const query = new URLSearchParams({ bucket: 'default', key })
    const result = await this.json<{ shares?: Share[] }>(`/shares?${query}`)
    return result.shares ?? []
  }

  async revokeShare(id: string): Promise<void> {
    await this.request(`/shares/${encodeURIComponent(id)}`, { method: 'DELETE' })
  }

  async publishAsset(key: string, slug: string): Promise<PublishedAsset> {
    return this.json<PublishedAsset>('/assets', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ key, slug, cache_control: 'public, max-age=86400' }),
    })
  }

  async listAssets(): Promise<PublicAsset[]> {
    const result = await this.json<{ assets?: PublicAsset[] }>('/assets')
    return result.assets ?? []
  }

  async unpublishAsset(slug: string): Promise<void> {
    await this.request(`/assets/${encodeKey(slug)}`, { method: 'DELETE' })
  }

  async listACL(key: string, kind: ResourceKind): Promise<ACLEntry[]> {
    const query = new URLSearchParams({ bucket: 'default', key, kind })
    const result = await this.json<{ entries?: ACLEntry[] }>(`/access/acl?${query}`)
    return result.entries ?? []
  }

  async putACL(input: PutACLInput): Promise<ACLEntry[]> {
    const result = await this.json<{ entries?: ACLEntry[] }>('/access/acl', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    })
    return result.entries ?? []
  }

  async deleteACL(id: string): Promise<void> {
    await this.request(`/access/acl/${encodeURIComponent(id)}`, { method: 'DELETE' })
  }

  async exportArchive(prefix: string): Promise<Blob> {
    const query = new URLSearchParams({ bucket: 'default', prefix })
    const response = await this.request(`/exports/archive?${query}`, {
      headers: { Accept: 'application/gzip' },
    })
    return response.blob()
  }

  async listDepartments(): Promise<Department[]> {
    const result = await this.json<{ departments?: Department[] }>('/admin/departments')
    return result.departments ?? []
  }

  createDepartment(name: string, parentId = ''): Promise<Department> {
    return this.json<Department>('/admin/departments', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, parent_id: parentId }),
    })
  }

  getDepartment(id: string): Promise<DepartmentDetail> {
    return this.json<DepartmentDetail>(`/admin/departments/${encodeURIComponent(id)}`)
  }

  async deleteDepartment(id: string): Promise<void> {
    await this.request(`/admin/departments/${encodeURIComponent(id)}`, { method: 'DELETE' })
  }

  async putDepartmentMember(id: string, subject: string, role: DepartmentRole): Promise<void> {
    await this.json(`/admin/departments/${encodeURIComponent(id)}/members/${encodeURIComponent(subject)}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ role }),
    })
  }

  async deleteDepartmentMember(id: string, subject: string): Promise<void> {
    await this.request(`/admin/departments/${encodeURIComponent(id)}/members/${encodeURIComponent(subject)}`, {
      method: 'DELETE',
    })
  }

  async search(query: string, mode: SearchMode): Promise<SearchHit[]> {
    const result = await this.json<{ hits?: SearchHit[] }>('/search', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ query, mode, k: 8 }),
    })
    return result.hits ?? []
  }

  getLineage(objectId: number): Promise<LineageResponse> {
    return this.json<LineageResponse>(`/lineage/objects/${encodeURIComponent(objectId)}?limit=100`)
  }

  async streamChat(
    query: string,
    mode: SearchMode,
    onToken: (token: string) => void,
    signal?: AbortSignal,
  ): Promise<ChatResponse> {
    const response = await this.request('/chat/stream', {
      method: 'POST',
      headers: { Accept: 'text/event-stream', 'Content-Type': 'application/json' },
      body: JSON.stringify({ query, mode, k: 5 }),
      signal,
    })
    if (!response.body) throw new VaultApiError(502, 'StreamError', 'Chat 响应没有数据流')
    const reader = response.body.getReader()
    const decoder = new TextDecoder()
    const frames = new SSEDecoder()
    let completed: ChatResponse | undefined
    for (;;) {
      const part = await reader.read()
      const text = decoder.decode(part.value, { stream: !part.done })
      for (const frame of frames.push(text, part.done)) {
        completed = handleChatFrame(frame, onToken) ?? completed
      }
      if (part.done) break
    }
    if (!completed) throw new VaultApiError(502, 'StreamError', 'Chat 数据流未返回完成事件')
    return completed
  }

  private async json<T>(path: string, init?: RequestInit): Promise<T> {
    const response = await this.request(path, init)
    return (await response.json()) as T
  }

  private async request(path: string, init: RequestInit = {}): Promise<Response> {
    const headers = new Headers(init.headers)
    if (!headers.has('Accept')) headers.set('Accept', 'application/json')
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

function handleChatFrame(frame: SSEFrame, onToken: (token: string) => void): ChatResponse | undefined {
  if (frame.event === 'token') {
    const token = JSON.parse(frame.data) as unknown
    onToken(typeof token === 'string' ? token : String(token))
    return undefined
  }
  if (frame.event === 'error') {
    const error = JSON.parse(frame.data) as { code?: string; message?: string }
    throw new VaultApiError(200, error.code ?? 'StreamError', error.message ?? 'Chat 流处理失败')
  }
  if (frame.event === 'done') {
    const result = JSON.parse(frame.data) as Partial<ChatResponse>
    return {
      answer: result.answer ?? '',
      model: result.model ?? '',
      citations: result.citations ?? [],
    }
  }
  return undefined
}
