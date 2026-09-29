import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { VaultEvent } from '../api/events'
import { ActivityTable } from './ActivityPage'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

const events: VaultEvent[] = [
  {
    id: 1,
    tenant: 'tenant-a',
    bucket: 'default',
    key: 'docs/default.txt',
    type: 'created',
    created_at: '2026-09-26T10:00:00Z',
  },
  {
    id: 2,
    tenant: 'tenant-a',
    bucket: 'archive',
    key: 'docs/archive.txt',
    type: 'deleted',
    created_at: '2026-09-26T10:01:00Z',
  },
]

let mounted: { container: HTMLDivElement; root: Root } | undefined

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
})

describe('ActivityTable object navigation', () => {
  it('only opens objects from the bucket the detail page can address', () => {
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    const open = vi.fn((_key: string, _deleted: boolean) => undefined)
    mounted = { container, root }

    act(() => root.render(<ActivityTable events={events} open={open} />))
    const buttons = [...container.querySelectorAll('button')]

    expect(buttons).toHaveLength(2)
    expect(buttons[0].disabled).toBe(false)
    expect(buttons[1].disabled).toBe(true)
    expect(buttons[1].textContent).toContain('跨桶暂不可查')
    act(() => {
      buttons[1].click()
      buttons[0].click()
    })
    expect(open).toHaveBeenCalledTimes(1)
    expect(open).toHaveBeenCalledWith('docs/default.txt', false)
  })
})
