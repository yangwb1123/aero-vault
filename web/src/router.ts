import * as React from 'react'

const prefix = '#/'
const objectPrefix = 'object/'
const deletedObjectPrefix = 'deleted-object/'

export interface ObjectRoute {
  key: string
  deleted: boolean
}

export function objectRoute(key: string, deleted = false): string {
  return `${deleted ? deletedObjectPrefix : objectPrefix}${key}`
}

export function parseObjectRoute(route: string): ObjectRoute | undefined {
  if (route.startsWith(deletedObjectPrefix)) {
    const key = route.slice(deletedObjectPrefix.length)
    return key ? { key, deleted: true } : undefined
  }
  if (route.startsWith(objectPrefix)) {
    const key = route.slice(objectPrefix.length)
    return key ? { key, deleted: false } : undefined
  }
  return undefined
}

// decodeURIComponent throws URIError on malformed percent escapes (e.g. a
// hand-edited `#/object/100%` or a key containing a literal `%`). Routing must
// stay total: an undecodable payload falls back instead of unmounting the SPA.
function decodeHashSegment(raw: string): string | undefined {
  try {
    return decodeURIComponent(raw)
  } catch {
    return undefined
  }
}

export function routeFromHash(fallback: string): string {
  const hash = window.location.hash
  if (!hash || hash === '#' || hash === prefix) return fallback
  const raw = hash.startsWith(prefix) ? hash.slice(prefix.length) : hash.replace(/^#/, '')
  const decoded = decodeHashSegment(raw)
  // An object Key is opaque: trimming a route can address a different file.
  return decoded && parseObjectRoute(decoded) ? decoded : decoded?.trim() || fallback
}

export function useHashRoute(fallback: string): [string, (route: string) => void] {
  const [route, setRoute] = React.useState(() => routeFromHash(fallback))
  React.useEffect(() => {
    const sync = () => setRoute(routeFromHash(fallback))
    if (!window.location.hash) window.history.replaceState(null, '', `${prefix}${fallback}`)
    window.addEventListener('hashchange', sync)
    return () => window.removeEventListener('hashchange', sync)
  }, [fallback])
  const navigate = React.useCallback((next: string) => {
    if (routeFromHash('') !== next) window.location.hash = `${prefix}${encodeURIComponent(next)}`
  }, [])
  return [route, navigate]
}
