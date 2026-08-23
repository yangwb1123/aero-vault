import { describe, expect, it } from 'vitest'
import type { VaultEvent } from '../api/events'
import { addVaultEvent } from './useVaultEvents'

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
})
