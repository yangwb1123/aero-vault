import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { VaultClient } from '../../api/vault'
import type { ObjectVersion } from '../../api/objects'
import { VersionsPanel } from './VersionsPanel'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const objectVersions: ObjectVersion[] = [
  { version_id: 'v/1', size: 13, etag: 'etag-v1', updated_at: '2026-09-26T10:00:00Z' },
]

let anchors: HTMLAnchorElement[] = []
let captured: Blob[] = []
let revoked: string[] = []
let revokedAtClick: number[] = []
let revokedAfterClickTask: number[] = []
let issued = 0

// jsdom ships neither object URLs nor a click that survives navigation.
beforeEach(() => {
  anchors = []
  captured = []
  revoked = []
  revokedAtClick = []
  revokedAfterClickTask = []
  issued = 0
  Object.defineProperty(URL, 'createObjectURL', {
    configurable: true,
    writable: true,
    value: vi.fn((blob: Blob) => {
      captured.push(blob)
      issued += 1
      return `blob:v/${issued}`
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
    // Recording inside the click keeps the "URL was alive when the browser was
    // told to navigate" observation independent of act()'s own task draining.
    value: vi.fn(function recordClick(this: HTMLAnchorElement) {
      anchors.push(this)
      revokedAtClick.push(revoked.length)
      // A microtask still belongs to the click task, so it observes a release
      // that happens in the same tick but not one deferred to a later macrotask.
      void Promise.resolve().then(() => revokedAfterClickTask.push(revoked.length))
    }),
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

function versionFetcher(status = 200): typeof fetch {
  return (async (input: RequestInfo | URL) => {
    const path = String(input)
    if (path.endsWith('/versions')) {
      return new Response(JSON.stringify({ versions: objectVersions }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (status !== 200) {
      return new Response(JSON.stringify({ error: { code: 'Forbidden', message: '没有版本下载权限', request_id: 'req-9' } }), {
        status,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    return new Response('version bytes', {
      status: 200,
      headers: { 'Content-Length': '13', 'Content-Type': 'text/plain' },
    })
  }) as typeof fetch
}

function button(container: HTMLElement, label: string): HTMLButtonElement {
  const found = [...container.querySelectorAll('button')]
    .find((candidate) => candidate.textContent?.trim() === label)
  if (!(found instanceof HTMLButtonElement)) throw new Error(`button not found: ${label}`)
  return found
}

function dangerText(container: HTMLElement): string {
  return container.querySelector('[data-iris-alert-tone="danger"]')?.textContent ?? ''
}

async function flush(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => window.setTimeout(resolve, 0))
  })
}

let mounted: { container: HTMLDivElement; root: Root } | undefined

function renderPanel(client: VaultClient): HTMLDivElement {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  mounted = { container, root }
  act(() => root.render(<VersionsPanel client={client} objectKey="docs/a.txt" />))
  return container
}

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
  vi.restoreAllMocks()
})

describe('VersionsPanel download delivery', () => {
  it('delivers a version download end to end and releases the object URL afterwards', async () => {
    const container = renderPanel(new VaultClient('/v1', () => 'token', versionFetcher()))
    await flush()
    expect(container.textContent).toContain('v/1')

    await act(async () => {
      button(container, '下载').click()
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(anchors).toHaveLength(1)
    expect(anchors[0].download).toBe('a.txt-v_1')
    expect(anchors[0].getAttribute('href')).toBe('blob:v/1')
    // The object URL was still alive when the browser was told to navigate,
    // and it was not released anywhere in the click's own task.
    expect(revokedAtClick).toEqual([0])
    expect(revokedAfterClickTask).toEqual([0])

    await flush()

    expect(revoked).toEqual(['blob:v/1'])
    expect(anchors[0].isConnected).toBe(false)
    expect(await blobText(captured[0])).toBe('version bytes')
    expect(dangerText(container)).toBe('')
    expect(container.textContent).not.toContain('版本下载失败')
  })

  it('surfaces a delivery failure in the danger alert instead of failing silently', async () => {
    Object.defineProperty(URL, 'createObjectURL', { configurable: true, writable: true, value: undefined })
    const container = renderPanel(new VaultClient('/v1', () => 'token', versionFetcher()))
    await flush()

    await act(async () => {
      button(container, '下载').click()
      await Promise.resolve()
      await Promise.resolve()
    })
    await flush()

    expect(anchors).toHaveLength(0)
    expect(dangerText(container)).toContain('当前浏览器不支持文件下载')
    expect(dangerText(container)).not.toContain('版本下载失败')
    expect(button(container, '下载').disabled).toBe(false)
  })

  it('renders the backend message when the version request is rejected', async () => {
    const container = renderPanel(new VaultClient('/v1', () => 'token', versionFetcher(403)))
    await flush()

    await act(async () => {
      button(container, '下载').click()
      await Promise.resolve()
      await Promise.resolve()
    })
    await flush()

    expect(anchors).toHaveLength(0)
    expect(captured).toHaveLength(0)
    expect(dangerText(container)).toContain('没有版本下载权限')
    expect(container.textContent).toContain('v/1')
  })
})
