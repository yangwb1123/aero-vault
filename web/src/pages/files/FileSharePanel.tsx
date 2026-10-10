import { IrisButton } from '@iris-ui-kit/react'
import type { VaultClient, VaultObject } from '../../api/vault'
import { PageHeader } from '../../components/Page'
import { SharePanel } from '../access/SharePanel'

export function FileSharePanel({ client, object, close }: {
  client: VaultClient
  object: VaultObject
  close(): void
}): React.ReactElement {
  return (
    <section className="access-stack" aria-label="对象分享">
      <PageHeader title={`分享：${object.key}`} description={`存储桶：${object.bucket}；分享权限仍由服务端独立校验。`}
        actions={<IrisButton variant="outline" autoFocus onClick={close}>关闭分享</IrisButton>} />
      <SharePanel client={client} bucket={object.bucket} objectKey={object.key} />
    </section>
  )
}
