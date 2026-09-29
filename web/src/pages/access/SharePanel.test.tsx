import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { CreatedShare, Share } from '../../api/access'
import type { VaultClient } from '../../api/vault'
import { SharePanel } from './SharePanel'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

type ShareClient = Pick<VaultClient, 'listShares' | 'createShare' | 'revokeShare'>

function createClient(overrides: Partial<ShareClient> = {}): VaultClient {
  const defaults: ShareClient = {
    listShares: async () => [],
    createShare: async () => ({ share: makeShare('share-default', 'bucket-a', ''), token: '', url: '' }),
    revokeShare: async () => undefined,
  }
  return { ...defaults, ...overrides } as unknown as VaultClient
}

function makeShare(id: string, bucket: string, key: string): Share {
  return {
    id,
    bucket,
    key,
    name: id,
    allow_preview: true,
    allow_download: true,
    use_count: 0,
    created_by: 'alice',
    created_at: '2026-09-26T10:00:00Z',
  }
}

function mountPanel(client: VaultClient, bucket: string): { container: HTMLDivElement; root: Root } {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  act(() => root.render(<SharePanel client={client} bucket={bucket} />))
  return { container, root }
}

function keyInput(container: HTMLElement): HTMLInputElement {
  const found = container.querySelector<HTMLInputElement>('input[placeholder="docs/report.pdf"]')
  if (!found) throw new Error('share key input not found')
  return found
}

function button(container: HTMLElement, label: string): HTMLButtonElement {
  const found = [...container.querySelectorAll('button')]
    .find((candidate) => candidate.textContent?.trim() === label)
  if (!(found instanceof HTMLButtonElement)) throw new Error(`button not found: ${label}`)
  return found
}

async function changeInput(element: HTMLInputElement, value: string): Promise<void> {
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
    setter?.call(element, value)
    element.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function click(element: HTMLButtonElement): Promise<void> {
  await act(async () => {
    element.click()
    await Promise.resolve()
  })
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve: (value: T) => void = () => {}
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

let mounted: { container: HTMLDivElement; root: Root } | undefined

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
  vi.restoreAllMocks()
})

describe('SharePanel target scope', () => {
  it('discards a delayed share list after the object key changes', async () => {
    const oldList = deferred<Share[]>()
    const listShares = vi.fn((_bucket: string, key: string) =>
      key === 'docs/a.txt' ? oldList.promise : Promise.resolve([]))
    const client = createClient({ listShares })
    mounted = mountPanel(client, 'bucket-a')

    await changeInput(keyInput(mounted.container), 'docs/a.txt')
    await click(button(mounted.container, '刷新列表'))
    await changeInput(keyInput(mounted.container), 'docs/b.txt')
    await click(button(mounted.container, '刷新列表'))
    expect(mounted.container.textContent).toContain('没有有效分享链接')

    await act(async () => {
      oldList.resolve([makeShare('old-share', 'bucket-a', 'docs/a.txt')])
      await oldList.promise
    })
    expect(mounted.container.textContent).toContain('没有有效分享链接')
    expect(mounted.container.textContent).not.toContain('old-share')
  })

  it('does not expose a create response after the bucket target changes', async () => {
    const pendingCreate = deferred<CreatedShare>()
    const createShare = vi.fn(() => pendingCreate.promise)
    const listShares = vi.fn(async () => [])
    const client = createClient({ createShare, listShares })
    mounted = mountPanel(client, 'bucket-a')

    await changeInput(keyInput(mounted.container), 'docs/a.txt')
    await click(button(mounted.container, '创建分享'))
    expect(createShare).toHaveBeenCalledWith(expect.objectContaining({ bucket: 'bucket-a', key: 'docs/a.txt' }))

    act(() => mounted?.root.render(<SharePanel client={client} bucket="bucket-b" />))
    await act(async () => {
      pendingCreate.resolve({
        share: makeShare('new-share', 'bucket-a', 'docs/a.txt'),
        token: 'secret-token',
        url: 'https://share.example/secret-token',
      })
      await pendingCreate.promise
    })

    expect(listShares).not.toHaveBeenCalled()
    expect(mounted.container.textContent).not.toContain('https://share.example/secret-token')
  })
})
