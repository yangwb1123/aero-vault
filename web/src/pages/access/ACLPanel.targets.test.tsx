import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ACLEntry } from '../../api/access'
import type { VaultClient } from '../../api/vault'
import { ACLPanel } from './ACLPanel'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let mounted: { container: HTMLDivElement; root: Root } | undefined
const empty = (): ACLEntry[] => []
function createClient(listACL = vi.fn(async () => empty()), putACL = vi.fn(async () => empty())) {
  return { listACL, putACL, deleteACL: vi.fn(async () => undefined) } as unknown as VaultClient
}
afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
})
async function render(client: VaultClient, bucket = 'bucket-a') {
  if (!mounted) {
    const container = document.createElement('div')
    document.body.append(container)
    mounted = { container, root: createRoot(container) }
  }
  await act(async () => mounted?.root.render(<ACLPanel client={client} bucket={bucket} />))
  return mounted.container
}
function field(container: HTMLElement, label: string): HTMLInputElement | HTMLSelectElement {
  const element = [...container.querySelectorAll('label')].find((item) => item.querySelector('span')?.textContent === label)
  return element!.querySelector('input,select')!
}
async function change(container: HTMLElement, label: string, value: string) {
  const element = field(container, label)
  await act(async () => {
    if (element instanceof HTMLSelectElement) {
      element.value = value
      element.dispatchEvent(new Event('change', { bubbles: true }))
    } else {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(element, value)
      element.dispatchEvent(new Event('input', { bubbles: true }))
    }
  })
}
async function click(container: HTMLElement, label: string) {
  const button = [...container.querySelectorAll('button')].find((item) => item.textContent?.trim() === label)!
  await act(async () => button.click())
}

describe('ACLPanel manual targets', () => {
  it('preserves manual folder inheritance and switches to an empty bucket key', async () => {
    const putACL = vi.fn(async () => empty())
    const container = await render(createClient(undefined, putACL))
    await change(container, '资源类型', 'folder')
    await change(container, '资源 Key', ' team/docs/ ')
    await change(container, '主体 ID', 'bob')
    await act(async () => container.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click())
    await click(container, '添加规则')
    expect(putACL).toHaveBeenLastCalledWith(expect.objectContaining({ key: 'team/docs/', resource_kind: 'folder', inherit: true }))
    await change(container, '资源类型', 'bucket')
    expect((field(container, '资源 Key') as HTMLInputElement).disabled).toBe(true)
    await change(container, '主体 ID', 'bob')
    await click(container, '添加规则')
    expect(putACL).toHaveBeenLastCalledWith(expect.objectContaining({ key: '', resource_kind: 'bucket', inherit: false }))
  })

  it('does not refresh an old bucket after a delayed manual write', async () => {
    let resolve: (entries: ACLEntry[]) => void = () => undefined
    const pending = new Promise<ACLEntry[]>((accept) => { resolve = accept })
    const listACL = vi.fn(async () => empty())
    const client = createClient(listACL, vi.fn(() => pending))
    const container = await render(client)
    await change(container, '资源 Key', 'file.txt')
    await change(container, '主体 ID', 'bob')
    await click(container, '添加规则')
    await render(client, 'bucket-b')
    await act(async () => { resolve([]); await pending })
    expect(listACL).not.toHaveBeenCalled()
  })
})
