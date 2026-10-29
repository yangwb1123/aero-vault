import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { VaultApiError } from '../api/transport'
import { PageError } from './Page'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let mounted: { container: HTMLDivElement; root: Root } | undefined

function render(error: Error, retry = vi.fn()): HTMLDivElement {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  mounted = { container, root }
  act(() => root.render(<PageError error={error} retry={retry} />))
  return container
}

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
  vi.restoreAllMocks()
})

const categories = [
  { status: 401, title: '需要身份认证', guidance: '登录' },
  { status: 403, title: '没有访问权限', guidance: '管理员' },
  { status: 404, title: '资源不存在或不可用', guidance: '路径' },
  { status: 409, title: '资源状态冲突', guidance: '最新' },
  { status: 412, title: '资源状态冲突', guidance: '最新' },
  { status: 413, title: '请求大小超限', guidance: '限制' },
  { status: 429, title: '请求过于频繁', guidance: '稍候' },
  { status: 503, title: '服务暂不可用', guidance: '稍后' },
]

describe('PageError read failure presentation', () => {
  it.each(categories)('explains HTTP $status without hiding safe server details', ({ status, title, guidance }) => {
    const error = new VaultApiError(status, 'ServerCode', '服务端原始说明：文件读取被拒绝', 'req-42')
    const container = render(error)
    const alert = container.querySelector('[data-iris-alert-tone="danger"]')

    expect(alert?.textContent).toContain(title)
    expect(alert?.textContent).toContain(guidance)
    expect(alert?.textContent).toContain('request_id: req-42')
    expect(container.querySelector('summary')?.textContent).toBe('服务端信息')
    expect(container.querySelector('details')?.textContent).toContain(error.message)
    expect(container.querySelector('details')?.open).toBe(false)
  })

})

describe('PageError explicit read retry', () => {
  it('offers a caller-controlled retry without any automatic request or session changes', async () => {
    const retry = vi.fn()
    const fetcher = vi.spyOn(window, 'fetch')
    const persist = vi.spyOn(Storage.prototype, 'setItem')
    const remove = vi.spyOn(Storage.prototype, 'removeItem')
    const error = new VaultApiError(401, 'Unauthorized', 'authentication required', 'auth-request')
    const container = render(error, retry)
    await act(async () => { await new Promise((resolve) => window.setTimeout(resolve, 0)) })
    act(() => mounted?.root.render(<PageError error={error} retry={retry} />))

    expect(retry).not.toHaveBeenCalled()
    expect(fetcher).not.toHaveBeenCalled()
    expect(persist).not.toHaveBeenCalled()
    expect(remove).not.toHaveBeenCalled()
    expect(container.querySelectorAll('button')).toHaveLength(1)
    expect(container.querySelector('button')?.textContent).toBe('重试')
    expect(container.querySelector('a')).toBeNull()

    act(() => container.querySelector<HTMLButtonElement>('button')?.click())
    expect(retry).toHaveBeenCalledTimes(1)
    expect(fetcher).not.toHaveBeenCalled()
    expect(persist).not.toHaveBeenCalled()
    expect(remove).not.toHaveBeenCalled()
  })

  it('handles plain network errors with guidance and no invented request ID', () => {
    const container = render(new TypeError('Failed to fetch'))
    expect(container.textContent).toContain('读取失败')
    expect(container.textContent).toContain('网络')
    expect(container.textContent).toContain('Failed to fetch')
    expect(container.textContent).not.toContain('request_id:')
  })

})

describe('PageError cancellation', () => {
  it('does not turn cancellation into a danger alert or retry', () => {
    const retry = vi.fn()
    const container = render(new DOMException('read cancelled', 'AbortError'), retry)
    expect(container.querySelector('[data-iris-alert-tone="danger"]')).toBeNull()
    expect(container.querySelector('button')).toBeNull()
    expect(container.textContent).not.toContain('read cancelled')
    expect(retry).not.toHaveBeenCalled()
  })

})

describe('PageError private diagnostics', () => {
  it.each([
    '<!doctype html><html><body>proxy-secret</body></html>',
    'rejected Authorization: Bearer synthetic-secret',
    'failed https://example.invalid/v1/files/key?token=synthetic-secret',
    'failed /share/synthetic-share-secret',
  ])('does not dump proxy pages or request secrets into the DOM (%s)', (message) => {
    const error = new VaultApiError(502, 'ProxyError', message, 'req-safe')
    const container = render(error)

    expect(container.textContent).toContain('服务暂不可用')
    expect(container.textContent).toContain('request_id: req-safe')
    expect(container.textContent).toContain('详细信息已隐藏')
    expect(container.textContent).not.toContain('synthetic-secret')
    expect(container.textContent).not.toContain('synthetic-share-secret')
    expect(container.textContent).not.toContain('proxy-secret')
    expect(container.textContent).not.toContain('<html>')
    expect(container.querySelector('html, script, a')).toBeNull()
    expect(error.message).toBe(message)
  })

  it('replaces the old diagnostics when the caller supplies a different failure', () => {
    const container = render(new VaultApiError(503, 'Unavailable', 'old message', 'old-request'))
    act(() => mounted?.root.render(
      <PageError error={new VaultApiError(404, 'NotFound', 'new message')} retry={vi.fn()} />,
    ))

    expect(container.textContent).toContain('资源不存在或不可用')
    expect(container.textContent).toContain('new message')
    expect(container.textContent).not.toContain('old message')
    expect(container.textContent).not.toContain('old-request')
    expect(container.textContent).not.toContain('request_id:')
  })
})
