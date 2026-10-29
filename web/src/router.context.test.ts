import { afterEach, describe, expect, it } from 'vitest'
import { fileLocationRoute, parseFileLocationRoute } from './pages/files/fileLocation'
import { objectRoute, parseObjectRoute, routeFromHash } from './router'

const location = { path: ' team /目录/%2F+?# /', view: 'mine' as const, deleted: true }
const contextPrefix = 'file-object/v1/'
const payloadRoute = (payload: unknown): string => `${contextPrefix}${JSON.stringify(payload)}`
function roundTripHash(route: string): string {
  window.history.replaceState(null, '', `#/${encodeURIComponent(route)}`)
  return routeFromHash('overview')
}
afterEach(() => { window.history.replaceState(null, '', '#/') })

describe('contextual object routes', () => {
  it('carries an internal file location separately from an opaque exact object Key', () => {
    for (const key of ['nested/a.txt', '目录/资料.txt', '100%/%2F/%252F', 'a+b?c#d', ' docs/ report.txt ', '  ']) {
      for (const deleted of [false, true]) {
        const route = objectRoute(key, deleted, location)
        expect(route.startsWith('object/')).toBe(false)
        expect(route.startsWith('deleted-object/')).toBe(false)
        expect(parseObjectRoute(roundTripHash(route))).toEqual({ key, deleted, returnTo: location })
      }
    }
  })

  it('preserves legacy links byte-for-byte in meaning, including apparent query/return syntax', () => {
    for (const key of ['目录/a%2Fb+?#.txt ', '  ', 'a?returnTo=files#mine', 'file-object/v1/{}']) {
      for (const deleted of [false, true]) {
        const route = objectRoute(key, deleted)
        expect(route).toBe(`${deleted ? 'deleted-object/' : 'object/'}${key}`)
        expect(parseObjectRoute(roundTripHash(route))).toEqual({ key, deleted })
      }
    }
    expect(parseObjectRoute('object/')).toBeUndefined()
    expect(parseObjectRoute('deleted-object/')).toBeUndefined()
  })

  it('decodes only the outer hash, not the Key, path or nested return route', () => {
    const key = ' %2F/%25/+/?/#/资料 '
    const route = objectRoute(key, false, location)
    expect(roundTripHash(route)).toBe(route)
    expect(parseObjectRoute(route)).toEqual({ key, deleted: false, returnTo: location })
    expect(parseFileLocationRoute(roundTripHash(fileLocationRoute(location)))).toEqual(location)
    const serialized = JSON.parse(route.slice(contextPrefix.length))
    expect(Object.keys(serialized).sort()).toEqual(['deleted', 'key', 'returnTo'])
    expect(serialized.returnTo).toBe(fileLocationRoute(location))
  })

  it('drops non-location state when creating the return route', () => {
    const state = { ...location, bucket: 'other', token: 'private-token', password: 'private-password', selected: ['one'] }
    const route = objectRoute('one', true, state)
    expect(parseObjectRoute(route)).toEqual({ key: 'one', deleted: true, returnTo: location })
    for (const secret of ['other', 'private-token', 'private-password', 'selected']) expect(route).not.toContain(secret)
  })
})

describe('malformed contextual route payloads', () => {
  it.each([
    null, [], {}, { key: 'a', deleted: false },
    { key: '', deleted: false, returnTo: 'files' }, { key: 1, deleted: false, returnTo: 'files' },
    { key: 'a', deleted: 'false', returnTo: 'files' },
    { key: 'a', deleted: false, returnTo: 'https://example.test/' },
    { key: 'a', deleted: false, returnTo: '//example.test/' },
    { key: 'a', deleted: false, returnTo: 'object/a' },
    { key: 'a', deleted: false, returnTo: 'files/v1/{}' },
    { key: 'a', deleted: false, returnTo: 'files', bucket: 'other' },
    { key: 'a'.repeat(4097), deleted: false, returnTo: 'files' },
  ])('rejects invalid schema and non-file return destinations %#', (payload) => {
    const route = payloadRoute(payload)
    expect(parseObjectRoute(route)).toBeUndefined()
    expect(roundTripHash(route)).toBe('overview')
  })

  it('rejects oversized, truncated, double-encoded and unsupported payloads without throwing', () => {
    for (const route of [`${contextPrefix}{`, `${contextPrefix}${' '.repeat(65537)}`,
      'file-object/v2/{}', 'files/v1/{', 'files/v2/{}', `${contextPrefix}%7B%7D`]) {
      expect(() => roundTripHash(route)).not.toThrow()
      expect(roundTripHash(route)).toBe('overview')
    }
    expect(() => objectRoute('a'.repeat(4097), false, location)).toThrow()
    expect(() => objectRoute('', false, location)).toThrow()
  })
})
