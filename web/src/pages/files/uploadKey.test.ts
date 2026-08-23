import { describe, expect, it } from 'vitest'
import { uploadKey } from './uploadKey'

describe('uploadKey', () => {
  it('joins an optional prefix without changing the filename', () => {
    expect(uploadKey('', 'a.txt')).toBe('a.txt')
    expect(uploadKey('docs', 'a.txt')).toBe('docs/a.txt')
    expect(uploadKey('docs/', 'a b.txt')).toBe('docs/a b.txt')
  })
})
