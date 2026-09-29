import * as React from 'react'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it, vi } from 'vitest'
import type { VaultEvent } from '../api/events'
import type { VaultClient } from '../api/vault'
import { addVaultEvent, useVaultEvents } from './useVaultEvents'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const event = (id: number): VaultEvent => ({
  id,
  tenant: 'acme',
  bucket: 'default',
  key: `file-${id}.txt`,
  type: 'created',
  created_at: '2026-08-23T10:00:00Z',
})

describe('event buffer', () => {
  it('prepends, deduplicates, and bounds lifecycle events', () => {
    expect(addVaultEvent([event(1)], event(2), 2).map((item) => item.id)).toEqual([2, 1])
    const current = [event(2), event(1)]
    expect(addVaultEvent(current, event(2), 2)).toBe(current)
    expect(addVaultEvent(current, event(3), 2).map((item) => item.id)).toEqual([3, 2])
  })

  it('clears prior-client events and restarts replay from zero', async () => {
    const oldStream = liveEventResponse(event(10))
    const firstClient = createClient(oldStream.response)
    const nextClient = createClient(eventResponse(event(20)))
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    await act(async () => {
      root.render(React.createElement(EventProbe, { client: firstClient }))
      await tick()
    })
    expect(container.querySelector('[data-events]')?.textContent).toBe('file-10.txt')
    expect(container.querySelector('[data-last-id]')?.textContent).toBe('10')

    await act(async () => {
      root.render(React.createElement(EventProbe, { client: nextClient }))
      await tick()
    })
    expect(container.querySelector('[data-events]')?.textContent).toBe('file-20.txt')
    expect(container.querySelector('[data-last-id]')?.textContent).toBe('20')
    expect(nextClient.openEventStream).toHaveBeenCalledWith(0, expect.any(AbortSignal))

    await act(async () => {
      oldStream.push(event(30))
      oldStream.close()
      await tick()
    })
    expect(container.querySelector('[data-events]')?.textContent).toBe('file-20.txt')
    expect(container.querySelector('[data-last-id]')?.textContent).toBe('20')

    act(() => root.unmount())
    container.remove()
  })
})

function EventProbe({ client }: { client: VaultClient }): React.ReactElement {
  const stream = useVaultEvents(client, true)
  return React.createElement('div', null,
    React.createElement('span', { 'data-events': '' }, stream.events.map((item) => item.key).join(',')),
    React.createElement('span', { 'data-last-id': '' }, String(stream.lastEventID)),
  )
}

function createClient(response: Response): VaultClient {
  return { openEventStream: vi.fn(async () => response) } as unknown as VaultClient
}

function eventResponse(value: VaultEvent): Response {
  const stream = liveEventResponse(value)
  stream.close()
  return stream.response
}

function liveEventResponse(value: VaultEvent): {
  response: Response
  push(next: VaultEvent): void
  close(): void
} {
  let controller: ReadableStreamDefaultController<Uint8Array> | undefined
  const response = new Response(new ReadableStream<Uint8Array>({
    start(streamController) {
      controller = streamController
      streamController.enqueue(encodeEvent(value))
    },
  }))
  return {
    response,
    push(next) {
      if (!controller) throw new Error('event stream is not open')
      controller.enqueue(encodeEvent(next))
    },
    close() {
      controller?.close()
    },
  }
}

function encodeEvent(value: VaultEvent): Uint8Array {
  return new TextEncoder().encode(`data: ${JSON.stringify(value)}\n\n`)
}

function tick(): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, 0))
}
