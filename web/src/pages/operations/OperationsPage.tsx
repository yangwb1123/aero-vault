import * as React from 'react'
import { IrisAlert, IrisButton, IrisTabs, IrisTabsContent, IrisTabsList, IrisTabsTrigger } from '@iris-ui-kit/react'
import type { AdminJobStatus } from '../../api/admin'
import type { VaultClient } from '../../api/vault'
import { PageError, PageHeader, PageLoading } from '../../components/Page'
import { useResource } from '../../hooks/useResource'
import { JobsPanel } from './JobsPanel'
import { OperationsSummary } from './OperationsSummary'
import { TenantsPanel } from './TenantsPanel'
import { WebhooksPanel } from './WebhooksPanel'

export interface JobFilters {
  status: AdminJobStatus | ''
  type: string
}

export function OperationsPage({ client }: { client: VaultClient }): React.ReactElement {
  const [filters, setFilters] = React.useState<JobFilters>({ status: '', type: '' })
  const [busyJob, setBusyJob] = React.useState<number>()
  const [message, setMessage] = React.useState<{ tone: 'success' | 'danger'; text: string }>()
  const load = React.useCallback(async () => {
    const [jobs, tenants, webhooks] = await Promise.all([
      client.listAdminJobs(filters.status, filters.type),
      client.listAdminTenants(),
      client.listAdminWebhookFailures(),
    ])
    return { jobs, tenants, webhooks }
  }, [client, filters])
  const resource = useResource(load)

  const retry = async (id: number) => {
    setBusyJob(id)
    setMessage(undefined)
    try {
      await client.retryAdminJob(id)
      setMessage({ tone: 'success', text: `任务 #${id} 已重新进入待处理队列。` })
      resource.reload()
    } catch (reason) {
      setMessage({ tone: 'danger', text: reason instanceof Error ? reason.message : '任务重试失败' })
    } finally {
      setBusyJob(undefined)
    }
  }

  return (
    <section>
      <PageHeader
        title="运行运维"
        description="查看 Aero Vault 后台任务、租户状态和 Webhook 投递；需要 Snaplink operator admin 权限。"
        actions={<IrisButton variant="outline" onClick={resource.reload}>刷新</IrisButton>}
      />
      <IrisAlert tone="info" title="权限与审计边界">
        后端仍执行 operator admin 授权；平台审计查询与治理请前往 audit-governance。
      </IrisAlert>
      {message ? <IrisAlert tone={message.tone}>{message.text}</IrisAlert> : null}
      {resource.loading ? <PageLoading /> : null}
      {resource.error ? <PageError error={resource.error} retry={resource.reload} /> : null}
      {resource.data ? (
        <div className="operations-stack">
          <OperationsSummary data={resource.data} />
          <IrisTabs defaultValue="jobs">
            <IrisTabsList className="operations-tabs">
              <IrisTabsTrigger value="jobs">后台任务</IrisTabsTrigger>
              <IrisTabsTrigger value="tenants">租户状态</IrisTabsTrigger>
              <IrisTabsTrigger value="webhooks">Webhook 投递</IrisTabsTrigger>
            </IrisTabsList>
            <IrisTabsContent value="jobs"><JobsPanel response={resource.data.jobs} filters={filters} onFiltersChange={setFilters} busyJob={busyJob} retry={retry} /></IrisTabsContent>
            <IrisTabsContent value="tenants"><TenantsPanel tenants={resource.data.tenants} /></IrisTabsContent>
            <IrisTabsContent value="webhooks"><WebhooksPanel failures={resource.data.webhooks} /></IrisTabsContent>
          </IrisTabs>
        </div>
      ) : null}
    </section>
  )
}
