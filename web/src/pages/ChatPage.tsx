import * as React from 'react'
import { IrisAlert, IrisBadge, IrisButton, IrisCard, IrisTextarea } from '@iris-ui-kit/react'
import type { ChatResponse, SearchMode, VaultClient } from '../api/vault'
import { PageHeader } from '../components/Page'
import { SearchModeField } from '../components/SearchModeField'

export function ChatPage({ client }: { client: VaultClient }): React.ReactElement {
  const [query, setQuery] = React.useState('')
  const [mode, setMode] = React.useState<SearchMode>('hybrid')
  const [answer, setAnswer] = React.useState('')
  const [result, setResult] = React.useState<ChatResponse>()
  const [error, setError] = React.useState<string>()
  const [loading, setLoading] = React.useState(false)
  const abortRef = React.useRef<AbortController>()

  React.useEffect(() => () => abortRef.current?.abort(), [])

  const ask = async () => {
    const value = query.trim()
    if (!value) return
    abortRef.current?.abort()
    const abort = new AbortController()
    abortRef.current = abort
    setAnswer('')
    setResult(undefined)
    setError(undefined)
    setLoading(true)
    try {
      const completed = await client.streamChat(
        value,
        mode,
        (token) => setAnswer((current) => current + token),
        abort.signal,
      )
      setResult(completed)
      setAnswer((current) => current || completed.answer)
    } catch (reason) {
      if (!abort.signal.aborted) setError(reason instanceof Error ? reason.message : 'Chat 失败')
    } finally {
      if (abortRef.current === abort) {
        abortRef.current = undefined
        setLoading(false)
      }
    }
  }

  return (
    <section>
      <PageHeader title="知识 Chat" description="基于当前租户知识库流式生成回答，并显示最终引用。" />
      <IrisCard variant="outline">
        <form className="chat-form" onSubmit={(event) => { event.preventDefault(); void ask() }}>
          <IrisTextarea
            value={query}
            rows={4}
            placeholder="询问知识库中的内容"
            onChange={(event) => setQuery(event.target.value)}
          />
          <div className="chat-actions">
            <SearchModeField value={mode} onChange={setMode} />
            {loading ? <IrisButton type="button" variant="outline" onClick={() => abortRef.current?.abort()}>停止</IrisButton> : null}
            <IrisButton type="submit" loading={loading}>发送</IrisButton>
          </div>
        </form>
      </IrisCard>
      {error ? <IrisAlert tone="danger" title="Chat 失败">{error}</IrisAlert> : null}
      {answer || loading ? (
        <IrisCard variant="outline" header={<div className="hit-header"><strong>回答</strong>{result?.model ? <IrisBadge>{result.model}</IrisBadge> : null}</div>}>
          <div className="chat-answer">{answer}{loading ? <span className="stream-cursor" aria-label="生成中" /> : null}</div>
        </IrisCard>
      ) : null}
      {result?.citations.length ? (
        <div className="citation-list">
          <h2>引用</h2>
          {result.citations.map((hit, index) => (
            <IrisCard key={`${hit.chunk_id}:${index}`} variant="outline" header={`[#${index + 1}] ${hit.object_key}`}>
              <div className="hit-source">{hit.bucket} · object #{hit.object_id} · chunk #{hit.chunk_id}</div>
              <p className="hit-content">{hit.chunk}</p>
            </IrisCard>
          ))}
        </div>
      ) : null}
    </section>
  )
}
