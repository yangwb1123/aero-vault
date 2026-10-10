import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { VaultClient } from '../../api/vault'
import { ObjectPage } from './ObjectPage'

vi.mock('./PreviewPanel', () => ({ PreviewPanel: () => <div>Preview</div> }))
vi.mock('./MetadataPanel', () => ({ MetadataPanel: () => null }))
vi.mock('./VersionsPanel', () => ({ VersionsPanel: () => null }))
vi.mock('./GovernancePanel', () => ({ GovernancePanel: () => null }))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
let mounted: { container: HTMLDivElement; root: Root } | undefined

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
})

async function render(deleted = false) {
  const listShares = vi.fn(async () => [])
  const client = { listShares } as unknown as VaultClient
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  mounted = { container, root }
  await act(async () => root.render(<ObjectPage client={client} objectKey="docs/100% report.txt " deleted={deleted}
    onBack={() => undefined} onRestored={() => undefined} />))
  const tab = [...container.querySelectorAll<HTMLButtonElement>('[role="tab"]')]
    .find((item) => item.textContent === '对象分享')!
  return { container, tab, listShares }
}

describe('ObjectPage object sharing tab', () => {
  it('opens the same panel bound to the full active object key', async () => {
    const { container, tab, listShares } = await render()
    expect(listShares).not.toHaveBeenCalled()
    await act(async () => tab.click())
    expect(listShares).toHaveBeenCalledWith('default', 'docs/100% report.txt ')
    expect(container.querySelector<HTMLInputElement>('input[placeholder="docs/report.pdf"]')?.readOnly).toBe(true)
  })

  it('disables sharing for deleted objects without loading their shares', async () => {
    const { container, tab, listShares } = await render(true)
    expect(tab.disabled).toBe(true)
    await act(async () => tab.click())
    expect(listShares).not.toHaveBeenCalled()
    expect(container.querySelector('input[placeholder="docs/report.pdf"]')).toBeNull()
  })
})
