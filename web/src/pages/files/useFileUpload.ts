import * as React from 'react'
import type { IrisFileUploadFile } from '@iris-ui-kit/react'
import type { VaultClient } from '../../api/vault'
import { createUploadIdempotencyKeyFactory } from './uploadKey'
import { runUploadQueue, uploadResultMessage } from './uploadQueue'

interface UploadState {
  client: VaultClient
  initialPrefix: string
  prefix: string
  files: IrisFileUploadFile[]
  busy: boolean
  stopping: boolean
  completed: number
  total: number
  message?: ReturnType<typeof uploadResultMessage>
}
interface UploadScope {
  client: VaultClient
  initialPrefix: string
  id: number
  active: boolean
  controller?: AbortController
}
type SetState = React.Dispatch<React.SetStateAction<UploadState>>

function initialState(client: VaultClient, initialPrefix: string): UploadState {
  return { client, initialPrefix, prefix: initialPrefix, files: [], busy: false, stopping: false, completed: 0, total: 0 }
}

export function useFileUpload(client: VaultClient, initialPrefix: string, onUploaded: () => void) {
  const [state, setState] = React.useState(() => initialState(client, initialPrefix))
  const scope = React.useRef<UploadScope>({ client, initialPrefix, id: 0, active: true })
  const keyFor = React.useMemo(() => createUploadIdempotencyKeyFactory(), [client])
  React.useLayoutEffect(() => {
    if (scope.current.client !== client || scope.current.initialPrefix !== initialPrefix) {
      scope.current = { client, initialPrefix, id: scope.current.id + 1, active: true }
      setState(initialState(client, initialPrefix))
    }
    scope.current.active = true
    return () => {
      scope.current.id += 1
      scope.current.active = false
      scope.current.controller?.abort()
      scope.current.controller = undefined
    }
  }, [client, initialPrefix])
  const current = state.client === client && state.initialPrefix === initialPrefix ? state : initialState(client, initialPrefix)
  const matches = () => scope.current.active && scope.current.client === client && scope.current.initialPrefix === initialPrefix
  const change = (values: Partial<Pick<UploadState, 'prefix' | 'files' | 'message'>>) => {
    if (matches() && !scope.current.controller) setState((previous) => matches() ? { ...previous, ...values } : previous)
  }
  const cancel = () => {
    if (!matches() || !scope.current.controller || scope.current.controller.signal.aborted) return
    setState((previous) => matches() ? { ...previous, stopping: true } : previous)
    scope.current.controller.abort()
  }
  return { ...current, change, cancel, upload: createUploadAction(current, scope, setState, keyFor, onUploaded) }
}

function createUploadAction(state: UploadState, scope: React.MutableRefObject<UploadScope>, setState: SetState,
  keyFor: (file: File, key: string) => string, onUploaded: () => void) {
  return async () => {
    if (!state.files.length || scope.current.controller || !scope.current.active ||
      scope.current.client !== state.client || scope.current.initialPrefix !== state.initialPrefix) return
    const files = [...state.files]
    const id = ++scope.current.id
    const controller = new AbortController()
    scope.current.controller = controller
    const isCurrent = () => scope.current.active && scope.current.id === id && scope.current.client === state.client &&
      scope.current.initialPrefix === state.initialPrefix
    const patch = (values: Partial<UploadState>) => setState((previous) => isCurrent() ? { ...previous, ...values } : previous)
    patch({ busy: true, stopping: false, message: undefined, completed: 0, total: files.length })
    try {
      const result = await runUploadQueue({ client: state.client, files, prefix: state.prefix, signal: controller.signal,
        keyFor, isCurrent, progress: (completed) => patch({ completed }) })
      if (!isCurrent()) return
      const message = uploadResultMessage(result)
      patch({ files: files.slice(result.completed), message })
      if (result.completed > 0 || result.cancelled) {
        try { onUploaded() } catch {
          patch({ message: { tone: 'danger', text: `${message.text} 文件列表刷新失败，请手动核对。` } })
        }
      }
    } finally {
      if (isCurrent()) {
        scope.current.controller = undefined
        patch({ busy: false, stopping: false })
      }
    }
  }
}
