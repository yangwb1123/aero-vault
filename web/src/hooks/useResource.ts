import * as React from 'react'

export function useResource<T>(load: () => Promise<T>): {
  data?: T
  error?: Error
  loading: boolean
  reload(): void
} {
  const [revision, setRevision] = React.useState(0)
  const [state, setState] = React.useState<{ data?: T; error?: Error; loading: boolean }>({
    loading: true,
  })
  React.useEffect(() => {
    let active = true
    setState((value) => ({ ...value, error: undefined, loading: true }))
    load().then(
      (data) => active && setState({ data, loading: false }),
      (reason: unknown) =>
        active &&
        setState({
          error: reason instanceof Error ? reason : new Error('读取失败'),
          loading: false,
        }),
    )
    return () => {
      active = false
    }
  }, [load, revision])
  return { ...state, reload: () => setRevision((value) => value + 1) }
}
