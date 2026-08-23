import * as React from 'react'
import { IrisAlert, IrisButton, IrisCard, IrisTextarea } from '@iris-ui-kit/react'
import type { VaultClient } from '../../api/vault'
import { PageError, PageLoading } from '../../components/Page'
import { useResource } from '../../hooks/useResource'
import { parseStringRecord } from './recordJSON'

export function MetadataPanel({ client, objectKey }: { client: VaultClient; objectKey: string }): React.ReactElement {
  const load = React.useCallback(async () => {
    const [tags, metadata] = await Promise.all([client.getTags(objectKey), client.getMetadata(objectKey)])
    return { tags, metadata }
  }, [client, objectKey])
  const resource = useResource(load)

  if (resource.loading) return <PageLoading />
  if (resource.error) return <PageError error={resource.error} retry={resource.reload} />
  if (!resource.data) return <></>
  return (
    <div className="object-grid">
      <RecordEditor title="标签" initial={resource.data.tags} save={(value) => client.putTags(objectKey, value)} />
      <RecordEditor title="元数据" initial={resource.data.metadata} save={(value) => client.putMetadata(objectKey, value)} />
    </div>
  )
}

function RecordEditor({ title, initial, save }: { title: string; initial: Record<string, string>; save(value: Record<string, string>): Promise<void> }): React.ReactElement {
  const [raw, setRaw] = React.useState(() => JSON.stringify(initial, null, 2))
  const [busy, setBusy] = React.useState(false)
  const [message, setMessage] = React.useState<{ tone: 'success' | 'danger'; text: string }>()
  const submit = async () => {
    setBusy(true)
    setMessage(undefined)
    try {
      await save(parseStringRecord(raw, title))
      setMessage({ tone: 'success', text: `${title}已保存。` })
    } catch (reason) {
      setMessage({ tone: 'danger', text: reason instanceof Error ? reason.message : `${title}保存失败` })
    } finally { setBusy(false) }
  }
  return (
    <IrisCard variant="outline" header={title}>
      <div className="record-editor">
        <p className="muted">使用 JSON 字符串键值对象；提交会替换当前全部{title}。</p>
        <IrisTextarea rows={12} value={raw} spellCheck={false} onChange={(event) => setRaw(event.target.value)} />
        {message ? <IrisAlert tone={message.tone}>{message.text}</IrisAlert> : null}
        <IrisButton loading={busy} onClick={() => void submit()}>保存{title}</IrisButton>
      </div>
    </IrisCard>
  )
}
