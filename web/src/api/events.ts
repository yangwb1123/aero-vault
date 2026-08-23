import { SSEDecoder } from './sse'
import { VaultApiError } from './transport'

export type VaultEventType = 'created' | 'updated' | 'deleted' | 'accessed' | string

export interface VaultEvent {
  id: number
  tenant: string
  bucket: string
  key: string
  type: VaultEventType
  object_id?: number | null
  created_at: string
}

export async function consumeEventStream(response: Response, onEvent: (event: VaultEvent) => void): Promise<void> {
  if (!response.body) throw new VaultApiError(502, 'StreamError', '事件响应没有数据流')
  const reader = response.body.getReader()
  const text = new TextDecoder()
  const frames = new SSEDecoder()
  for (;;) {
    const part = await reader.read()
    const chunk = text.decode(part.value, { stream: !part.done })
    for (const frame of frames.push(chunk, part.done)) onEvent(parseVaultEvent(frame.data))
    if (part.done) return
  }
}

function parseVaultEvent(data: string): VaultEvent {
  let value: Partial<VaultEvent>
  try {
    value = JSON.parse(data) as Partial<VaultEvent>
  } catch {
    throw new VaultApiError(502, 'StreamError', '事件流包含无效 JSON')
  }
  if (!Number.isSafeInteger(value.id) || !value.type || !value.key || !value.created_at) {
    throw new VaultApiError(502, 'StreamError', '事件流缺少必要字段')
  }
  return {
    id: value.id!,
    tenant: value.tenant ?? '',
    bucket: value.bucket ?? 'default',
    key: value.key,
    type: value.type,
    object_id: value.object_id,
    created_at: value.created_at,
  }
}
