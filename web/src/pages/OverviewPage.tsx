import * as React from 'react'
import { IrisBadge, IrisCard } from '@iris-ui-kit/react'
import type { VaultClient, VaultSession } from '../api/vault'
import { PageError, PageHeader, PageLoading } from '../components/Page'
import { useResource } from '../hooks/useResource'

const formatBytes = (value: number): string => {
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const unit = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1)
  return `${(value / 1024 ** unit).toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`
}

export function OverviewPage({ client }: { client: VaultClient }): React.ReactElement {
  const load = React.useCallback(
    async () => {
      const [session, usage, buckets] = await Promise.all([
        client.getSession(),
        client.getUsage(),
        client.listBuckets(),
      ])
      return { session, usage, buckets }
    },
    [client],
  )
  const resource = useResource(load)
  if (resource.loading) return <PageLoading />
  if (resource.error) return <PageError error={resource.error} retry={resource.reload} />
  const { session, usage, buckets } = resource.data!

  return (
    <section>
      <PageHeader title="空间概览" description="当前租户的文件存储、配额与可见存储桶，均来自 Aero Vault 实时接口。" />
      <p className="muted">文件浏览桶：<strong>default（固定）</strong>。文件与回收站仅浏览当前租户的默认桶；此处可见桶列表不切换文件浏览范围。</p>
      <div className="metric-grid">
        <IrisCard variant="outline" header="已用空间">
          <div className="metric-value">{formatBytes(usage.used_bytes)}</div>
          <div className="metric-label">上限 {formatBytes(usage.max_bytes)}</div>
        </IrisCard>
        <IrisCard variant="outline" header="对象数量">
          <div className="metric-value">{usage.used_objects}</div>
          <div className="metric-label">上限 {usage.max_objects || '未设置'}</div>
        </IrisCard>
        <IrisCard variant="outline" header="存储桶">
          <div className="metric-value">{buckets.length}</div>
          <div className="metric-label">当前租户可见</div>
        </IrisCard>
      </div>
      <SessionCard session={session} />
    </section>
  )
}

function SessionCard({ session }: { session: VaultSession }): React.ReactElement {
  return (
    <IrisCard variant="outline" header="服务端会话">
      <div className="status-row">
        <span>认证状态</span>
        <IrisBadge tone={session.authenticated ? 'success' : 'warning'}>
          {session.authenticated ? '已认证' : '匿名会话'}
        </IrisBadge>
      </div>
      <div className="status-row"><span>主体</span><strong>{session.subject_id || 'anonymous'}</strong></div>
      <div className="status-row"><span>服务端租户</span><strong>{session.tenant_id}</strong></div>
      <div className="status-row"><span>主体类型</span><strong>{session.principal_kind}</strong></div>
      <div className="status-row"><span>Scopes</span><strong>{session.scopes.join(', ') || '—'}</strong></div>
    </IrisCard>
  )
}
