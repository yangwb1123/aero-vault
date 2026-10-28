import * as React from 'react'
import { IrisAlert, IrisButton, IrisCard, IrisInput } from '@iris-ui-kit/react'
import { VaultApiError } from '../../api/vault'
import type { VaultClient } from '../../api/vault'
import { downloadBlob, filenameFromDisposition, readDownloadBlob } from '../../download'
import type { DownloadProgress } from '../../download'

export function BackupPanel({ client, bucket }: { client: VaultClient; bucket: string }): React.ReactElement {
  const [prefix, setPrefix] = React.useState('')
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState<string>()
  const [progress, setProgress] = React.useState<DownloadProgress>()
  const requestRef = React.useRef(0)
  const busyRef = React.useRef(false)

  React.useLayoutEffect(() => {
    requestRef.current += 1
    busyRef.current = false
    setBusy(false)
    setError(undefined)
    setProgress(undefined)
  }, [bucket, client, prefix])
  React.useEffect(() => () => { requestRef.current += 1; busyRef.current = false }, [])

  const download = async () => {
    // A second submit inside the same tick must not start a second export of
    // the same bucket; the disabled button only covers the DOM click path.
    if (busyRef.current) return
    const requestID = ++requestRef.current
    const requestClient = client
    const requestBucket = bucket
    const requestPrefix = prefix.trim()
    const isCurrent = () => requestRef.current === requestID
    busyRef.current = true
    setBusy(true)
    setError(undefined)
    setProgress(undefined)
    try {
      const response = await requestClient.exportArchive(requestBucket, requestPrefix)
      if (!isCurrent()) return
      const filename = filenameFromDisposition(response, generatedArchiveName())
      const archive = await readDownloadBlob(response, (update) => {
        if (isCurrent()) setProgress(update)
      })
      if (!isCurrent()) return
      await downloadBlob(archive, filename)
    } catch (reason) {
      if (isCurrent()) setError(describeFailure(reason, '备份导出失败'))
    } finally {
      busyRef.current = false
      if (isCurrent()) setBusy(false)
    }
  }

  return <ArchiveForm prefix={prefix} setPrefix={setPrefix} busy={busy} error={error} progress={progress} download={download} />
}

function ArchiveForm({ prefix, setPrefix, busy, error, progress, download }: {
  prefix: string
  setPrefix(prefix: string): void
  busy: boolean
  error?: string
  progress?: DownloadProgress
  download(): Promise<void>
}): React.ReactElement {
  return (
    <div className="access-stack">
      <IrisCard variant="outline" header="导出授权对象归档">
        <p className="muted">仅导出当前用户有权访问的对象，压缩包包含 manifest.json、元数据、标签和对象内容。这不是灾难恢复备份，不包含完整治理状态，也不保证数据库与 Blob 的一致恢复点。下载交给浏览器不等于确认文件落盘。</p>
        <form className="backup-form" onSubmit={(event) => { event.preventDefault(); void download() }}>
          <label className="access-field"><span>Key 前缀（空为全部）</span><IrisInput value={prefix} placeholder="team/project/" disabled={busy} onChange={(event) => setPrefix(event.target.value)} /></label>
          <IrisButton type="submit" loading={busy} disabled={busy}>下载 tar.gz</IrisButton>
        </form>
      </IrisCard>
      {progress ? <p className="muted" role="status">{formatProgress(progress)}</p> : null}
      {error ? <IrisAlert tone="danger">{error}</IrisAlert> : null}
    </div>
  )
}

function generatedArchiveName(): string {
  return `aero-backup-${new Date().toISOString().slice(0, 10)}.tar.gz`
}

function formatProgress(progress: DownloadProgress): string {
  const total = progress.total
  if (total === undefined) return `已接收 ${formatBytes(progress.loaded)}`
  return `${formatBytes(progress.loaded)} / ${formatBytes(total)}`
}

function formatBytes(bytes: number): string {
  if (bytes >= 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(1)} KiB`
  return `${bytes} B`
}

// The backend answers every failure envelope with a request_id; surfacing it
// turns "it failed" into something support can look up.
function describeFailure(reason: unknown, fallback: string): string {
  if (reason instanceof VaultApiError && reason.requestId) return `${reason.message}（request_id: ${reason.requestId}）`
  return reason instanceof Error ? reason.message : fallback
}
