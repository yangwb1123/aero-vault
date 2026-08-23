import * as React from 'react'
import { IrisAlert, IrisButton, IrisCard, IrisInput } from '@iris-ui-kit/react'
import type { BucketEncryption, BucketSecurityClient, BucketSSEAlgorithm } from '../../../api/bucketSecurity'

export function BucketEncryptionPanel({ api, bucket, config, onSaved }: {
  api: BucketSecurityClient; bucket: string; config: BucketEncryption; onSaved(): void
}): React.ReactElement {
  const [algorithm, setAlgorithm] = React.useState(config.sse_algorithm)
  const [keyID, setKeyID] = React.useState(config.sse_kms_key_id)
  const [busy, setBusy] = React.useState(false)
  const [message, setMessage] = React.useState<{ tone: 'success' | 'danger'; text: string }>()

  const save = async () => {
    if (algorithm === 'aws:kms' && !keyID.trim()) { setMessage({ tone: 'danger', text: 'aws:kms 必须配置 KMS Key ID。' }); return }
    setBusy(true); setMessage(undefined)
    try {
      if (algorithm) await api.putEncryption(bucket, { sse_algorithm: algorithm, sse_kms_key_id: algorithm === 'aws:kms' ? keyID.trim() : '' })
      else await api.deleteEncryption(bucket)
      setMessage({ tone: 'success', text: algorithm ? '服务端加密默认值已更新。' : '存储桶加密默认值已清除。' }); onSaved()
    } catch (reason) {
      setMessage({ tone: 'danger', text: reason instanceof Error ? reason.message : '服务端加密更新失败' })
    } finally { setBusy(false) }
  }

  return (
    <IrisCard variant="outline" header="默认服务端加密">
      {message ? <IrisAlert tone={message.tone}>{message.text}</IrisAlert> : null}
      <p className="muted">仅当当前存储后端能够满足所选算法时服务端才会接受配置；KMS 优先级与密钥来源由 Aero Vault 后端控制。</p>
      <form className="bucket-encryption-form" onSubmit={(event) => { event.preventDefault(); void save() }}>
        <label><span>算法</span><select value={algorithm} onChange={(event) => setAlgorithm(event.target.value as BucketSSEAlgorithm)}><option value="">不设置存储桶默认值</option><option value="AES256">AES256</option><option value="aws:kms">aws:kms</option></select></label>
        <label><span>KMS Key ID</span><IrisInput value={keyID} disabled={algorithm !== 'aws:kms'} placeholder="仅 aws:kms 必填" onChange={(event) => setKeyID(event.target.value)} /></label>
        <IrisButton type="submit" loading={busy}>保存加密设置</IrisButton>
      </form>
    </IrisCard>
  )
}
