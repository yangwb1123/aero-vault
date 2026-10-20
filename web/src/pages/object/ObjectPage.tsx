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
import { ACLPanel } from '../access/ACLPanel'
import { SharePanel } from '../access/SharePanel'
import { GovernancePanel } from './GovernancePanel'
import { MetadataPanel } from './MetadataPanel'
import { PreviewPanel } from './PreviewPanel'
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
        description="管理对象标签、元数据、版本、分享、直接权限规则与合规状态。"
        actions={<><IrisBadge tone={deleted ? 'warning' : 'success'}>{deleted ? '已软删除' : '活动对象'}</IrisBadge><IrisButton variant="outline" onClick={onBack}>返回文件</IrisButton></>}
      />
      <IrisTabs defaultValue={deleted ? 'governance' : 'preview'}>
        <IrisTabsList className="object-tabs">
          <IrisTabsTrigger value="preview" disabled={deleted}>内容预览</IrisTabsTrigger>
          <IrisTabsTrigger value="metadata" disabled={deleted}>标签与元数据</IrisTabsTrigger>
          <IrisTabsTrigger value="versions">对象版本</IrisTabsTrigger>
          <IrisTabsTrigger value="sharing" disabled={deleted}>对象分享</IrisTabsTrigger>
          <IrisTabsTrigger value="permissions" disabled={deleted}>权限规则</IrisTabsTrigger>
          <IrisTabsTrigger value="governance">访问与合规</IrisTabsTrigger>
        </IrisTabsList>
        <IrisTabsContent className="object-panel" value="preview"><PreviewPanel client={client} objectKey={objectKey} /></IrisTabsContent>
        <IrisTabsContent className="object-panel" value="metadata"><MetadataPanel client={client} objectKey={objectKey} /></IrisTabsContent>
        <IrisTabsContent className="object-panel" value="versions"><VersionsPanel client={client} objectKey={objectKey} /></IrisTabsContent>
        {!deleted ? <IrisTabsContent className="object-panel" value="sharing"><SharePanel client={client} bucket="default" objectKey={objectKey} /></IrisTabsContent> : null}
        {!deleted ? <IrisTabsContent className="object-panel" value="permissions"><ACLPanel client={client} bucket="default" objectKey={objectKey} /></IrisTabsContent> : null}
        <IrisTabsContent className="object-panel" value="governance"><GovernancePanel client={client} objectKey={objectKey} deleted={deleted} onRestored={onRestored} /></IrisTabsContent>
      </IrisTabs>
    </section>
  )
}
