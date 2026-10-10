import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { FileListView, FolderListing } from '../api/files'
import type { VaultClient } from '../api/vault'
import { useFolderPagination } from './useFolderPagination'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

type Props = { client: VaultClient; path: string; view: FileListView; initial: FolderListing }
const first: FolderListing = {
  prefix: 'docs/', items: [{ name: 'a', type: 'folder' }], has_more: true, next_marker: 'docs/a/1',
}

function Probe({ client, path, view, initial }: Props): React.ReactElement {
  const pager = useFolderPagination(client, path, view, initial)
  return <div>
    <pre data-listing>{JSON.stringify(pager.listing)}</pre>
    <span data-loading>{String(pager.loading)}</span><span data-error>{pager.error}</span>
    <button onClick={pager.loadMore}>more</button>
  </div>
}

let mounted: { container: HTMLDivElement; root: Root } | undefined

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
  vi.restoreAllMocks()
})

function mount(client: VaultClient, initial = first): { container: HTMLElement; render(props: Partial<Props>): void } {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  mounted = { container, root }
  let props: Props = { client, path: 'docs/', view: 'mine', initial }
  const render = (changes: Partial<Props>) => {
    props = { ...props, ...changes }
    act(() => root.render(<Probe {...props} />))
  }
  render({})
  return { container, render }
}

function listing(container: HTMLElement): FolderListing {
  return JSON.parse(container.querySelector('[data-listing]')?.textContent || 'null')
}

async function more(container: HTMLElement): Promise<void> {
  await act(async () => { container.querySelector('button')?.click(); await Promise.resolve() })
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve: (value: T) => void = () => undefined
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

function clientWith(listFolder: VaultClient['listFolder']): VaultClient {
  return { listFolder } as VaultClient
}

describe('folder continuation isolation', () => {
  it('keeps path/view/marker, deduplicates by type+name and updates file metadata', async () => {
    const listFolder = vi.fn(async () => ({ prefix: 'docs/', has_more: false, items: [
      { name: 'a', type: 'folder' as const }, { name: 'b', type: 'folder' as const },
      { name: 'same', type: 'folder' as const }, { name: 'same', type: 'file' as const, etag: 'new' },
    ] }))
    const { container } = mount(clientWith(listFolder), { ...first, items: [
      ...first.items, { name: 'same', type: 'file', etag: 'old' },
    ] })
    await more(container)
    expect(listFolder).toHaveBeenCalledWith('docs/', 'mine', 'docs/a/1')
    expect(listing(container).items).toEqual([
      { name: 'a', type: 'folder' }, { name: 'same', type: 'file', etag: 'new' },
      { name: 'b', type: 'folder' }, { name: 'same', type: 'folder' },
    ])
    expect(listing(container).has_more).toBe(false)
  })

  it('does not dispatch concurrent duplicate continuations', async () => {
    const pending = deferred<FolderListing>()
    const listFolder = vi.fn(() => pending.promise)
    const { container } = mount(clientWith(listFolder))
    act(() => { container.querySelector('button')?.click(); container.querySelector('button')?.click() })
    expect(listFolder).toHaveBeenCalledTimes(1)
    expect(container.querySelector('[data-loading]')?.textContent).toBe('true')
    await act(async () => { pending.resolve({ ...first, has_more: false }); await pending.promise })
    expect(container.querySelector('[data-loading]')?.textContent).toBe('false')
  })

  it('retains the loaded page and retries the same marker on error', async () => {
    const listFolder = vi.fn<VaultClient['listFolder']>()
      .mockRejectedValueOnce(new Error('provider unavailable'))
      .mockResolvedValueOnce({ prefix: 'docs/', items: [{ name: 'b', type: 'folder' }], has_more: false })
    const { container } = mount(clientWith(listFolder))
    await more(container)
    expect(listing(container)).toEqual(first)
    expect(container.querySelector('[data-error]')?.textContent).toBe('provider unavailable')
    await more(container)
    expect(listing(container).items.map((item) => item.name)).toEqual(['a', 'b'])
    expect(listFolder.mock.calls).toEqual([['docs/', 'mine', 'docs/a/1'], ['docs/', 'mine', 'docs/a/1']])
    expect(container.querySelector('[data-error]')?.textContent).toBe('')
  })

  it.each(['path', 'view', 'client', 'refresh'] as const)('ignores an old request after %s changes', async (scope) => {
    const pending = deferred<FolderListing>()
    const { container, render } = mount(clientWith(() => pending.promise))
    await more(container)
    const changes: Partial<Props> = {
      path: scope === 'path' ? 'other/' : 'docs/',
      view: scope === 'view' ? 'all' : 'mine',
    }
    if (scope === 'client') changes.client = clientWith(async () => first)
    if (scope === 'refresh') changes.initial = { ...first, items: [{ name: 'fresh', type: 'folder' }] }
    render(changes)
    await act(async () => {
      pending.resolve({ prefix: 'docs/', items: [{ name: 'stale', type: 'folder' }], has_more: false })
      await pending.promise
    })
    expect(listing(container)).toEqual(changes.initial ?? first)
    expect(container.querySelector('[data-loading]')?.textContent).toBe('false')
    expect(container.textContent).not.toContain('stale')
  })

  it.each([undefined, '', 'docs/a/1'])('stops on missing/repeated marker %s without losing new directories', async (marker) => {
    const listFolder = vi.fn(async () => ({
      prefix: 'docs/', items: [{ name: 'b', type: 'folder' as const }], has_more: true, next_marker: marker,
    }))
    const { container } = mount(clientWith(listFolder))
    await more(container)
    expect(listing(container).items.map((item) => item.name)).toEqual(['a', 'b'])
    expect(listing(container).has_more).toBe(true)
    expect(listing(container).next_marker).toBeUndefined()
    await more(container)
    expect(listFolder).toHaveBeenCalledTimes(1)
  })

  it('stops a cursor cycle across multiple pages', async () => {
    const listFolder = vi.fn<VaultClient['listFolder']>()
      .mockResolvedValueOnce({ ...first, next_marker: 'docs/b/1' })
      .mockResolvedValueOnce({ ...first, next_marker: 'docs/a/1' })
    const { container } = mount(clientWith(listFolder))
    await more(container)
    await more(container)
    await more(container)
    expect(listFolder).toHaveBeenCalledTimes(2)
    expect(listing(container).next_marker).toBeUndefined()
  })

  it('rejects a continuation for another folder without replacing the loaded page', async () => {
    const { container } = mount(clientWith(async () => ({ prefix: 'other/', items: [], has_more: false })))
    await more(container)
    expect(listing(container)).toEqual(first)
    expect(container.querySelector('[data-error]')?.textContent).toContain('路径不匹配')
  })

  it('ignores completion after unmount', async () => {
    const pending = deferred<FolderListing>()
    const error = vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const { container } = mount(clientWith(() => pending.promise))
    await more(container)
    act(() => mounted?.root.unmount())
    mounted = undefined
    await act(async () => { pending.resolve({ ...first, has_more: false }); await pending.promise })
    expect(container.textContent).toBe('')
    expect(error).not.toHaveBeenCalled()
    container.remove()
  })
})
