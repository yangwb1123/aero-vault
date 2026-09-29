import { describe, expect, it } from 'vitest'
import { createUploadIdempotencyKeyFactory, uploadKey } from './uploadKey'

describe('uploadKey', () => {
  it('joins an optional prefix without changing the filename', () => {
    expect(uploadKey('', 'a.txt')).toBe('a.txt')
    expect(uploadKey('docs', 'a.txt')).toBe('docs/a.txt')
    expect(uploadKey('docs/', 'a b.txt')).toBe('docs/a b.txt')
  })

  it('reuses retry keys only for the same file and target', () => {
    let next = 0
    const keyFor = createUploadIdempotencyKeyFactory(() => `retry-${++next}`)
    const file = new File(['one'], 'same.txt')
    expect(keyFor(file, 'docs/same.txt')).toBe('retry-1')
    expect(keyFor(file, 'docs/same.txt')).toBe('retry-1')
    expect(keyFor(file, 'other/same.txt')).toBe('retry-2')
    expect(keyFor(new File(['one'], 'same.txt'), 'docs/same.txt')).toBe('retry-3')
  })
})
