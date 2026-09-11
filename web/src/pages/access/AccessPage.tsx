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
  const bucketOptions: Array<SelectOption<string>> = (buckets.data ?? []).map((value) => ({
    value,
    label: value === 'default' ? `${value}（默认）` : value,
  }))

  React.useEffect(() => {
    if (selectedBucket !== bucket) setBucket(selectedBucket)
  }, [bucket, selectedBucket])

  return (
    <section>
      <PageHeader
        title="访问与发布"
        description="管理分享能力、公开图片、对象 ACL 和可移植备份。身份与角色来自 Snaplink，资源授权由 Aero Vault 执行。"
        actions={!buckets.loading && selectedBucket ? (
          <AccessSelect
            label="当前存储桶"
            value={selectedBucket}
            options={bucketOptions}
            onChange={setBucket}
          />
        ) : undefined}
      />
      {buckets.loading ? <PageLoading /> : null}
      {buckets.error ? <PageError error={buckets.error} retry={buckets.reload} /> : null}
      {!buckets.loading && buckets.data && !selectedBucket ? (
        <IrisEmptyState title="暂无可用存储桶" description="创建存储桶后即可管理分享、公开图片、ACL 和备份。" />
      ) : null}
      {!buckets.loading && !buckets.error && selectedBucket ? (
        <IrisTabs defaultValue="shares">
          <IrisTabsList className="access-tabs">
            <IrisTabsTrigger value="shares">分享链接</IrisTabsTrigger>
            <IrisTabsTrigger value="assets">公开图片</IrisTabsTrigger>
            <IrisTabsTrigger value="acl">资源 ACL</IrisTabsTrigger>
            <IrisTabsTrigger value="backup">备份导出</IrisTabsTrigger>
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

function selectBucket(selected: string, buckets?: string[]): string {
  if (!buckets) return ''
  if (selected && buckets.includes(selected)) return selected
  if (buckets.includes('default')) return 'default'
  return buckets[0] ?? ''
}
