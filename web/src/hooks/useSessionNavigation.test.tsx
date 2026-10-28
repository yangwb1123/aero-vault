import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { VaultClient, VaultSession } from '../api/vault'
import { useSessionNavigation } from './useSessionNavigation'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
const reader: VaultSession = {
  authenticated: true, tenant_id: 'acme', principal_kind: 'user',
  roles: [], groups: [], scopes: ['read'],
}
const admin: VaultSession = { ...reader, scopes: ['admin'] }
let mounted: { container: HTMLDivElement; root: Root } | undefined
let latest: ReturnType<typeof useSessionNavigation>

function Probe({ client }: { client: VaultClient }): React.ReactElement {
  latest = useSessionNavigation(client)
  return <span>{latest.session?.tenant_id}</span>
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void; reject(reason: unknown): void } {
  let resolve: (value: T) => void = () => {}
  let reject: (reason: unknown) => void = () => {}
  const promise = new Promise<T>((accept, fail) => { resolve = accept; reject = fail })
  return { promise, resolve, reject }
}

function clientFor(getSession: () => Promise<VaultSession>, listAdminTenants = vi.fn(async () => [])): VaultClient {
  return { getSession: vi.fn(getSession), listAdminTenants } as unknown as VaultClient
}

function render(client: VaultClient): void {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  mounted = { container, root }
  rerender(client)
}

function rerender(client: VaultClient): void {
  act(() => mounted?.root.render(<Probe client={client} />))
}

async function settle(): Promise<void> {
  await act(async () => { await new Promise((resolve) => window.setTimeout(resolve, 0)) })
}

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
})

describe('useSessionNavigation observations', () => {
  it('starts unknown, uses getSession and never probes operator routes for a reader', async () => {
    const pending = deferred<VaultSession>()
    const client = clientFor(() => pending.promise)
    render(client)
    expect(latest.status).toBe('loading')
    expect(latest.management).toEqual({ buckets: false, departments: false, operations: false })

    await act(async () => { pending.resolve(reader); await pending.promise })
    await settle()
    expect(latest.status).toBe('ready')
    expect(latest.session).toEqual(reader)
    expect(client.getSession).toHaveBeenCalledTimes(1)
    expect(client.listAdminTenants).not.toHaveBeenCalled()
  })

  it.each([['read', 'write'], ['read']])('keeps non-admin scopes %j out of the operator probe', async (...scopes) => {
    const client = clientFor(async () => ({ ...reader, roles: ['vault.tenant_admin'], scopes }))
    render(client)
    await settle()
    expect(latest.management).toEqual({ buckets: true, departments: true, operations: false })
    expect(client.listAdminTenants).not.toHaveBeenCalled()
  })

  it('does not grant operations before the backend operator-only read has completed', async () => {
    const operator = deferred<never[]>()
    const listAdminTenants = vi.fn(() => operator.promise)
    const client = clientFor(async () => admin, listAdminTenants)
    render(client)
    await settle()
    expect(latest.management).toEqual({ buckets: true, departments: true, operations: false })
    expect(latest.operatorLoading).toBe(true)

    await act(async () => { operator.resolve([]); await operator.promise })
    await settle()
    expect(latest.management.operations).toBe(true)
    expect(client.listAdminTenants).toHaveBeenCalledTimes(1)
  })

})

describe('failed and incomplete observations', () => {
  it.each(['Forbidden', 'service fault'])('keeps operations unavailable after %s without discarding the session', async (reason) => {
    const listAdminTenants = vi.fn(async () => { throw new Error(reason) })
    render(clientFor(async () => admin, listAdminTenants))
    await settle()
    expect(latest.status).toBe('ready')
    expect(latest.session?.tenant_id).toBe('acme')
    expect(latest.management).toEqual({ buckets: true, departments: true, operations: false })
    expect(latest.operatorFailed).toBe(true)
    expect(listAdminTenants).toHaveBeenCalledTimes(1)
  })

  it.each([
    { ...reader, authenticated: false, principal_kind: 'anonymous', scopes: [] },
    { authenticated: true, tenant_id: 'acme' },
  ])('keeps anonymous or incomplete responses conservative %#', async (payload) => {
    const client = clientFor(async () => payload as VaultSession)
    render(client)
    await settle()
    expect(latest.management).toEqual({ buckets: false, departments: false, operations: false })
    expect(client.listAdminTenants).not.toHaveBeenCalled()
  })

  it('reports session read errors without synthesizing an authenticated observation', async () => {
    const client = clientFor(async () => { throw new Error('session unavailable') })
    render(client)
    await settle()
    expect(latest.status).toBe('error')
    expect(latest.session).toBeUndefined()
    expect(latest.management).toEqual({ buckets: false, departments: false, operations: false })
    expect(client.listAdminTenants).not.toHaveBeenCalled()
  })
})

describe('session request fencing', () => {
  it('ignores a stale session completion after replacing the client', async () => {
    const oldSession = deferred<VaultSession>()
    const oldClient = clientFor(() => oldSession.promise)
    const currentClient = clientFor(async () => reader)
    render(oldClient)
    rerender(currentClient)
    await settle()
    await act(async () => { oldSession.resolve(admin); await oldSession.promise })
    await settle()

    expect(latest.session).toEqual(reader)
    expect(latest.management).toEqual({ buckets: false, departments: false, operations: false })
    expect(oldClient.listAdminTenants).not.toHaveBeenCalled()
  })

  it('hides established privileges synchronously and ignores stale operator completion', async () => {
    const operator = deferred<never[]>()
    const oldClient = clientFor(async () => admin, vi.fn(() => operator.promise))
    const pendingSession = deferred<VaultSession>()
    render(oldClient)
    await settle()
    expect(latest.management.departments).toBe(true)
    rerender(clientFor(() => pendingSession.promise))
    expect(latest.session).toBeUndefined()
    expect(latest.management).toEqual({ buckets: false, departments: false, operations: false })

    await act(async () => { operator.resolve([]); await operator.promise })
    await settle()
    expect(latest.management.operations).toBe(false)
    await act(async () => { pendingSession.resolve(reader); await pendingSession.promise })
    await settle()
    expect(latest.session).toEqual(reader)
    expect(latest.management.operations).toBe(false)
  })

})

describe('same-client rechecks', () => {
  it('does not revive an earlier observation when a client is reused after another client', async () => {
    const first = deferred<VaultSession>()
    const second = deferred<VaultSession>()
    const read = vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
    const oldClient = clientFor(read)
    render(oldClient)
    rerender(clientFor(async () => reader))
    await settle()
    rerender(oldClient)
    await act(async () => { first.resolve(admin); await first.promise })
    await settle()
    expect(latest.status).toBe('loading')
    expect(latest.management.departments).toBe(false)
    await act(async () => { second.resolve(reader); await second.promise })
    await settle()
    expect(latest.session).toEqual(reader)
    expect(read).toHaveBeenCalledTimes(2)
  })

  it('revokes eligibility while explicitly rechecking the same client', async () => {
    const read = vi.fn().mockResolvedValueOnce(admin).mockResolvedValueOnce(reader)
    const client = clientFor(read)
    render(client)
    await settle()
    expect(latest.management.operations).toBe(true)
    act(() => latest.reload())
    expect(latest.session).toBeUndefined()
    expect(latest.management.operations).toBe(false)
    await settle()
    expect(latest.session).toEqual(reader)
    expect(latest.management.departments).toBe(false)
  })
})
