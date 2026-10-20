import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { VaultClient } from '../../api/vault'
import { UploadPanel } from './UploadPanel'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
const files = () => [new File(['one'], 'one.txt'), new File(['two'], 'two.txt'), new File(['three'], 'three.txt')]
const clientWith = (upload: VaultClient['upload']) => ({ upload }) as unknown as VaultClient
let mounted: { container: HTMLDivElement; root: Root } | undefined

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
})
function deferred() {
  let resolve: () => void = () => undefined
  const promise = new Promise<void>((accept) => { resolve = accept })
  return { promise, resolve }
}
async function render(client: VaultClient, onUploaded = vi.fn(), initialPrefix = 'docs/', strict = false) {
  if (!mounted) {
    const container = document.createElement('div')
    document.body.append(container)
    mounted = { container, root: createRoot(container) }
  }
  const panel = <UploadPanel client={client} initialPrefix={initialPrefix} onUploaded={onUploaded} />
  await act(async () => mounted?.root.render(strict ? <React.StrictMode>{panel}</React.StrictMode> : panel))
  return mounted.container
}
function button(container: HTMLElement, label: string): HTMLButtonElement {
  const target = [...container.querySelectorAll('button')].find((item) => item.textContent?.trim() === label)
  if (!target) throw new Error(`missing ${label}`)
  return target
}
async function click(element: HTMLElement) { await act(async () => element.click()) }
async function dropFiles(container: HTMLElement, entries: File[]) {
  const drop = new Event('drop', { bubbles: true, cancelable: true })
  Object.defineProperty(drop, 'dataTransfer', { value: { files: entries } })
  await act(async () => container.querySelector('[data-iris-file-upload-zone]')!.dispatchEvent(drop))
}

describe('UploadPanel user cancellation', () => {
  it('retains unconfirmed files, skips unsent files and reuses the attempt key on explicit retry', async () => {
    let signal: AbortSignal | undefined
    const upload = vi.fn((_key: string, _file: File, _id?: string, active?: AbortSignal) => {
      if (upload.mock.calls.length !== 2) return Promise.resolve()
      signal = active
      return new Promise<void>((_, reject) => active?.addEventListener('abort', () => reject(new DOMException('stopped', 'AbortError')), { once: true }))
    })
    const refresh = vi.fn()
    const container = await render(clientWith(upload), refresh)
    await dropFiles(container, files())
    await click(button(container, '上传 3 个文件'))
    expect(upload).toHaveBeenCalledTimes(2)
    expect(container.textContent).toContain('已确认完成 1/3 个文件')
    await click(button(container, '取消上传'))
    expect(signal?.aborted).toBe(true)
    expect(upload).toHaveBeenCalledTimes(2)
    expect(container.textContent).toContain('请核对文件列表后重试')
    expect(container.textContent).not.toContain('one.txt')
    expect(container.textContent).toContain('two.txt')
    expect(refresh).toHaveBeenCalledTimes(1)
    await click(button(container, '上传 2 个文件'))
    expect(upload).toHaveBeenCalledTimes(4)
    expect(upload.mock.calls[2][0]).toBe('docs/two.txt')
    expect(upload.mock.calls[2][2]).toBe(upload.mock.calls[1][2])
    expect(upload.mock.calls[3][0]).toBe('docs/three.txt')
    expect(container.textContent).toContain('已上传 2 个文件。')
    expect(refresh).toHaveBeenCalledTimes(2)
  })

  it('blocks duplicate submissions while the transport is still stopping, then counts its late success', async () => {
    const pending = deferred()
    const upload = vi.fn(() => pending.promise)
    const refresh = vi.fn()
    const container = await render(clientWith(upload), refresh)
    await dropFiles(container, files().slice(0, 2))
    await act(async () => { button(container, '上传 2 个文件').click(); button(container, '上传 2 个文件').click() })
    expect(upload).toHaveBeenCalledTimes(1)
    await click(button(container, '取消上传'))
    expect(container.textContent).toContain('正在停止上传')
    expect(button(container, '上传 2 个文件').disabled).toBe(true)
    expect(button(container, '取消上传').disabled).toBe(true)
    expect(container.querySelector<HTMLInputElement>('input[placeholder="team/project/"]')?.disabled).toBe(true)
    await act(async () => { pending.resolve(); await pending.promise })
    expect(upload).toHaveBeenCalledTimes(1)
    expect(container.textContent).toContain('已确认完成 1/2')
    expect(container.textContent).not.toContain('one.txt')
    expect(button(container, '上传 1 个文件').disabled).toBe(false)
    expect(refresh).toHaveBeenCalledTimes(1)
  })

  it('uses a real VaultClient request with the original credentials, body, key and abort signal', async () => {
    let signal: AbortSignal | undefined
    const fetcher = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => new Promise<Response>((_, reject) => {
      signal = init?.signal as AbortSignal
      signal?.addEventListener('abort', () => reject(new DOMException('stopped', 'AbortError')), { once: true })
    }))
    const refresh = vi.fn()
    const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
    const container = await render(client, refresh)
    const file = files()[0]
    await dropFiles(container, [file])
    await click(button(container, '上传 1 个文件'))
    expect(String(fetcher.mock.calls[0][0])).toBe('/v1/files/docs/one.txt')
    const init = fetcher.mock.calls[0][1]!
    expect(init.body).toBe(file)
    expect(init.method).toBe('PUT')
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer token')
    expect(new Headers(init.headers).get('Idempotency-Key')).toBeTruthy()
    await click(button(container, '取消上传'))
    expect(signal?.aborted).toBe(true)
    expect(container.textContent).toContain('未发送或未确认项已保留')
    expect(container.textContent).not.toContain('已上传 1 个文件。')
    expect(refresh).toHaveBeenCalledTimes(1)
  })

  it('does not let a late old-directory response clear or refresh the new request', async () => {
    const old = deferred()
    const next = deferred()
    let signal: AbortSignal | undefined
    const upload = vi.fn((_key: string, _file: File, _id?: string, active?: AbortSignal) => {
      if (upload.mock.calls.length === 1) { signal = active; return old.promise }
      return next.promise
    })
    const client = clientWith(upload)
    const refresh = vi.fn()
    const container = await render(client, refresh)
    await dropFiles(container, files().slice(0, 2))
    await click(button(container, '上传 2 个文件'))
    await render(client, refresh, 'other/')
    expect(signal?.aborted).toBe(true)
    await dropFiles(container, [files()[2]])
    await click(button(container, '上传 1 个文件'))
    await act(async () => { old.resolve(); await old.promise })
    expect(upload).toHaveBeenCalledTimes(2)
    expect(upload.mock.calls[1][0]).toBe('other/three.txt')
    expect(button(container, '上传 1 个文件').disabled).toBe(true)
    expect(refresh).not.toHaveBeenCalled()
    await act(async () => { next.resolve(); await next.promise })
    expect(refresh).toHaveBeenCalledTimes(1)
    expect(container.textContent).toContain('已上传 1 个文件。')
  })

  it('aborts on unmount without sending the queue or calling the refresh callback', async () => {
    const pending = deferred()
    let signal: AbortSignal | undefined
    const upload = vi.fn((_key: string, _file: File, _id?: string, active?: AbortSignal) => { signal = active; return pending.promise })
    const refresh = vi.fn()
    const container = await render(clientWith(upload), refresh)
    await dropFiles(container, files().slice(0, 2))
    await click(button(container, '上传 2 个文件'))
    act(() => mounted?.root.unmount())
    mounted = undefined
    expect(signal?.aborted).toBe(true)
    await act(async () => { pending.resolve(); await pending.promise })
    expect(upload).toHaveBeenCalledTimes(1)
    expect(refresh).not.toHaveBeenCalled()
    container.remove()
  })

  it('does not leave the queue locked if a completed upload refresh callback fails', async () => {
    const upload = vi.fn(async () => undefined)
    const refresh = vi.fn(() => { throw new Error('refresh failed') })
    const container = await render(clientWith(upload), refresh)
    await dropFiles(container, [files()[0]])
    await click(button(container, '上传 1 个文件'))
    expect(container.textContent).toContain('已上传 1 个文件。 文件列表刷新失败，请手动核对。')
    expect(container.querySelector<HTMLInputElement>('input[placeholder="team/project/"]')?.disabled).toBe(false)
    expect(container.textContent).not.toContain('one.txt')
  })

  it('does not leak request fences through StrictMode replay', async () => {
    const upload = vi.fn(async () => undefined)
    const refresh = vi.fn()
    const container = await render(clientWith(upload), refresh, 'docs/', true)
    await dropFiles(container, [files()[0]])
    await click(button(container, '上传 1 个文件'))
    expect(upload).toHaveBeenCalledTimes(1)
    expect(refresh).toHaveBeenCalledTimes(1)
    expect(container.textContent).toContain('已上传 1 个文件。')
  })
})
