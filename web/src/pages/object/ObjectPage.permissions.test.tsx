import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { VaultClient } from '../../api/vault'
import { ObjectPage } from './ObjectPage'

vi.mock('./PreviewPanel', () => ({ PreviewPanel: () => null }))
vi.mock('./MetadataPanel', () => ({ MetadataPanel: () => null }))
vi.mock('./VersionsPanel', () => ({ VersionsPanel: () => null }))
vi.mock('./GovernancePanel', () => ({ GovernancePanel: () => null }))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
const key = ' docs/100% report.txt '
let mounted: { container: HTMLDivElement; root: Root } | undefined

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
})

async function render(deleted = false) {
  const fetcher = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
    new Response(JSON.stringify({ entries: [] }), { headers: { 'Content-Type': 'application/json' } }))
  const client = new VaultClient('/v1', () => 'token', fetcher as typeof fetch)
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  mounted = { container, root }
  await act(async () => root.render(<ObjectPage client={client} objectKey={key} deleted={deleted}
    onBack={() => undefined} onRestored={() => undefined} />))
  const tab = [...container.querySelectorAll<HTMLButtonElement>('[role="tab"]')]
    .find((item) => item.textContent === '权限规则')!
  return { container, tab, fetcher }
}

async function setInput(input: HTMLInputElement, value: string): Promise<void> {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

describe('ObjectPage direct ACL tab', () => {
  it('binds lazy reads and writes to the exact object using the existing REST contract', async () => {
    const { container, tab, fetcher } = await render()
    expect(fetcher).not.toHaveBeenCalled()
    await act(async () => tab.click())
    const url = new URL(String(fetcher.mock.calls[0]?.[0]), 'http://localhost')
    expect(url.pathname).toBe('/v1/access/acl')
    expect(Object.fromEntries(url.searchParams)).toEqual({ bucket: 'default', key, kind: 'object' })
    expect(container.querySelector<HTMLInputElement>('input[placeholder="team/docs/report.pdf"]')?.readOnly).toBe(true)
    await setInput(container.querySelector('input[placeholder="subject / department / group / role"]')!, 'bob')
    const grant = [...container.querySelectorAll('button')].find((item) => item.textContent === '添加规则')!
    await act(async () => grant.click())
    const write = fetcher.mock.calls.find(([, init]) => init?.method === 'PUT')!
    expect(String(write[0])).toBe('/v1/access/acl')
    expect(new Headers(write[1]?.headers).get('Authorization')).toBe('Bearer token')
    expect(JSON.parse(String(write[1]?.body))).toEqual({ bucket: 'default', key, resource_kind: 'object',
      principal_type: 'user', principal_id: 'bob', actions: ['object:read', 'object:download'], effect: 'allow', inherit: false })
  })

  it('does not load permissions for deleted objects', async () => {
    const { tab, fetcher } = await render(true)
    expect(tab.disabled).toBe(true)
    await act(async () => tab.click())
    expect(fetcher).not.toHaveBeenCalled()
  })

  it('discards panel state when leaving and re-entering the permission tab', async () => {
    const { container, tab, fetcher } = await render()
    await act(async () => tab.click())
    await setInput(container.querySelector('input[placeholder="subject / department / group / role"]')!, 'previous-user')
    const preview = [...container.querySelectorAll<HTMLButtonElement>('[role="tab"]')].find((item) => item.textContent === '内容预览')!
    await act(async () => preview.click())
    await act(async () => tab.click())
    expect(container.querySelector<HTMLInputElement>('input[placeholder="subject / department / group / role"]')?.value).toBe('')
    expect(fetcher).toHaveBeenCalledTimes(2)
  })
})
