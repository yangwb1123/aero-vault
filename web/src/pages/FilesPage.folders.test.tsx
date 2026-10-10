import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { VaultClient } from '../api/vault'
import { FilesPage } from './FilesPage'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let mounted: { root: Root; container: HTMLDivElement } | undefined

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
})

function mount(fetcher: typeof fetch): HTMLElement {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  mounted = { root, container }
  const client = new VaultClient('/v1', () => 'token', fetcher)
  act(() => root.render(<FilesPage client={client} onOpenObject={() => undefined} />))
  return container
}

async function settle(): Promise<void> {
  await act(async () => { await new Promise((resolve) => window.setTimeout(resolve, 0)) })
}

function button(container: HTMLElement, label: string): HTMLButtonElement {
  const result = [...container.querySelectorAll('button')].find((item) => item.textContent?.trim() === label)
  if (!result) throw new Error(`missing ${label}`)
  return result
}

async function click(container: HTMLElement, label: string): Promise<void> {
  await act(async () => { button(container, label).click(); await Promise.resolve() })
  await settle()
}

function response(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } })
}

const emptyFiles = { objects: [], has_more: false }

describe('FilesPage folder discovery', () => {
  it('continues an empty directory page and keeps mine without paging files', async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost')
      if (url.pathname === '/v1/files') return response(emptyFiles)
      if (url.searchParams.has('marker')) return response({
        prefix: '', items: [{ name: 'late-dir', type: 'folder' }], has_more: false, next_marker: '',
      })
      return response({ prefix: '', items: [], has_more: true, next_marker: 'directory-marker/' })
    })
    const container = mount(fetcher as typeof fetch)
    await settle()
    await click(container, '我的文件')
    expect(container.textContent).toContain('尚未发现子目录')
    expect(container.textContent).not.toContain('没有子目录')
    const filesBefore = fetcher.mock.calls.filter(([input]) => String(input).includes('/files?')).length
    await click(container, '加载更多目录')
    expect(container.textContent).toContain('late-dir')
    expect(container.textContent).not.toContain('目录列表尚未加载完整')
    expect(container.querySelector('.folder-item .text-danger')).toBeNull()
    expect(fetcher.mock.calls.filter(([input]) => String(input).includes('/files?'))).toHaveLength(filesBefore)
    const last = new URL(String(fetcher.mock.calls.at(-1)?.[0]), 'http://localhost')
    expect(last.searchParams.get('view')).toBe('mine')
    expect(last.searchParams.get('marker')).toBe('directory-marker/')
  })

  it('deduplicates directories and retains them after a failed continuation', async () => {
    let calls = 0
    const fetcher = async (input: RequestInfo | URL): Promise<Response> => {
      const url = new URL(String(input), 'http://localhost')
      if (url.pathname === '/v1/files') return response(emptyFiles)
      if (url.searchParams.has('marker')) {
        if (++calls === 1) return response({ error: { code: 'Unavailable', message: '目录暂不可用' } }, 503)
        return response({ prefix: '', items: [
          { name: 'a', type: 'folder' }, { name: 'b', type: 'folder' },
        ], has_more: false })
      }
      return response({ prefix: '', items: [{ name: 'a', type: 'folder' }], has_more: true, next_marker: 'a/1' })
    }
    const container = mount(fetcher)
    await settle()
    await click(container, '加载更多目录')
    expect(container.querySelectorAll('.folder-item')).toHaveLength(1)
    expect(container.textContent).toContain('目录暂不可用')
    await click(container, '加载更多目录')
    expect(container.querySelectorAll('.folder-item')).toHaveLength(2)
    expect(container.textContent).not.toContain('目录暂不可用')
  })

  it('offers reload rather than an endless retry for an unusable marker', async () => {
    const fetcher = async (input: RequestInfo | URL): Promise<Response> => {
      const url = new URL(String(input), 'http://localhost')
      return response(url.pathname === '/v1/files' ? emptyFiles
        : { prefix: '', items: [], has_more: true, next_marker: '' })
    }
    const container = mount(fetcher)
    await settle()
    expect(container.textContent).toContain('后续目录游标不可用')
    expect(container.textContent).not.toContain('没有子目录')
    expect([...container.querySelectorAll('button')].some((item) => item.textContent === '加载更多目录')).toBe(false)
    await click(container, '重新加载目录')
    expect(container.textContent).toContain('后续目录游标不可用')
  })
})
