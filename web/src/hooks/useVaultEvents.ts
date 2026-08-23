import * as React from 'react'
import { consumeEventStream, type VaultEvent } from '../api/events'
import type { VaultClient } from '../api/vault'

export type EventStreamStatus = 'connecting' | 'connected' | 'retrying' | 'paused'

export function addVaultEvent(current: VaultEvent[], next: VaultEvent, limit = 200): VaultEvent[] {
  if (current.some((event) => event.id === next.id)) return current
  return [next, ...current].slice(0, limit)
}

export function useVaultEvents(client: VaultClient, enabled: boolean): {
  events: VaultEvent[]
  status: EventStreamStatus
  error?: string
  lastEventID: number
  clear(): void
  reconnect(): void
} {
  const [events, setEvents] = React.useState<VaultEvent[]>([])
  const [status, setStatus] = React.useState<EventStreamStatus>(enabled ? 'connecting' : 'paused')
  const [error, setError] = React.useState<string>()
  const [lastEventID, setLastEventID] = React.useState(0)
  const [revision, setRevision] = React.useState(0)
  const lastID = React.useRef(0)

  React.useEffect(() => {
    if (!enabled) {
      setStatus('paused')
      setError(undefined)
      return
    }
    const controller = new AbortController()
    let stopped = false
    let retryTimer: number | undefined
    let retryDelay = 1000

    const schedule = (message: string) => {
      if (stopped) return
      setStatus('retrying')
      setError(message)
      retryTimer = window.setTimeout(() => void connect(), retryDelay)
      retryDelay = Math.min(retryDelay * 2, 15_000)
    }
    const accept = (event: VaultEvent) => {
      lastID.current = Math.max(lastID.current, event.id)
      setLastEventID(lastID.current)
      setEvents((current) => addVaultEvent(current, event))
    }
    async function connect() {
      setStatus('connecting')
      try {
        const response = await client.openEventStream(lastID.current, controller.signal)
        if (stopped) return
        retryDelay = 1000
        setStatus('connected')
        setError(undefined)
        await consumeEventStream(response, accept)
        schedule('事件连接已结束，正在重连。')
      } catch (reason) {
        if (stopped || controller.signal.aborted) return
        schedule(reason instanceof Error ? reason.message : '事件连接失败')
      }
    }
    void connect()
    return () => {
      stopped = true
      controller.abort()
      if (retryTimer !== undefined) window.clearTimeout(retryTimer)
    }
  }, [client, enabled, revision])

  return {
    events,
    status,
    error,
    lastEventID,
    clear: () => setEvents([]),
    reconnect: () => setRevision((value) => value + 1),
  }
}
