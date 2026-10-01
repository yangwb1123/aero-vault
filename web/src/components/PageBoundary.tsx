import * as React from 'react'
import { IrisAlert, IrisButton } from '@iris-ui-kit/react'

interface PageBoundaryProps {
  children?: React.ReactNode
  resetKey?: string
}

interface PageBoundaryState {
  error: Error | null
}

/**
 * PageBoundary keeps a render-time crash inside the current page: the layout
 * chrome stays mounted and the user gets a retry instead of a blank SPA.
 * `resetKey` (the active route) clears a caught error, so navigating away from
 * a crashed page recovers without a reload.
 */
export class PageBoundary extends React.Component<PageBoundaryProps, PageBoundaryState> {
  state: PageBoundaryState = { error: null }

  static getDerivedStateFromError(error: Error): PageBoundaryState {
    return { error }
  }

  componentDidCatch(error: Error, info: React.ErrorInfo): void {
    console.error('[web] page render failed', error.message, info.componentStack)
  }

  componentDidUpdate(previous: PageBoundaryProps): void {
    if (previous.resetKey !== this.props.resetKey && this.state.error) {
      this.setState({ error: null })
    }
  }

  private readonly reset = (): void => this.setState({ error: null })

  render(): React.ReactNode {
    const { error } = this.state
    if (!error) return this.props.children
    return (
      <IrisAlert tone="danger" title="页面渲染失败">
        <div className="alert-actions">
          <span>{error.message}</span>
          <IrisButton size="sm" variant="outline" onClick={this.reset}>
            重试
          </IrisButton>
        </div>
      </IrisAlert>
    )
  }
}