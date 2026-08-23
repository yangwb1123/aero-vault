import * as React from 'react'
import { IrisAlert, IrisBadge, IrisButton, IrisCard } from '@iris-ui-kit/react'
import type { VaultClient } from '../../api/vault'
import { PageError, PageLoading } from '../../components/Page'
import { downloadBlob } from '../../download'
import { useResource } from '../../hooks/useResource'

export function VersionsPanel({ client, objectKey }: { client: VaultClient; objectKey: string }): React.ReactElement {
  const [busy, setBusy] = React.useState('')
  const [error, setError] = React.useState<string>()
  const load = React.useCallback(() => client.listVersions(objectKey), [client, objectKey])
  const resource = useResource(load)
  const download = async (versionID: string) => {
    setBusy(versionID)
    setError(undefined)
    try {
      const safeVersion = versionID.replaceAll('/', '_')
      downloadBlob(await client.downloadVersion(objectKey, versionID), `${objectKey.split('/').pop() || 'object'}-${safeVersion}`)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '版本下载失败')
    } finally { setBusy('') }
  }

  if (resource.loading) return <PageLoading />
  if (resource.error) return <PageError error={resource.error} retry={resource.reload} />
  return (
    <div className="access-stack">
      {error ? <IrisAlert tone="danger">{error}</IrisAlert> : null}
      <IrisCard variant="outline" header="版本历史">
        <div className="table-scroll"><table className="vault-table">
          <thead><tr><th>Version ID</th><th>大小</th><th>更新时间</th><th>状态</th><th>操作</th></tr></thead>
          <tbody>{resource.data?.map((version) => (
            <tr key={version.version_id}><td><strong>{version.version_id}</strong><div className="muted">{version.etag}</div></td>
              <td>{version.size.toLocaleString('zh-CN')} B</td><td>{new Date(version.updated_at).toLocaleString('zh-CN')}</td>
              <td>{version.deleted_at ? <IrisBadge tone="warning">删除标记</IrisBadge> : version.locked_until ? <IrisBadge>已锁定</IrisBadge> : '可用'}</td>
              <td><IrisButton size="sm" variant="ghost" loading={busy === version.version_id} onClick={() => void download(version.version_id)}>下载</IrisButton></td></tr>
          ))}{resource.data?.length === 0 ? <tr><td className="empty-cell" colSpan={5}>暂无版本</td></tr> : null}</tbody>
        </table></div>
      </IrisCard>
    </div>
  )
}
