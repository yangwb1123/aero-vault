import { describe, expect, it } from 'vitest'
import { defaultFileLocation, fileLocationRoute, parseFileLocationRoute, type FileLocation } from './fileLocation'

const prefix = 'files/v1/'
const encodePayload = (payload: unknown): string => `${prefix}${JSON.stringify(payload)}`

describe('bounded file-location codec', () => {
  it('retains the root files route and round-trips literal nested directory scopes', () => {
    expect(fileLocationRoute(defaultFileLocation)).toBe('files')
    expect(parseFileLocationRoute('files')).toEqual({ path: '', view: 'all', deleted: false })
    for (const path of ['', 'team/docs/', '团队/资料/', '100%/%2F/+/?/#/', ' team /  /']) {
      for (const view of ['all', 'mine'] as const) {
        for (const deleted of [false, true]) {
          const location = { path, view, deleted }
          expect(parseFileLocationRoute(fileLocationRoute(location))).toEqual(location)
        }
      }
    }
  })

  it('serializes only the location whitelist, not credentials, bucket switches or ephemeral state', () => {
    const value = { path: 'docs/', view: 'mine' as const, deleted: true, token: 'secret-token',
      password: 'share-secret', selected: ['private.txt'], response: { objects: [] }, bucket: 'other' }
    const route = fileLocationRoute(value)
    expect(JSON.parse(route.slice(prefix.length))).toEqual({ path: 'docs/', view: 'mine', deleted: true })
    for (const secret of ['secret-token', 'share-secret', 'private.txt', 'objects', 'other']) {
      expect(route).not.toContain(secret)
    }
  })

  it.each([
    null, [], {}, { path: '', view: 'all' }, { path: 1, view: 'all', deleted: false },
    { path: 'directory-without-slash', view: 'all', deleted: false },
    { path: '', view: 'owned', deleted: false }, { path: '', view: 'all', deleted: 'false' },
    { path: '', view: 'all', deleted: false, bucket: 'other' },
    { path: '', view: 'all', deleted: false, returnTo: 'https://example.test' },
    { path: 'a'.repeat(4096) + '/', view: 'all', deleted: false },
  ])('rejects malformed or non-whitelisted state %#', (payload) => {
    expect(parseFileLocationRoute(encodePayload(payload))).toBeUndefined()
  })

  it('bounds parsing before JSON work and rejects unsupported, truncated and malformed payloads', () => {
    for (const route of ['files/', 'files/v2/{}', `${prefix}{`, `${prefix}%7B%7D`, `${prefix}${' '.repeat(32769)}`]) {
      expect(parseFileLocationRoute(route)).toBeUndefined()
    }
    expect(parseFileLocationRoute('object/docs/a')).toBeUndefined()
    expect(() => fileLocationRoute({ path: 'a'.repeat(4096) + '/', view: 'all', deleted: false })).toThrow()
    expect(() => fileLocationRoute({ path: '', view: 'bad', deleted: false } as unknown as FileLocation)).toThrow()
  })

})

describe('literal file-location limits', () => {
  it('supports the full location bound without trimming or decoding percent sequences', () => {
    const location = { path: `${' '.repeat(4095)}/`, view: 'mine' as const, deleted: true }
    expect(parseFileLocationRoute(fileLocationRoute(location))).toEqual(location)
    const escaped = { path: '%2F/%25/+/?/#/目录/', view: 'all' as const, deleted: false }
    expect(parseFileLocationRoute(fileLocationRoute(escaped))).toEqual(escaped)
  })
})
