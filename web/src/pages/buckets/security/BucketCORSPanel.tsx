import * as React from 'react'
import { IrisAlert, IrisButton, IrisCard, IrisTextarea } from '@iris-ui-kit/react'
import { parseCORSRules, type BucketCORSRule, type BucketSecurityClient } from '../../../api/bucketSecurity'

export function BucketCORSPanel({ api, bucket, rules, onSaved }: {
  api: BucketSecurityClient; bucket: string; rules: BucketCORSRule[]; onSaved(): void
}): React.ReactElement {
  const [raw, setRaw] = React.useState(JSON.stringify(rules, null, 2))
  const [busy, setBusy] = React.useState('')
  const [message, setMessage] = React.useState<{ tone: 'success' | 'danger'; text: string }>()

  const save = async () => {
    let parsed: BucketCORSRule[]
    try { parsed = parseCORSRules(raw) } catch (reason) {
      setMessage({ tone: 'danger', text: reason instanceof Error ? reason.message : 'CORS JSON 无效' }); return
    }
    setBusy('save'); setMessage(undefined)
    try { await api.putCORS(bucket, parsed); setMessage({ tone: 'success', text: 'CORS 规则已更新。' }); onSaved() } catch (reason) {
      setMessage({ tone: 'danger', text: reason instanceof Error ? reason.message : 'CORS 更新失败' })
    } finally { setBusy('') }
  }
  const clear = async () => {
    if (!window.confirm('清除当前存储桶的全部 CORS 规则？')) return
    setBusy('clear'); setMessage(undefined)
    try { await api.deleteCORS(bucket); setRaw('[]'); setMessage({ tone: 'success', text: 'CORS 规则已清除。' }); onSaved() } catch (reason) {
      setMessage({ tone: 'danger', text: reason instanceof Error ? reason.message : 'CORS 清除失败' })
    } finally { setBusy('') }
  }

  return (
    <IrisCard variant="outline" header="跨域访问规则">
      {message ? <IrisAlert tone={message.tone}>{message.text}</IrisAlert> : null}
      <p className="muted">使用 snake_case 编辑规则；保存时客户端会映射为后端 CORS 契约。allowed_origins 与 allowed_methods 必须是非空字符串数组。</p>
      <IrisTextarea rows={16} value={raw} spellCheck={false} placeholder={corsExample} onChange={(event) => setRaw(event.target.value)} />
      <div className="bucket-security-actions"><IrisButton variant="ghost" loading={busy === 'clear'} onClick={() => void clear()}>清除 CORS</IrisButton><IrisButton loading={busy === 'save'} onClick={() => void save()}>保存 CORS</IrisButton></div>
    </IrisCard>
  )
}

const corsExample = `[
  {
    "allowed_origins": ["https://app.example.com"],
    "allowed_methods": ["GET", "HEAD"],
    "allowed_headers": ["Authorization"],
    "expose_headers": ["ETag"],
    "max_age_seconds": 600
  }
]`
