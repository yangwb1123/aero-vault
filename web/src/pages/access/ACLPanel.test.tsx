import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ACLEntry, ResourceKind } from '../../api/access'
import type { VaultClient } from '../../api/vault'
import { ACLPanel } from './ACLPanel'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

type ACLClient = Pick<VaultClient, 'listACL' | 'putACL' | 'deleteACL'>

function createClient(overrides: Partial<ACLClient> = {}): VaultClient {
  const defaults: ACLClient = {
    listACL: async () => [],
    putACL: async () => [],
    deleteACL: async () => undefined,
  }
  return { ...defaults, ...overrides } as unknown as VaultClient
}

function mountPanel(client: VaultClient): { container: HTMLDivElement; root: Root } {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  act(() => root.render(<ACLPanel client={client} bucket="bucket-a" />))
  return { container, root }
}

function button(container: HTMLElement, label: string): HTMLButtonElement {
  const found = [...container.querySelectorAll('button')]
    .find((candidate) => candidate.textContent?.trim() === label)
  if (!(found instanceof HTMLButtonElement)) throw new Error(`button not found: ${label}`)
  return found
}

function resourceKeyInput(container: HTMLElement): HTMLInputElement {
  const label = [...container.querySelectorAll('label')]
    .find((candidate) => candidate.textContent?.includes('资源 Key'))
  const found = label?.querySelector('input')
  if (!(found instanceof HTMLInputElement)) throw new Error('resource key input not found')
  return found
}

async function click(element: HTMLButtonElement): Promise<void> {
  await act(async () => {
    element.click()
    await Promise.resolve()
  })
}

async function changeInput(element: HTMLInputElement, value: string): Promise<void> {
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
    setter?.call(element, value)
    element.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve: (value: T) => void = () => {}
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

const entryA: ACLEntry = {
  id: 'acl-a',
  bucket: 'bucket-a',
  key: 'folder/a.txt',
  resource_kind: 'object',
  principal_type: 'user',
  principal_id: 'alice',
  action: 'object:read',
  effect: 'allow',
  inherit: false,
  created_by: 'admin',
  created_at: '2026-09-26T10:00:00Z',
}

let mounted: { container: HTMLDivElement; root: Root } | undefined

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
  vi.restoreAllMocks()
})

describe('ACLPanel resource scope', () => {
  it('discards a delayed list response after the resource key changes', async () => {
    const oldList = deferred<ACLEntry[]>()
    const currentList = deferred<ACLEntry[]>()
    const responses = [oldList, currentList]
    const listACL = vi.fn((_bucket: string, _key: string, _kind: ResourceKind) =>
      responses.shift()?.promise ?? Promise.resolve([]))
    mounted = mountPanel(createClient({ listACL }))

    await changeInput(resourceKeyInput(mounted.container), 'folder/a.txt')
    await click(button(mounted.container, '读取规则'))
    expect(listACL).toHaveBeenNthCalledWith(1, 'bucket-a', 'folder/a.txt', 'object')

    await changeInput(resourceKeyInput(mounted.container), 'folder/b.txt')
    await click(button(mounted.container, '读取规则'))
    await act(async () => {
      currentList.resolve([])
      await currentList.promise
    })
    expect(mounted.container.textContent).toContain('暂无 ACL 规则')

    await act(async () => {
      oldList.resolve([entryA])
      await oldList.promise
    })
    expect(mounted.container.textContent).toContain('暂无 ACL 规则')
    expect(mounted.container.textContent).not.toContain('alice')
    expect(mounted.container.textContent).not.toContain('acl-a')
  })

  it('does not refresh the new resource or retain stale rows after an old-scope delete', async () => {
    const pendingDelete = deferred<void>()
    const listACL = vi.fn(async (_bucket: string, _key: string, _kind: ResourceKind) => [entryA])
    const deleteACL = vi.fn(() => pendingDelete.promise)
    mounted = mountPanel(createClient({ listACL, deleteACL }))

    await changeInput(resourceKeyInput(mounted.container), 'folder/a.txt')
    await click(button(mounted.container, '读取规则'))
    expect(mounted.container.textContent).toContain('alice')

    vi.stubGlobal('confirm', vi.fn(() => true))
    await click(button(mounted.container, '删除'))
    expect(deleteACL).toHaveBeenCalledWith('acl-a')

    await changeInput(resourceKeyInput(mounted.container), 'folder/b.txt')
    await act(async () => {
      pendingDelete.resolve()
      await pendingDelete.promise
    })

    expect(listACL).toHaveBeenCalledTimes(1)
    expect(mounted.container.textContent).not.toContain('alice')
    expect(mounted.container.textContent).not.toContain('acl-a')
    expect(mounted.container.textContent).not.toContain('ACL 操作失败')
  })
})
