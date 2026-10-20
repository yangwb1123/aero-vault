import type { ObjectPreview } from './objects'
import { objectReadPath } from './objectReadPath'

type PreviewRequest = (path: string, init?: RequestInit) => Promise<Response>

export async function readObjectPreview(request: PreviewRequest, key: string, versionID?: string, signal?: AbortSignal): Promise<ObjectPreview> {
  if (versionID === '') throw new Error('历史预览必须指定 Version ID')
  signal?.throwIfAborted()
  const response = await request(objectReadPath(key, 'content', versionID), { signal, headers: { Accept: '*/*', Range: 'bytes=0-4095' } })
  if (signal?.aborted) await requireActivePreview(response, signal)
  const contentType = response.headers.get('Content-Type')?.split(';')[0] ?? ''
  if (contentType !== 'image/jpeg') return { body: await readPreviewBody(response, signal), contentType, partial: response.status === 206 }
  await response.body?.cancel()
  signal?.throwIfAborted()
  const thumbnail = await request(`${objectReadPath(key, 'thumbnail', versionID)}&w=1280&h=960`, { signal, headers: { Accept: 'image/jpeg' } })
  return { body: await readPreviewBody(thumbnail, signal), contentType: 'image/jpeg', partial: false }
}

async function readPreviewBody(response: Response, signal?: AbortSignal): Promise<Blob> {
  if (signal?.aborted) await requireActivePreview(response, signal)
  const body = await response.blob()
  signal?.throwIfAborted()
  return body
}

async function requireActivePreview(response: Response, signal?: AbortSignal): Promise<void> {
  if (!signal?.aborted) return
  try { await response.body?.cancel() } finally { signal.throwIfAborted() }
}
