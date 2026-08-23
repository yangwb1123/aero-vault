import * as React from 'react'
import { IrisAlert, IrisButton, IrisCard, IrisEmptyState, IrisInput } from '@iris-ui-kit/react'
import type { VaultClient } from '../../api/vault'
import { PageError, PageHeader, PageLoading } from '../../components/Page'
import { useResource } from '../../hooks/useResource'
import { BucketDetailPanel } from './BucketDetailPanel'

export function BucketsPage({ client }: { client: VaultClient }): React.ReactElement {
  const [name, setName] = React.useState('')
  const [selected, setSelected] = React.useState('')
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState<string>()
  const load = React.useCallback(() => client.listBuckets(), [client])
  const resource = useResource(load)

  React.useEffect(() => {
    if (resource.data && (!selected || !resource.data.includes(selected))) {
      setSelected(resource.data[0] ?? '')
    }
  }, [resource.data, selected])

  const create = async () => {
    const bucket = name.trim()
    if (!bucket || bucket.includes('/')) {
      setError('存储桶名称不能为空或包含斜杠。')
      return
    }
    setBusy(true)
    setError(undefined)
    try {
      await client.createBucket(bucket)
      setName('')
      setSelected(bucket)
      resource.reload()
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '创建存储桶失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <section>
      <PageHeader title="存储桶" description="创建和管理当前租户的版本控制、默认对象锁与生命周期策略。" />
      <div className="bucket-stack">
        <IrisCard variant="outline" header="创建存储桶">
          <form className="bucket-create" onSubmit={(event) => { event.preventDefault(); void create() }}>
            <label className="access-field"><span>存储桶名称</span><IrisInput required value={name} placeholder="project-archive" onChange={(event) => setName(event.target.value)} /></label>
            <IrisButton type="submit" loading={busy}>创建</IrisButton>
          </form>
        </IrisCard>
        {error ? <IrisAlert tone="danger">{error}</IrisAlert> : null}
        {resource.loading ? <PageLoading /> : null}
        {resource.error ? <PageError error={resource.error} retry={resource.reload} /> : null}
        {resource.data ? (
          <div className="bucket-layout">
            <BucketList buckets={resource.data} selected={selected} onSelect={setSelected} />
            {selected ? <BucketDetailPanel key={selected} client={client} bucket={selected} onDeleted={() => { setSelected(''); resource.reload() }} /> : (
              <IrisEmptyState title="暂无存储桶" description="创建存储桶后即可配置数据保护策略。" />
            )}
          </div>
        ) : null}
      </div>
    </section>
  )
}

function BucketList({ buckets, selected, onSelect }: { buckets: string[]; selected: string; onSelect(bucket: string): void }): React.ReactElement {
  return (
    <IrisCard variant="outline" header={`存储桶（${buckets.length}）`}>
      <div className="bucket-list">
        {buckets.map((bucket) => (
          <button key={bucket} type="button" className="bucket-list-item" data-active={bucket === selected || undefined} onClick={() => onSelect(bucket)}>
            <strong>{bucket}</strong><span>{bucket === 'default' ? '默认存储桶' : '租户存储桶'}</span>
          </button>
        ))}
        {buckets.length === 0 ? <span className="muted">暂无存储桶</span> : null}
      </div>
    </IrisCard>
  )
}
