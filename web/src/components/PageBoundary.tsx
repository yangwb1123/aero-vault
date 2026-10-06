import * as React from 'react'
import { IrisAlert, IrisButton } from '@iris-ui-kit/react'

interface PageBoundaryProps {
  children?: React.ReactNode
  resetKey?: string
}

interface PageBoundaryState {
  failed: boolean
}

/** Keep page render errors inside the layout; route changes allow recovery. */
export class PageBoundary extends React.Component<PageBoundaryProps, PageBoundaryState> {
  state: PageBoundaryState = { failed: false }

  static getDerivedStateFromError(): PageBoundaryState {
    return { failed: true }
  }

  componentDidCatch(error: unknown, info: React.ErrorInfo): void {
    console.error('[web] page render failed', error instanceof Error ? error.message : String(error), info.componentStack)
  }

  componentDidUpdate(previous: PageBoundaryProps): void {
    if (previous.resetKey !== this.props.resetKey && this.state.failed) {
      this.setState({ failed: false })
    }
  }

  private readonly reset = (): void => this.setState({ failed: false })

  render(): React.ReactNode {
    if (!this.state.failed) return this.props.children
    return (
      <IrisAlert tone="danger" title="页面渲染失败">
        <div className="alert-actions">
          <span>页面发生异常，请重试或从菜单打开其他页面。</span>
          <IrisButton size="sm" variant="outline" onClick={this.reset}>
            重试
          </IrisButton>
        </div>
      </IrisAlert>
    )
  }
}
