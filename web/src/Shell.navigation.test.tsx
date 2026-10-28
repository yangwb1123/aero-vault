import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { IrisProvider } from '@iris-ui-kit/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { VaultSession } from './api/vault'
import type { WebConfig } from './config'
import { Shell } from './Shell'
import { vaultSkin } from './skin'

const observed = vi.hoisted(() => ({
  auth: { session: undefined as { accessToken: string } | undefined, login: vi.fn(), logout: vi.fn() },
  buckets: vi.fn(), departments: vi.fn(), operations: vi.fn(),
}))
vi.mock('./auth/AuthProvider', () => ({ useAuth: () => observed.auth }))
vi.mock('./pages/buckets/BucketsPage', () => ({
  BucketsPage: () => { observed.buckets(); return <h1>bucket-page-mounted</h1> },
}))
vi.mock('./pages/departments/DepartmentsPage', () => ({
  DepartmentsPage: () => { observed.departments(); return <h1>department-page-mounted</h1> },
}))
vi.mock('./pages/operations/OperationsPage', () => ({
  OperationsPage: () => { observed.operations(); return <h1>operations-page-mounted</h1> },
}))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const config: WebConfig = {
  apiBase: '/v1', oidcLoginPath: '/auth/oidc/login',
  oidcLogoutPath: '/auth/oidc/logout', allowAnonymous: true,
}
const reader: VaultSession = {
  authenticated: true, tenant_id: 'server-tenant', subject_id: 'reader-7',
  principal_kind: 'user', roles: [], groups: [], scopes: ['read'],
}
let sessionReply: () => Promise<Response>
let operatorReply: () => Promise<Response>
let fetcher: ReturnType<typeof vi.fn>
let mounted: { container: HTMLDivElement; root: Root } | undefined

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve: (value: T) => void = () => {}
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

async function settle(): Promise<void> {
  await act(async () => { await new Promise((resolve) => window.setTimeout(resolve, 0)) })
}

function render(route = 'services'): HTMLDivElement {
  window.history.replaceState(null, '', `#/${route}`)
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  mounted = { container, root }
  rerender(config)
  return container
}

function rerender(nextConfig: WebConfig): void {
  act(() => mounted?.root.render(
    <IrisProvider skin={vaultSkin}><Shell config={nextConfig} /></IrisProvider>,
  ))
}

function expectNoManagementMount(): void {
  expect(observed.buckets).not.toHaveBeenCalled()
  expect(observed.departments).not.toHaveBeenCalled()
  expect(observed.operations).not.toHaveBeenCalled()
}

function requestedPaths(): string[] {
  return fetcher.mock.calls.map(([input]) => new URL(String(input), 'http://localhost').pathname)
}

beforeEach(() => {
  observed.auth.session = undefined
  sessionReply = async () => json(reader)
  operatorReply = async () => json({ error: { code: 'Forbidden', message: 'operator admin scope required' } }, 403)
  fetcher = vi.fn(async (input: RequestInfo | URL) => {
    const path = new URL(String(input), 'http://localhost').pathname
    if (path.endsWith('/session')) return sessionReply()
    if (path.endsWith('/admin/tenants')) return operatorReply()
    throw new Error(`unexpected request: ${path}`)
  })
  vi.stubGlobal('fetch', fetcher)
})

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
  vi.clearAllMocks()
  vi.unstubAllGlobals()
  window.history.replaceState(null, '', '#/')
})

describe('session-aware primary navigation', () => {
  it.each([['read'], ['read', 'write']])('does not treat scopes %j or a local token as management access', async (...scopes) => {
    observed.auth.session = { accessToken: 'opaque-not-a-jwt' }
    sessionReply = async () => json({ ...reader, scopes })
    const container = render()
    await settle()

    expect(container.textContent).not.toContain('部门与成员')
    expect(container.textContent).not.toContain('运行运维')
    expect(container.textContent).not.toContain('存储桶')
    expect(container.textContent).toContain('文件')
    expect(container.textContent).toContain('回收站')
    expect(container.textContent).toContain('分享')
    expect(container.textContent).toContain('可选 AI')
    expect(container.textContent).toContain('服务端租户：server-tenant')
    expect(container.textContent).toContain('文件浏览桶：default（固定）')
    expect(requestedPaths()).toEqual(['/v1/session'])
    expectNoManagementMount()
  })

  it.each(['buckets', 'departments', 'operations'])('explains an unavailable direct %s route without mounting it', async (route) => {
    const container = render(route)
    await settle()

    expect(container.querySelector('h1')?.textContent).toBe('管理入口暂不可用')
    expect(container.textContent).toContain('服务端')
    expect(container.textContent).toContain('返回文件')
    expectNoManagementMount()
    expect(requestedPaths()).toEqual(['/v1/session'])
  })

})

describe('backend management eligibility', () => {
  it.each(['vault.tenant_admin', 'vault.file_admin'])('recognizes the exact backend department role %s', async (role) => {
    sessionReply = async () => json({ ...reader, roles: [role], scopes: ['read', 'write'] })
    const container = render('departments')
    await settle()

    expect(observed.departments).toHaveBeenCalled()
    expect(container.querySelector('h1')?.textContent).toBe('department-page-mounted')
    expect(container.textContent).toContain('管理')
    expect(observed.operations).not.toHaveBeenCalled()
    expect(requestedPaths()).toEqual(['/v1/session'])
  })

  it('does not infer management from ownership, generic roles or publisher role prefixes', async () => {
    sessionReply = async () => json({ ...reader, roles: ['owner', 'admin', 'vault.publisher_admin', 'vault.tenant_admin.extra'] })
    const container = render('departments')
    await settle()

    expect(container.querySelector('h1')?.textContent).toBe('管理入口暂不可用')
    expectNoManagementMount()
  })

})

describe('operator-only operations', () => {
  it('allows tenant management with admin scope but not operator operations after a denied read', async () => {
    sessionReply = async () => json({ ...reader, scopes: ['admin'] })
    const container = render('operations')
    await settle()

    expect(container.querySelector('h1')?.textContent).toBe('管理入口暂不可用')
    expect(observed.operations).not.toHaveBeenCalled()
    expect(requestedPaths()).toEqual(['/v1/session', '/v1/admin/tenants'])
  })

  it('mounts operations only after its existing operator-only read succeeds', async () => {
    sessionReply = async () => json({ ...reader, scopes: ['admin'] })
    const pending = deferred<Response>()
    operatorReply = () => pending.promise
    const container = render('operations')
    await settle()
    expect(observed.operations).not.toHaveBeenCalled()

    await act(async () => { pending.resolve(json({ tenants: [] })); await pending.promise })
    await settle()
    expect(container.querySelector('h1')?.textContent).toBe('operations-page-mounted')
    expect(observed.operations).toHaveBeenCalled()
    expect(requestedPaths()).toEqual(['/v1/session', '/v1/admin/tenants'])
  })
})

describe('unconfirmed and changed sessions', () => {
  it.each([
    { ...reader, authenticated: false, principal_kind: 'anonymous', scopes: [] },
    { authenticated: true, tenant_id: 'server-tenant' },
    { ...reader, principal_kind: 'unrecognized', scopes: ['admin'] },
  ])('does not advertise management for an anonymous or unknown observation %#', async (payload) => {
    sessionReply = async () => json(payload)
    const container = render('departments')
    await settle()
    expect(container.querySelector('h1')?.textContent).toBe('管理入口暂不可用')
    expectNoManagementMount()
  })

  it('keeps pending and failed session reads conservative and offers an explicit retry', async () => {
    const pending = deferred<Response>()
    sessionReply = () => pending.promise
    const container = render('departments')
    expectNoManagementMount()
    expect(container.textContent).not.toContain('server-tenant')

    await act(async () => { pending.resolve(json({ error: 'unavailable' }, 503)); await pending.promise })
    await settle()
    expect(container.querySelector('h1')?.textContent).toBe('管理入口暂不可用')
    expect(container.textContent).toContain('会话')
    expectNoManagementMount()
    sessionReply = async () => json({ ...reader, scopes: ['admin'] })
    const retry = [...container.querySelectorAll('button')].find((item) => item.textContent === '重新检查会话')
    expect(retry).toBeDefined()
    await act(async () => { retry?.click() })
    await settle()
    expect(observed.departments).toHaveBeenCalled()
  })

})

describe('client changes', () => {
  it('ignores a late privileged session after changing the API client', async () => {
    const oldSession = deferred<Response>()
    sessionReply = () => oldSession.promise
    const container = render('departments')
    sessionReply = async () => json({ ...reader, tenant_id: 'new-reader-tenant' })
    rerender({ ...config, apiBase: '/new-v1' })
    await settle()

    await act(async () => {
      oldSession.resolve(json({ ...reader, tenant_id: 'stale-admin-tenant', scopes: ['admin'] }))
      await oldSession.promise
    })
    await settle()
    expect(container.textContent).toContain('服务端租户：new-reader-tenant')
    expect(container.textContent).not.toContain('stale-admin-tenant')
    expect(container.querySelector('h1')?.textContent).toBe('管理入口暂不可用')
    expectNoManagementMount()
    expect(requestedPaths()).toEqual(['/v1/session', '/new-v1/session'])
  })

  it('removes established management immediately and ignores an old operator check', async () => {
    sessionReply = async () => json({ ...reader, scopes: ['admin'] })
    const oldOperator = deferred<Response>()
    operatorReply = () => oldOperator.promise
    const container = render('departments')
    await settle()
    expect(observed.departments).toHaveBeenCalled()
    const mounts = observed.departments.mock.calls.length
    const newSession = deferred<Response>()
    sessionReply = () => newSession.promise
    rerender({ ...config, apiBase: '/next-v1' })

    expect(container.querySelector('h1')?.textContent).toBe('管理入口暂不可用')
    expect(container.textContent).not.toContain('服务端租户：server-tenant')
    await act(async () => { oldOperator.resolve(json({ tenants: [] })); await oldOperator.promise })
    await settle()
    expect(observed.departments).toHaveBeenCalledTimes(mounts)
    expect(observed.operations).not.toHaveBeenCalled()
    expect(container.querySelector('h1')?.textContent).toBe('管理入口暂不可用')
  })
})
