import * as React from 'react'
import { IrisAlert, IrisBadge, IrisButton, IrisCard, IrisEmptyState, IrisTextarea } from '@iris-ui-kit/react'
import type { AgentResponse, AgentStep } from '../api/agent'
import type { VaultClient } from '../api/vault'
import { PageHeader } from '../components/Page'

export function AgentPage({ client }: { client: VaultClient }): React.ReactElement {
  const [query, setQuery] = React.useState('')
  const [result, setResult] = React.useState<AgentResponse>()
  const [error, setError] = React.useState<string>()
  const [loading, setLoading] = React.useState(false)
  const abortRef = React.useRef<AbortController>()

  React.useEffect(() => () => abortRef.current?.abort(), [])

  const run = async () => {
    const value = query.trim()
    if (!value) return
    abortRef.current?.abort()
    const abort = new AbortController()
    abortRef.current = abort
    setResult(undefined)
    setError(undefined)
    setLoading(true)
    try {
      setResult(await client.runAgent(value, abort.signal))
    } catch (reason) {
      if (!abort.signal.aborted) setError(reason instanceof Error ? reason.message : 'Agent 执行失败')
    } finally {
      if (abortRef.current === abort) {
        abortRef.current = undefined
        setLoading(false)
      }
    }
  }

  return (
    <section>
      <PageHeader title="知识 Agent" description="让模型在当前租户内按需列出、读取和检索文件，并展示完整工具轨迹。" />
      <IrisAlert tone="info" title="执行边界">
        Agent 最多执行服务端配置的工具步数；它不返回 citations，也不参与 Chat 的每日费用预算检查。
      </IrisAlert>
      <IrisCard variant="outline">
        <form className="agent-form" onSubmit={(event) => { event.preventDefault(); void run() }}>
          <IrisTextarea value={query} rows={4} placeholder="例如：查找本周项目文档并总结关键风险" onChange={(event) => setQuery(event.target.value)} />
          <div className="agent-actions">
            <span className="muted">可用工具：list_files · read_file · search (hybrid)</span>
            <div className="row-actions">
              {loading ? <IrisButton type="button" variant="outline" onClick={() => abortRef.current?.abort()}>停止</IrisButton> : null}
              <IrisButton type="submit" loading={loading} disabled={!query.trim()}>运行 Agent</IrisButton>
            </div>
          </div>
        </form>
      </IrisCard>
      {error ? <IrisAlert tone="danger" title="Agent 执行失败">{error}</IrisAlert> : null}
      {result ? <AgentResult result={result} /> : null}
    </section>
  )
}

function AgentResult({ result }: { result: AgentResponse }): React.ReactElement {
  return (
    <div className="agent-result">
      <IrisCard variant="outline" header={<div className="hit-header"><strong>最终回答</strong>{result.model ? <IrisBadge>{result.model}</IrisBadge> : null}</div>}>
        <div className="chat-answer">{result.answer || 'Agent 未返回文本回答。'}</div>
      </IrisCard>
      <div className="agent-trace-heading"><h2>工具轨迹</h2><IrisBadge tone="neutral">{result.steps.length} 步</IrisBadge></div>
      {result.steps.length ? result.steps.map((step, index) => <AgentStepCard key={`${index}:${step.tool}`} step={step} index={index} />) : (
        <IrisEmptyState title="未调用工具" description="模型直接生成了最终回答。" />
      )}
    </div>
  )
}

function AgentStepCard({ step, index }: { step: AgentStep; index: number }): React.ReactElement {
  return (
    <IrisCard variant="outline" header={<div className="hit-header"><strong>步骤 {index + 1}</strong><IrisBadge>{toolLabel(step.tool)}</IrisBadge></div>}>
      <div className="agent-step-grid">
        <div><span className="agent-step-label">参数</span><pre>{JSON.stringify(step.args ?? {}, null, 2)}</pre></div>
        <div><span className="agent-step-label">结果</span><pre>{step.result || '（空结果）'}</pre></div>
      </div>
    </IrisCard>
  )
}

function toolLabel(tool: string): string {
  return ({ list_files: '列出文件', read_file: '读取文件', search: '混合检索' } as Record<string, string>)[tool] ?? tool
}
