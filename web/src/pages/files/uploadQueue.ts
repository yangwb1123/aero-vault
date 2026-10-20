import type { VaultClient } from '../../api/vault'
import { uploadKey } from './uploadKey'

export interface QueuedUpload {
  name: string
  file: File
}
export interface UploadResult {
  completed: number
  total: number
  cancelled: boolean
  failed: boolean
  error?: unknown
}
export interface UploadAttempt {
  client: Pick<VaultClient, 'upload'>
  files: QueuedUpload[]
  prefix: string
  signal: AbortSignal
  keyFor(file: File, key: string): string
  isCurrent(): boolean
  progress(completed: number): void
}

export async function runUploadQueue(attempt: UploadAttempt): Promise<UploadResult> {
  let completed = 0
  let error: unknown
  let failed = false
  try {
    for (const entry of attempt.files) {
      if (!attempt.isCurrent() || attempt.signal.aborted) break
      const key = uploadKey(attempt.prefix, entry.name)
      await attempt.client.upload(key, entry.file, attempt.keyFor(entry.file, key), attempt.signal)
      completed += 1
      if (attempt.isCurrent()) attempt.progress(completed)
    }
  } catch (reason) {
    failed = true
    error = reason
  }
  return { completed, total: attempt.files.length, cancelled: attempt.signal.aborted, failed, error }
}

export function uploadResultMessage(result: UploadResult): { tone: 'success' | 'danger' | 'warning'; text: string } {
  if (result.cancelled) return { tone: 'warning', text: `已停止上传队列，已确认完成 ${result.completed}/${result.total}。未发送或未确认项已保留；当前请求可能已在服务端保存，请核对文件列表后重试。` }
  if (result.failed) return { tone: 'danger', text: `已完成 ${result.completed}/${result.total}：${result.error instanceof Error ? result.error.message : '上传失败'}` }
  return { tone: 'success', text: `已上传 ${result.completed} 个文件。` }
}
