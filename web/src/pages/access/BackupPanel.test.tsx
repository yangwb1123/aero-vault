import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { downloadBlob } from '../../download'
import { VaultApiError } from '../../api/vault'
import type { VaultClient } from '../../api/vault'
import { BackupPanel } from './BackupPanel'

vi.mock('../../download', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../download')>()),
  downloadBlob: vi.fn(),
}))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

function createClient(exportArchive: (bucket: string, prefix: string) => Promise<Response>): VaultClient {
  return { exportArchive } as unknown as VaultClient
}

function button(container: HTMLElement): HTMLButtonElement {
  const found = [...container.querySelectorAll('button')]
    .find((candidate) => candidate.textContent?.includes('下载 tar.gz'))
  if (!(found instanceof HTMLButtonElement)) throw new Error('backup button not found')
  return found
}

function dangerText(container: HTMLElement): string {
  return container.querySelector('[data-iris-alert-tone="danger"]')?.textContent ?? ''
}

// A chunked archive: the first chunk is delivered immediately, the second one
// only after release() so the in-flight progress text is observable.
function chunkedArchive(headers: Record<string, string>): { response: Response; release(): void } {
  const gate = deferred<void>()
  let step = 0
  const reader = {
    read: async () => {
      step += 1
      if (step === 1) return { done: false, value: new Uint8Array(1024) }
      if (step === 2) {
        await gate.promise
        return { done: false, value: new Uint8Array(512) }
      }
      return { done: true, value: undefined }
    },
    cancel: vi.fn(async () => undefined),
  }
  return {
    response: { headers: new Headers(headers), body: { getReader: () => reader } } as unknown as Response,
    release: () => gate.resolve(),
  }
}

async function click(element: HTMLButtonElement): Promise<void> {
  await act(async () => {
    element.click()
    await Promise.resolve()
  })
}

async function settle(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => window.setTimeout(resolve, 0))
  })
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve: (value: T) => void = () => {}
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

let mounted: { container: HTMLDivElement; root: Root } | undefined

function renderPanel(client: VaultClient): HTMLDivElement {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  mounted = { container, root }
  act(() => root.render(<BackupPanel client={client} bucket="bucket-a" />))
  return container
}

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
  vi.clearAllMocks()
})

describe('BackupPanel request scope', () => {
  it('does not download an archive after the authenticated client changes', async () => {
    const pendingArchive = deferred<Response>()
    const oldClient = createClient(() => pendingArchive.promise)
    const newClient = createClient(async () => new Response())
    const container = renderPanel(oldClient)
    const root = mounted?.root
    if (!root) throw new Error('root not mounted')

    await click(button(container))
    act(() => root.render(<BackupPanel client={newClient} bucket="bucket-a" />))
    await act(async () => {
      pendingArchive.resolve(chunkedArchive({}).response)
      await pendingArchive.promise
    })

    expect(vi.mocked(downloadBlob)).not.toHaveBeenCalled()
    expect(button(container).disabled).toBe(false)
  })
})

describe('BackupPanel archive delivery', () => {
  it('reports byte progress while the archive streams and prefers the server filename', async () => {
    const archive = chunkedArchive({
      'Content-Type': 'application/gzip',
      'Content-Disposition': 'attachment; filename="aero-default-20260929T053000Z.tar.gz"',
    })
    const container = renderPanel(createClient(async () => archive.response))

    await click(button(container))

    expect(container.querySelector('[role="status"]')?.textContent).toBe('已接收 1.0 KiB')
    expect(vi.mocked(downloadBlob)).not.toHaveBeenCalled()

    archive.release()
    await settle()

    expect(container.querySelector('[role="status"]')?.textContent).toBe('1.5 KiB / 1.5 KiB')
    expect(vi.mocked(downloadBlob)).toHaveBeenCalledTimes(1)
    const [blob, filename] = vi.mocked(downloadBlob).mock.calls[0]
    expect(blob.size).toBe(1536)
    expect(filename).toBe('aero-default-20260929T053000Z.tar.gz')
    expect(dangerText(container)).toBe('')
    expect(button(container).disabled).toBe(false)
  })

  it('falls back to the generated name when the response has no Content-Disposition', async () => {
    const archive = chunkedArchive({ 'Content-Type': 'application/gzip' })
    const container = renderPanel(createClient(async () => archive.response))

    archive.release()
    await click(button(container))
    await settle()

    expect(vi.mocked(downloadBlob)).toHaveBeenCalledTimes(1)
    expect(vi.mocked(downloadBlob).mock.calls[0][1]).toMatch(/^aero-backup-\d{4}-\d{2}-\d{2}\.tar\.gz$/)
  })

  it('surfaces a rejected export with the backend request id and downloads nothing', async () => {
    const client = createClient(async () => {
      throw new VaultApiError(403, 'Forbidden', '没有导出权限', 'req-42')
    })
    const container = renderPanel(client)

    await click(button(container))

    expect(dangerText(container)).toContain('没有导出权限')
    expect(dangerText(container)).toContain('request_id: req-42')
    expect(vi.mocked(downloadBlob)).not.toHaveBeenCalled()
    expect(button(container).disabled).toBe(false)
  })

  it('disables the export control while an export is in flight and blocks a second submit', async () => {
    const archive = chunkedArchive({})
    const pendingArchive = deferred<Response>()
    const exportArchive = vi.fn(() => pendingArchive.promise)
    const container = renderPanel(createClient(exportArchive))
    const control = button(container)
    const form = container.querySelector('form')
    if (!form) throw new Error('backup form not found')

    await click(control)
    expect(exportArchive).toHaveBeenCalledTimes(1)
    expect(control.disabled).toBe(true)

    await act(async () => {
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      control.click()
      await Promise.resolve()
    })
    expect(exportArchive).toHaveBeenCalledTimes(1)

    pendingArchive.resolve(archive.response)
    archive.release()
    await settle()

    expect(control.disabled).toBe(false)
    expect(vi.mocked(downloadBlob)).toHaveBeenCalledTimes(1)
    expect(dangerText(container)).toBe('')
  })

  it('reports a delivery failure instead of claiming the export finished', async () => {
    const archive = chunkedArchive({})
    const container = renderPanel(createClient(async () => archive.response))
    vi.mocked(downloadBlob).mockRejectedValueOnce(new Error('当前浏览器不支持文件下载'))

    archive.release()
    await click(button(container))
    await settle()

    expect(dangerText(container)).toContain('当前浏览器不支持文件下载')
    expect(container.textContent).not.toContain('已完成')
    expect(button(container).disabled).toBe(false)
  })
})
