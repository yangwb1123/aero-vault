import * as React from 'react'
import { fileLocationRoute, parseFileLocationRoute, type FileLocation } from './pages/files/fileLocation'

const prefix = '#/'
const objectPrefix = 'object/'
const deletedObjectPrefix = 'deleted-object/'
const contextualObjectPrefix = 'file-object/v1/'
const maxContextRouteLength = 65536

export interface ObjectRoute {
  key: string
  deleted: boolean
  returnTo?: FileLocation
}

function isContextObjectKey(key: unknown): key is string {
  return typeof key === 'string' && key.length > 0 && key.length <= 4096
}

export function objectRoute(key: string, deleted = false, returnTo?: FileLocation): string {
  // Legacy keys consume the entire suffix, including any apparent ?query/#hash.
  if (!returnTo) return `${deleted ? deletedObjectPrefix : objectPrefix}${key}`
  if (!isContextObjectKey(key)) throw new Error('对象位置超出支持范围。')
  const payload = { key, deleted, returnTo: fileLocationRoute(returnTo) }
  return `${contextualObjectPrefix}${JSON.stringify(payload)}`
}

function contextObjectFromPayload(payload: unknown): ObjectRoute | undefined {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return undefined
  const value = payload as Record<string, unknown>
  if (Object.keys(value).sort().join(',') !== 'deleted,key,returnTo') return undefined
  if (!isContextObjectKey(value.key) || typeof value.deleted !== 'boolean' || typeof value.returnTo !== 'string') return undefined
  const returnTo = parseFileLocationRoute(value.returnTo)
  return returnTo ? { key: value.key, deleted: value.deleted, returnTo } : undefined
}

function parseContextObjectRoute(route: string): ObjectRoute | undefined {
  if (!route.startsWith(contextualObjectPrefix) || route.length > maxContextRouteLength) return undefined
  try {
    return contextObjectFromPayload(JSON.parse(route.slice(contextualObjectPrefix.length)))
  } catch {
    return undefined
  }
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
  return parseContextObjectRoute(route)
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

function decodedHashRoute(decoded: string | undefined, fallback: string): string {
  // Object Keys and structured locations are opaque after this single decode.
  if (!decoded) return fallback
  if (parseObjectRoute(decoded) || parseFileLocationRoute(decoded)) return decoded
  if (decoded.startsWith('files/') || decoded.startsWith('file-object/')) return fallback
  return decoded.trim() || fallback
}

export function routeFromHash(fallback: string): string {
  const hash = window.location.hash
  if (!hash || hash === '#' || hash === prefix) return fallback
  const raw = hash.startsWith(prefix) ? hash.slice(prefix.length) : hash.replace(/^#/, '')
  return decodedHashRoute(decodeHashSegment(raw), fallback)
}

export function useHashRoute(fallback: string): [string, (route: string, options?: { replace?: boolean }) => void] {
  const [route, setRoute] = React.useState(() => routeFromHash(fallback))
  React.useEffect(() => {
    const sync = () => setRoute(routeFromHash(fallback))
    if (!window.location.hash) window.history.replaceState(null, '', `${prefix}${fallback}`)
    window.addEventListener('hashchange', sync)
    return () => window.removeEventListener('hashchange', sync)
  }, [fallback])
  const navigate = React.useCallback((next: string, options?: { replace?: boolean }) => {
    const hash = `${prefix}${encodeURIComponent(next)}`
    if (options?.replace) {
      window.history.replaceState(null, '', hash)
      // replaceState emits no hashchange; synchronize through the same parser.
      setRoute(routeFromHash(fallback))
    } else if (routeFromHash('') !== next) {
      window.location.hash = hash
    }
  }, [fallback])
  return [route, navigate]
}
