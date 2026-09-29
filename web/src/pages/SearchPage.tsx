import * as React from 'react'
import { IrisAlert, IrisBadge, IrisButton, IrisCard, IrisEmptyState, IrisInput } from '@iris-ui-kit/react'
import type { SearchHit, SearchMode, VaultClient } from '../api/vault'
import { PageHeader } from '../components/Page'
import { SearchModeField } from '../components/SearchModeField'

export function SearchPage({ client }: { client: VaultClient }): React.ReactElement {
  const [query, setQuery] = React.useState('')
  const [mode, setMode] = React.useState<SearchMode>('hybrid')
  const [hits, setHits] = React.useState<SearchHit[]>()
  const [loading, setLoading] = React.useState(false)
  const [error, setError] = React.useState<string>()
  const requestRef = React.useRef(0)

  const invalidateSearch = () => {
    requestRef.current += 1
    setHits(undefined)
    setLoading(false)
    setError(undefined)
  }

  React.useLayoutEffect(() => {
    invalidateSearch()
  }, [client])
  React.useEffect(() => () => { requestRef.current += 1 }, [])

  const search = async () => {
    const requestID = ++requestRef.current
    const value = query.trim()
    if (!value) {
      setHits(undefined)
      setLoading(false)
      setError(undefined)
      return
    }
    const requestClient = client
    const requestMode = mode
    setLoading(true)
    setHits(undefined)
    setError(undefined)
    try {
      const nextHits = await requestClient.search(value, requestMode)
      if (requestRef.current === requestID) setHits(nextHits)
    } catch (reason) {
      if (requestRef.current === requestID) {
        setError(reason instanceof Error ? reason.message : '搜索失败')
      }
    } finally {
      if (requestRef.current === requestID) setLoading(false)
    }
  }

  return (
    <section>
      <PageHeader title="知识检索" description="在 Aero Vault 的已索引内容中执行向量、BM25 或混合检索。" />
      <IrisCard variant="outline">
        <form className="knowledge-form" onSubmit={(event) => { event.preventDefault(); void search() }}>
          <IrisInput
            value={query}
            placeholder="输入要查找的内容"
            onChange={(event) => {
              setQuery(event.target.value)
              invalidateSearch()
            }}
          />
          <SearchModeField value={mode} onChange={(value) => {
            setMode(value)
            invalidateSearch()
          }} />
          <IrisButton type="submit" loading={loading}>搜索</IrisButton>
        </form>
      </IrisCard>
      {error ? <IrisAlert tone="danger" title="搜索失败">{error}</IrisAlert> : null}
      {hits?.length === 0 ? (
        <IrisEmptyState title="没有命中" description="尝试更换关键词或检索模式。" />
      ) : null}
      <div className="hit-list">
        {hits?.map((hit) => (
          <IrisCard
            key={`${hit.chunk_id}:${hit.seq}`}
            variant="outline"
            header={<div className="hit-header"><strong>{hit.object_key}</strong><IrisBadge>{hit.score.toFixed(4)}</IrisBadge></div>}
          >
            <div className="hit-source">
              {hit.bucket} · object #{hit.object_id} · chunk #{hit.chunk_id} · {hit.embed_model || mode}
            </div>
            <p className="hit-content">{hit.chunk}</p>
          </IrisCard>
        ))}
      </div>
    </section>
  )
}
