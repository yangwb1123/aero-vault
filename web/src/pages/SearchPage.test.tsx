import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { SearchHit, SearchMode, VaultClient } from '../api/vault'
import { SearchPage } from './SearchPage'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

function createClient(search: (query: string, mode: SearchMode) => Promise<SearchHit[]>): VaultClient {
  return { search } as unknown as VaultClient
}

function input(container: HTMLElement): HTMLInputElement {
  const found = container.querySelector<HTMLInputElement>('input[placeholder="输入要查找的内容"]')
  if (!found) throw new Error('search input not found')
  return found
}

function button(container: HTMLElement): HTMLButtonElement {
  const found = [...container.querySelectorAll('button')]
    .find((candidate) => candidate.textContent?.trim() === '搜索')
  if (!(found instanceof HTMLButtonElement)) throw new Error('search button not found')
  return found
}

async function changeInput(element: HTMLInputElement, value: string): Promise<void> {
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
    setter?.call(element, value)
    element.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function click(element: HTMLButtonElement): Promise<void> {
  await act(async () => {
    element.click()
    await Promise.resolve()
  })
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve: (value: T) => void = () => {}
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

function hit(objectKey: string, objectID: number): SearchHit {
  return {
    score: 0.9,
    chunk: `content-${objectKey}`,
    chunk_id: objectID,
    object_id: objectID,
    bucket: 'default',
    object_key: objectKey,
    seq: 0,
    embed_model: 'test-model',
  }
}

let mounted: { container: HTMLDivElement; root: Root } | undefined

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
})

describe('SearchPage request ordering', () => {
  it('ignores older results after a newer search is submitted', async () => {
    const oldSearch = deferred<SearchHit[]>()
    const currentSearch = deferred<SearchHit[]>()
    const responses = [oldSearch, currentSearch]
    const search = vi.fn((_query: string, _mode: SearchMode) => responses.shift()?.promise ?? Promise.resolve([]))
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    mounted = { container, root }

    act(() => root.render(<SearchPage client={createClient(search)} />))
    await changeInput(input(container), 'older query')
    await click(button(container))
    await changeInput(input(container), 'newer query')
    await click(button(container))

    expect(search).toHaveBeenNthCalledWith(1, 'older query', 'hybrid')
    expect(search).toHaveBeenNthCalledWith(2, 'newer query', 'hybrid')
    await act(async () => {
      currentSearch.resolve([hit('newer-result.txt', 2)])
      await currentSearch.promise
    })
    expect(container.textContent).toContain('newer-result.txt')

    await act(async () => {
      oldSearch.resolve([hit('older-result.txt', 1)])
      await oldSearch.promise
    })
    expect(container.textContent).toContain('newer-result.txt')
    expect(container.textContent).not.toContain('older-result.txt')
  })
})
