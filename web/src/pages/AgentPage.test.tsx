import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { AgentResponse } from '../api/agent'
import type { VaultClient } from '../api/vault'
import { AgentPage } from './AgentPage'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

function createClient(runAgent: (query: string, signal?: AbortSignal) => Promise<AgentResponse>): VaultClient {
  return { runAgent } as unknown as VaultClient
}

function queryInput(container: HTMLElement): HTMLTextAreaElement {
  const found = container.querySelector<HTMLTextAreaElement>('textarea[placeholder="例如：查找本周项目文档并总结关键风险"]')
  if (!found) throw new Error('Agent query input not found')
  return found
}

function button(container: HTMLElement): HTMLButtonElement {
  const found = [...container.querySelectorAll('button')]
    .find((candidate) => candidate.textContent?.trim() === '运行 Agent')
  if (!(found instanceof HTMLButtonElement)) throw new Error('Agent button not found')
  return found
}

async function changeInput(element: HTMLTextAreaElement, value: string): Promise<void> {
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')?.set
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

let mounted: { container: HTMLDivElement; root: Root } | undefined

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
})

describe('AgentPage request ordering', () => {
  it('ignores a cancelled result that resolves after the replacement run', async () => {
    const pending: Array<{ promise: Promise<AgentResponse>; resolve(value: AgentResponse): void }> = []
    const signals: AbortSignal[] = []
    const runAgent = vi.fn((_query: string, signal?: AbortSignal) => {
      const request = deferred<AgentResponse>()
      pending.push(request)
      if (signal) signals.push(signal)
      return request.promise
    })
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    mounted = { container, root }

    act(() => root.render(<AgentPage client={createClient(runAgent)} />))
    await changeInput(queryInput(container), 'older query')
    await click(button(container))
    await changeInput(queryInput(container), 'newer query')
    expect(signals[0].aborted).toBe(true)
    await click(button(container))

    await act(async () => {
      pending[1].resolve({ answer: 'newer result', model: 'newer-model', steps: [] })
      await pending[1].promise
    })
    await act(async () => {
      pending[0].resolve({ answer: 'older result', model: 'older-model', steps: [] })
      await pending[0].promise
    })

    expect(container.textContent).toContain('newer result')
    expect(container.textContent).not.toContain('older result')
    expect(container.textContent).toContain('newer-model')
    expect(container.textContent).not.toContain('older-model')
  })

  it('ignores a pending run when the client changes', async () => {
    const request = deferred<AgentResponse>()
    let signal: AbortSignal | undefined
    const runAgent = vi.fn((_query: string, active?: AbortSignal) => {
      signal = active
      return request.promise
    })
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    mounted = { container, root }

    act(() => root.render(<AgentPage client={createClient(runAgent)} />))
    await changeInput(queryInput(container), 'active query')
    await click(button(container))
    const replacementClient = createClient(vi.fn(async () => ({ answer: '', model: '', steps: [] })))
    act(() => root.render(<AgentPage client={replacementClient} />))
    expect(signal?.aborted).toBe(true)

    await act(async () => {
      request.resolve({ answer: 'stale result', model: 'stale-model', steps: [] })
      await request.promise
    })
    expect(container.textContent).not.toContain('stale result')
    expect(container.textContent).not.toContain('stale-model')
  })
})
