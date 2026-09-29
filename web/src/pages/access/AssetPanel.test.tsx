import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { PublicAsset } from '../../api/access'
import type { VaultClient } from '../../api/vault'
import { AssetPanel } from './AssetPanel'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

function createClient(publishAsset: (bucket: string, key: string, slug: string) => Promise<unknown>): VaultClient {
  return {
    listAssets: async () => [] as PublicAsset[],
    publishAsset,
  } as unknown as VaultClient
}

function input(container: HTMLElement, placeholder: string): HTMLInputElement {
  const found = [...container.querySelectorAll('input')]
    .find((candidate) => candidate.placeholder === placeholder)
  if (!found) throw new Error(`input not found: ${placeholder}`)
  return found
}

function button(container: HTMLElement): HTMLButtonElement {
  const found = [...container.querySelectorAll('button')]
    .find((candidate) => candidate.textContent?.trim() === '发布图片')
  if (!(found instanceof HTMLButtonElement)) throw new Error('publish button not found')
  return found
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

let mounted: { container: HTMLDivElement; root: Root } | undefined

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
})

describe('AssetPanel operation scope', () => {
  it('clears the form and ignores a publish completion after client change', async () => {
    const pending = deferred<unknown>()
    const oldPublish = vi.fn(() => pending.promise)
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    mounted = { container, root }

    act(() => root.render(<AssetPanel client={createClient(oldPublish)} bucket="project" />))
    await act(async () => { await new Promise((resolve) => window.setTimeout(resolve, 0)) })
    await changeInput(input(container, 'images/hero.jpg'), 'images/private.jpg')
    await changeInput(input(container, 'blog/hero.jpg'), 'blog/private.jpg')
    await act(async () => {
      button(container).click()
      await Promise.resolve()
    })
    expect(oldPublish).toHaveBeenCalledWith('project', 'images/private.jpg', 'blog/private.jpg')

    const nextClient = createClient(vi.fn(async () => undefined))
    await act(async () => {
      root.render(<AssetPanel client={nextClient} bucket="project" />)
      await new Promise((resolve) => window.setTimeout(resolve, 0))
    })
    expect(input(container, 'images/hero.jpg').value).toBe('')

    await act(async () => {
      pending.resolve(undefined)
      await pending.promise
    })
    expect(container.textContent).not.toContain('公开资源配置已更新')
  })
})
