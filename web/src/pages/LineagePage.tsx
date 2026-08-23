import * as React from 'react'
import { IrisAlert, IrisButton, IrisCard, IrisEmptyState, IrisNumberInput } from '@iris-ui-kit/react'
import type { LineageResponse, VaultClient } from '../api/vault'
import { PageHeader } from '../components/Page'

const formatCost = (micros = 0): string => `$${(micros / 1_000_000).toFixed(6)}`

export function LineagePage({ client }: { client: VaultClient }): React.ReactElement {
  const [objectId, setObjectId] = React.useState<number | null>(null)
  const [result, setResult] = React.useState<LineageResponse>()
  const [error, setError] = React.useState<string>()
  const [loading, setLoading] = React.useState(false)

  const load = async () => {
    if (!objectId || objectId < 1) return
    setLoading(true)
    setError(undefined)
    try {
      setResult(await client.getLineage(objectId))
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '读取血缘失败')
    } finally {
      setLoading(false)
    }
  }

  return (
    <section>
      <PageHeader title="对象血缘" description="查看对象被 Search、Chat 或 Agent 消费的 AI 使用记录。" />
      <IrisCard variant="outline">
        <form className="lineage-form" onSubmit={(event) => { event.preventDefault(); void load() }}>
          <label><span>Object ID</span><IrisNumberInput min={1} value={objectId ?? undefined} onChange={setObjectId} /></label>
          <IrisButton type="submit" loading={loading}>读取血缘</IrisButton>
        </form>
      </IrisCard>
      {error ? <IrisAlert tone="danger">{error}</IrisAlert> : null}
      {result?.entries.length === 0 ? <IrisEmptyState title="暂无血缘记录" description="该对象尚未被 AI 能力消费。" /> : null}
      {result?.entries.length ? (
        <IrisCard variant="outline">
          <div className="table-scroll">
            <table className="vault-table">
              <thead><tr><th>时间</th><th>调用方</th><th>查询</th><th>模型</th><th>Tokens</th><th>延迟</th><th>费用</th></tr></thead>
              <tbody>{result.entries.map((entry) => (
                <tr key={entry.usage_id}>
                  <td>{new Date(entry.created_at).toLocaleString('zh-CN')}</td>
                  <td>{entry.caller}</td>
                  <td>{entry.query || '—'}</td>
                  <td>{entry.model || '—'}</td>
                  <td>{entry.total_tokens ?? 0}</td>
                  <td>{entry.latency_ms ?? 0} ms</td>
                  <td>{formatCost(entry.cost_micros)}</td>
                </tr>
              ))}</tbody>
            </table>
          </div>
        </IrisCard>
      ) : null}
    </section>
  )
}
