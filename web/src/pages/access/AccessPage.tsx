import * as React from 'react'
import {
  IrisEmptyState,
  IrisTabs,
  IrisTabsContent,
  IrisTabsList,
  IrisTabsTrigger,
} from '@iris-ui-kit/react'
import type { VaultClient } from '../../api/vault'
import { PageError, PageHeader, PageLoading } from '../../components/Page'
import { useResource } from '../../hooks/useResource'
import { AccessSelect, type SelectOption } from './AccessSelect'
import { ACLPanel } from './ACLPanel'
import { AssetPanel } from './AssetPanel'
import { BackupPanel } from './BackupPanel'
import { SharePanel } from './SharePanel'

export function AccessPage({ client }: { client: VaultClient }): React.ReactElement {
  const load = React.useCallback(() => client.listBuckets(), [client])
  const buckets = useResource(load)
  const [bucket, setBucket] = React.useState('')
  const selectedBucket = selectBucket(bucket, buckets.data)

  React.useEffect(() => {
    if (selectedBucket !== bucket) setBucket(selectedBucket)
  }, [bucket, selectedBucket])

  return (
    <section>
      <AccessHeader buckets={buckets.data} loading={buckets.loading} bucket={selectedBucket} onChange={setBucket} />
      {buckets.loading ? <PageLoading /> : null}
      {buckets.error ? <PageError error={buckets.error} retry={buckets.reload} /> : null}
      {!buckets.loading && buckets.data && !selectedBucket ? (
        <IrisEmptyState title="暂无可用存储桶" description="当前会话没有可见存储桶；分享、公开图片、ACL 和对象归档需有可访问的存储桶。" />
      ) : null}
      {!buckets.loading && !buckets.error && selectedBucket ? (
        <IrisTabs defaultValue="shares">
          <IrisTabsList className="access-tabs">
            <IrisTabsTrigger value="shares">分享链接</IrisTabsTrigger>
            <IrisTabsTrigger value="assets">公开图片</IrisTabsTrigger>
            <IrisTabsTrigger value="acl">资源 ACL</IrisTabsTrigger>
            <IrisTabsTrigger value="backup">对象归档</IrisTabsTrigger>
          </IrisTabsList>
          <IrisTabsContent className="access-panel" value="shares"><SharePanel key={`shares:${selectedBucket}`} client={client} bucket={selectedBucket} /></IrisTabsContent>
          <IrisTabsContent className="access-panel" value="assets"><AssetPanel key={`assets:${selectedBucket}`} client={client} bucket={selectedBucket} /></IrisTabsContent>
          <IrisTabsContent className="access-panel" value="acl"><ACLPanel key={`acl:${selectedBucket}`} client={client} bucket={selectedBucket} /></IrisTabsContent>
          <IrisTabsContent className="access-panel" value="backup"><BackupPanel key={`backup:${selectedBucket}`} client={client} bucket={selectedBucket} /></IrisTabsContent>
        </IrisTabs>
      ) : null}
    </section>
  )
}

function AccessHeader({ buckets, loading, bucket, onChange }: {
  buckets?: string[]
  loading: boolean
  bucket: string
  onChange(bucket: string): void
}): React.ReactElement {
  const options: Array<SelectOption<string>> = (buckets ?? []).map((value) => ({
    value,
    label: value === 'default' ? `${value}（默认）` : value,
  }))
  return (
    <PageHeader
      title="访问与发布"
      description="管理分享链接、公开图片、对象 ACL 和授权对象归档。此处选择存储桶仅用于这些面板，不切换文件浏览范围（固定为 default）；资源授权由 Aero Vault 执行。"
      actions={!loading && bucket ? <AccessSelect label="当前存储桶" value={bucket} options={options} onChange={onChange} /> : undefined}
    />
  )
}

function selectBucket(selected: string, buckets?: string[]): string {
  if (!buckets) return ''
  if (selected && buckets.includes(selected)) return selected
  if (buckets.includes('default')) return 'default'
  return buckets[0] ?? ''
}
