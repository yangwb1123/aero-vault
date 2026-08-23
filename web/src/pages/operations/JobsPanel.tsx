import * as React from 'react'
import {
  IrisBadge,
  IrisButton,
  IrisCard,
  IrisInput,
  IrisSelect,
  IrisTable,
  type IrisBadgeTone,
  type IrisTableColumn,
} from '@iris-ui-kit/react'
import type { AdminJob, AdminJobsResponse, AdminJobStatus } from '../../api/admin'
import type { JobFilters } from './OperationsPage'

const statusItems: Array<{ value: AdminJobStatus | ''; label: string }> = [
  { value: '', label: '全部状态' },
  { value: 'pending', label: '待处理' },
  { value: 'running', label: '运行中' },
  { value: 'succeeded', label: '已完成' },
  { value: 'failed', label: '失败' },
]

export function JobsPanel({ response, filters, onFiltersChange, busyJob, retry }: {
  response: AdminJobsResponse
  filters: JobFilters
  onFiltersChange(filters: JobFilters): void
  busyJob?: number
  retry(id: number): Promise<void>
}): React.ReactElement {
  const [type, setType] = React.useState(filters.type)
  const columns = React.useMemo(() => jobColumns(busyJob, retry), [busyJob, retry])
  return (
    <IrisCard variant="outline" header="后台任务队列">
      <form className="operations-filters" onSubmit={(event) => {
        event.preventDefault()
        onFiltersChange({ ...filters, type: type.trim() })
      }}>
        <label><span>状态</span><IrisSelect items={statusItems} value={filters.status} onValueChange={(status) => onFiltersChange({ ...filters, status })} /></label>
        <label><span>任务类型</span><IrisInput value={type} placeholder="例如 index_object" onChange={(event) => setType(event.target.value)} /></label>
        <IrisButton type="submit" variant="outline">应用筛选</IrisButton>
      </form>
      <IrisTable
        columns={columns}
        data={response.jobs}
        rowKey="id"
        striped
        bordered
        keyboardNavigation
        emptyState={<span className="muted">当前筛选条件下没有后台任务。</span>}
        rowExpandable={(job) => Boolean(job.last_error || job.payload || job.result)}
        renderDetail={(job) => <JobDetail job={job} />}
      />
    </IrisCard>
  )
}

function jobColumns(busyJob: number | undefined, retry: (id: number) => Promise<void>): IrisTableColumn<AdminJob>[] {
  return [
    { key: 'id', title: 'ID', sortable: true, width: 80 },
    { key: 'tenant', title: '租户', sortable: true },
    { key: 'type', title: '类型', sortable: true },
    { key: 'status', title: '状态', sortable: true, render: (value) => <IrisBadge tone={statusTone(String(value))}>{statusLabel(String(value))}</IrisBadge> },
    { key: 'attempts', title: '尝试', render: (_value, job) => `${job.attempts}/${job.max_attempts}` },
    { key: 'created_at', title: '创建时间', sortable: true, render: (value) => formatDate(value) },
    { key: 'actions', title: '操作', pinned: 'right', render: (_value, job) => job.status === 'failed' ? (
      <IrisButton size="sm" variant="outline" loading={busyJob === job.id} disabled={busyJob !== undefined && busyJob !== job.id} onClick={() => {
        if (window.confirm(`重新执行失败任务 #${job.id}？`)) void retry(job.id)
      }}>重试</IrisButton>
    ) : <span className="muted">—</span> },
  ]
}

function JobDetail({ job }: { job: AdminJob }): React.ReactElement {
  return (
    <div className="operations-detail">
      {job.last_error ? <div><strong>最后错误</strong><pre>{job.last_error}</pre></div> : null}
      {job.payload ? <div><strong>Payload</strong><pre>{job.payload}</pre></div> : null}
      {job.result ? <div><strong>Result</strong><pre>{job.result}</pre></div> : null}
      {job.worker ? <div><strong>Worker</strong><span>{job.worker}</span></div> : null}
    </div>
  )
}

function statusTone(status: string): IrisBadgeTone {
  if (status === 'succeeded') return 'success'
  if (status === 'failed') return 'danger'
  if (status === 'running') return 'warning'
  return 'neutral'
}

function statusLabel(status: string): string {
  return ({ pending: '待处理', running: '运行中', succeeded: '已完成', failed: '失败' } as Record<string, string>)[status] ?? status
}

function formatDate(value: unknown): string {
  return typeof value === 'string' && value ? new Date(value).toLocaleString('zh-CN') : '—'
}
