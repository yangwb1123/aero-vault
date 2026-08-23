import * as React from 'react'
import { IrisAlert, IrisBadge, IrisButton, IrisCard, IrisEmptyState, IrisInput, type IrisBadgeTone } from '@iris-ui-kit/react'
import type { VaultEvent } from '../api/events'
import type { VaultClient } from '../api/vault'
import { PageHeader } from '../components/Page'
import { useVaultEvents, type EventStreamStatus } from '../hooks/useVaultEvents'

export function ActivityPage({ client, onOpenObject }: { client: VaultClient; onOpenObject(key: string, deleted: boolean): void }): React.ReactElement {
  const [live, setLive] = React.useState(true)
  const [type, setType] = React.useState('')
  const [query, setQuery] = React.useState('')
  const stream = useVaultEvents(client, live)
  const visible = React.useMemo(() => stream.events.filter((event) =>
    (!type || event.type === type) && (!query.trim() || event.key.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()))),
  [query, stream.events, type])

  return (
    <section>
      <PageHeader
        title="对象动态"
        description="当前租户的 Aero Vault 持久事件流；断线后使用 Last-Event-ID 自动回放。"
        actions={<><IrisBadge tone={statusTone(stream.status)}>{statusLabel(stream.status)}</IrisBadge><IrisButton variant="outline" onClick={() => setLive((value) => !value)}>{live ? '暂停' : '继续'}</IrisButton><IrisButton variant="outline" disabled={!live} onClick={stream.reconnect}>立即重连</IrisButton><IrisButton variant="ghost" onClick={stream.clear}>清空页面</IrisButton></>}
      />
      <IrisAlert tone="info" title="事件与通知边界">
        此页面展示存储对象活动，不替代用户消息通知；面向人的通知和会话继续由 aero-im 管理。
      </IrisAlert>
      {stream.error ? <IrisAlert tone="warning" title="事件连接暂不可用">{stream.error}</IrisAlert> : null}
      <IrisCard variant="outline" header={<div className="activity-heading"><strong>实时事件</strong><span className="muted">最后事件 ID：{stream.lastEventID || '等待首条事件'}</span></div>}>
        <div className="activity-filters">
          <label className="access-field"><span>事件类型</span><select value={type} onChange={(event) => setType(event.target.value)}><option value="">全部类型</option><option value="created">创建</option><option value="updated">更新</option><option value="deleted">删除</option><option value="accessed">访问</option></select></label>
          <label className="access-field"><span>对象 Key</span><IrisInput value={query} placeholder="筛选 key" onChange={(event) => setQuery(event.target.value)} /></label>
        </div>
        {visible.length ? <ActivityTable events={visible} open={onOpenObject} /> : <IrisEmptyState title="暂无对象动态" description={live ? '等待当前租户产生新的对象事件。' : '事件连接已暂停。'} />}
        {stream.events.length ? <div className="table-note">内存中保留最近 {stream.events.length}/200 条事件；筛选后显示 {visible.length} 条。</div> : null}
      </IrisCard>
    </section>
  )
}

function ActivityTable({ events, open }: { events: VaultEvent[]; open(key: string, deleted: boolean): void }): React.ReactElement {
  return (
    <div className="table-scroll"><table className="vault-table">
      <thead><tr><th>ID</th><th>时间</th><th>动作</th><th>对象</th><th>Object ID</th><th>操作</th></tr></thead>
      <tbody>{events.map((event) => <tr key={event.id}>
        <td>{event.id}</td><td>{new Date(event.created_at).toLocaleString('zh-CN')}</td>
        <td><IrisBadge tone={eventTone(event.type)}>{eventLabel(event.type)}</IrisBadge></td>
        <td><strong>{event.key}</strong><div className="muted">{event.bucket}</div></td><td>{event.object_id ?? '—'}</td>
        <td><IrisButton size="sm" variant="ghost" onClick={() => open(event.key, event.type === 'deleted')}>查看对象</IrisButton></td>
      </tr>)}</tbody>
    </table></div>
  )
}

function statusTone(status: EventStreamStatus): IrisBadgeTone {
  if (status === 'connected') return 'success'
  if (status === 'retrying') return 'danger'
  return status === 'paused' ? 'neutral' : 'warning'
}

function statusLabel(status: EventStreamStatus): string {
  return ({ connecting: '连接中', connected: '实时连接', retrying: '等待重连', paused: '已暂停' } as Record<EventStreamStatus, string>)[status]
}

function eventTone(type: string): IrisBadgeTone {
  if (type === 'created') return 'success'
  if (type === 'deleted') return 'danger'
  if (type === 'updated') return 'warning'
  return 'neutral'
}

function eventLabel(type: string): string {
  return ({ created: '创建', updated: '更新', deleted: '删除', accessed: '访问' } as Record<string, string>)[type] ?? type
}
