import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it } from 'vitest'
import { useResource } from './useResource'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

function ResourceProbe({ load }: { load: () => Promise<string> }): React.ReactElement {
  const resource = useResource(load)
  return React.createElement('div', null,
    React.createElement('span', { 'data-loading': '' }, String(resource.loading)),
    React.createElement('span', { 'data-value': '' }, resource.data ?? ''),
    React.createElement('button', { onClick: resource.reload }, 'reload'),
  )
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve: (value: T) => void = () => {}
  const promise = new Promise<T>((accept) => { resolve = accept })
  return { promise, resolve }
}

function text(container: HTMLElement, selector: string): string {
  return container.querySelector(selector)?.textContent ?? ''
}

let mounted: { container: HTMLDivElement; root: Root } | undefined

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
})

describe('useResource scope isolation', () => {
  it('hides prior-scope data and ignores its pending reload', async () => {
    const initial = deferred<string>()
    const staleReload = deferred<string>()
    const current = deferred<string>()
    let calls = 0
    const oldLoad = () => {
      calls += 1
      return calls === 1 ? initial.promise : staleReload.promise
    }
    const newLoad = () => current.promise
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    mounted = { container, root }

    await act(async () => {
      root.render(React.createElement(ResourceProbe, { load: oldLoad }))
      await Promise.resolve()
    })
    await act(async () => {
      initial.resolve('private old scope')
      await initial.promise
    })
    expect(text(container, '[data-value]')).toBe('private old scope')

    await act(async () => {
      container.querySelector('button')?.click()
      await Promise.resolve()
    })
    expect(text(container, '[data-loading]')).toBe('true')
    expect(text(container, '[data-value]')).toBe('')

    act(() => root.render(React.createElement(ResourceProbe, { load: newLoad })))
    expect(text(container, '[data-loading]')).toBe('true')
    expect(text(container, '[data-value]')).toBe('')
    await act(async () => {
      staleReload.resolve('stale completion')
      await staleReload.promise
    })
    expect(text(container, '[data-value]')).toBe('')

    await act(async () => {
      current.resolve('current scope')
      await current.promise
    })
    expect(text(container, '[data-value]')).toBe('current scope')
  })
})
