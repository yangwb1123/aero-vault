import { IrisAlert, IrisButton, IrisCard } from '@iris-ui-kit/react'
import type { WebConfig } from '../config'
import { PageHeader } from '../components/Page'

const services = [
  ['账户中心', '账户资料、成员关系和跨系统操作由 aero-id 管理。', 'accountConsoleUrl'],
  ['消息通知', '通知、会话与工作区由 aero-im 管理。', 'notificationConsoleUrl'],
  ['审计治理', '所有审计查询、完整性和保留策略由 audit-governance 管理。', 'auditConsoleUrl'],
] as const

export function ServicesPage({ config }: { config: WebConfig }): React.ReactElement {
  return (
    <section>
      <PageHeader title="平台服务" description="统一身份下的领域服务入口。" />
      <IrisAlert tone="info" title="令牌 audience 隔离">
        页面跳转不会转发当前 Aero Vault access token；目标服务必须通过 Snaplink 获取自己的 audience 令牌。
      </IrisAlert>
      <div className="service-grid">
        {services.map(([title, description, key]) => {
          const url = config[key]
          return (
            <IrisCard
              key={key}
              variant="outline"
              header={title}
              footer={url ? <IrisButton asChild variant="outline"><a href={url}>打开控制台</a></IrisButton> : <span className="muted">尚未配置入口 URL</span>}
            >
              {description}
            </IrisCard>
          )
        })}
        <IrisCard
          variant="outline"
          header="旧版 Aero Vault 控制台"
          footer={<IrisButton asChild variant="outline"><a href="/ui/legacy/">打开旧版</a></IrisButton>}
        >
          搜索、Chat、血缘、分享、公开资产和部门 ACL 等能力在迁移完成前继续保留。
        </IrisCard>
      </div>
    </section>
  )
}
