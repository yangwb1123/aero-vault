import * as React from 'react'
import { IrisAlert, IrisButton, IrisCard, IrisInput } from '@iris-ui-kit/react'
import type { VaultClient } from '../../api/vault'

export function BackupPanel({ client }: { client: VaultClient }): React.ReactElement {
  const [prefix, setPrefix] = React.useState('')
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState<string>()

  const download = async () => {
    setBusy(true)
    setError(undefined)
    try {
      const archive = await client.exportArchive(prefix.trim())
      saveArchive(archive)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '备份导出失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="access-stack">
      <IrisCard variant="outline" header="导出可移植备份">
        <p className="muted">导出当前用户有权访问的对象，压缩包包含 manifest.json、元数据、标签和对象内容。</p>
        <form className="backup-form" onSubmit={(event) => { event.preventDefault(); void download() }}>
          <label className="access-field"><span>Key 前缀（空为全部）</span><IrisInput value={prefix} placeholder="team/project/" onChange={(event) => setPrefix(event.target.value)} /></label>
          <IrisButton type="submit" loading={busy}>下载 tar.gz</IrisButton>
        </form>
      </IrisCard>
      {error ? <IrisAlert tone="danger">{error}</IrisAlert> : null}
    </div>
  )
}

function saveArchive(blob: Blob): void {
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = `aero-backup-${new Date().toISOString().slice(0, 10)}.tar.gz`
  anchor.click()
  URL.revokeObjectURL(url)
}
