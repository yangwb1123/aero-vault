import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { VaultClient } from '../../api/vault'
import { UploadPanel } from './UploadPanel'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

function createClient(upload: VaultClient['upload']): VaultClient {
  return { upload } as unknown as VaultClient
}

function button(container: HTMLElement, label: string): HTMLButtonElement {
  const found = [...container.querySelectorAll('button')]
    .find((candidate) => candidate.textContent?.trim() === label)
  if (!(found instanceof HTMLButtonElement)) throw new Error(`button not found: ${label}`)
  return found
}

async function click(element: HTMLButtonElement): Promise<void> {
  await act(async () => {
    element.click()
    await Promise.resolve()
  })
}

async function dropFiles(container: HTMLElement, files: File[]): Promise<void> {
  const zone = container.querySelector<HTMLElement>('[data-iris-file-upload-zone]')
  if (!zone) throw new Error('upload dropzone not found')
  const drop = new Event('drop', { bubbles: true, cancelable: true })
  Object.defineProperty(drop, 'dataTransfer', { value: { files } })
  await act(async () => { zone.dispatchEvent(drop) })
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve: (value: T) => void = () => {}
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

let mounted: { container: HTMLDivElement; root: Root } | undefined

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
})

describe('UploadPanel retry safety', () => {
  it('uploads sequentially and reuses the failed file idempotency key on retry', async () => {
    const firstRequest = deferred<void>()
    const uploads: Array<{ key: string; file: File; idempotencyKey?: string }> = []
    const upload = vi.fn((key: string, file: File, idempotencyKey?: string) => {
      uploads.push({ key, file, idempotencyKey })
      if (uploads.length === 1) return firstRequest.promise
      if (uploads.length === 2) return Promise.reject(new Error('connection lost'))
      return Promise.resolve()
    })
    const onUploaded = vi.fn()
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    mounted = { container, root }
    act(() => root.render(<UploadPanel client={createClient(upload)} initialPrefix="docs/" onUploaded={onUploaded} />))

    const firstFile = new File(['one'], 'one.txt', { type: 'text/plain' })
    const retryFile = new File(['two'], 'two.txt', { type: 'text/plain' })
    await dropFiles(container, [firstFile, retryFile])

    await click(button(container, '上传 2 个文件'))
    expect(upload).toHaveBeenCalledTimes(1)
    expect(uploads[0].key).toBe('docs/one.txt')
    expect(uploads[0].file).toBe(firstFile)

    await act(async () => {
      firstRequest.resolve()
      await firstRequest.promise
      await new Promise((resolve) => window.setTimeout(resolve, 0))
    })
    expect(upload).toHaveBeenCalledTimes(2)
    expect(uploads[1].key).toBe('docs/two.txt')
    expect(container.textContent).toContain('已完成 1/2')
    expect(onUploaded).toHaveBeenCalledTimes(1)

    await click(button(container, '上传 1 个文件'))
    await act(async () => { await new Promise((resolve) => window.setTimeout(resolve, 0)) })
    expect(upload).toHaveBeenCalledTimes(3)
    expect(uploads[2].file).toBe(retryFile)
    expect(uploads[1].idempotencyKey).toBeTruthy()
    expect(uploads[2].idempotencyKey).toBe(uploads[1].idempotencyKey)
    expect(container.textContent).toContain('已上传 1 个文件。')
    expect(onUploaded).toHaveBeenCalledTimes(2)
  })

  it('aborts the active request and discards queued files when the client changes', async () => {
    const request = deferred<void>()
    let signal: AbortSignal | undefined
    const upload = vi.fn((_key: string, _file: File, _idempotencyKey?: string, active?: AbortSignal) => {
      signal = active
      return request.promise
    })
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    mounted = { container, root }
    act(() => root.render(<UploadPanel client={createClient(upload)} initialPrefix="docs/" onUploaded={() => undefined} />))

    const firstFile = new File(['one'], 'one.txt', { type: 'text/plain' })
    const queuedFile = new File(['two'], 'two.txt', { type: 'text/plain' })
    await dropFiles(container, [firstFile, queuedFile])
    await click(button(container, '上传 2 个文件'))
    expect(upload).toHaveBeenCalledTimes(1)

    const nextClient = createClient(vi.fn(async () => undefined))
    act(() => root.render(<UploadPanel client={nextClient} initialPrefix="docs/" onUploaded={() => undefined} />))
    expect(signal?.aborted).toBe(true)
    expect(container.textContent).not.toContain('two.txt')

    await act(async () => {
      request.resolve()
      await request.promise
    })
    expect(upload).toHaveBeenCalledTimes(1)
    expect(container.textContent).not.toContain('已上传')
  })
})
