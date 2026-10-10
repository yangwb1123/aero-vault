import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { CreatedShare, Share } from '../../api/access'
import type { VaultClient } from '../../api/vault'
import { SharePanel } from './SharePanel'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

type ShareClient = Pick<VaultClient, 'listShares' | 'createShare' | 'revokeShare'>
const target = ' docs/100% report.txt '
const makeShare = (id = 'share-a'): Share => ({
  id, bucket: 'bucket-a', key: target, allow_preview: true, allow_download: true,
  use_count: 0, created_by: 'alice', created_at: '2026-10-01T00:00:00Z',
})
const created = (): CreatedShare => ({ share: makeShare(), token: 'secret', url: 'https://vault.example/share/secret' })

function createClient(overrides: Partial<ShareClient> = {}): VaultClient {
  return {
    listShares: vi.fn(async () => []), createShare: vi.fn(async () => created()),
    revokeShare: vi.fn(async () => undefined), ...overrides,
  } as unknown as VaultClient
}

let mounted: { container: HTMLDivElement; root: Root } | undefined

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
  vi.restoreAllMocks()
})

async function mountPanel(client: VaultClient, objectKey = target, strict = false): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  mounted = { container, root }
  await act(async () => root.render(strict
    ? <React.StrictMode><SharePanel client={client} bucket="bucket-a" objectKey={objectKey} /></React.StrictMode>
    : <SharePanel client={client} bucket="bucket-a" objectKey={objectKey} />))
  return container
}

function button(container: HTMLElement, label: string): HTMLButtonElement {
  const found = [...container.querySelectorAll('button')].find((element) => element.textContent?.trim() === label)
  if (!(found instanceof HTMLButtonElement)) throw new Error(`button not found: ${label}`)
  return found
}

async function click(element: HTMLElement): Promise<void> {
  await act(async () => element.click())
}

async function changeInput(element: HTMLInputElement, value: string): Promise<void> {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(element, value)
    element.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve: (value: T) => void = () => {}
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

describe('SharePanel bound object target', () => {
  it('loads exact keys, passes settings and clears the copy URL after revocation', async () => {
    let active = false
    const listShares = vi.fn(async () => active ? [makeShare()] : [])
    const createShare = vi.fn(async () => { active = true; return created() })
    const revokeShare = vi.fn(async () => { active = false })
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const client = createClient({ listShares, createShare, revokeShare })
    const container = await mountPanel(client)
    const key = container.querySelector<HTMLInputElement>('input[placeholder="docs/report.pdf"]')!
    expect(key.value).toBe(target)
    expect(key.readOnly).toBe(true)
    expect(listShares).toHaveBeenLastCalledWith('bucket-a', target)
    await changeInput(container.querySelector('input[placeholder="外部评审"]')!, ' Review ')
    await changeInput(container.querySelector('input[type="password"]')!, 'pw')
    await click(button(container, '创建分享'))
    expect(createShare).toHaveBeenCalledWith({
      bucket: 'bucket-a', key: target, name: 'Review', password: 'pw',
      allow_preview: true, allow_download: true, ttl_seconds: 3600, max_uses: undefined,
    })
    expect(container.querySelector('a')?.href).toBe(created().url)
    expect(container.textContent).toContain('复制')
    await click(button(container, '撤销'))
    expect(revokeShare).toHaveBeenCalledWith('share-a')
    expect(container.querySelector('a')).toBeNull()
    expect(container.textContent).toContain('没有有效分享链接')
  })

  it('rejects a setting with neither preview nor download', async () => {
    const createShare = vi.fn(async () => created())
    const container = await mountPanel(createClient({ createShare }))
    const checkboxes = container.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')
    expect(checkboxes).toHaveLength(2)
    for (const checkbox of checkboxes) await click(checkbox)
    await click(button(container, '创建分享'))
    expect(createShare).not.toHaveBeenCalled()
    expect(container.textContent).toContain('至少允许预览或下载之一')
  })

  it('displays authorization errors rather than pretending the object has no shares', async () => {
    const client = createClient({ listShares: async () => { throw new Error('AccessDenied') } })
    const container = await mountPanel(client)
    expect(container.textContent).toContain('AccessDenied')
    expect(container.textContent).not.toContain('没有有效分享链接')
  })

  it('does not refresh or show a delayed create after the bound key changes', async () => {
    const pending = deferred<CreatedShare>()
    const listShares = vi.fn(async () => [])
    const client = createClient({ createShare: () => pending.promise, listShares })
    const container = await mountPanel(client)
    await click(button(container, '创建分享'))
    await act(async () => mounted?.root.render(<SharePanel client={client} bucket="bucket-a" objectKey="docs/b.txt" />))
    await act(async () => { pending.resolve(created()); await pending.promise })
    expect(listShares.mock.calls).toEqual([['bucket-a', target], ['bucket-a', 'docs/b.txt']])
    expect(container.querySelector('a')).toBeNull()
  })

  it('drops the old list and clears the password when the client changes', async () => {
    const pending = deferred<Share[]>()
    const oldClient = createClient({ listShares: () => pending.promise })
    const container = await mountPanel(oldClient)
    await changeInput(container.querySelector('input[type="password"]')!, 'old-secret')
    const newClient = createClient({ listShares: async () => [makeShare('new-share')] })
    await act(async () => mounted?.root.render(<SharePanel client={newClient} bucket="bucket-a" objectKey={target} />))
    await act(async () => { pending.resolve([makeShare('old-share')]); await pending.promise })
    expect(container.textContent).toContain('new-share')
    expect(container.textContent).not.toContain('old-share')
    expect(container.querySelector<HTMLInputElement>('input[type="password"]')?.value).toBe('')
  })

  it('does not start the post-create list after unmount', async () => {
    const pending = deferred<CreatedShare>()
    const listShares = vi.fn(async () => [])
    const client = createClient({ createShare: () => pending.promise, listShares })
    const container = await mountPanel(client)
    await click(button(container, '创建分享'))
    act(() => mounted?.root.unmount())
    mounted = undefined
    await act(async () => { pending.resolve(created()); await pending.promise })
    expect(listShares).toHaveBeenCalledTimes(1)
    container.remove()
  })

  it('does not start an old-target refresh after a delayed revoke', async () => {
    const pending = deferred<void>()
    const listShares = vi.fn(async () => [makeShare()])
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const client = createClient({ listShares, revokeShare: () => pending.promise })
    const container = await mountPanel(client)
    await click(button(container, '撤销'))
    await act(async () => mounted?.root.render(<SharePanel client={client} bucket="bucket-a" objectKey="docs/b.txt" />))
    await act(async () => { pending.resolve(undefined); await pending.promise })
    expect(listShares.mock.calls).toEqual([['bucket-a', target], ['bucket-a', 'docs/b.txt']])
    expect(button(container, '刷新列表').disabled).toBe(false)
  })

  it('finishes the automatic list under StrictMode effect replay', async () => {
    const container = await mountPanel(createClient({ listShares: async () => [makeShare('strict-share')] }), target, true)
    expect(container.textContent).toContain('strict-share')
    expect(button(container, '刷新列表').disabled).toBe(false)
  })
})
