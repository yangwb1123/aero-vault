import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it } from 'vitest'
import { objectRoute, parseObjectRoute, routeFromHash, useHashRoute } from './router'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let mounted: { container: HTMLDivElement; root: Root } | undefined

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
  window.history.replaceState(null, '', '#/')
})

describe('object routes', () => {
  it('round-trips nested active and deleted keys', () => {
    expect(parseObjectRoute(objectRoute('docs/a b.txt'))).toEqual({ key: 'docs/a b.txt', deleted: false })
    expect(parseObjectRoute(objectRoute('trash/a.txt', true))).toEqual({ key: 'trash/a.txt', deleted: true })
  })

  it('decodes the single encoded hash payload', () => {
    window.history.replaceState(null, '', '#/object%2Fdocs%2Fa%20b.txt')
    expect(routeFromHash('files')).toBe('object/docs/a b.txt')
  })

  it('keeps the legacy hash form and the empty-hash fallback', () => {
    window.history.replaceState(null, '', '#activity')
    expect(routeFromHash('overview')).toBe('activity')
    window.history.replaceState(null, '', '/')
    expect(window.location.hash).toBe('')
    expect(routeFromHash('overview')).toBe('overview')
  })
})

describe('malformed hash resilience', () => {
  it('never throws for malformed percent escapes', () => {
    const malformed = ['#/object/100%', '#/%', '#/%E0%A4%A']
    for (const hash of malformed) {
      window.history.replaceState(null, '', hash)
      let route = ''
      expect(() => {
        route = routeFromHash('overview')
      }).not.toThrow()
      expect(route).toBe('overview')
    }
  })

  it('keeps the fallback route for an empty or bare hash', () => {
    for (const hash of ['#/', '#', '/']) {
      window.history.replaceState(null, '', hash)
      expect(routeFromHash('overview')).toBe('overview')
    }
  })

  it('round-trips a key containing a literal percent sign', () => {
    for (const key of ['reports/100% done.txt', 'a%zz.txt', 'docs/a b.txt']) {
      window.history.replaceState(null, '', `#/${encodeURIComponent(objectRoute(key))}`)
      const route = routeFromHash('overview')
      expect(route).toBe(objectRoute(key))
      expect(parseObjectRoute(route)).toEqual({ key, deleted: false })
    }
  })
})

function renderRouteProbe(fallback: string): { current(): string; navigate(route: string): void } {
  const probe = { route: '', navigate: (route: string): void => {} }
  function Probe(): null {
    const [route, navigate] = useHashRoute(fallback)
    probe.route = route
    probe.navigate = navigate
    return null
  }
  const container = document.createElement('div')
  document.body.append(container)
  const root: Root = createRoot(container)
  mounted = { container, root }
  act(() => root.render(React.createElement(Probe)))
  return {
    current: () => probe.route,
    navigate: (route: string) => act(() => probe.navigate(route)),
  }
}

// jsdom dispatches `hashchange` in a queued task; the timer only bounds the
// wait so a missing event fails the assertion instead of hanging the run.
async function nextHashChange(): Promise<void> {
  await act(async () => {
    await new Promise<void>((resolve) => {
      window.addEventListener('hashchange', () => resolve(), { once: true })
      window.setTimeout(() => resolve(), 50)
    })
  })
}

describe('useHashRoute malformed hash recovery', () => {
  it('recovers to the fallback route when the hash is hand-edited after mount', async () => {
    window.history.replaceState(null, '', '#/files')
    const probe = renderRouteProbe('overview')
    expect(probe.current()).toBe('files')

    // A hand-edited or chat-pasted link carrying a literal `%` must not throw
    // inside the hashchange listener; the router falls back instead.
    act(() => {
      window.location.hash = '#/object/100%'
    })
    await nextHashChange()
    expect(probe.current()).toBe('overview')
  })

  it('navigates to a percent-literal key and reads it back unchanged', async () => {
    window.history.replaceState(null, '', '#/files')
    const probe = renderRouteProbe('overview')
    const route = objectRoute('reports/100% done.txt')

    probe.navigate(route)
    await nextHashChange()
    expect(window.location.hash).toBe(`#/${encodeURIComponent(route)}`)
    expect(probe.current()).toBe(route)
    expect(parseObjectRoute(probe.current())).toEqual({ key: 'reports/100% done.txt', deleted: false })
  })
})
