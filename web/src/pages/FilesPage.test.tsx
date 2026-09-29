import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { VaultClient, VaultObject } from '../api/vault'
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

function createClient(overrides: Pick<VaultClient, 'listFiles'> & Partial<Pick<VaultClient, 'deleteFile'>>): VaultClient {
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
