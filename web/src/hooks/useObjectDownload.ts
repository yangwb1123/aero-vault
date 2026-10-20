import * as React from 'react'
import type { VaultClient } from '../api/vault'
import { downloadBlob } from '../download'

export interface ObjectDownloadTarget { readonly key: string; readonly versionID?: string }
interface DownloadNotice { tone: 'success' | 'danger' | 'warning'; text: string }
interface DownloadState {
  client: VaultClient
  scopeKey: string
  pending?: ObjectDownloadTarget
  handoff: boolean
  stopping: boolean
  error?: string
  notice?: DownloadNotice
}
interface DownloadScope {
  client: VaultClient
  scopeKey: string
  id: number
  active: boolean
  request?: { controller: AbortController; handoff: boolean }
}
type SetState = React.Dispatch<React.SetStateAction<DownloadState>>

function initialState(client: VaultClient, scopeKey: string): DownloadState {
  return { client, scopeKey, handoff: false, stopping: false }
}

export function useObjectDownload(client: VaultClient, scopeKey: string) {
  const [state, setState] = React.useState(() => initialState(client, scopeKey))
  const scope = React.useRef<DownloadScope>({ client, scopeKey, id: 0, active: true })
  React.useLayoutEffect(() => {
    scope.current = { client, scopeKey, id: scope.current.id + 1, active: true }
    setState(initialState(client, scopeKey))
    return () => {
      scope.current.id += 1
      scope.current.active = false
      scope.current.request?.controller.abort()
      scope.current.request = undefined
    }
  }, [client, scopeKey])
  const current = state.client === client && state.scopeKey === scopeKey ? state : initialState(client, scopeKey)
  const cancel = () => {
    const owner = scope.current
    if (!ownsDownloadScope(owner, client, scopeKey) || !owner.request || owner.request.handoff || owner.request.controller.signal.aborted) return
    const id = owner.id
    setState((previous) => scope.current.active && scope.current.id === id ? { ...previous, stopping: true } : previous)
    owner.request.controller.abort()
  }
  return { pending: current.pending, error: current.error, notice: current.notice, stopping: current.stopping,
    canCancel: Boolean(current.pending) && !current.handoff && !current.stopping,
    cancel, download: createObjectDownload(client, scopeKey, scope, setState) }
}

function createObjectDownload(client: VaultClient, scopeKey: string, scope: React.MutableRefObject<DownloadScope>, setState: SetState) {
  return async (target: ObjectDownloadTarget) => {
    if (!ownsDownloadScope(scope.current, client, scopeKey) || scope.current.request) return
    const captured = { ...target }
    const id = ++scope.current.id
    const request = { controller: new AbortController(), handoff: false }
    scope.current.request = request
    const isCurrent = () => scope.current.id === id && ownsDownloadScope(scope.current, client, scopeKey)
    const patch = (values: Partial<DownloadState>) => setState((previous) => isCurrent() ? { ...previous, ...values } : previous)
    patch({ pending: captured, handoff: false, stopping: false, error: undefined, notice: undefined })
    try {
      const blob = await readObjectDownload(client, captured, request.controller.signal)
      if (!isCurrent() || request.controller.signal.aborted) return
      request.handoff = true
      patch({ handoff: true })
      await downloadBlob(blob, objectDownloadFilename(captured))
      patch({ notice: { tone: 'success', text: '文件已交给浏览器处理；这不代表已保存到本地。' } })
    } catch (reason) {
      if (!request.controller.signal.aborted) {
        const error = objectDownloadError(captured, reason)
        patch({ error, notice: { tone: 'danger', text: error } })
      }
    } finally {
      if (isCurrent()) {
        if (request.controller.signal.aborted) patch({ notice: { tone: 'warning', text: '下载准备已取消，未交给浏览器；服务端可能已读取并记录访问。' } })
        scope.current.request = undefined
        patch({ pending: undefined, stopping: false, handoff: false })
      }
    }
  }
}

function ownsDownloadScope(scope: DownloadScope, client: VaultClient, scopeKey: string): boolean {
  return scope.active && scope.client === client && scope.scopeKey === scopeKey
}

function readObjectDownload(client: VaultClient, target: ObjectDownloadTarget, signal: AbortSignal): Promise<Blob> {
  return target.versionID === undefined ? client.download(target.key, signal) : client.downloadVersion(target.key, target.versionID, signal)
}

function objectDownloadError(target: ObjectDownloadTarget, reason: unknown): string {
  if (reason instanceof Error) return reason.message
  return target.versionID === undefined ? '文件下载失败' : '版本下载失败'
}

function objectDownloadFilename(target: ObjectDownloadTarget): string {
  const fallback = target.versionID === undefined ? 'download' : 'object'
  const name = target.key.split('/').pop() || fallback
  return target.versionID === undefined ? name : `${name}-${target.versionID.replaceAll('/', '_')}`
}
