import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ACLEntry } from '../../api/access'
import type { VaultClient } from '../../api/vault'
import { ACLPanel } from './ACLPanel'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
type ACLClient = Pick<VaultClient, 'listACL' | 'putACL' | 'deleteACL'>
const key = ' docs/100% report.txt '
const entry = (principalID = 'bob'): ACLEntry => ({
  id: 'acl-a', bucket: 'bucket-a', key, resource_kind: 'object', principal_type: 'user', principal_id: principalID,
  action: 'object:read', effect: 'allow', inherit: false, created_by: 'alice', created_at: '2026-10-01T00:00:00Z',
})
function createClient(overrides: Partial<ACLClient> = {}): VaultClient {
  return { listACL: vi.fn(async () => []), putACL: vi.fn(async () => []), deleteACL: vi.fn(async () => undefined),
    ...overrides } as unknown as VaultClient
}
let mounted: { container: HTMLDivElement; root: Root } | undefined

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
  vi.restoreAllMocks()
})

async function render(client: VaultClient, objectKey = key, strict = false): Promise<HTMLElement> {
  if (!mounted) {
    const container = document.createElement('div')
    document.body.append(container)
    mounted = { container, root: createRoot(container) }
  }
  await act(async () => mounted?.root.render(strict
    ? <React.StrictMode><ACLPanel client={client} bucket="bucket-a" objectKey={objectKey} /></React.StrictMode>
    : <ACLPanel client={client} bucket="bucket-a" objectKey={objectKey} />))
  return mounted.container
}
function field<T extends HTMLInputElement | HTMLSelectElement>(container: HTMLElement, label: string): T {
  const target = [...container.querySelectorAll('label')].find((item) => item.querySelector('span')?.textContent === label)
  const input = target?.querySelector('input,select')
  if (!input) throw new Error(`missing field ${label}`)
  return input as T
}
function button(container: HTMLElement, label: string): HTMLButtonElement {
  const result = [...container.querySelectorAll('button')].find((item) => item.textContent?.trim() === label)
  if (!result) throw new Error(`missing button ${label}`)
  return result
}
async function click(element: HTMLElement): Promise<void> { await act(async () => element.click()) }
async function changeInput(element: HTMLInputElement, value: string): Promise<void> {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(element, value)
    element.dispatchEvent(new Event('input', { bubbles: true }))
  })
}
async function choose(element: HTMLSelectElement, value: string): Promise<void> {
  await act(async () => { element.value = value; element.dispatchEvent(new Event('change', { bubbles: true })) })
}
function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve: (value: T) => void = () => undefined
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

describe('ACLPanel fixed object rules', () => {
  it('loads the exact Key, fixes object type and explains the limits of an empty list', async () => {
    const listACL = vi.fn(async () => [])
    const container = await render(createClient({ listACL }))
    expect(listACL).toHaveBeenCalledWith('bucket-a', key, 'object')
    expect(field<HTMLInputElement>(container, '资源 Key').value).toBe(key)
    expect(field<HTMLInputElement>(container, '资源 Key').readOnly).toBe(true)
    expect(field<HTMLInputElement>(container, '资源类型').value).toBe('对象')
    expect(field<HTMLInputElement>(container, '资源类型').readOnly).toBe(true)
    expect(container.textContent).toContain('空列表不代表公开')
    expect(container.textContent).toContain('暂无 ACL 规则')
    expect(container.querySelector('input[type="checkbox"]')).toBeNull()
  })

  it('adds deduplicated actions to the fixed resource with no inheritance', async () => {
    const putACL = vi.fn(async () => [])
    const container = await render(createClient({ putACL }))
    await changeInput(field(container, '主体 ID'), ' bob ')
    await changeInput(field(container, 'Actions（逗号分隔）'), ' object:read, object:read,object:download, ')
    await click(button(container, '添加规则'))
    expect(putACL).toHaveBeenCalledWith({ bucket: 'bucket-a', key, resource_kind: 'object', principal_type: 'user',
      principal_id: 'bob', actions: ['object:read', 'object:download'], effect: 'allow', inherit: false })
  })

  it('requires confirmation for Deny and honors cancellation', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    const putACL = vi.fn(async () => [])
    const container = await render(createClient({ putACL }))
    await changeInput(field(container, '主体 ID'), 'bob')
    await choose(field(container, '授权效果'), 'deny')
    await click(button(container, '添加规则'))
    expect(putACL).not.toHaveBeenCalled()
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining('可能令自己失去访问或管理能力'))
    confirm.mockReturnValue(true)
    await click(button(container, '添加规则'))
    expect(putACL).toHaveBeenCalledWith(expect.objectContaining({ effect: 'deny', key }))
  })

  it('does not send empty action lists', async () => {
    const putACL = vi.fn(async () => [])
    const container = await render(createClient({ putACL }))
    await changeInput(field(container, '主体 ID'), 'bob')
    await changeInput(field(container, 'Actions（逗号分隔）'), ' , , ')
    await click(button(container, '添加规则'))
    expect(putACL).not.toHaveBeenCalled()
    expect(container.textContent).toContain('至少填写一个授权 action')
  })

  it('does not interpret read denial as no ACL', async () => {
    const container = await render(createClient({ listACL: async () => { throw new Error('AccessDenied') } }))
    expect(container.textContent).toContain('AccessDenied')
    expect(container.textContent).not.toContain('暂无 ACL 规则')
  })

  it('prevents same-turn duplicate writes and disables controls while waiting', async () => {
    const pending = deferred<ACLEntry[]>()
    const putACL = vi.fn(() => pending.promise)
    const listACL = vi.fn(async () => [])
    const container = await render(createClient({ putACL, listACL }))
    await changeInput(field(container, '主体 ID'), 'bob')
    await act(async () => {
      for (let i = 0; i < 2; i += 1) container.querySelector('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    })
    expect(putACL).toHaveBeenCalledTimes(1)
    expect(button(container, '读取规则').disabled).toBe(true)
    expect(button(container, '添加规则').disabled).toBe(true)
    await act(async () => { pending.resolve([]); await pending.promise })
    expect(listACL).toHaveBeenCalledTimes(2)
    expect(button(container, '添加规则').disabled).toBe(false)
  })

  it('keeps old rows hidden when switching A to B and back during an old read', async () => {
    const pending = deferred<ACLEntry[]>()
    let calls = 0
    const client = createClient({ listACL: () => ++calls === 1 ? pending.promise : Promise.resolve([]) })
    const container = await render(client)
    await render(client, 'docs/b.txt')
    await render(client)
    await act(async () => { pending.resolve([entry('old-user')]); await pending.promise })
    expect(container.textContent).not.toContain('old-user')
    expect(container.textContent).toContain('暂无 ACL 规则')
  })

  it('does not refresh an old client after its delayed write and resets form fields', async () => {
    const pending = deferred<ACLEntry[]>()
    const listACL = vi.fn(async () => [])
    const client = createClient({ listACL, putACL: () => pending.promise })
    const container = await render(client)
    await changeInput(field(container, '主体 ID'), 'old-user')
    await click(button(container, '添加规则'))
    await render(createClient())
    await act(async () => { pending.resolve([]); await pending.promise })
    expect(listACL).toHaveBeenCalledTimes(1)
    expect(field<HTMLInputElement>(container, '主体 ID').value).toBe('')
    expect(button(container, '添加规则').disabled).toBe(false)
  })

  it('does not refresh after a delete completes on an unmounted panel', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const pending = deferred<void>()
    const listACL = vi.fn(async () => [entry()])
    const client = createClient({ listACL, deleteACL: () => pending.promise })
    const container = await render(client)
    await click(button(container, '删除'))
    act(() => mounted?.root.unmount())
    mounted = undefined
    await act(async () => { pending.resolve(undefined); await pending.promise })
    expect(listACL).toHaveBeenCalledTimes(1)
    container.remove()
  })

  it('warns on a refresh failure after a write and rereads without resending the write', async () => {
    let reads = 0
    const putACL = vi.fn(async () => [entry()])
    const client = createClient({ putACL, listACL: async () => {
      reads += 1
      if (reads === 2) throw new Error('rules temporarily unavailable')
      return reads === 1 ? [] : [entry()]
    } })
    const container = await render(client)
    await changeInput(field(container, '主体 ID'), 'bob')
    await click(button(container, '添加规则'))
    expect(container.textContent).toContain('变更可能已生效，请重新读取规则确认')
    expect(container.textContent).toContain('当前列表为上次成功读取的结果，可能已过时')
    await click(button(container, '读取规则'))
    expect(container.textContent).toContain('user: bob')
    expect(putACL).toHaveBeenCalledTimes(1)
    expect(container.textContent).not.toContain('temporarily unavailable')
  })

  it('completes automatic reads under StrictMode replay', async () => {
    const container = await render(createClient({ listACL: async () => [entry('strict-user')] }), key, true)
    expect(container.textContent).toContain('strict-user')
    expect(button(container, '读取规则').disabled).toBe(false)
  })
})
