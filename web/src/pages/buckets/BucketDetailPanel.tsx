import * as React from 'react'
import { IrisAlert, IrisBadge, IrisButton, IrisCard, IrisNumberInput, IrisSwitch } from '@iris-ui-kit/react'
import type { BucketConfig } from '../../api/buckets'
import type { VaultClient } from '../../api/vault'
import { PageError, PageLoading } from '../../components/Page'
import { useResource } from '../../hooks/useResource'

export function BucketDetailPanel({ client, bucket, onDeleted }: { client: VaultClient; bucket: string; onDeleted(): void }): React.ReactElement {
  const load = React.useCallback(async () => {
    const [config, stats] = await Promise.all([client.getBucketConfig(bucket), client.getBucketStats(bucket)])
    return { config, stats }
  }, [bucket, client])
  const resource = useResource(load)
  if (resource.loading) return <PageLoading />
  if (resource.error) return <PageError error={resource.error} retry={resource.reload} />
  const { config, stats } = resource.data!
  return (
    <div className="bucket-detail">
      <div className="bucket-heading"><div><h2>{bucket}</h2><span className="muted">当前租户的存储策略</span></div><IrisBadge tone={config.versioning ? 'success' : 'neutral'}>{config.versioning ? '版本控制已启用' : '版本控制关闭'}</IrisBadge></div>
      <div className="metric-grid bucket-metrics">
        <IrisCard variant="outline" header="活动对象"><div className="metric-value">{stats.object_count}</div></IrisCard>
        <IrisCard variant="outline" header="占用空间"><div className="metric-value">{formatBytes(stats.total_size_bytes)}</div></IrisCard>
      </div>
      <BucketSettings client={client} bucket={bucket} config={config} onSaved={resource.reload} />
      <DangerZone client={client} bucket={bucket} onDeleted={onDeleted} />
    </div>
  )
}

function BucketSettings({ client, bucket, config, onSaved }: { client: VaultClient; bucket: string; config: BucketConfig; onSaved(): void }): React.ReactElement {
  const [versioning, setVersioning] = React.useState(config.versioning)
  const [lockSeconds, setLockSeconds] = React.useState<number | null>(config.object_lock_seconds)
  const [days, setDays] = React.useState<number | null>(config.expire_after_days ?? 0)
  const [action, setAction] = React.useState<'soft_delete' | 'hard_delete'>(config.expire_action === 'hard_delete' ? 'hard_delete' : 'soft_delete')
  const [busy, setBusy] = React.useState('')
  const [message, setMessage] = React.useState<{ tone: 'success' | 'danger'; text: string }>()

  const run = async (label: string, operation: () => Promise<void>) => {
    setBusy(label)
    setMessage(undefined)
    try {
      await operation()
      setMessage({ tone: 'success', text: '存储桶配置已更新。' })
      onSaved()
    } catch (reason) {
      setMessage({ tone: 'danger', text: reason instanceof Error ? reason.message : '更新存储桶配置失败' })
    } finally {
      setBusy('')
    }
  }

  return (
    <IrisCard variant="outline" header="数据保护设置">
      {message ? <IrisAlert tone={message.tone}>{message.text}</IrisAlert> : null}
      <div className="bucket-settings">
        <form onSubmit={(event) => { event.preventDefault(); void run('versioning', () => client.setBucketVersioning(bucket, versioning)) }}>
          <label><span>版本控制</span><IrisSwitch checked={versioning} onChange={setVersioning} /></label>
          <p>保留覆盖和删除前的对象版本。</p><IrisButton type="submit" variant="outline" loading={busy === 'versioning'}>保存</IrisButton>
        </form>
        <form onSubmit={(event) => { event.preventDefault(); void run('lock', () => client.setBucketObjectLock(bucket, lockSeconds ?? 0)) }}>
          <label><span>默认对象锁（秒）</span><IrisNumberInput min={0} value={lockSeconds} onChange={setLockSeconds} /></label>
          <p>新对象在保留期内不可覆盖或硬删除；0 表示关闭。</p><IrisButton type="submit" variant="outline" loading={busy === 'lock'}>保存</IrisButton>
        </form>
        <form className="bucket-lifecycle" onSubmit={(event) => { event.preventDefault(); void run('lifecycle', () => client.setBucketLifecycle(bucket, { days: days ?? 0, action })) }}>
          <label><span>过期天数</span><IrisNumberInput min={0} value={days} onChange={setDays} /></label>
          <label><span>过期动作</span><select value={action} onChange={(event) => setAction(event.target.value as typeof action)}><option value="soft_delete">软删除</option><option value="hard_delete">硬删除</option></select></label>
          <p>0 表示关闭自动过期；法律保留和 WORM 仍优先阻止硬删除。</p><IrisButton type="submit" variant="outline" loading={busy === 'lifecycle'}>保存</IrisButton>
        </form>
      </div>
    </IrisCard>
  )
}

function DangerZone({ client, bucket, onDeleted }: { client: VaultClient; bucket: string; onDeleted(): void }): React.ReactElement {
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState<string>()
  const remove = async () => {
    if (!window.confirm(`删除存储桶 ${bucket} 及其中全部对象？此操作不可撤销。`)) return
    setBusy(true); setError(undefined)
    try { await client.deleteBucket(bucket); onDeleted() } catch (reason) {
      setError(reason instanceof Error ? reason.message : '删除存储桶失败')
    } finally { setBusy(false) }
  }
  return (
    <IrisCard variant="outline" header="危险操作">
      {error ? <IrisAlert tone="danger">{error}</IrisAlert> : null}
      <p className="muted">删除会级联清理存储桶内所有可删除对象；受 WORM 或法律保留保护的对象会阻止操作。</p>
      <IrisButton variant="outline" className="text-danger" disabled={bucket === 'default'} loading={busy} onClick={() => void remove()}>{bucket === 'default' ? '默认存储桶不可删除' : '删除存储桶'}</IrisButton>
    </IrisCard>
  )
}

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const unit = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1)
  return `${(value / 1024 ** unit).toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`
}
