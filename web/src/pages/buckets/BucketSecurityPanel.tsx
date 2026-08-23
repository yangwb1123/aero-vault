import * as React from 'react'
import { IrisTabs, IrisTabsContent, IrisTabsList, IrisTabsTrigger } from '@iris-ui-kit/react'
import type { VaultClient } from '../../api/vault'
import { PageError, PageLoading } from '../../components/Page'
import { useResource } from '../../hooks/useResource'
import { BucketAccessPanel } from './security/BucketAccessPanel'
import { BucketCORSPanel } from './security/BucketCORSPanel'
import { BucketEncryptionPanel } from './security/BucketEncryptionPanel'

export function BucketSecurityPanel({ client, bucket }: { client: VaultClient; bucket: string }): React.ReactElement {
  const api = client.bucketSecurity
  const load = React.useCallback(async () => {
    const [acl, policy, cors, encryption] = await Promise.all([
      api.getACL(bucket), api.getPolicy(bucket), api.getCORS(bucket), api.getEncryption(bucket),
    ])
    return { acl, policy, cors, encryption }
  }, [api, bucket])
  const resource = useResource(load)

  if (resource.loading) return <PageLoading />
  if (resource.error) return <PageError error={resource.error} retry={resource.reload} />
  const data = resource.data!
  return (
    <IrisTabs defaultValue="access">
      <IrisTabsList className="bucket-security-tabs">
        <IrisTabsTrigger value="access">ACL 与策略</IrisTabsTrigger>
        <IrisTabsTrigger value="cors">CORS</IrisTabsTrigger>
        <IrisTabsTrigger value="encryption">服务端加密</IrisTabsTrigger>
      </IrisTabsList>
      <IrisTabsContent value="access"><BucketAccessPanel api={api} bucket={bucket} acl={data.acl} policy={data.policy} onSaved={resource.reload} /></IrisTabsContent>
      <IrisTabsContent value="cors"><BucketCORSPanel api={api} bucket={bucket} rules={data.cors} onSaved={resource.reload} /></IrisTabsContent>
      <IrisTabsContent value="encryption"><BucketEncryptionPanel api={api} bucket={bucket} config={data.encryption} onSaved={resource.reload} /></IrisTabsContent>
    </IrisTabs>
  )
}
