// Single delivery primitive for the SPA: object / version downloads and the
// whole-bucket backup archive all flow through here. Two rules keep the
// browser from observing a half-released payload:
//
//   1. the anchor is attached to the live document and the object URL is only
//      revoked on a later macrotask, so Firefox/Safari resolve the `blob:`
//      navigation after the click call has already returned;
//   2. failures are thrown, never silently turned into a no-op that looks like
//      a successful download.

export interface DownloadProgress {
  loaded: number
  total?: number
  done: boolean
}

export type ProgressListener = (progress: DownloadProgress) => void

// Progress is byte-stepped: a multi-gigabyte archive must not drive one React
// update per network chunk.
const PROGRESS_STEP_BYTES = 512 * 1024

// BlobPart only accepts ArrayBuffer-backed views, while the DOM types a fetch
// stream reader as SharedArrayBuffer-capable; every real chunk is plain bytes.
type StreamReader = ReadableStreamDefaultReader<Uint8Array<ArrayBuffer>>

export function downloadBlob(blob: Blob, filename: string): Promise<void> {
  if (typeof URL.createObjectURL !== 'function') throw new Error('当前浏览器不支持文件下载')
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename
  anchor.rel = 'noopener'
  anchor.style.display = 'none'
  document.body.append(anchor)
  try {
    anchor.click()
  } catch (reason) {
    // The navigation never started, so nothing can still be reading the URL.
    releaseAnchor(anchor, url)
    throw reason
  }
  return new Promise<void>((resolve) => {
    window.setTimeout(() => {
      releaseAnchor(anchor, url)
      resolve()
    }, 0)
  })
}

export async function readDownloadBlob(response: Response, onProgress?: ProgressListener): Promise<Blob> {
  const total = parseTotal(response.headers.get('Content-Length'))
  const type = response.headers.get('Content-Type') ?? ''
  const reader = streamReader(response)
  if (!reader) return bufferWholeResponse(response, total, type, onProgress)
  const chunks: BlobPart[] = []
  let loaded = 0
  let emitted = 0
  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      if (value) {
        chunks.push(value)
        loaded += value.byteLength
      }
      if (chunks.length === 1 || loaded - emitted >= PROGRESS_STEP_BYTES) {
        emitted = loaded
        onProgress?.({ loaded, total, done: false })
      }
    }
  } catch (reason) {
    await reader.cancel().catch(() => false)
    throw reason
  }
  onProgress?.({ loaded, total: total ?? loaded, done: true })
  return new Blob(chunks, { type })
}

export function filenameFromDisposition(response: Response, fallback: string): string {
  const header = response.headers.get('Content-Disposition') ?? ''
  const extended = headerParameter(header, /filename\*\s*=\s*([^;]+)/i)
  const plain = headerParameter(header, /\bfilename\s*=\s*("([^"]*)"|[^;]+)/i)
  const candidate = extended ? decodeExtendedValue(extended) : plain ? unquote(plain) : ''
  return sanitizeFilename(candidate) || sanitizeFilename(fallback) || 'download'
}

async function bufferWholeResponse(
  response: Response,
  total: number | undefined,
  type: string,
  onProgress: ProgressListener | undefined,
): Promise<Blob> {
  const blob = await response.blob()
  onProgress?.({ loaded: blob.size, total: total ?? blob.size, done: true })
  return type && blob.type !== type ? new Blob([blob], { type }) : blob
}

function releaseAnchor(anchor: HTMLAnchorElement, url: string): void {
  anchor.remove()
  if (typeof URL.revokeObjectURL === 'function') URL.revokeObjectURL(url)
}

function streamReader(response: Response): StreamReader | undefined {
  const body = response.body
  if (!body || typeof body.getReader !== 'function') return undefined
  return body.getReader() as StreamReader
}

function parseTotal(raw: string | null): number | undefined {
  if (!raw) return undefined
  const parsed = Number(raw)
  return Number.isInteger(parsed) && parsed > 0 ? parsed : undefined
}

function headerParameter(header: string, pattern: RegExp): string {
  return pattern.exec(header)?.[1]?.trim() ?? ''
}

function decodeExtendedValue(value: string): string {
  const separator = value.indexOf("''")
  const encoded = separator >= 0 ? value.slice(separator + 2).trim() : value
  try {
    return decodeURIComponent(encoded)
  } catch {
    // A malformed escape must not lose the whole filename.
    return encoded
  }
}

function unquote(value: string): string {
  return value.trim().replace(/^"/, '').replace(/"$/, '')
}

function sanitizeFilename(value: string): string {
  const segment = value.split(/[\\/]/).pop() ?? ''
  const printable = [...segment].filter((char) => {
    const code = char.codePointAt(0) ?? 0
    return code > 0x1f && code !== 0x7f && char !== '"' && char !== "'"
  }).join('')
  return printable.trim()
}
