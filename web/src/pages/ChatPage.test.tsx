import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ChatResponse, SearchMode, VaultClient } from '../api/vault'
import { ChatPage } from './ChatPage'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

function createClient(streamChat: (
  query: string,
  mode: SearchMode,
  onToken: (token: string) => void,
  signal?: AbortSignal,
) => Promise<ChatResponse>): VaultClient {
  return { streamChat } as unknown as VaultClient
}

function queryInput(container: HTMLElement): HTMLTextAreaElement {
  const found = container.querySelector<HTMLTextAreaElement>('textarea[placeholder="询问知识库中的内容"]')
  if (!found) throw new Error('chat query input not found')
  return found
}

function button(container: HTMLElement, label: string): HTMLButtonElement {
  const found = [...container.querySelectorAll('button')]
    .find((candidate) => candidate.textContent?.trim() === label)
  if (!(found instanceof HTMLButtonElement)) throw new Error(`button not found: ${label}`)
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

describe('ChatPage request ordering', () => {
  it('ignores stale tokens and completion after the prompt changes', async () => {
    const pending: Array<ReturnType<typeof deferred<ChatResponse>>> = []
    const tokenCallbacks: Array<(token: string) => void> = []
    const signals: AbortSignal[] = []
    const streamChat = vi.fn((_query: string, _mode: SearchMode, onToken: (token: string) => void, signal?: AbortSignal) => {
      const request = deferred<ChatResponse>()
      pending.push(request)
      tokenCallbacks.push(onToken)
      if (signal) signals.push(signal)
      return request.promise
    })
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    mounted = { container, root }

    act(() => root.render(<ChatPage client={createClient(streamChat)} />))
    await changeInput(queryInput(container), 'older question')
    await click(button(container, '发送'))
    await changeInput(queryInput(container), 'newer question')
    expect(signals[0].aborted).toBe(true)
    await click(button(container, '发送'))
    act(() => {
      tokenCallbacks[0]('stale token')
      tokenCallbacks[1]('fresh token')
    })

    await act(async () => {
      pending[1].resolve({ answer: 'fresh answer', model: 'fresh-model', citations: [] })
      await pending[1].promise
    })
    await act(async () => {
      pending[0].resolve({ answer: 'stale answer', model: 'stale-model', citations: [] })
      await pending[0].promise
    })

    expect(container.textContent).toContain('fresh token')
    expect(container.textContent).not.toContain('stale token')
    expect(container.textContent).toContain('fresh-model')
    expect(container.textContent).not.toContain('stale-model')
  })

  it('aborts and clears an active stream when the client changes', async () => {
    const request = deferred<ChatResponse>()
    let onToken: (token: string) => void = () => {}
    let signal: AbortSignal | undefined
    const streamChat = vi.fn((_query: string, _mode: SearchMode, callback: (token: string) => void, active?: AbortSignal) => {
      onToken = callback
      signal = active
      return request.promise
    })
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    mounted = { container, root }

    act(() => root.render(<ChatPage client={createClient(streamChat)} />))
    await changeInput(queryInput(container), 'active question')
    await click(button(container, '发送'))
    act(() => onToken('partial answer'))
    expect(container.textContent).toContain('partial answer')

    const replacementClient = createClient(vi.fn(async () => ({ answer: '', model: '', citations: [] })))
    act(() => root.render(<ChatPage client={replacementClient} />))
    expect(signal?.aborted).toBe(true)
    act(() => onToken('stale token'))
    await act(async () => {
      request.resolve({ answer: 'stale completion', model: 'stale-model', citations: [] })
      await request.promise
    })

    expect(container.textContent).not.toContain('partial answer')
    expect(container.textContent).not.toContain('stale token')
    expect(container.textContent).not.toContain('stale completion')
  })
})
