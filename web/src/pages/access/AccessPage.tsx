import {
  IrisTabs,
  IrisTabsContent,
  IrisTabsList,
  IrisTabsTrigger,
} from '@iris-ui-kit/react'
import type { VaultClient } from '../../api/vault'
import { PageHeader } from '../../components/Page'
import { ACLPanel } from './ACLPanel'
import { AssetPanel } from './AssetPanel'
import { BackupPanel } from './BackupPanel'
import { SharePanel } from './SharePanel'

export function AccessPage({ client }: { client: VaultClient }): React.ReactElement {
  return (
    <section>
      <PageHeader
        title="访问与发布"
        description="管理分享能力、公开图片、对象 ACL 和可移植备份。身份与角色来自 Snaplink，资源授权由 Aero Vault 执行。"
      />
      <IrisTabs defaultValue="shares">
        <IrisTabsList className="access-tabs">
          <IrisTabsTrigger value="shares">分享链接</IrisTabsTrigger>
          <IrisTabsTrigger value="assets">公开图片</IrisTabsTrigger>
          <IrisTabsTrigger value="acl">资源 ACL</IrisTabsTrigger>
          <IrisTabsTrigger value="backup">备份导出</IrisTabsTrigger>
        </IrisTabsList>
        <IrisTabsContent className="access-panel" value="shares"><SharePanel client={client} /></IrisTabsContent>
        <IrisTabsContent className="access-panel" value="assets"><AssetPanel client={client} /></IrisTabsContent>
        <IrisTabsContent className="access-panel" value="acl"><ACLPanel client={client} /></IrisTabsContent>
        <IrisTabsContent className="access-panel" value="backup"><BackupPanel client={client} /></IrisTabsContent>
      </IrisTabs>
    </section>
  )
}
