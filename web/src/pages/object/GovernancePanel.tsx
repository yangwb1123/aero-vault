import * as React from 'react'
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisCopyButton,
  IrisInput,
  IrisNumberInput,
} from '@iris-ui-kit/react'
import type { LegalHold, PresignedLink } from '../../api/objects'
import type { VaultClient } from '../../api/vault'

export function GovernancePanel({
  client,
  objectKey,
  deleted,
  onRestored,
}: {
  client: VaultClient
  objectKey: string
  deleted: boolean
  onRestored(): void
}): React.ReactElement {
  const [versionID, setVersionID] = React.useState('')
  const [reason, setReason] = React.useState('')
  const [hold, setHold] = React.useState<LegalHold>()
  const [holdLoaded, setHoldLoaded] = React.useState(false)
  const [operation, setOperation] = React.useState<'get' | 'put'>('get')
  const [expires, setExpires] = React.useState<number | null>(900)
  const [signed, setSigned] = React.useState<PresignedLink>()
  const [busy, setBusy] = React.useState('')
  const [message, setMessage] = React.useState<{ tone: 'success' | 'danger'; text: string }>()

  const run = async (label: string, action: () => Promise<void>) => {
    setBusy(label)
    setMessage(undefined)
    try { await action() } catch (reasonValue) {
      setMessage({ tone: 'danger', text: reasonValue instanceof Error ? reasonValue.message : '对象治理操作失败' })
    } finally { setBusy('') }
  }
  const loadHold = () => run('hold-load', async () => {
    setHold(await client.getLegalHold(objectKey, versionID.trim()))
    setHoldLoaded(true)
  })
  const placeHold = () => run('hold-put', async () => {
    if (!reason.trim()) throw new Error('设置 Legal Hold 必须填写保留原因')
    await client.putLegalHold(objectKey, reason.trim(), versionID.trim())
    setHold(await client.getLegalHold(objectKey, versionID.trim()))
    setHoldLoaded(true)
    setMessage({ tone: 'success', text: 'Legal Hold 已设置。' })
  })
  const removeHold = () => run('hold-delete', async () => {
    await client.removeLegalHold(objectKey, versionID.trim())
    setHold(undefined)
    setHoldLoaded(true)
    setMessage({ tone: 'success', text: 'Legal Hold 已移除。' })
  })
  const presign = () => run('presign', async () => {
    const result = await client.presignObject(objectKey, operation, expires && expires > 0 ? expires : 300)
    setSigned(result)
  })
  const restore = () => run('restore', async () => {
    await client.restoreObject(objectKey)
    onRestored()
  })

  return (
    <div className="object-grid">
      <IrisCard variant="outline" header="Legal Hold">
        <div className="governance-form">
          <label className="access-field"><span>Version ID（空为当前对象）</span><IrisInput value={versionID} onChange={(event) => { setVersionID(event.target.value); setHoldLoaded(false) }} /></label>
          <label className="access-field"><span>保留原因</span><IrisInput value={reason} placeholder="case-42" onChange={(event) => setReason(event.target.value)} /></label>
          <div className="access-actions">
            <IrisButton variant="outline" loading={busy === 'hold-load'} onClick={() => void loadHold()}>查询</IrisButton>
            {hold ? <IrisButton variant="outline" loading={busy === 'hold-delete'} onClick={() => {
              if (window.confirm('移除 Legal Hold？移除后对象可能被永久删除。')) void removeHold()
            }}>移除 Hold</IrisButton> : <IrisButton loading={busy === 'hold-put'} onClick={() => void placeHold()}>设置 Hold</IrisButton>}
          </div>
          {holdLoaded ? <HoldStatus hold={hold} /> : null}
        </div>
      </IrisCard>
      <IrisCard variant="outline" header="临时访问链接">
        <form className="governance-form" onSubmit={(event) => { event.preventDefault(); void presign() }}>
          <label className="access-field"><span>操作</span><select value={operation} onChange={(event) => setOperation(event.target.value as 'get' | 'put')}><option value="get">GET 下载</option><option value="put">PUT 上传</option></select></label>
          <label className="access-field"><span>有效期（秒）</span><IrisNumberInput min={1} value={expires} onChange={setExpires} /></label>
          <div className="access-actions"><IrisButton type="submit" loading={busy === 'presign'}>生成预签名 URL</IrisButton></div>
          {signed ? <div className="signed-link"><a href={signed.url} target="_blank" rel="noreferrer">{signed.url}</a><IrisCopyButton text={signed.url}>复制</IrisCopyButton><span className="muted">到期：{new Date(signed.expires).toLocaleString('zh-CN')}</span></div> : null}
        </form>
      </IrisCard>
      {deleted ? <IrisCard variant="outline" header="恢复对象"><p className="muted">恢复后对象重新出现在活动文件列表，原有版本关系保持不变。</p><IrisButton loading={busy === 'restore'} onClick={() => void restore()}>恢复对象</IrisButton></IrisCard> : null}
      {message ? <IrisAlert tone={message.tone}>{message.text}</IrisAlert> : null}
    </div>
  )
}

function HoldStatus({ hold }: { hold?: LegalHold }): React.ReactElement {
  if (!hold) return <IrisAlert tone="success">当前目标没有 Legal Hold。</IrisAlert>
  return <IrisAlert tone="warning" title="删除保护生效中">{hold.hold_reason || '未填写原因'} · {hold.created_by} · {new Date(hold.created_at).toLocaleString('zh-CN')}</IrisAlert>
}
