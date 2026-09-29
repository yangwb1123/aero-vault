import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { LineageResponse, VaultClient } from '../api/vault'
import { LineagePage } from './LineagePage'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

function createClient(getLineage: (objectID: number) => Promise<LineageResponse>): VaultClient {
  return { getLineage } as unknown as VaultClient
}

function objectIDInput(container: HTMLElement): HTMLInputElement {
  const found = container.querySelector<HTMLInputElement>('input[role="spinbutton"]')
  if (!found) throw new Error('Object ID input not found')
  return found
}

function button(container: HTMLElement): HTMLButtonElement {
  const found = [...container.querySelectorAll('button')]
    .find((candidate) => candidate.textContent?.trim() === '读取血缘')
  if (!(found instanceof HTMLButtonElement)) throw new Error('lineage button not found')
  return found
}

async function setObjectID(element: HTMLInputElement, value: string): Promise<void> {
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

function response(objectID: number, query: string): LineageResponse {
  return {
    object_id: objectID,
    entries: [{
      usage_id: objectID,
      caller: 'chat',
      query,
      chunk_ids: [],
      object_ids: [objectID],
      created_at: '2026-09-26T10:00:00Z',
    }],
  }
}

let mounted: { container: HTMLDivElement; root: Root } | undefined

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
})

describe('LineagePage request ordering', () => {
  it('does not show an older object result after a newer ID is requested', async () => {
    const oldRequest = deferred<LineageResponse>()
    const currentRequest = deferred<LineageResponse>()
    const pending = [oldRequest, currentRequest]
    const getLineage = vi.fn((_objectID: number) => pending.shift()?.promise ?? Promise.resolve(response(3, 'later')))
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    mounted = { container, root }

    act(() => root.render(<LineagePage client={createClient(getLineage)} />))
    await setObjectID(objectIDInput(container), '1')
    await click(button(container))
    await setObjectID(objectIDInput(container), '2')
    await click(button(container))
    expect(getLineage).toHaveBeenNthCalledWith(1, 1)
    expect(getLineage).toHaveBeenNthCalledWith(2, 2)

    await act(async () => {
      currentRequest.resolve(response(2, 'current query'))
      await currentRequest.promise
    })
    await act(async () => {
      oldRequest.resolve(response(1, 'old query'))
      await oldRequest.promise
    })

    expect(container.textContent).toContain('current query')
    expect(container.textContent).not.toContain('old query')
  })
})
