import {
  IrisBadge,
  IrisButton,
  IrisTabs,
  IrisTabsContent,
  IrisTabsList,
  IrisTabsTrigger,
} from '@iris-ui-kit/react'
import type { VaultClient } from '../../api/vault'
import { PageHeader } from '../../components/Page'
import { GovernancePanel } from './GovernancePanel'
import { MetadataPanel } from './MetadataPanel'
import { VersionsPanel } from './VersionsPanel'

export function ObjectPage({
  client,
  objectKey,
  deleted,
  onBack,
  onRestored,
}: {
  client: VaultClient
  objectKey: string
  deleted: boolean
  onBack(): void
  onRestored(): void
}): React.ReactElement {
  return (
    <section>
      <PageHeader
        title={objectKey}
        description="管理对象标签、元数据、版本、临时访问链接与合规状态。"
        actions={<><IrisBadge tone={deleted ? 'warning' : 'success'}>{deleted ? '已软删除' : '活动对象'}</IrisBadge><IrisButton variant="outline" onClick={onBack}>返回文件</IrisButton></>}
      />
      <IrisTabs defaultValue={deleted ? 'governance' : 'metadata'}>
        <IrisTabsList className="object-tabs">
          <IrisTabsTrigger value="metadata" disabled={deleted}>标签与元数据</IrisTabsTrigger>
          <IrisTabsTrigger value="versions">对象版本</IrisTabsTrigger>
          <IrisTabsTrigger value="governance">访问与合规</IrisTabsTrigger>
        </IrisTabsList>
        <IrisTabsContent className="object-panel" value="metadata"><MetadataPanel client={client} objectKey={objectKey} /></IrisTabsContent>
        <IrisTabsContent className="object-panel" value="versions"><VersionsPanel client={client} objectKey={objectKey} /></IrisTabsContent>
        <IrisTabsContent className="object-panel" value="governance"><GovernancePanel client={client} objectKey={objectKey} deleted={deleted} onRestored={onRestored} /></IrisTabsContent>
      </IrisTabs>
    </section>
  )
}
