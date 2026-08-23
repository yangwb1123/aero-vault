import { VaultTransport } from './transport'

export type BucketACL = 'private' | 'public-read' | 'public-read-write' | 'authenticated-read'
export type BucketSSEAlgorithm = '' | 'AES256' | 'aws:kms'

export interface BucketCORSRule {
  allowed_origins: string[]
  allowed_methods: string[]
  allowed_headers: string[]
  expose_headers: string[]
  max_age_seconds: number
}

export interface BucketEncryption {
  sse_algorithm: BucketSSEAlgorithm
  sse_kms_key_id: string
}

export class BucketSecurityClient extends VaultTransport {
  async getACL(bucket: string): Promise<BucketACL> {
    const result = await this.json<{ acl?: BucketACL }>(`${pathFor(bucket)}/acl`)
    return result.acl ?? 'private'
  }

  async putACL(bucket: string, acl: BucketACL): Promise<void> {
    await this.putJSON(`${pathFor(bucket)}/acl`, { acl })
  }

  async getPolicy(bucket: string): Promise<string> {
    const result = await this.json<{ policy?: string }>(`${pathFor(bucket)}/policy`)
    return result.policy ?? ''
  }

  async putPolicy(bucket: string, policy: string): Promise<void> {
    await this.putJSON(`${pathFor(bucket)}/policy`, { policy })
  }

  async deletePolicy(bucket: string): Promise<void> {
    await this.json(`${pathFor(bucket)}/policy`, { method: 'DELETE' })
  }

  getCORS(bucket: string): Promise<BucketCORSRule[]> {
    return this.json<BucketCORSRule[]>(`${pathFor(bucket)}/cors`)
  }

  async putCORS(bucket: string, rules: BucketCORSRule[]): Promise<void> {
    await this.putJSON(`${pathFor(bucket)}/cors`, rules.map(toCORSWire))
  }

  async deleteCORS(bucket: string): Promise<void> {
    await this.json(`${pathFor(bucket)}/cors`, { method: 'DELETE' })
  }

  async getEncryption(bucket: string): Promise<BucketEncryption> {
    const result = await this.json<Partial<BucketEncryption>>(`${pathFor(bucket)}/encryption`)
    return { sse_algorithm: result.sse_algorithm ?? '', sse_kms_key_id: result.sse_kms_key_id ?? '' }
  }

  async putEncryption(bucket: string, config: BucketEncryption): Promise<void> {
    await this.putJSON(`${pathFor(bucket)}/encryption`, config)
  }

  async deleteEncryption(bucket: string): Promise<void> {
    await this.json(`${pathFor(bucket)}/encryption`, { method: 'DELETE' })
  }

  private async putJSON(path: string, body: unknown): Promise<void> {
    await this.json(path, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  }
}

export function parseCORSRules(raw: string): BucketCORSRule[] {
  const value = JSON.parse(raw) as unknown
  if (!Array.isArray(value)) throw new Error('CORS 配置必须是规则数组。')
  return value.map((item, index) => parseCORSRule(item, index))
}

function parseCORSRule(value: unknown, index: number): BucketCORSRule {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error(`第 ${index + 1} 条 CORS 规则必须是对象。`)
  const item = value as Record<string, unknown>
  const maxAge = item.max_age_seconds ?? 0
  if (!Number.isInteger(maxAge) || Number(maxAge) < 0) throw new Error(`第 ${index + 1} 条规则的 max_age_seconds 必须是非负整数。`)
  return {
    allowed_origins: stringList(item.allowed_origins, 'allowed_origins', index, true),
    allowed_methods: stringList(item.allowed_methods, 'allowed_methods', index, true),
    allowed_headers: stringList(item.allowed_headers, 'allowed_headers', index),
    expose_headers: stringList(item.expose_headers, 'expose_headers', index),
    max_age_seconds: Number(maxAge),
  }
}

function stringList(value: unknown, field: string, index: number, required = false): string[] {
  if (value === undefined && !required) return []
  if (!Array.isArray(value) || value.some((item) => typeof item !== 'string') || (required && value.length === 0)) {
    throw new Error(`第 ${index + 1} 条规则的 ${field} 必须是${required ? '非空' : ''}字符串数组。`)
  }
  return value as string[]
}

function toCORSWire(rule: BucketCORSRule): Record<string, unknown> {
  return {
    AllowedOrigins: rule.allowed_origins,
    AllowedMethods: rule.allowed_methods,
    AllowedHeaders: rule.allowed_headers,
    ExposeHeaders: rule.expose_headers,
    MaxAgeSeconds: rule.max_age_seconds,
  }
}

function pathFor(bucket: string): string {
  return `/buckets/${encodeURIComponent(bucket)}`
}
