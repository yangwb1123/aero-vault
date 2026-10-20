import * as React from 'react'
import { IrisAlert, IrisBadge, IrisButton, IrisCard } from '@iris-ui-kit/react'
import type { ObjectVersion } from '../../api/objects'
import type { VaultClient } from '../../api/vault'
import { PageError, PageLoading } from '../../components/Page'
import { useResource } from '../../hooks/useResource'
import { PreviewPanel } from './PreviewPanel'
import { useVersionDownload } from './useVersionDownload'

export function VersionsPanel({ client, objectKey }: { client: VaultClient; objectKey: string }): React.ReactElement {
  const [selected, setSelected] = React.useState<{ client: VaultClient; objectKey: string; versionID: string }>()
  const load = React.useCallback(() => client.listVersions(objectKey), [client, objectKey])
  const resource = useResource(load)
  const download = useVersionDownload(client, objectKey)
  React.useLayoutEffect(() => setSelected(undefined), [client, objectKey])
  const versionID = selected?.client === client && selected.objectKey === objectKey &&
    resource.data?.some((version) => version.version_id === selected.versionID && !version.is_delete_marker) ? selected.versionID : undefined
  const reload = () => { if (!download.busy) { setSelected(undefined); resource.reload() } }

  if (resource.loading) return <PageLoading />
  if (resource.error) return <PageError error={resource.error} retry={reload} />
  return (
    <div className="access-stack">
      {download.error ? <IrisAlert tone="danger">{download.error}</IrisAlert> : null}
      <IrisCard variant="outline" header={<div className="preview-heading"><strong>版本历史</strong><IrisButton size="sm" variant="outline" disabled={Boolean(download.busy)} onClick={reload}>刷新版本</IrisButton></div>}>
        <p className="muted">预览和下载固定到所选 Version ID；不会回退到最新对象，也不会恢复或覆盖当前文件。</p>
        <VersionTable versions={resource.data ?? []} busy={download.busy} preview={(id) => setSelected({ client, objectKey, versionID: id })} download={(id) => void download.download(id)} />
      </IrisCard>
      {versionID !== undefined ? <div className="access-stack">
        <div className="access-actions"><IrisButton size="sm" variant="outline" onClick={() => setSelected(undefined)}>关闭版本预览</IrisButton></div>
        <PreviewPanel key={versionID} client={client} objectKey={objectKey} versionID={versionID} />
      </div> : null}
    </div>
  )
}

function VersionTable({ versions, busy, preview, download }: {
  versions: ObjectVersion[]; busy: string; preview(id: string): void; download(id: string): void
}): React.ReactElement {
  return <div className="table-scroll"><table className="vault-table">
    <thead><tr><th>Version ID</th><th>大小</th><th>更新时间</th><th>状态</th><th>操作</th></tr></thead>
    <tbody>{versions.map((version) => {
      const unavailable = Boolean(version.is_delete_marker) || !version.version_id
      return <tr key={version.version_id}><td><strong>{version.version_id}</strong><div className="muted">{version.etag}</div></td>
        <td>{version.size.toLocaleString('zh-CN')} B</td><td>{new Date(version.updated_at).toLocaleString('zh-CN')}</td>
        <td>{version.is_delete_marker ? <IrisBadge tone="warning">删除标记</IrisBadge> : version.deleted_at ? <IrisBadge tone="warning">已软删除版本</IrisBadge> : version.locked_until ? <IrisBadge>已锁定</IrisBadge> : '可用'}</td>
        <td><IrisButton size="sm" variant="ghost" disabled={unavailable} onClick={() => preview(version.version_id)}>预览</IrisButton>
          <IrisButton size="sm" variant="ghost" disabled={unavailable || Boolean(busy)} loading={busy === version.version_id} onClick={() => download(version.version_id)}>下载</IrisButton></td></tr>
    })}{versions.length === 0 ? <tr><td className="empty-cell" colSpan={5}>暂无版本</td></tr> : null}</tbody>
  </table></div>
}
