import { IrisBadge, IrisCard, IrisTable, type IrisBadgeTone, type IrisTableColumn } from '@iris-ui-kit/react'
import type { AdminTenant } from '../../api/admin'

const columns: IrisTableColumn<AdminTenant>[] = [
  { key: 'tenant_id', title: '租户 ID', sortable: true },
  { key: 'display_name', title: '显示名称', sortable: true, render: (value) => String(value || '—') },
  { key: 'status', title: '状态', sortable: true, render: (value) => <IrisBadge tone={tenantTone(String(value))}>{value as string}</IrisBadge> },
  { key: 'created_at', title: '创建时间', sortable: true, render: (value) => typeof value === 'string' && value ? new Date(value).toLocaleString('zh-CN') : '—' },
]

export function TenantsPanel({ tenants }: { tenants: AdminTenant[] }): React.ReactElement {
  return (
    <IrisCard variant="outline" header="租户运行状态">
      <p className="muted">账户资料和成员生命周期由 aero-id 管理；此处只展示 Aero Vault 本地租户状态。</p>
      <IrisTable columns={columns} data={tenants} rowKey="tenant_id" striped bordered keyboardNavigation emptyState={<span className="muted">暂无租户记录。</span>} />
    </IrisCard>
  )
}

function tenantTone(status: string): IrisBadgeTone {
  return status === 'active' ? 'success' : status === 'disabled' ? 'danger' : 'neutral'
}
