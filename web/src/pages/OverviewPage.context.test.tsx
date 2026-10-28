import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { VaultClient, VaultSession } from '../api/vault'
import { OverviewPage } from './OverviewPage'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
const session: VaultSession = {
  authenticated: true, tenant_id: 'server-reported-tenant', subject_id: 'reader-7',
  principal_kind: 'user', roles: [], groups: [], scopes: ['read'],
}
let mounted: { container: HTMLDivElement; root: Root } | undefined

function clientFor(getSession = async () => session): VaultClient {
  return {
    getSession: vi.fn(getSession),
    getUsage: vi.fn(async () => ({ used_bytes: 1024, used_objects: 2, max_bytes: 2048, max_objects: 10 })),
    listBuckets: vi.fn(async () => ['default', 'images']),
  } as unknown as VaultClient
}

function render(client: VaultClient): HTMLDivElement {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  mounted = { container, root }
  act(() => root.render(<OverviewPage client={client} />))
  return container
}

async function settle(): Promise<void> {
  await act(async () => { await new Promise((resolve) => window.setTimeout(resolve, 0)) })
}

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
})

describe('overview storage context', () => {
  it('preserves the overview heading while identifying the tenant and fixed file-browser bucket', async () => {
    const client = clientFor()
    const container = render(client)
    await settle()

    expect(container.querySelector('h1')?.textContent).toBe('空间概览')
    expect(container.textContent).toContain('服务端租户')
    expect(container.textContent).toContain('server-reported-tenant')
    expect(container.textContent).toContain('文件浏览桶')
    expect(container.textContent).toContain('default（固定）')
    expect(container.textContent).toContain('不切换文件浏览范围')
    expect(container.querySelector('select')).toBeNull()
    expect(client.getSession).toHaveBeenCalledTimes(1)
    expect(client.getUsage).toHaveBeenCalledTimes(1)
    expect(client.listBuckets).toHaveBeenCalledTimes(1)
    expect(container.textContent).toContain('1.0 KB')
    expect(container.textContent).toContain('2.0 KB')
  })

  it('keeps an actual session failure visible instead of fabricating a tenant', async () => {
    const container = render(clientFor(async () => { throw new Error('session read unavailable') }))
    await settle()

    expect(container.textContent).toContain('session read unavailable')
    expect(container.textContent).not.toContain('server-reported-tenant')
    expect(container.querySelector('h1')).toBeNull()
  })
})
