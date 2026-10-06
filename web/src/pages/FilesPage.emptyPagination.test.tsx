import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { VaultClient, type FilePage, type VaultObject } from '../api/vault'
import { FilesPage } from './FilesPage'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const nested: VaultObject = {
  bucket: 'default', key: 'child/deep.txt', size: 12, etag: 'nested',
  backend: 'local', content_type: 'text/plain',
  created_at: '2026-10-06T10:00:00Z', updated_at: '2026-10-06T10:00:00Z',
}
const direct = { ...nested, key: 'visible.txt', etag: 'direct' }
let mounted: { container: HTMLDivElement; root: Root } | undefined

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
})

function clientFor(page: (marker: string) => Promise<FilePage>): VaultClient {
  return new VaultClient('/v1', () => '', async (input) => {
    const url = new URL(String(input), 'http://localhost')
    const data = url.pathname === '/v1/files'
      ? await page(url.searchParams.get('marker') ?? '')
      : { prefix: '', items: [] }
    return new Response(JSON.stringify(data), { status: 200 })
  })
}

function mount(client: VaultClient): HTMLElement {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  mounted = { container, root }
  act(() => root.render(<FilesPage client={client} onOpenObject={() => undefined} />))
  return container
}

async function settle(): Promise<void> {
  await act(async () => { await new Promise((resolve) => window.setTimeout(resolve, 0)) })
}

async function click(container: HTMLElement, label: string): Promise<void> {
  const button = [...container.querySelectorAll('button')]
    .find((candidate) => candidate.textContent?.trim() === label)
  if (!button) throw new Error(`missing button ${label}`)
  await act(async () => { button.click(); await Promise.resolve() })
  await settle()
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve: (value: T) => void = () => undefined
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

describe('FilesPage honest empty states across filtered marker pages', () => {
  it('does not declare the directory empty while filtered pages still have a cursor', async () => {
    const page = vi.fn(async (marker: string): Promise<FilePage> => {
      if (marker === 'second') return { objects: [direct], has_more: false }
      if (marker === 'first') return { objects: [{ ...nested, key: 'child/second.txt' }], has_more: true, next_marker: 'second' }
      return { objects: [nested], has_more: true, next_marker: 'first' }
    })
    const container = mount(clientFor(page))
    await settle()
    expect(container.textContent).not.toContain('没有匹配文件')
    expect(container.textContent).toContain('已加载页面中暂无匹配文件')
    expect(container.textContent).toContain('仍有页面未加载')
    await click(container, '加载更多')
    expect(container.textContent).not.toContain('没有匹配文件')
    await click(container, '加载更多')
    expect(page.mock.calls.map(([marker]) => marker)).toEqual(['', 'first', 'second'])
    expect(container.textContent).toContain('visible.txt')
    expect(container.textContent).not.toContain('没有匹配文件')
    expect(container.textContent).not.toContain('加载更多')
  })

  it('declares no matching files only after the marker chain is exhausted', async () => {
    const page = async (marker: string): Promise<FilePage> => marker
      ? { objects: [], has_more: false }
      : { objects: [nested], has_more: true, next_marker: 'first' }
    const container = mount(clientFor(page))
    await settle()
    expect(container.textContent).not.toContain('没有匹配文件')
    await click(container, '加载更多')
    expect(container.textContent).toContain('没有匹配文件')
    expect(container.textContent).not.toContain('加载更多')
  })

  it('offers a full reload instead of claiming empty when the continuation cursor is missing', async () => {
    let attempt = 0
    const page = vi.fn(async (): Promise<FilePage> => ++attempt === 1
      ? { objects: [nested], has_more: true }
      : { objects: [direct], has_more: false })
    const container = mount(clientFor(page))
    await settle()
    expect(container.textContent).not.toContain('没有匹配文件')
    expect(container.textContent).toContain('后续页面游标不可用')
    await click(container, '重新加载')
    expect(page).toHaveBeenCalledTimes(2)
    expect(container.textContent).toContain('visible.txt')
    expect(container.textContent).not.toContain('后续页面游标不可用')
  })

  it('retains the same cursor after a page failure so the user can retry', async () => {
    let nextAttempts = 0
    const page = vi.fn(async (marker: string): Promise<FilePage> => {
      if (!marker) return { objects: [nested], has_more: true, next_marker: 'first' }
      if (++nextAttempts === 1) throw new Error('暂时无法加载下一页')
      return { objects: [direct], has_more: false }
    })
    const container = mount(clientFor(page))
    await settle()
    await click(container, '加载更多')
    expect(container.textContent).toContain('暂时无法加载下一页')
    expect(container.textContent).not.toContain('没有匹配文件')
    await click(container, '加载更多')
    expect(page.mock.calls.map(([marker]) => marker)).toEqual(['', 'first', 'first'])
    expect(container.textContent).toContain('visible.txt')
    expect(container.textContent).not.toContain('暂时无法加载下一页')
  })

  it('ignores the old client pagination result after switching clients', async () => {
    const pending = deferred<FilePage>()
    const old = clientFor(async (marker) => marker ? pending.promise
      : { objects: [nested], has_more: true, next_marker: 'first' })
    const current = clientFor(async () => ({ objects: [direct], has_more: false }))
    const container = mount(old)
    await settle()
    const button = [...container.querySelectorAll('button')].find((item) => item.textContent === '加载更多')
    if (!button) throw new Error('missing load more button')
    act(() => button.click())
    await act(async () => { await Promise.resolve() })
    act(() => mounted?.root.render(<FilesPage client={current} onOpenObject={() => undefined} />))
    await settle()
    await act(async () => {
      pending.resolve({ objects: [], has_more: true })
      await pending.promise
    })
    expect(container.textContent).toContain('visible.txt')
    expect(container.textContent).not.toContain('后续页面游标不可用')
    expect(container.textContent).not.toContain('没有匹配文件')
  })
})
