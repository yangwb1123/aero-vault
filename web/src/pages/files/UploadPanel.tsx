import * as React from 'react'
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisFileUpload,
  IrisInput,
  type IrisFileUploadFile,
} from '@iris-ui-kit/react'
import type { VaultClient } from '../../api/vault'
import { createUploadIdempotencyKeyFactory, uploadKey } from './uploadKey'

export function UploadPanel({
  client,
  initialPrefix,
  onUploaded,
}: {
  client: VaultClient
  initialPrefix: string
  onUploaded(): void
}): React.ReactElement {
  const [prefix, setPrefix] = React.useState(initialPrefix)
  const [files, setFiles] = React.useState<IrisFileUploadFile[]>([])
  const [busy, setBusy] = React.useState(false)
  const [message, setMessage] = React.useState<{ tone: 'success' | 'danger'; text: string }>()
  const [idempotencyKeyFor] = React.useState(() => createUploadIdempotencyKeyFactory())
  const scopeRef = React.useRef({ client, initialPrefix })
  const requestRef = React.useRef(0)
  const controllerRef = React.useRef<AbortController>()
  const busyRef = React.useRef(false)

  React.useLayoutEffect(() => {
    if (scopeRef.current.client !== client || scopeRef.current.initialPrefix !== initialPrefix) {
      scopeRef.current = { client, initialPrefix }
      setPrefix(initialPrefix)
      setFiles([])
      setBusy(false)
      setMessage(undefined)
    }
    return () => {
      requestRef.current += 1
      controllerRef.current?.abort()
      controllerRef.current = undefined
      busyRef.current = false
    }
  }, [client, initialPrefix])

  const upload = async () => {
    if (files.length === 0 || busyRef.current) return
    const queued = [...files]
    const targetPrefix = prefix
    const targetClient = client
    const targetInitialPrefix = initialPrefix
    const requestID = ++requestRef.current
    const controller = new AbortController()
    controllerRef.current = controller
    const isCurrent = () => requestRef.current === requestID &&
      scopeRef.current.client === targetClient && scopeRef.current.initialPrefix === targetInitialPrefix
    busyRef.current = true
    setBusy(true)
    setMessage(undefined)
    let completed = 0
    try {
      // intentional sequential uploads: retries reuse each file's idempotency key
      for (const entry of queued) {
        if (!isCurrent()) return
        const key = uploadKey(targetPrefix, entry.name)
        await targetClient.upload(key, entry.file, idempotencyKeyFor(entry.file, key), controller.signal)
        completed++
      }
      if (!isCurrent()) return
      setFiles([])
      setMessage({ tone: 'success', text: `已上传 ${completed} 个文件。` })
      onUploaded()
    } catch (reason) {
      if (!isCurrent()) return
      setFiles(queued.slice(completed))
      setMessage({
        tone: 'danger',
        text: `已完成 ${completed}/${queued.length}：${reason instanceof Error ? reason.message : '上传失败'}`,
      })
      if (completed > 0) onUploaded()
    } finally {
      if (isCurrent()) {
        controllerRef.current = undefined
        busyRef.current = false
        setBusy(false)
      }
    }
  }

  return (
    <IrisCard variant="outline" header="拖拽或选择文件上传">
      <div className="upload-panel">
        <label className="access-field"><span>目标 Key 前缀（可选）</span><IrisInput value={prefix} placeholder="team/project/" disabled={busy} onChange={(event) => setPrefix(event.target.value)} /></label>
        <IrisFileUpload
          multiple
          maxFiles={20}
          value={files}
          disabled={busy}
          label="点击选择或将文件拖到这里（最多 20 个）"
          onValueChange={setFiles}
          onReject={() => setMessage({ tone: 'danger', text: '部分文件超过数量限制或不符合上传条件。' })}
        />
        {message ? <IrisAlert tone={message.tone}>{message.text}</IrisAlert> : null}
        <div className="access-actions"><IrisButton disabled={files.length === 0} loading={busy} onClick={() => void upload()}>上传 {files.length || ''} 个文件</IrisButton></div>
      </div>
    </IrisCard>
  )
}
