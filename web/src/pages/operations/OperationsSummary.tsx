import { IrisCard } from '@iris-ui-kit/react'
import type { AdminJobsResponse, AdminTenant, AdminWebhookFailure } from '../../api/admin'

interface OperationsData {
  jobs: AdminJobsResponse
  tenants: AdminTenant[]
  webhooks: AdminWebhookFailure[]
}

export function OperationsSummary({ data }: { data: OperationsData }): React.ReactElement {
  const activeTenants = data.tenants.filter((tenant) => tenant.status === 'active').length
  const failedWebhooks = data.webhooks.filter((failure) => !failure.succeeded).length
  return (
    <div className="metric-grid">
      <Metric title="待处理任务" value={data.jobs.stats.pending ?? 0} detail={`${data.jobs.stats.running ?? 0} 个正在运行`} />
      <Metric title="失败任务" value={data.jobs.stats.failed ?? 0} detail={`${data.jobs.stats.succeeded ?? 0} 个已完成`} />
      <Metric title="活动租户" value={activeTenants} detail={`共 ${data.tenants.length} 个租户`} />
      <Metric title="Webhook 异常" value={failedWebhooks} detail="最近 100 条投递记录" />
    </div>
  )
}

function Metric({ title, value, detail }: { title: string; value: number; detail: string }): React.ReactElement {
  return (
    <IrisCard variant="outline" header={title}>
      <div className="metric-value">{value}</div>
      <div className="metric-label">{detail}</div>
    </IrisCard>
  )
}
