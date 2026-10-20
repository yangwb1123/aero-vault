import { describe, expect, it, vi } from 'vitest'
import { createUploadIdempotencyKeyFactory } from './uploadKey'
import { runUploadQueue, uploadResultMessage, type UploadAttempt } from './uploadQueue'

function attempt(overrides: Partial<UploadAttempt> = {}): UploadAttempt {
  return { client: { upload: vi.fn(async () => undefined) }, files: [
    { name: 'one.txt', file: new File(['one'], 'one.txt') }, { name: 'two.txt', file: new File(['two'], 'two.txt') },
  ], prefix: 'docs/', signal: new AbortController().signal,
  keyFor: createUploadIdempotencyKeyFactory(), isCurrent: () => true, progress: vi.fn(), ...overrides }
}
function deferred() {
  let resolve: () => void = () => undefined
  const promise = new Promise<void>((accept) => { resolve = accept })
  return { promise, resolve }
}

describe('upload queue processing', () => {
  it('sends sequentially and counts only confirmed responses', async () => {
    const pending = deferred()
    const upload = vi.fn().mockImplementationOnce(() => pending.promise).mockResolvedValue(undefined)
    const input = attempt({ client: { upload } })
    const running = runUploadQueue(input)
    expect(upload).toHaveBeenCalledTimes(1)
    expect(input.progress).not.toHaveBeenCalled()
    pending.resolve()
    const result = await running
    expect(result).toMatchObject({ completed: 2, total: 2, cancelled: false, failed: false })
    expect(input.progress).toHaveBeenNthCalledWith(1, 1)
    expect(input.progress).toHaveBeenNthCalledWith(2, 2)
  })

  it('does not send any file when already aborted', async () => {
    const controller = new AbortController()
    controller.abort()
    const input = attempt({ signal: controller.signal })
    expect(await runUploadQueue(input)).toMatchObject({ completed: 0, cancelled: true })
    expect(input.client.upload).not.toHaveBeenCalled()
  })

  it('stops after an abort-aware active request rejects without sending later files', async () => {
    const controller = new AbortController()
    const upload = vi.fn((_key: string, _file: File, _id?: string, signal?: AbortSignal) => new Promise<void>((_, reject) => {
      signal?.addEventListener('abort', () => reject(new DOMException('stopped', 'AbortError')), { once: true })
    }))
    const running = runUploadQueue(attempt({ signal: controller.signal, client: { upload } }))
    controller.abort()
    expect(await running).toMatchObject({ completed: 0, cancelled: true, failed: true })
    expect(upload).toHaveBeenCalledTimes(1)
  })

  it('counts a late successful active response but does not send the next file', async () => {
    const controller = new AbortController()
    const pending = deferred()
    const upload = vi.fn(() => pending.promise)
    const input = attempt({ signal: controller.signal, client: { upload } })
    const running = runUploadQueue(input)
    controller.abort()
    pending.resolve()
    expect(await running).toMatchObject({ completed: 1, total: 2, cancelled: true, failed: false })
    expect(input.progress).toHaveBeenCalledWith(1)
    expect(upload).toHaveBeenCalledTimes(1)
  })

  it('can stop between files without discarding a confirmed success', async () => {
    const controller = new AbortController()
    const input = attempt({ signal: controller.signal, progress: () => controller.abort() })
    expect(await runUploadQueue(input)).toMatchObject({ completed: 1, cancelled: true })
    expect(input.client.upload).toHaveBeenCalledTimes(1)
  })

  it('does not publish progress or send another file after scope invalidation', async () => {
    let current = true
    const pending = deferred()
    const input = attempt({ client: { upload: vi.fn(() => pending.promise) }, isCurrent: () => current })
    const running = runUploadQueue(input)
    current = false
    pending.resolve()
    await running
    expect(input.progress).not.toHaveBeenCalled()
    expect(input.client.upload).toHaveBeenCalledTimes(1)
  })

  it('keeps the completed count and error when a later file fails normally', async () => {
    const upload = vi.fn().mockResolvedValueOnce(undefined).mockRejectedValueOnce(new Error('QuotaExceeded'))
    const result = await runUploadQueue(attempt({ client: { upload } }))
    expect(result).toMatchObject({ completed: 1, total: 2, failed: true, cancelled: false })
    expect(uploadResultMessage(result)).toEqual({ tone: 'danger', text: '已完成 1/2：QuotaExceeded' })
  })

  it('does not mistake an undefined rejection reason for success', async () => {
    const result = await runUploadQueue(attempt({ client: { upload: async () => { throw undefined } } }))
    expect(result.failed).toBe(true)
    expect(uploadResultMessage(result).tone).toBe('danger')
    expect(uploadResultMessage(result).text).toContain('上传失败')
  })

  it('passes the existing key, File, idempotency value and exact signal contract', async () => {
    const file = new File(['bytes'], ' 100% report.txt ')
    const input = attempt({ files: [{ name: file.name, file }], keyFor: () => 'attempt-key' })
    await runUploadQueue(input)
    expect(input.client.upload).toHaveBeenCalledWith('docs/ 100% report.txt ', file, 'attempt-key', input.signal)
  })
})
