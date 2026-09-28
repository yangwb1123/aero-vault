import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { LegalHold } from '../../api/objects'
import type { VaultClient } from '../../api/vault'
import { GovernancePanel } from './GovernancePanel'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

type HoldClient = Pick<
  VaultClient,
  'getLegalHold' | 'putLegalHold' | 'removeLegalHold' | 'presignObject' | 'restoreObject'
>

function createClient(overrides: Partial<HoldClient> = {}): VaultClient {
  const defaults: HoldClient = {
    getLegalHold: async () => undefined,
    putLegalHold: async () => undefined,
    removeLegalHold: async () => undefined,
    presignObject: async () => ({ url: '', expires: '' }),
    restoreObject: async () => undefined,
  }
  return { ...defaults, ...overrides } as unknown as VaultClient
}

function mountPanel(client: VaultClient): { container: HTMLDivElement; root: Root } {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  act(() => root.render(
    <GovernancePanel client={client} objectKey="report.pdf" deleted={false} onRestored={() => undefined} />,
  ))
  return { container, root }
}

function button(container: HTMLElement, label: string): HTMLButtonElement {
  const found = [...container.querySelectorAll('button')]
    .find((candidate) => candidate.textContent?.trim() === label)
  if (!(found instanceof HTMLButtonElement)) throw new Error(`button not found: ${label}`)
  return found
}

function input(container: HTMLElement, labelText: string): HTMLInputElement {
  const label = [...container.querySelectorAll('label')]
    .find((candidate) => candidate.textContent?.includes(labelText))
  const found = label?.querySelector('input')
  if (!(found instanceof HTMLInputElement)) throw new Error(`input not found: ${labelText}`)
  return found
}

async function click(element: HTMLButtonElement): Promise<void> {
  await act(async () => {
    element.click()
    await Promise.resolve()
  })
}

async function changeInput(element: HTMLInputElement, value: string): Promise<void> {
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
    setter?.call(element, value)
    element.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve: (value: T) => void = () => {}
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

const holdA: LegalHold = {
  key: 'report.pdf',
  version_id: 'version-a',
  hold_reason: 'case-a',
  created_by: 'alice',
  created_at: '2026-09-26T10:00:00Z',
}

let mounted: { container: HTMLDivElement; root: Root } | undefined

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
  vi.restoreAllMocks()
})

describe('GovernancePanel Legal Hold target scope', () => {
  it('clears the previous version hold as soon as the selected version changes', async () => {
    const client = createClient({ getLegalHold: vi.fn(async () => holdA) })
    mounted = mountPanel(client)

    await click(button(mounted.container, '查询'))
    expect(mounted.container.textContent).toContain('case-a')
    expect(mounted.container.textContent).toContain('移除 Hold')

    await changeInput(input(mounted.container, 'Version ID'), 'version-b')

    expect(mounted.container.textContent).not.toContain('case-a')
    expect(mounted.container.textContent).not.toContain('移除 Hold')
    expect(mounted.container.textContent).not.toContain('当前目标没有 Legal Hold')
    expect(mounted.container.textContent).toContain('设置 Hold')
  })

  it('ignores a delayed read for the prior version after the new target loads', async () => {
    const oldRead = deferred<LegalHold | undefined>()
    const newRead = deferred<LegalHold | undefined>()
    const reads = [oldRead, newRead]
    const getLegalHold = vi.fn(() => reads.shift()?.promise ?? Promise.resolve(undefined))
    mounted = mountPanel(createClient({ getLegalHold }))

    await click(button(mounted.container, '查询'))
    await changeInput(input(mounted.container, 'Version ID'), 'version-b')
    await click(button(mounted.container, '查询'))

    await act(async () => {
      newRead.resolve(undefined)
      await newRead.promise
    })
    expect(mounted.container.textContent).toContain('当前目标没有 Legal Hold')

    await act(async () => {
      oldRead.resolve(holdA)
      await oldRead.promise
    })
    expect(mounted.container.textContent).toContain('当前目标没有 Legal Hold')
    expect(mounted.container.textContent).not.toContain('case-a')
    expect(mounted.container.textContent).not.toContain('移除 Hold')
  })

  it('does not refresh or report success for a write after switching versions', async () => {
    const pendingWrite = deferred<void>()
    const putLegalHold = vi.fn(() => pendingWrite.promise)
    const getLegalHold = vi.fn(async () => holdA)
    mounted = mountPanel(createClient({ putLegalHold, getLegalHold }))

    await changeInput(input(mounted.container, 'Version ID'), 'version-a')
    await changeInput(input(mounted.container, '保留原因'), 'case-a')
    await click(button(mounted.container, '设置 Hold'))
    expect(putLegalHold).toHaveBeenCalledWith('report.pdf', 'case-a', 'version-a')

    await changeInput(input(mounted.container, 'Version ID'), 'version-b')
    await act(async () => {
      pendingWrite.resolve()
      await pendingWrite.promise
    })

    expect(getLegalHold).not.toHaveBeenCalled()
    expect(mounted.container.textContent).not.toContain('Legal Hold 已设置')
    expect(mounted.container.textContent).not.toContain('case-a')
    expect(mounted.container.textContent).not.toContain('移除 Hold')
  })
})
