import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { downloadBlob } from '../../download'
import type { VaultClient } from '../../api/vault'
import { BackupPanel } from './BackupPanel'

vi.mock('../../download', () => ({ downloadBlob: vi.fn() }))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

function createClient(exportArchive: (bucket: string, prefix: string) => Promise<Blob>): VaultClient {
  return { exportArchive } as unknown as VaultClient
}

function button(container: HTMLElement): HTMLButtonElement {
  const found = [...container.querySelectorAll('button')]
    .find((candidate) => candidate.textContent?.trim() === '下载 tar.gz')
  if (!(found instanceof HTMLButtonElement)) throw new Error('backup button not found')
  return found
}

async function click(element: HTMLButtonElement): Promise<void> {
  await act(async () => {
    element.click()
    await Promise.resolve()
  })
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve: (value: T) => void = () => {}
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

let mounted: { container: HTMLDivElement; root: Root } | undefined

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
  vi.clearAllMocks()
})

describe('BackupPanel request scope', () => {
  it('does not download an archive after the authenticated client changes', async () => {
    const pendingArchive = deferred<Blob>()
    const oldClient = createClient(() => pendingArchive.promise)
    const newClient = createClient(async () => new Blob(['new tenant']))
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    mounted = { container, root }

    act(() => root.render(<BackupPanel client={oldClient} bucket="bucket-a" />))
    await click(button(container))
    act(() => root.render(<BackupPanel client={newClient} bucket="bucket-a" />))
    await act(async () => {
      pendingArchive.resolve(new Blob(['old tenant']))
      await pendingArchive.promise
    })

    expect(vi.mocked(downloadBlob)).not.toHaveBeenCalled()
  })
})
