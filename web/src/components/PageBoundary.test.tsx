import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PageBoundary } from './PageBoundary'

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

// React 18 re-renders a throwing child in dev before the boundary gives up, so
// the crash is gated by a mutable flag instead of an attempt counter.
let shouldThrow = true

function Boom(): React.ReactElement {
  if (shouldThrow) throw new Error('boom')
  return <div>页面已恢复</div>
}

function RouteProbe({ route }: { route: string }): React.ReactElement {
  if (route === 'files') throw new Error('files page crashed')
  return <div>{`页面 ${route}`}</div>
}

function button(container: HTMLElement, label: string): HTMLButtonElement {
  const found = [...container.querySelectorAll('button')]
    .find((candidate) => candidate.textContent?.trim() === label)
  if (!(found instanceof HTMLButtonElement)) throw new Error(`button not found: ${label}`)
  return found
}

let mounted: { container: HTMLDivElement; root: Root } | undefined
let consoleError: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  // React logs every caught render error; only the boundary's own log is asserted.
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
})

afterEach(() => {
  if (mounted) act(() => mounted?.root.unmount())
  mounted?.container.remove()
  mounted = undefined
  consoleError.mockRestore()
  window.history.replaceState(null, '', '#/')
  shouldThrow = true
})

function renderBoundary(element: React.ReactElement): HTMLElement {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  mounted = { container, root }
  act(() => root.render(element))
  return container
}

describe('PageBoundary render recovery', () => {
  it('renders a recoverable fallback when a child throws during render', () => {
    const container = renderBoundary(<PageBoundary><Boom /></PageBoundary>)

    expect(container.textContent).toContain('页面渲染失败')
    expect(container.textContent).toContain('boom')
    expect(container.textContent).not.toContain('页面已恢复')

    shouldThrow = false
    act(() => button(container, '重试').click())

    expect(container.textContent).toContain('页面已恢复')
    expect(container.textContent).not.toContain('boom')
  })

  it('clears a caught error when resetKey changes', () => {
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    mounted = { container, root }

    act(() => root.render(
      <PageBoundary resetKey="files"><RouteProbe route="files" /></PageBoundary>,
    ))
    expect(container.textContent).toContain('files page crashed')

    act(() => root.render(
      <PageBoundary resetKey="activity"><RouteProbe route="activity" /></PageBoundary>,
    ))
    expect(container.textContent).toContain('页面 activity')
    expect(container.textContent).not.toContain('files page crashed')
  })

  it('logs the render failure once with its message and component stack', () => {
    renderBoundary(<PageBoundary><Boom /></PageBoundary>)

    const logs = consoleError.mock.calls.filter((call) => call[0] === '[web] page render failed')
    expect(logs).toHaveLength(1)
    expect(logs[0][1]).toBe('boom')
    expect(String(logs[0][2])).toContain('Boom')
  })
})