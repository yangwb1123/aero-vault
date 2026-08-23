import { IrisBadge, IrisCard, IrisTable, type IrisBadgeTone, type IrisTableColumn } from '@iris-ui-kit/react'
import type { AdminWebhookFailure } from '../../api/admin'

const columns: IrisTableColumn<AdminWebhookFailure>[] = [
  { key: 'id', title: 'ID', sortable: true, width: 80 },
  { key: 'eventId', title: '事件', sortable: true },
  { key: 'url', title: '目标 URL' },
  { key: 'attempts', title: '尝试', sortable: true },
  { key: 'lastStatus', title: 'HTTP', sortable: true, render: (value) => Number(value) || '—' },
  { key: 'state', title: '状态', render: (_value, row) => <IrisBadge tone={webhookTone(row)}>{webhookLabel(row)}</IrisBadge> },
  { key: 'createdAt', title: '首次失败', sortable: true, render: (value) => formatDate(value) },
]

export function WebhooksPanel({ failures }: { failures: AdminWebhookFailure[] }): React.ReactElement {
  return (
    <IrisCard variant="outline" header="Webhook 投递记录">
      <IrisTable
        columns={columns}
        data={failures}
        rowKey="id"
        striped
        bordered
        keyboardNavigation
        emptyState={<span className="muted">暂无 Webhook 失败记录。</span>}
        rowExpandable={(failure) => Boolean(failure.lastError || failure.nextRetryAt)}
        renderDetail={(failure) => (
          <div className="operations-detail">
            {failure.lastError ? <div><strong>最后错误</strong><pre>{failure.lastError}</pre></div> : null}
            {failure.nextRetryAt ? <div><strong>下次重试</strong><span>{formatDate(failure.nextRetryAt)}</span></div> : null}
          </div>
        )}
      />
    </IrisCard>
  )
}

function webhookTone(row: AdminWebhookFailure): IrisBadgeTone {
  if (row.succeeded) return 'success'
  if (row.deadLettered) return 'danger'
  return 'warning'
}

function webhookLabel(row: AdminWebhookFailure): string {
  if (row.succeeded) return '已恢复'
  if (row.deadLettered) return '死信'
  return '等待重试'
}

function formatDate(value: unknown): string {
  return typeof value === 'string' && value ? new Date(value).toLocaleString('zh-CN') : '—'
}
