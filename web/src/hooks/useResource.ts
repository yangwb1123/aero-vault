import * as React from 'react'

export function useResource<T>(load: () => Promise<T>): {
  data?: T
  error?: Error
  loading: boolean
  reload(): void
} {
  const [revision, setRevision] = React.useState(0)
  const requestRef = React.useRef(0)
  const [state, setState] = React.useState<{
    load: typeof load
    revision: number
    data?: T
    error?: Error
    loading: boolean
  }>({ load, revision, loading: true })
  React.useEffect(() => {
    const requestID = ++requestRef.current
    let active = true
    setState((current) => current.load === load && current.revision === revision
      ? { ...current, error: undefined, loading: true }
      : { load, revision, loading: true })
    load().then(
      (data) => active && requestRef.current === requestID && setState({ load, revision, data, loading: false }),
      (reason: unknown) =>
        active &&
        requestRef.current === requestID &&
        setState({
          load,
          revision,
          error: reason instanceof Error ? reason : new Error('读取失败'),
          loading: false,
        }),
    )
    return () => {
      active = false
    }
  }, [load, revision])
  const matchesRequest = state.load === load && state.revision === revision
  return {
    data: matchesRequest ? state.data : undefined,
    error: matchesRequest ? state.error : undefined,
    loading: matchesRequest ? state.loading : true,
    reload: () => setRevision((value) => value + 1),
  }
}
