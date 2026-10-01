import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { VaultApiError, VaultClient } from '../api/vault'
import type { VaultObject } from '../api/vault'
import { FilesPage } from './FilesPage'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const firstFile: VaultObject = {
  bucket: 'default',
  key: 'first.txt',
  size: 12,
  etag: 'etag-a',
  backend: 'local',
  content_type: 'text/plain',
  created_at: '2026-09-26T10:00:00Z',
  updated_at: '2026-09-26T10:00:00Z',
}
const secondFile: VaultObject = { ...firstFile, key: 'second.txt', etag: 'etag-b' }

function createClient(overrides: Partial<Pick<VaultClient, 'listFiles' | 'listFolder' | 'download' | 'deleteFile'>>): VaultClient {
  return {
    ...overrides,
    listFolder: async (prefix: string) => ({ prefix, items: [] }),
  } as unknown as VaultClient
}

function button(container: HTMLElement, label: string): HTMLButtonElement {
  const found = [...container.querySelectorAll('button')]
    .find((candidate) => candidate.textContent?.trim() === label)
  if (!(found instanceof HTMLButtonElement)) throw new Error(`button not found: ${label}`)
  return found
}

async function flushEffects(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => window.setTimeout(resolve, 0))
  })
}

async function click(element: HTMLButtonElement): Promise<void> {
  await act(async () => {
    element.click()
    await Promise.resolve()
    await Promise.resolve()
  })
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve: (value: T) => void = () => {}
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

async function changeInput(element: HTMLInputElement, value: string): Promise<void> {
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
    setter?.call(element, value)
    element.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

let mounted: { container: HTMLDivElement; root: Root } | undefined

// jsdom ships neither object URLs nor a click that survives navigation; the
// object download path needs both to be observable.
let anchors: HTMLAnchorElement[] = []
let captured: Blob[] = []
let revoked: string[] = []

beforeEach(() => {
  anchors = []
  captured = []
  revoked = []
  let issued = 0
  Object.defineProperty(URL, 'createObjectURL', {
    configurable: true,
    writable: true,
    value: vi.fn((blob: Blob) => {
      captured.push(blob)
      issued += 1
      return `blob:files/${issued}`
    }),
  })
  Object.defineProperty(URL, 'revokeObjectURL', {
    configurable: true,
    writable: true,
    value: vi.fn((url: string) => { revoked.push(url) }),
  })
  Object.defineProperty(HTMLAnchorElement.prototype, 'click', {
    configurable: true,
    writable: true,
    value: vi.fn(function recordClick(this: HTMLAnchorElement) { anchors.push(this) }),
  })
})

function blobText(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result))
    reader.onerror = () => reject(reader.error)
    reader.readAsText(blob)
  })
}

function dangerText(container: HTMLElement): string {
  return container.querySelector('[data-iris-alert-tone="danger"]')?.textContent ?? ''
}

async function clickAndSettle(element: HTMLButtonElement): Promise<void> {
  await act(async () => {
    element.click()
    await Promise.resolve()
    await Promise.resolve()
  })
  // Delivery releases the object URL on a later macrotask; drain both turns.
  await flushEffects()
  await flushEffects()
}

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
  vi.restoreAllMocks()
})

describe('FilesPage cursor pagination', () => {
  it('appends the next marker page and keeps existing rows visible', async () => {
    const listFiles = vi.fn(async (_prefix: string, _deleted: boolean, marker = '') =>
      marker
        ? { objects: [secondFile], has_more: false }
        : { objects: [firstFile], next_marker: 'first.txt', has_more: true })
    const client = createClient({ listFiles })
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    mounted = { container, root }

    act(() => root.render(<FilesPage client={client} onOpenObject={() => undefined} />))
    await flushEffects()

    expect(container.textContent).toContain('first.txt')
    await click(button(container, '加载更多'))

    expect(listFiles).toHaveBeenLastCalledWith('', false, 'first.txt')
    expect(container.textContent).toContain('first.txt')
    expect(container.textContent).toContain('second.txt')
    expect(container.textContent).not.toContain('加载更多')
  })

  it('ignores a completed object operation after navigating to another prefix', async () => {
    const pendingDelete = deferred<void>()
    const currentFile = { ...firstFile, key: 'docs/current.txt' }
    const listFiles = vi.fn((prefix: string) => Promise.resolve({
      objects: prefix ? [currentFile] : [firstFile],
      has_more: false,
    }))
    const deleteFile = vi.fn(() => pendingDelete.promise)
    const client = createClient({ listFiles, deleteFile })
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    mounted = { container, root }
    vi.spyOn(window, 'confirm').mockReturnValue(true)

    act(() => root.render(<FilesPage client={client} onOpenObject={() => undefined} />))
    await flushEffects()
    await click(button(container, '删除'))
    expect(deleteFile).toHaveBeenCalledWith('first.txt')

    const pathInput = container.querySelector<HTMLInputElement>('input[placeholder="目录路径，例如 team/docs/"]')
    if (!pathInput) throw new Error('path input not found')
    await changeInput(pathInput, 'docs/')
    await click(button(container, '进入'))
    await flushEffects()
    expect(container.textContent).toContain('docs/current.txt')

    await act(async () => {
      pendingDelete.resolve()
      await pendingDelete.promise
    })
    expect(container.textContent).toContain('docs/current.txt')
    expect(container.textContent).not.toContain('操作已完成')
  })

  it('ignores a delayed next-page response after navigating to another prefix', async () => {
    const pendingNext = deferred<{ objects: VaultObject[]; has_more: boolean }>()
    const currentFile = { ...firstFile, key: 'docs/current.txt' }
    const listFiles = vi.fn((prefix: string, _deleted: boolean, marker = '') => {
      if (marker) return pendingNext.promise
      if (prefix) return Promise.resolve({ objects: [currentFile], has_more: false })
      return Promise.resolve({ objects: [firstFile], next_marker: 'first.txt', has_more: true })
    })
    const client = createClient({ listFiles })
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    mounted = { container, root }

    act(() => root.render(<FilesPage client={client} onOpenObject={() => undefined} />))
    await flushEffects()
    await click(button(container, '加载更多'))
    const pathInput = container.querySelector<HTMLInputElement>('input[placeholder="目录路径，例如 team/docs/"]')
    if (!pathInput) throw new Error('path input not found')
    await changeInput(pathInput, 'docs/')
    await click(button(container, '进入'))
    await flushEffects()

    expect(container.textContent).toContain('docs/current.txt')
    await act(async () => {
      pendingNext.resolve({ objects: [secondFile], has_more: false })
      await pendingNext.promise
    })
    expect(container.textContent).toContain('docs/current.txt')
    expect(container.textContent).not.toContain('second.txt')
  })
})

describe('FilesPage object download', () => {
  const objectFetcher = async (input: RequestInfo | URL): Promise<Response> => {
    const path = String(input)
    if (path.endsWith('/files/first.txt')) {
      return new Response('file bytes', {
        status: 200,
        headers: { 'Content-Length': '10', 'Content-Type': 'text/plain' },
      })
    }
    const body = path.includes('/files?') ? { objects: [firstFile], has_more: false } : { prefix: '', items: [] }
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }

  function renderDownloadPage(client: VaultClient): HTMLDivElement {
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    mounted = { container, root }
    act(() => root.render(<FilesPage client={client} onOpenObject={() => undefined} />))
    return container
  }

  it('delivers an object download through the blob path and releases the object URL', async () => {
    const client = new VaultClient('/v1', () => 'token', objectFetcher as typeof fetch)
    const container = renderDownloadPage(client)
    await flushEffects()

    await clickAndSettle(button(container, '下载'))

    expect(anchors).toHaveLength(1)
    expect(anchors[0].download).toBe('first.txt')
    expect(anchors[0].isConnected).toBe(false)
    expect(await blobText(captured[0])).toBe('file bytes')
    expect(revoked).toEqual(['blob:files/1'])
    expect(dangerText(container)).toBe('')
    expect(container.textContent).toContain('操作已完成')
  })

  it('renders a danger alert and no success message when the object download fails', async () => {
    const download = vi.fn(async () => {
      throw new VaultApiError(404, 'NotFound', '对象不存在', 'req-7')
    })
    const listFiles = vi.fn(async () => ({ objects: [firstFile], has_more: false }))
    const container = renderDownloadPage(createClient({ listFiles, download }))
    await flushEffects()

    await clickAndSettle(button(container, '下载'))

    expect(download).toHaveBeenCalledWith('first.txt')
    expect(anchors).toHaveLength(0)
    expect(captured).toHaveLength(0)
    expect(dangerText(container)).toContain('对象不存在')
    expect(container.textContent).not.toContain('操作已完成')
    expect(container.textContent).toContain('first.txt')
  })
})
