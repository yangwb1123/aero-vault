import { afterEach, describe, expect, it } from 'vitest'
import { objectRoute, parseObjectRoute, routeFromHash } from './router'

afterEach(() => window.history.replaceState(null, '', '#/'))

describe('object routes', () => {
  it('round-trips nested active and deleted keys', () => {
    expect(parseObjectRoute(objectRoute('docs/a b.txt'))).toEqual({ key: 'docs/a b.txt', deleted: false })
    expect(parseObjectRoute(objectRoute('trash/a.txt', true))).toEqual({ key: 'trash/a.txt', deleted: true })
  })

  it('decodes the single encoded hash payload', () => {
    window.history.replaceState(null, '', '#/object%2Fdocs%2Fa%20b.txt')
    expect(routeFromHash('files')).toBe('object/docs/a b.txt')
  })
})
