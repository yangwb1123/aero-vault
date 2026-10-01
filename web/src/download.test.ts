import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { downloadBlob, filenameFromDisposition, readDownloadBlob } from './download'
import type { DownloadProgress } from './download'

let order: string[] = []
let anchors: HTMLAnchorElement[] = []
let attachedAtClick: boolean[] = []
let createObjectURL: ReturnType<typeof vi.fn>
let revokeObjectURL: ReturnType<typeof vi.fn>

function bytes(text: string): Uint8Array {
  return new TextEncoder().encode(text)
}

// jsdom's Blob has no text()/arrayBuffer(); FileReader is the supported path.
function blobText(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result))
    reader.onerror = () => reject(reader.error)
    reader.readAsText(blob)
  })
}

function stubResponse(headers: Record<string, string>, body: Blob | null = null): Response {
  return {
    headers: new Headers(headers),
    body,
    blob: async () => body ?? new Blob([]),
  } as unknown as Response
}

function streamResponse(chunks: Uint8Array[], headers: Record<string, string> = {}, cancel = vi.fn(async () => undefined)): {
  response: Response
  cancel: ReturnType<typeof vi.fn>
} {
  let index = 0
  const reader = {
    read: async () => (index >= chunks.length
      ? { done: true, value: undefined }
      : { done: false, value: chunks[index++] }),
    cancel,
  }
  return { response: { headers: new Headers(headers), body: { getReader: () => reader } } as unknown as Response, cancel }
}

beforeEach(() => {
  order = []
  anchors = []
  attachedAtClick = []
  let issued = 0
  createObjectURL = vi.fn(() => {
    issued += 1
    const url = `blob:aero/${issued}`
    order.push(`create:${url}`)
    return url
  })
  revokeObjectURL = vi.fn((url: string) => { order.push(`revoke:${url}`) })
  Object.defineProperty(URL, 'createObjectURL', { value: createObjectURL, configurable: true, writable: true })
  Object.defineProperty(URL, 'revokeObjectURL', { value: revokeObjectURL, configurable: true, writable: true })
  Object.defineProperty(HTMLAnchorElement.prototype, 'click', {
    configurable: true,
    writable: true,
    value: vi.fn(function recordClick(this: HTMLAnchorElement) {
      anchors.push(this)
      attachedAtClick.push(this.isConnected)
      order.push('click')
    }),
  })
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('downloadBlob delivery lifetime', () => {
  it('keeps the object URL alive until the next task and detaches the anchor', async () => {
    const delivered = downloadBlob(new Blob(['payload']), 'report.pdf')

    expect(order).toEqual(['create:blob:aero/1', 'click'])
    expect(revokeObjectURL).not.toHaveBeenCalled()
    expect(attachedAtClick).toEqual([true])
    expect(anchors[0].isConnected).toBe(true)

    await delivered

    expect(order).toEqual(['create:blob:aero/1', 'click', 'revoke:blob:aero/1'])
    expect(revokeObjectURL).toHaveBeenCalledTimes(1)
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:aero/1')
    expect(anchors[0].isConnected).toBe(false)
    expect(document.body.contains(anchors[0])).toBe(false)
    expect(document.querySelectorAll('a').length).toBe(0)
  })

  it('configures a hidden, non-navigating anchor and keeps concurrent downloads independent', async () => {
    const first = downloadBlob(new Blob(['a']), 'a.txt')
    const second = downloadBlob(new Blob(['b']), 'b.txt')

    expect(createObjectURL).toHaveBeenCalledTimes(2)
    expect(anchors).toHaveLength(2)

    await Promise.all([first, second])

    expect(revokeObjectURL).toHaveBeenCalledTimes(2)
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:aero/1')
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:aero/2')
    expect(anchors.map((anchor) => anchor.download)).toEqual(['a.txt', 'b.txt'])
    expect(anchors.map((anchor) => anchor.rel)).toEqual(['noopener', 'noopener'])
    expect(anchors.map((anchor) => anchor.style.display)).toEqual(['none', 'none'])
    expect(anchors.every((anchor) => !anchor.isConnected)).toBe(true)
  })

  it('fails loudly instead of silently doing nothing when object URLs are unavailable', () => {
    Object.defineProperty(URL, 'createObjectURL', { value: undefined, configurable: true, writable: true })

    expect(() => downloadBlob(new Blob(['payload']), 'report.pdf')).toThrow('当前浏览器不支持文件下载')
    expect(order).toEqual([])
    expect(document.querySelectorAll('a').length).toBe(0)
  })
})

describe('readDownloadBlob progress', () => {
  it('streams chunks into one blob and ends with a terminal progress event', async () => {
    const events: DownloadProgress[] = []
    const { response, cancel } = streamResponse([bytes('abcd'), bytes('efgh')], { 'Content-Type': 'application/gzip' })

    const blob = await readDownloadBlob(response, (progress) => events.push(progress))

    expect(await blobText(blob)).toBe('abcdefgh')
    expect(blob.type).toBe('application/gzip')
    expect(cancel).not.toHaveBeenCalled()
    expect(events).toEqual([
      { loaded: 4, total: undefined, done: false },
      { loaded: 8, total: 8, done: true },
    ])
  })

  it('reports Content-Length as the total only when it is a positive integer', async () => {
    const sized: DownloadProgress[] = []
    await readDownloadBlob(streamResponse([bytes('abcd')], { 'Content-Length': '4096' }).response, (progress) => sized.push(progress))
    expect(sized).toEqual([
      { loaded: 4, total: 4096, done: false },
      { loaded: 4, total: 4096, done: true },
    ])

    const garbage: DownloadProgress[] = []
    await readDownloadBlob(streamResponse([bytes('abcd')], { 'Content-Length': 'chunked' }).response, (progress) => garbage.push(progress))
    expect(garbage[0].total).toBeUndefined()

    const empty: DownloadProgress[] = []
    await readDownloadBlob(streamResponse([bytes('abcd')], { 'Content-Length': '0' }).response, (progress) => empty.push(progress))
    expect(empty[0].total).toBeUndefined()
  })

  it('steps progress reporting so a multi-gigabyte archive cannot flood the UI', async () => {
    const chunk = new Uint8Array(1024)
    const events: DownloadProgress[] = []

    await readDownloadBlob(streamResponse(Array.from({ length: 1024 }, () => chunk)).response, (progress) => events.push(progress))

    // 1 MiB of 1 KiB chunks: first chunk, one 512 KiB step, and the terminal event.
    expect(events.length).toBeLessThanOrEqual(4)
    expect(events[0].loaded).toBe(1024)
    expect(events[events.length - 1]).toEqual({ loaded: 1024 * 1024, total: 1024 * 1024, done: true })
  })

  it('falls back to response.blob() when the response exposes no stream reader', async () => {
    const events: DownloadProgress[] = []
    const blob = await readDownloadBlob(stubResponse({ 'Content-Type': 'application/gzip' }, new Blob(['abcde'])), (progress) => events.push(progress))

    expect(await blobText(blob)).toBe('abcde')
    expect(blob.type).toBe('application/gzip')
    expect(events).toEqual([{ loaded: 5, total: 5, done: true }])
  })

  it('cancels the reader and rethrows the original mid-stream failure', async () => {
    const failure = new Error('连接已重置')
    const events: DownloadProgress[] = []
    const reader = {
      read: vi.fn()
        .mockResolvedValueOnce({ done: false, value: bytes('abcd') })
        .mockRejectedValueOnce(failure),
      cancel: vi.fn(async () => undefined),
    }
    const response = { headers: new Headers(), body: { getReader: () => reader } } as unknown as Response

    await expect(readDownloadBlob(response, (progress) => events.push(progress))).rejects.toBe(failure)
    expect(reader.cancel).toHaveBeenCalledTimes(1)
    expect(events).toEqual([{ loaded: 4, total: undefined, done: false }])
  })
})

describe('filenameFromDisposition', () => {
  it('prefers the quoted server filename over the caller fallback', () => {
    const response = stubResponse({ 'Content-Disposition': 'attachment; filename="aero-default-20260929T053000Z.tar.gz"' })
    expect(filenameFromDisposition(response, 'aero-backup-2026-09-29.tar.gz')).toBe('aero-default-20260929T053000Z.tar.gz')
  })

  it('decodes the RFC 5987 extended form', () => {
    const response = stubResponse({ 'Content-Disposition': "attachment; filename*=UTF-8''%E6%95%B0%E6%8D%AE.tar.gz" })
    expect(filenameFromDisposition(response, 'fallback.tar.gz')).toBe('数据.tar.gz')
  })

  it('falls back when the header is absent, empty or carries no filename', () => {
    expect(filenameFromDisposition(stubResponse({}), 'fallback.tar.gz')).toBe('fallback.tar.gz')
    expect(filenameFromDisposition(stubResponse({ 'Content-Disposition': '' }), 'fallback.tar.gz')).toBe('fallback.tar.gz')
    expect(filenameFromDisposition(stubResponse({ 'Content-Disposition': 'attachment' }), 'fallback.tar.gz')).toBe('fallback.tar.gz')
  })

  it('strips directory components and control characters from a hostile filename', () => {
    const traversal = stubResponse({ 'Content-Disposition': 'attachment; filename="../../etc/passwd"' })
    expect(filenameFromDisposition(traversal, 'fallback.tar.gz')).toBe('passwd')

    const windows = stubResponse({ 'Content-Disposition': 'attachment; filename="C:\\temp\\archive.tar.gz"' })
    expect(filenameFromDisposition(windows, 'fallback.tar.gz')).toBe('archive.tar.gz')

    const blank = stubResponse({ 'Content-Disposition': 'attachment; filename="../"' })
    expect(filenameFromDisposition(blank, 'fallback.tar.gz')).toBe('fallback.tar.gz')
  })
})
