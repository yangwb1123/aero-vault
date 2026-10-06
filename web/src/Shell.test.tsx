import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { IrisProvider } from '@iris-ui-kit/react'
import { AuthProvider } from './auth/AuthProvider'
import type { WebConfig } from './config'
import { Shell } from './Shell'
import { vaultSkin } from './skin'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const config: WebConfig = {
  apiBase: '/v1',
  oidcLoginPath: '/auth/oidc/login',
  oidcLogoutPath: '/auth/oidc/logout',
  allowAnonymous: true,
}

const healthySession = {
  authenticated: false,
  tenant_id: 'default',
  principal_kind: 'anonymous',
  roles: [],
  groups: [],
  scopes: [],
}

// Mutable so a test can start from a payload that breaks page rendering.
const responses: Record<string, unknown> = {
  '/v1/session': { ...healthySession },
  '/v1/usage': {
    tenant: 'default',
    used_bytes: 0,
    used_objects: 0,
    max_bytes: 0,
    max_objects: 0,
    updated_at: '2026-10-01T00:00:00Z',
  },
  '/v1/buckets': { buckets: [] },
}

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}

let mounted: { container: HTMLDivElement; root: Root } | undefined
let consoleError: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  // A hand-edited or chat-pasted link can carry a payload the router cannot
  // decode; the shell must survive it and land on the fallback route.
  window.history.replaceState(null, '', '#/object/100%')
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const path = new URL(String(input), 'http://localhost').pathname
    const body = responses[path]
    if (!body) return new Response('{}', { status: 200 })
    return jsonResponse(body)
  }))
})

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
  vi.unstubAllGlobals()
  consoleError.mockRestore()
  window.history.replaceState(null, '', '#/')
  responses['/v1/session'] = { ...healthySession }
})

function button(container: HTMLElement, label: string): HTMLButtonElement {
  const found = [...container.querySelectorAll('button')]
    .find((candidate) => candidate.textContent?.trim() === label)
  if (!(found instanceof HTMLButtonElement)) throw new Error(`button not found: ${label}`)
  return found
}

function renderShell(container: HTMLDivElement): void {
  const root = createRoot(container)
  mounted = { container, root }
  act(() => root.render(
    <IrisProvider skin={vaultSkin}>
      <AuthProvider config={config}>
        <Shell config={config} />
      </AuthProvider>
    </IrisProvider>,
  ))
}

async function flushEffects(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => window.setTimeout(resolve, 0))
  })
}

describe('Shell resilience to an undecodable route hash', () => {
  it('keeps the layout mounted when the hash cannot be decoded', async () => {
    const container = document.createElement('div')
    document.body.append(container)
    renderShell(container)

    expect(container.firstElementChild).not.toBeNull()
    expect(container.textContent).toContain('Aero Vault')

    await flushEffects()
    expect(container.textContent).not.toContain('页面渲染失败')
    // The malformed payload falls back to `overview`, so the overview page —
    // not a crash fallback — is what the user sees.
    expect(container.textContent).toContain('空间概览')
  })

  it('clears a caught page error when navigating to another page', async () => {
    responses['/v1/session'] = { authenticated: false, tenant_id: 'default' }
    window.history.replaceState(null, '', '#/overview')
    const container = document.createElement('div')
    document.body.append(container)
    renderShell(container)
    await flushEffects()
    expect(container.textContent).toContain('页面渲染失败')
    expect(container.textContent).toContain('Aero Vault')

    act(() => {
      window.location.hash = '#/services'
      window.dispatchEvent(new HashChangeEvent('hashchange'))
    })
    await flushEffects()

    // This is integration coverage for Shell -> PageBoundary.resetKey, not
    // merely a unit test of the boundary with an explicitly supplied key.
    expect(container.querySelector('h1')?.textContent).toBe('平台服务')
    expect(container.textContent).not.toContain('页面渲染失败')
    expect(container.textContent).toContain('Aero Vault')
  })

  it('keeps the layout chrome when a page render throws and recovers on retry', async () => {
    // An API payload the page cannot render (missing `scopes`) crashes the
    // page component; the boundary must absorb it inside the admin layout.
    responses['/v1/session'] = { authenticated: false, tenant_id: 'default' }
    window.history.replaceState(null, '', '#/overview')
    const container = document.createElement('div')
    document.body.append(container)
    renderShell(container)
    await flushEffects()

    expect(container.textContent).toContain('页面渲染失败')
    expect(container.textContent).toContain('Aero Vault')

    responses['/v1/session'] = { ...healthySession }
    act(() => button(container, '重试').click())
    await flushEffects()

    expect(container.textContent).toContain('空间概览')
    expect(container.textContent).not.toContain('页面渲染失败')
  })
})
